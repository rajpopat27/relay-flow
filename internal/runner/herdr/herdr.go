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

func (a *adapter) FindTerminal(ctx context.Context, terminal runner.Terminal) (runner.Terminal, bool, error) {
	if terminal.ID == "" {
		return runner.Terminal{}, false, nil
	}

	pane, err := a.cli.GetPane(ctx, terminal.ID)
	if err != nil || pane.ID != terminal.ID || pane.TerminalID == "" {
		return runner.Terminal{}, false, nil
	}

	processInfo, err := a.cli.ProcessInfo(ctx, pane.ID)
	if err != nil || !usableProcessInfo(processInfo) {
		return runner.Terminal{}, false, nil
	}

	return runner.Terminal{ID: pane.ID, Title: terminal.Title}, true, nil
}

func usableProcessInfo(info herdrclicli.ProcessInfo) bool {
	if info.PaneID == "" || len(info.ForegroundProcesses) == 0 {
		return false
	}

	for _, process := range info.ForegroundProcesses {
		if info.ShellPID != nil && process.PID == *info.ShellPID {
			continue
		}
		if isShellProcess(process) {
			continue
		}
		return true
	}
	return false
}

func isShellProcess(process herdrclicli.ForegroundProcess) bool {
	name := strings.ToLower(filepath.Base(process.Name))
	if name == "" {
		name = strings.ToLower(filepath.Base(process.Argv0))
	}
	switch name {
	case "bash", "dash", "fish", "ksh", "nu", "pwsh", "powershell", "sh", "zsh":
		return true
	default:
		return false
	}
}

func (a *adapter) CreateTerminal(ctx context.Context, env runner.Environment, title string, command runner.Command) (runner.Terminal, error) {
	_, pane, err := a.cli.CreateTab(ctx, env.ID, env.Path, title, command.Env)
	if err != nil {
		return runner.Terminal{}, err
	}
	if pane.ID == "" {
		return runner.Terminal{}, fmt.Errorf("herdr: tab create returned an empty root pane ID")
	}

	if err := a.cli.RenamePane(ctx, pane.ID, title); err != nil {
		// A tab/root pane was created, so make a best-effort cleanup attempt
		// before returning the original setup failure.
		_ = a.cli.ClosePane(ctx, pane.ID)
		return runner.Terminal{}, err
	}
	if err := a.cli.RunPane(ctx, pane.ID, shellCommand(command)); err != nil {
		// Do not leave a ticket-owned pane behind when command submission fails.
		_ = a.cli.ClosePane(ctx, pane.ID)
		return runner.Terminal{}, err
	}
	return runner.Terminal{ID: pane.ID, Title: title}, nil
}

func shellCommand(command runner.Command) string {
	var b strings.Builder
	b.WriteString(shellQuote(command.Executable))
	for _, arg := range command.Args {
		b.WriteByte(' ')
		b.WriteString(shellQuote(arg))
	}
	return b.String()
}

func shellQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", `'\''`) + "'"
}

func (a *adapter) EnsureTerminal(ctx context.Context, env runner.Environment, stored runner.Terminal, title string, command runner.Command) (runner.Terminal, error) {
	if terminal, ok, err := a.FindTerminal(ctx, stored); err != nil {
		return runner.Terminal{}, err
	} else if ok {
		return terminal, nil
	}

	// An unusable stored pane must be closed, but label recovery still runs
	// afterward: a different pane may contain a live/recoverable lost create.
	if stored.ID != "" {
		_ = a.cli.ClosePane(ctx, stored.ID)
	}

	// A create acknowledgement can be lost before the pane handle is persisted.
	// Search stable tab/pane labels before creating a replacement so recovery is
	// idempotent across every point in the create/rename sequence.
	recovered, paneLabel, err := a.recoverTerminal(ctx, env.ID, title)
	if err != nil {
		return runner.Terminal{}, err
	}
	if recovered.ID != "" && recovered.ID != stored.ID {
		if terminal, ok, err := a.FindTerminal(ctx, recovered); err != nil {
			return runner.Terminal{}, err
		} else if ok {
			return terminal, nil
		}
		// A labelled pane recovered without the stored handle may be the root
		// shell left after rename but before pane run. Finish the pending
		// command in place rather than creating a duplicate. A tab-labelled
		// pane also needs its label applied before running the command.
		if !paneLabel {
			if err := a.cli.RenamePane(ctx, recovered.ID, title); err != nil {
				_ = a.cli.ClosePane(ctx, recovered.ID)
				return runner.Terminal{}, err
			}
		}
		if err := a.cli.RunPane(ctx, recovered.ID, shellCommand(command)); err != nil {
			_ = a.cli.ClosePane(ctx, recovered.ID)
			return runner.Terminal{}, err
		}
		return recovered, nil
	}

	return a.CreateTerminal(ctx, env, title, command)
}

func (a *adapter) recoverTerminal(ctx context.Context, workspaceID, title string) (runner.Terminal, bool, error) {
	if workspaceID == "" || title == "" {
		return runner.Terminal{}, false, nil
	}

	a.lookupMu.Lock()
	tabs, err := a.cli.ListTabs(ctx, workspaceID)
	if err != nil {
		a.lookupMu.Unlock()
		return runner.Terminal{}, false, err
	}
	panes, err := a.cli.ListPanes(ctx, workspaceID)
	a.lookupMu.Unlock()
	if err != nil {
		return runner.Terminal{}, false, err
	}

	ownedTabs := make(map[string]bool)
	for _, tab := range tabs {
		if tab.WorkspaceID == workspaceID && tab.Label == title {
			ownedTabs[tab.ID] = true
		}
	}

	seen := make(map[string]bool)
	type candidate struct {
		pane      herdrclicli.Pane
		paneLabel bool
	}
	candidates := make([]candidate, 0, len(panes))
	for _, pane := range panes {
		if pane.WorkspaceID != workspaceID || pane.ID == "" {
			continue
		}
		if pane.Label != title && !ownedTabs[pane.TabID] {
			continue
		}
		if seen[pane.ID] {
			continue
		}
		seen[pane.ID] = true
		candidates = append(candidates, candidate{pane: pane, paneLabel: pane.Label == title})
	}
	sort.Slice(candidates, func(i, j int) bool { return candidates[i].pane.ID < candidates[j].pane.ID })
	if len(candidates) == 0 {
		return runner.Terminal{}, false, nil
	}
	return runner.Terminal{ID: candidates[0].pane.ID, Title: title}, candidates[0].paneLabel, nil
}

func (a *adapter) SendTerminal(ctx context.Context, terminal runner.Terminal, text string) error {
	return a.cli.RunPane(ctx, terminal.ID, text)
}

func (a *adapter) CloseTerminal(ctx context.Context, terminal runner.Terminal) error {
	if terminal.ID == "" {
		return nil
	}
	if err := a.cli.ClosePane(ctx, terminal.ID); err != nil && !isMissingPaneError(err) {
		return err
	}
	return nil
}

func isMissingPaneError(err error) bool {
	if err == nil {
		return false
	}
	message := strings.ToLower(err.Error())
	for _, marker := range []string{
		"not found",
		"does not exist",
		"no such pane",
		"pane_missing",
		"pane-not-found",
		"pane_not_found",
	} {
		if strings.Contains(message, marker) {
			return true
		}
	}
	return false
}

func (*adapter) CloseTerminals(context.Context, runner.RunSpec) error {
	return errors.New("herdr: CloseTerminals not implemented")
}

func (*adapter) CleanupRun(context.Context, runner.RunSpec) error {
	return errors.New("herdr: CleanupRun not implemented")
}

var _ runner.Runner = (*adapter)(nil)
