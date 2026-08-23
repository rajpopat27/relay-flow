package repo

import (
	"context"
	"fmt"
	"path/filepath"

	"github.com/rajpopat27/relay-flow/internal/config"
	"github.com/rajpopat27/relay-flow/internal/runner"
	"github.com/rajpopat27/relay-flow/internal/task"
)

// ActiveRuns reports whether any run of a repo is active.
type ActiveRuns interface {
	HasActiveRepo(context.Context, string) (bool, error)
}

// WorkflowRefs reports whether any stored workflow references a repo.
type WorkflowRefs interface {
	ReferencesRepo(string) bool
}

// ServiceConfig wires the repo service to the machine config, runner, task
// factory, and consumer query interfaces.
type ServiceConfig struct {
	ConfigPath string
	TaskPlugin string
	Runner     runner.Runner
	Active     ActiveRuns
	Workflows  WorkflowRefs
}

type Service struct {
	cfgPath    string
	taskPlugin string
	runner     runner.Runner
	active     ActiveRuns
	workflows  WorkflowRefs
	reg        *Registry
}

func NewService(cfg ServiceConfig) *Service {
	return &Service{
		cfgPath:    cfg.ConfigPath,
		taskPlugin: cfg.TaskPlugin,
		runner:     cfg.Runner,
		active:     cfg.Active,
		workflows:  cfg.Workflows,
		reg:        NewRegistry(),
	}
}

// Registry exposes the in-memory repo set for wiring (poller group, binding).
func (s *Service) Registry() *Registry {
	return s.reg
}

func (s *Service) Discover(ctx context.Context) ([]runner.RepoCandidate, error) {
	return s.runner.DiscoverRepos(ctx)
}

// RequiredRepoKeys delegates to the task factory's method of the same name.
func (s *Service) RequiredRepoKeys() []string {
	keys, err := task.RequiredRepoKeys(s.taskPlugin)
	if err != nil {
		return nil
	}
	return keys
}

type RegisterInput struct {
	Name       string
	Path       string
	TaskConfig config.RawValues
}

// Register validates the runner repo, required task values, task-system
// connectivity, duplicate names, canonical paths, and duplicate canonical
// task scope before atomically writing machine config.
func (s *Service) Register(ctx context.Context, input RegisterInput) (Info, error) {
	if input.Name == "" {
		return Info{}, fmt.Errorf("repo: name is required")
	}
	cfg, err := config.LoadMachine(s.cfgPath)
	if err != nil {
		return Info{}, err
	}
	if _, exists := cfg.Repos[input.Name]; exists {
		return Info{}, fmt.Errorf("repo %q is already registered", input.Name)
	}
	canon := canonicalPath(input.Path)
	for name, r := range cfg.Repos {
		if canonicalPath(r.Path) == canon {
			return Info{}, fmt.Errorf("repo path %q is already registered as %q", input.Path, name)
		}
	}
	// Required repo keys must be present before any remote validation.
	keys, err := task.RequiredRepoKeys(s.taskPlugin)
	if err != nil {
		return Info{}, err
	}
	for _, k := range keys {
		v, ok := input.TaskConfig[k]
		if !ok || v == nil || v == "" {
			return Info{}, fmt.Errorf("repo %q: required task config key %q is missing", input.Name, k)
		}
	}
	// Duplicate canonical task scope: compute this repo's scope and compare
	// against every registered repo's scope.
	newScope, err := task.TaskScopeKey(s.taskPlugin, cfg.TaskConfig, input.TaskConfig)
	if err != nil {
		return Info{}, fmt.Errorf("repo %q: %w", input.Name, err)
	}
	for name, r := range cfg.Repos {
		scope, err := task.TaskScopeKey(s.taskPlugin, cfg.TaskConfig, r.TaskConfig)
		if err != nil {
			return Info{}, fmt.Errorf("repo %q: derive scope of registered repo %q: %w", input.Name, name, err)
		}
		if scope == newScope {
			return Info{}, fmt.Errorf("repo %q: task scope already used by repo %q", input.Name, name)
		}
	}
	// Runner validates the repo.
	if err := s.runner.ValidateRepo(ctx, input.Name, input.Path); err != nil {
		return Info{}, fmt.Errorf("repo %q: runner validation: %w", input.Name, err)
	}
	// Task-system connectivity: construct the repo-bound System.
	sys, err := task.New(ctx, s.taskPlugin, task.RepoSpec{
		Name:       input.Name,
		Path:       input.Path,
		RootConfig: cfg.TaskConfig,
		RepoConfig: input.TaskConfig,
	})
	if err != nil {
		return Info{}, fmt.Errorf("repo %q: task system connectivity: %w", input.Name, err)
	}
	// Persist atomically, then update the in-memory registry.
	r := config.Repo{Path: input.Path, TaskConfig: input.TaskConfig}
	if cfg.Repos == nil {
		cfg.Repos = map[string]config.Repo{}
	}
	cfg.Repos[input.Name] = r
	if err := config.SaveMachine(s.cfgPath, cfg); err != nil {
		return Info{}, err
	}
	rp := &Repo{Name: input.Name, Path: input.Path, TaskConfig: input.TaskConfig, TaskSystem: sys}
	s.reg.Replace(rp)
	return rp.Info(), nil
}

// Remove deletes a registered repo. Removal is rejected while a stored
// workflow references the repo or an active run uses it.
func (s *Service) Remove(ctx context.Context, name string) error {
	if s.workflows.ReferencesRepo(name) {
		return fmt.Errorf("repo %q is referenced by a stored workflow; removal is rejected", name)
	}
	active, err := s.active.HasActiveRepo(ctx, name)
	if err != nil {
		return fmt.Errorf("check active runs for repo %q: %w", name, err)
	}
	if active {
		return fmt.Errorf("repo %q has active runs; removal is rejected", name)
	}
	cfg, err := config.LoadMachine(s.cfgPath)
	if err != nil {
		return err
	}
	if _, ok := cfg.Repos[name]; !ok {
		return fmt.Errorf("repo %q is not registered", name)
	}
	delete(cfg.Repos, name)
	if err := config.SaveMachine(s.cfgPath, cfg); err != nil {
		return err
	}
	s.reg.Remove(name)
	return nil
}

func (s *Service) Get(name string) (Info, error) {
	rp, ok := s.reg.Get(name)
	if ok {
		return rp.Info(), nil
	}
	cfg, err := config.LoadMachine(s.cfgPath)
	if err != nil {
		return Info{}, err
	}
	r, ok := cfg.Repos[name]
	if !ok {
		return Info{}, fmt.Errorf("repo %q is not registered", name)
	}
	return Info{Name: name, Path: r.Path, TaskConfig: r.TaskConfig}, nil
}

func (s *Service) List() []Info {
	out := []Info{}
	for _, rp := range s.reg.List() {
		out = append(out, rp.Info())
	}
	return out
}

func canonicalPath(p string) string {
	abs, err := filepath.Abs(p)
	if err != nil {
		return filepath.Clean(p)
	}
	return filepath.Clean(abs)
}
