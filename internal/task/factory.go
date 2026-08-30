package task

import (
	"context"
	"fmt"
	"io"
	"sort"
	"strings"
	"sync"

	"github.com/rajpopat27/relay-flow/internal/config"
)

// RepoSpec carries the registration values used to construct a repo-bound
// task System.
type RepoSpec struct {
	Name       string
	Path       string
	RootConfig config.RawValues
	RepoConfig config.RawValues
}

// Factory constructs a task System. RequiredRepoKeys returns the explicit
// repo YAML keys needed at registration. TaskScopeKey derives an opaque
// canonical physical task scope (such as Jira site/project/component) used
// to reject duplicate scope registration.
type Factory struct {
	RequiredRepoKeys func() []string
	TaskScopeKey     func(rootConfig, repoConfig config.RawValues) (string, error)
	Auth             func(context.Context, []string, io.Reader) error
	New              func(context.Context, RepoSpec) (System, error)
}

var (
	registryMu sync.RWMutex
	registry   = map[string]Factory{}
)

// Register adds a task factory by name. Duplicate registration panics.
func Register(name string, factory Factory) {
	registryMu.Lock()
	defer registryMu.Unlock()
	if _, exists := registry[name]; exists {
		panic(fmt.Sprintf("task: duplicate registration of %q", name))
	}
	registry[name] = factory
}

func lookup(name string) (Factory, error) {
	registryMu.RLock()
	defer registryMu.RUnlock()
	f, ok := registry[name]
	if !ok {
		return Factory{}, fmt.Errorf("task: unknown plugin %q (registered: %s)", name, strings.Join(Names(), ", "))
	}
	return f, nil
}

// New constructs the repo-bound task System for the named plugin.
func New(ctx context.Context, name string, spec RepoSpec) (System, error) {
	f, err := lookup(name)
	if err != nil {
		return nil, err
	}
	return f.New(ctx, spec)
}

// Auth dispatches system-wide authentication to the selected task plugin.
// The plugin owns its flags, prompts, validation, credential format, and
// credential storage.
func Auth(ctx context.Context, name string, args []string, stdin io.Reader) error {
	f, err := lookup(name)
	if err != nil {
		return err
	}
	if f.Auth == nil {
		return fmt.Errorf("task: plugin %q does not support authentication", name)
	}
	return f.Auth(ctx, args, stdin)
}

// RequiredRepoKeys returns the repo YAML keys the named plugin requires at
// registration.
func RequiredRepoKeys(name string) ([]string, error) {
	f, err := lookup(name)
	if err != nil {
		return nil, err
	}
	return f.RequiredRepoKeys(), nil
}

// TaskScopeKey derives the canonical task scope for the named plugin.
func TaskScopeKey(name string, rootConfig, repoConfig config.RawValues) (string, error) {
	f, err := lookup(name)
	if err != nil {
		return "", err
	}
	return f.TaskScopeKey(rootConfig, repoConfig)
}

// ValidateName returns an error listing registered names when name is not
// a registered plugin. Used by `relay-flow init` to reject unknown plugin
// selections without constructing a System.
func ValidateName(name string) error {
	_, err := lookup(name)
	return err
}

// Names returns the registered plugin names sorted.
func Names() []string {
	registryMu.RLock()
	defer registryMu.RUnlock()
	out := make([]string, 0, len(registry))
	for name := range registry {
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}
