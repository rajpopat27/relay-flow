// Package herdr is the Herdr runner adapter. It owns the mapping from a
// registered repository to a shared Herdr workspace and the ticket-scoped
// panes within that workspace.
package herdr

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"sort"

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
	cli herdrclicli.Client
	cfg Config
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
	snapshot, err := a.cli.Snapshot(ctx)
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

func (*adapter) ValidateRepo(context.Context, string, string) error {
	return errors.New("herdr: ValidateRepo not implemented")
}

func (*adapter) EnsureEnvironment(context.Context, runner.RunSpec) (runner.Environment, error) {
	return runner.Environment{}, errors.New("herdr: EnsureEnvironment not implemented")
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
