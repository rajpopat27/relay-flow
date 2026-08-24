package workflow

import (
	"context"
	"fmt"
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
func (s *Service) Submit(ctx context.Context, yamlBytes []byte) (*Workflow, error) {
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
	return wf, nil
}

// Remove deletes a workflow definition. Removal is rejected while any run
// of the workflow is active.
func (s *Service) Remove(ctx context.Context, name string) error {
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
