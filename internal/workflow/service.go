package workflow

import (
	"context"
	"fmt"
	"sync"
)

// ActiveRuns reports whether any run of a workflow is active.
type ActiveRuns interface {
	HasActiveWorkflow(context.Context, string) (bool, error)
}

// RepoLookup reports whether a repo is registered.
type RepoLookup interface {
	Exists(string) bool
}

// Service manages workflow submission, removal, and lookup over the store
// and registry.
type Service struct {
	store  *Store
	reg    *Registry
	repos  RepoLookup
	active ActiveRuns
	// Gate, when non-nil, is the lifecycle mutex shared with
	// RunManager.EnsureRun (design.md decision 23): Submit/Remove hold it
	// while checking active runs and swapping disk + in-memory definitions,
	// so a run never starts against a workflow being replaced or removed.
	// A plain *sync.Mutex — no lock service.
	Gate *sync.Mutex
	// Rebind, when non-nil, is invoked under Gate after the in-memory
	// workflow registry changes (submit or remove). The composition root
	// wires it to repo.Registry.BindWorkflows so poller/router bindings are
	// rebuilt atomically with the workflow swap (spec: "Repo.Workflows
	// bindings are rebuilt on submit/remove/startup"). Nil in tests that
	// do not wire repos.
	Rebind func() error
	// ValidateTaskConfig validates the workflow against each referenced repo's
	// task system before storage. The composition root supplies this callback;
	// keeping it here avoids widening RepoLookup beyond its documented query.
	ValidateTaskConfig func(context.Context, *Workflow) error
}

func NewService(store *Store, active ActiveRuns, repos RepoLookup) *Service {
	return &Service{store: store, reg: &Registry{}, active: active, repos: repos}
}

// Registry exposes the in-memory workflow set for wiring (startup load).
func (s *Service) Registry() *Registry {
	return s.reg
}

// Submit creates or replaces a workflow. It parses, validates, and checks
// repos and active runs before atomically replacing the file; the in-memory
// replacement cannot fail afterward. There is no workflow versioning.
//
// When Gate is set, the ENTIRE Submit operation (parse, repo validation,
// active-run check, disk + in-memory swap, binding rebuild) holds the
// shared lifecycle mutex so no concurrent EnsureRun can resolve a stale
// definition and no concurrent repo registration can invalidate the
// repo-existence check (design.md decision 23).
func (s *Service) Submit(ctx context.Context, yamlBytes []byte) (*Workflow, error) {
	if s.Gate != nil {
		s.Gate.Lock()
		defer s.Gate.Unlock()
	}
	wf, err := Parse("", yamlBytes)
	if err != nil {
		return nil, err
	}
	if err := wf.Validate(); err != nil {
		return nil, err
	}
	for _, repo := range wf.Repos {
		if !s.repos.Exists(repo) {
			return nil, fmt.Errorf("workflow %q references unregistered repo %q", wf.Name, repo)
		}
	}
	if s.ValidateTaskConfig != nil {
		if err := s.ValidateTaskConfig(ctx, wf); err != nil {
			return nil, err
		}
	}
	active, err := s.active.HasActiveWorkflow(ctx, wf.Name)
	if err != nil {
		return nil, fmt.Errorf("check active runs for workflow %q: %w", wf.Name, err)
	}
	if active {
		return nil, fmt.Errorf("workflow %q has active runs; replacement is rejected", wf.Name)
	}
	if err := s.store.Put(wf.Name, yamlBytes); err != nil {
		return nil, err
	}
	s.reg.Replace(wf)
	if s.Rebind != nil {
		if err := s.Rebind(); err != nil {
			return nil, fmt.Errorf("rebind workflows for %q: %w", wf.Name, err)
		}
	}
	return wf, nil
}

// Remove deletes a workflow definition. Removal is rejected while any run
// of the workflow is active.
func (s *Service) Remove(ctx context.Context, name string) error {
	if s.Gate != nil {
		s.Gate.Lock()
		defer s.Gate.Unlock()
	}
	active, err := s.active.HasActiveWorkflow(ctx, name)
	if err != nil {
		return fmt.Errorf("check active runs for workflow %q: %w", name, err)
	}
	if active {
		return fmt.Errorf("workflow %q has active runs; removal is rejected", name)
	}
	if err := s.store.Remove(name); err != nil {
		return err
	}
	s.reg.Remove(name)
	if s.Rebind != nil {
		if err := s.Rebind(); err != nil {
			return fmt.Errorf("rebind workflows after removing %q: %w", name, err)
		}
	}
	return nil
}

// Get returns the workflow from the registry.
func (s *Service) Get(name string) (*Workflow, error) {
	wf, ok := s.reg.Get(name)
	if !ok {
		return nil, fmt.Errorf("workflow %q not found", name)
	}
	return wf, nil
}

// List returns all registered workflows.
func (s *Service) List() []*Workflow {
	return s.reg.List()
}
