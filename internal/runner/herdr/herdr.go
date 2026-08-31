// Package herdr is the Herdr runner adapter. It owns the mapping from a
// registered repository to a shared Herdr workspace and the ticket-scoped
// panes within that workspace.
package herdr

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"github.com/rajpopat27/relay-flow/internal/config"
	"github.com/rajpopat27/relay-flow/internal/runner"
	"github.com/rajpopat27/relay-flow/internal/runner/herdr/herdrclicli"
)

// Config is the adapter-owned machine-level Herdr runner configuration.
type Config struct {
	Session    string `yaml:"session,omitempty"`
	SocketPath string `yaml:"socketPath,omitempty"`
}

func init() {
	runner.Register("herdr", New)
}

// adapter implements runner.Runner around the public Herdr CLI. The
// herdrclicli.Client field is the package-local seam used by adapter tests;
// New always supplies the concrete production CLI client.
type adapter struct {
	cli      herdrclicli.Client
	cfg      Config
	lookupMu sync.Mutex
}

// New constructs the production Herdr runner from machine-level raw config.
// Configuration is decoded before constructing the CLI client so invalid
// values cannot trigger an external Herdr invocation.
func New(raw config.RawValues) (runner.Runner, error) {
	var cfg Config
	if err := config.DecodeStrict(raw, &cfg); err != nil {
		return nil, fmt.Errorf("herdr runnerConfig: %w", err)
	}
	cli := herdrclicli.New(herdrclicli.Options{
		Session:    cfg.Session,
		SocketPath: cfg.SocketPath,
	})
	return &adapter{cli: cli, cfg: cfg}, nil
}

// newAdapter wires an already-decoded config and a typed CLI test seam. It is
// intentionally unexported; production construction goes through New.
func newAdapter(cli herdrclicli.Client, cfg Config) *adapter {
	return &adapter{cli: cli, cfg: cfg}
}

// The remaining runner operations are implemented in the subsequent adapter
// tasks. Keeping the method set here lets the factory and package compile
// while those operations are filled in incrementally.
func (a *adapter) DiscoverRepos(ctx context.Context) ([]runner.RepoCandidate, error) {
	snapshot, err := a.snapshot(ctx)
	if err != nil {
		return nil, err
	}

	workspaces := make(map[string]herdrclicli.Workspace, len(snapshot.Workspaces))
	for _, workspace := range snapshot.Workspaces {
		workspaces[workspace.ID] = workspace
	}

	// A repository workspace normally has several panes (the root pane and
	// ticket panes), so deduplicate candidates by workspace and normalized cwd.
	// Keep the conversion here at the adapter boundary; core only receives the
	// runner-owned candidate values.
	candidates := make(map[string]runner.RepoCandidate)
	for _, pane := range snapshot.Panes {
		workspace, ok := workspaces[pane.WorkspaceID]
		if !ok {
			continue
		}
		path := normalizePath(pane.CWD)
		if path == "" {
			continue
		}
		key := workspace.ID + "\x00" + path
		candidates[key] = runner.RepoCandidate{Name: workspace.Label, Path: path}
	}

	out := make([]runner.RepoCandidate, 0, len(candidates))
	for _, candidate := range candidates {
		out = append(out, candidate)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Name != out[j].Name {
			return out[i].Name < out[j].Name
		}
		return out[i].Path < out[j].Path
	})
	return out, nil
}

func normalizePath(path string) string {
	if path == "" {
		return ""
	}
	absolute, err := filepath.Abs(path)
	if err != nil {
		return filepath.Clean(path)
	}
	return filepath.Clean(absolute)
}

func (a *adapter) ValidateRepo(ctx context.Context, name, path string) error {
	registeredPath := normalizePath(path)
	if registeredPath == "" {
		return fmt.Errorf("herdr: repository %q has an empty path", name)
	}
	if _, err := os.Stat(registeredPath); err != nil {
		return fmt.Errorf("herdr: repository %q path %q does not exist: %w", name, path, err)
	}
	_, err := a.workspace(ctx, name, path)
	return err
}

func (a *adapter) EnsureEnvironment(ctx context.Context, spec runner.RunSpec) (runner.Environment, error) {
	workspace, err := a.workspace(ctx, spec.RepoName, spec.RepoPath)
	if err != nil {
		return runner.Environment{}, err
	}
	return runner.Environment{ID: workspace.ID, Path: spec.RepoPath}, nil
}

func (a *adapter) snapshot(ctx context.Context) (herdrclicli.Snapshot, error) {
	a.lookupMu.Lock()
	defer a.lookupMu.Unlock()
	return a.cli.Snapshot(ctx)
}

func (a *adapter) workspace(ctx context.Context, name, path string) (herdrclicli.Workspace, error) {
	registeredPath := normalizePath(path)
	if registeredPath == "" {
		return herdrclicli.Workspace{}, fmt.Errorf("herdr: repository %q has an empty path", name)
	}

	snapshot, err := a.snapshot(ctx)
	if err != nil {
		return herdrclicli.Workspace{}, err
	}
	workspaces := make(map[string]herdrclicli.Workspace, len(snapshot.Workspaces))
	for _, candidate := range snapshot.Workspaces {
		workspaces[candidate.ID] = candidate
	}

	matches := make(map[string]herdrclicli.Workspace)
	for _, pane := range snapshot.Panes {
		candidate, ok := workspaces[pane.WorkspaceID]
		if !ok || normalizePath(pane.CWD) != registeredPath {
			continue
		}
		matches[candidate.ID] = candidate
	}

	if len(matches) == 0 {
		return herdrclicli.Workspace{}, fmt.Errorf(
			"herdr: repository %q at %q has no matching workspace; create one with herdr workspace create --cwd %q --label %q --no-focus",
			name, path, path, name,
		)
	}

	ordered := make([]herdrclicli.Workspace, 0, len(matches))
	for _, candidate := range matches {
		ordered = append(ordered, candidate)
	}
	sort.Slice(ordered, func(i, j int) bool {
		if ordered[i].ID != ordered[j].ID {
			return ordered[i].ID < ordered[j].ID
		}
		return ordered[i].Label < ordered[j].Label
	})

	var labelMatch *herdrclicli.Workspace
	for i := range ordered {
		if ordered[i].Label != name {
			continue
		}
		if labelMatch != nil {
			return herdrclicli.Workspace{}, ambiguousWorkspaceError(name, path, ordered)
		}
		labelMatch = &ordered[i]
	}
	if labelMatch != nil {
		return *labelMatch, nil
	}
	if len(ordered) == 1 {
		return ordered[0], nil
	}
	return herdrclicli.Workspace{}, ambiguousWorkspaceError(name, path, ordered)
}

func ambiguousWorkspaceError(name, path string, matches []herdrclicli.Workspace) error {
	ids := make([]string, len(matches))
	for i, match := range matches {
		ids[i] = match.ID
	}
	return fmt.Errorf("herdr: repository %q at %q matches ambiguous workspaces: %s", name, path, strings.Join(ids, ", "))
}

func (*adapter) SetEnvironmentStatus(context.Context, runner.Environment, string) error {
	return errors.New("herdr: SetEnvironmentStatus not implemented")
}

func (*adapter) FindTerminal(context.Context, runner.Terminal) (runner.Terminal, bool, error) {
	return runner.Terminal{}, false, errors.New("herdr: FindTerminal not implemented")
}

func (*adapter) CreateTerminal(context.Context, runner.Environment, string, runner.Command) (runner.Terminal, error) {
	return runner.Terminal{}, errors.New("herdr: CreateTerminal not implemented")
}

func (*adapter) EnsureTerminal(context.Context, runner.Environment, runner.Terminal, string, runner.Command) (runner.Terminal, error) {
	return runner.Terminal{}, errors.New("herdr: EnsureTerminal not implemented")
}

func (*adapter) SendTerminal(context.Context, runner.Terminal, string) error {
	return errors.New("herdr: SendTerminal not implemented")
}

func (*adapter) CloseTerminal(context.Context, runner.Terminal) error {
	return errors.New("herdr: CloseTerminal not implemented")
}

func (*adapter) CloseTerminals(context.Context, runner.RunSpec) error {
	return errors.New("herdr: CloseTerminals not implemented")
}

func (*adapter) CleanupRun(context.Context, runner.RunSpec) error {
	return errors.New("herdr: CleanupRun not implemented")
}

var _ runner.Runner = (*adapter)(nil)
