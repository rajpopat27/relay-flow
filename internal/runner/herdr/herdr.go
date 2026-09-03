// Package herdr is the Herdr runner adapter. It maps a registered repository
// to its Herdr-managed Git worktrees: each ticket gets its own worktree
// workspace, and each node gets one labelled pane inside it. It does not know
// task-system fields, workflow routes, report contents, or agent command
// syntax.
//
// External-call logging mirrors the Orca adapter: one debug line before the
// call and one info line after with the outcome only, never payloads.
package herdr

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/rajpopat27/relay-flow/internal/config"
	"github.com/rajpopat27/relay-flow/internal/runner"
	"github.com/rajpopat27/relay-flow/internal/runner/herdr/herdrcli"
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
// herdrcli.Client field is the package-local seam used by adapter tests;
// New always supplies the concrete production CLI client.
type adapter struct {
	cli herdrcli.Client
}

// New constructs the production Herdr runner from machine-level raw config.
// Configuration is decoded before constructing the CLI client so invalid
// values cannot trigger an external Herdr invocation.
func New(raw config.RawValues) (runner.Runner, error) {
	var cfg Config
	if err := config.DecodeStrict(raw, &cfg); err != nil {
		return nil, fmt.Errorf("herdr runnerConfig: %w", err)
	}
	cli := herdrcli.New(herdrcli.Options{
		Session:    cfg.Session,
		SocketPath: cfg.SocketPath,
	})
	return &adapter{cli: cli}, nil
}

// newAdapter wires a typed CLI test seam. It is intentionally unexported;
// production construction goes through New.
func newAdapter(cli herdrcli.Client) *adapter {
	return &adapter{cli: cli}
}

func logCall(operation string, attrs ...any) {
	args := []any{"op", operation}
	args = append(args, attrs...)
	slog.Debug("herdr call", args...)
}

// logOutcome records only safe operation identity and the result. Do not add
// command, prompt, environment, selector, or external error payloads here.
func logOutcome(operation, result string, attrs ...any) {
	args := []any{"op", operation}
	args = append(args, attrs...)
	args = append(args, "result", result)
	slog.Info("herdr outcome", args...)
}

// --- Repos ---

// DiscoverRepos returns the repositories Herdr currently has open, derived
// from the Git identity Herdr reports for every workspace. A ticket worktree
// workspace reports the same repository root as its source checkout, so the
// candidates are deduplicated by repository root.
func (a *adapter) DiscoverRepos(ctx context.Context) ([]runner.RepoCandidate, error) {
	logCall("discover-repos")
	snapshot, err := a.cli.Snapshot(ctx)
	if err != nil {
		logOutcome("discover-repos", "error")
		return nil, err
	}
	candidates := make(map[string]runner.RepoCandidate)
	for _, workspace := range snapshot.Workspaces {
		root := normalizePath(workspace.Worktree.RepoRoot)
		if root == "" {
			continue
		}
		candidates[root] = runner.RepoCandidate{Name: workspace.Worktree.RepoName, Path: root}
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
	logOutcome("discover-repos", "ok", "count", len(out))
	return out, nil
}

// ValidateRepo verifies the registered path is the root of a Git repository
// Herdr can manage. Registration needs no Herdr workspace and creates none.
func (a *adapter) ValidateRepo(ctx context.Context, name, path string) error {
	logCall("validate-repo", "repo", name)
	registered := normalizePath(path)
	if registered == "" {
		logOutcome("validate-repo", "error", "repo", name)
		return fmt.Errorf("herdr: repository %q has an empty path", name)
	}
	listing, err := a.cli.WorktreeList(ctx, registered)
	if err != nil {
		logOutcome("validate-repo", "error", "repo", name)
		return fmt.Errorf("herdr: repository %q at %q: %w", name, path, err)
	}
	if root := normalizePath(listing.Source.RepoRoot); root != registered {
		logOutcome("validate-repo", "error", "repo", name)
		return fmt.Errorf("herdr: repository %q path %q is inside repository %q; register %q instead", name, path, root, root)
	}
	logOutcome("validate-repo", "ok", "repo", name)
	return nil
}

// --- Environment ---

// EnsureEnvironment returns the ticket's Herdr worktree workspace, opening the
// existing ticket branch checkout or creating it from the repository's origin
// branch. Herdr reuses an existing branch, so previous agent commits are never
// discarded. The workspace ID is Herdr's current handle for the open
// workspace; the durable identity is the ticket branch and its checkout.
func (a *adapter) EnsureEnvironment(ctx context.Context, spec runner.RunSpec) (runner.Environment, error) {
	attrs := []any{"ticket", spec.TicketKey, "runID", string(spec.RunID), "repo", spec.RepoName}
	logCall("ensure-environment", attrs...)
	repoPath := normalizePath(spec.RepoPath)
	if repoPath == "" {
		logOutcome("ensure-environment", "error", attrs...)
		return runner.Environment{}, fmt.Errorf("herdr: repository %q has an empty path", spec.RepoName)
	}

	workspace, err := a.cli.WorktreeOpen(ctx, repoPath, spec.TicketKey, spec.TicketKey)
	outcome := "exists"
	if errors.Is(err, herdrcli.ErrWorktreeNotFound) {
		base, baseErr := originBaseRef(repoPath)
		if baseErr != nil {
			logOutcome("ensure-environment", "error", attrs...)
			return runner.Environment{}, baseErr
		}
		workspace, err = a.cli.WorktreeCreate(ctx, repoPath, spec.TicketKey, base, spec.TicketKey)
		outcome = "created"
	}
	if err != nil {
		logOutcome("ensure-environment", "error", attrs...)
		return runner.Environment{}, err
	}
	checkout := normalizePath(workspace.Worktree.CheckoutPath)
	if workspace.ID == "" || checkout == "" {
		logOutcome("ensure-environment", "error", attrs...)
		return runner.Environment{}, fmt.Errorf("herdr: ticket %q worktree workspace is incomplete", spec.TicketKey)
	}
	logOutcome("ensure-environment", outcome, attrs...)
	return runner.Environment{ID: workspace.ID, Path: checkout}, nil
}

// SetEnvironmentStatus is a successful no-op: Herdr has no workspace status
// primitive, and relay-flow's own run state remains the source of truth.
func (*adapter) SetEnvironmentStatus(context.Context, runner.Environment, string) error {
	logCall("set-environment-status")
	logOutcome("set-environment-status", "ok")
	return nil
}

// ticketWorkspace resolves the ticket's currently open worktree workspace
// without creating anything. found is false when the repository, the ticket
// checkout, or its workspace is absent, which lets cleanup roll forward.
func (a *adapter) ticketWorkspace(ctx context.Context, spec runner.RunSpec) (string, bool, error) {
	repoPath := normalizePath(spec.RepoPath)
	if repoPath == "" {
		return "", false, nil
	}
	listing, err := a.cli.WorktreeList(ctx, repoPath)
	if err != nil {
		if errors.Is(err, herdrcli.ErrNotGitWorktree) || errors.Is(err, herdrcli.ErrWorktreeNotFound) {
			return "", false, nil
		}
		return "", false, err
	}
	for _, worktree := range listing.Worktrees {
		if worktree.Branch == spec.TicketKey && worktree.OpenWorkspaceID != "" {
			return worktree.OpenWorkspaceID, true, nil
		}
	}
	return "", false, nil
}

// --- Terminals ---

// FindTerminal addresses a persisted pane handle directly and returns it only
// when Herdr still has a live agent process in that pane. A pane restored as a
// bare shell after a Herdr restart is not a usable agent terminal.
func (a *adapter) FindTerminal(ctx context.Context, terminal runner.Terminal) (runner.Terminal, bool, error) {
	attrs := []any{"title", terminal.Title, "handle", terminal.ID}
	logCall("find-terminal", attrs...)
	if terminal.ID == "" {
		logOutcome("find-terminal", "absent", attrs...)
		return runner.Terminal{}, false, nil
	}
	pane, err := a.cli.GetPane(ctx, terminal.ID)
	if errors.Is(err, herdrcli.ErrPaneNotFound) {
		logOutcome("find-terminal", "absent", attrs...)
		return runner.Terminal{}, false, nil
	}
	if err != nil {
		logOutcome("find-terminal", "error", attrs...)
		return runner.Terminal{}, false, err
	}
	if pane.ID != terminal.ID || pane.TerminalID == "" {
		logOutcome("find-terminal", "absent", attrs...)
		return runner.Terminal{}, false, nil
	}
	info, err := a.cli.ProcessInfo(ctx, pane.ID)
	if errors.Is(err, herdrcli.ErrPaneNotFound) {
		logOutcome("find-terminal", "absent", attrs...)
		return runner.Terminal{}, false, nil
	}
	if err != nil {
		logOutcome("find-terminal", "error", attrs...)
		return runner.Terminal{}, false, err
	}
	if !hasLiveForegroundProcess(info) {
		logOutcome("find-terminal", "absent", attrs...)
		return runner.Terminal{}, false, nil
	}
	logOutcome("find-terminal", "found", attrs...)
	return runner.Terminal{ID: pane.ID, Title: terminal.Title}, true, nil
}

// hasLiveForegroundProcess reports whether the pane runs something other than
// its own shell. Herdr reports the shell itself in the foreground when a pane
// is idle or was restored after a restart.
func hasLiveForegroundProcess(info herdrcli.ProcessInfo) bool {
	if info.PaneID == "" || info.ShellPID == nil {
		return false
	}
	for _, process := range info.ForegroundProcesses {
		if process.PID != *info.ShellPID {
			return true
		}
	}
	return false
}

// CreateTerminal always creates a node pane; it performs no label discovery.
func (a *adapter) CreateTerminal(ctx context.Context, env runner.Environment, title string, command runner.Command) (runner.Terminal, error) {
	attrs := []any{"envID", env.ID, "title", title}
	logCall("create-terminal", attrs...)
	_, pane, err := a.cli.CreateTab(ctx, env.ID, env.Path, title)
	if err != nil {
		logOutcome("create-terminal", "error", attrs...)
		return runner.Terminal{}, err
	}
	if pane.ID == "" {
		logOutcome("create-terminal", "error", attrs...)
		return runner.Terminal{}, fmt.Errorf("herdr: tab create returned an empty root pane ID")
	}
	if err := a.startNode(ctx, pane.ID, title, command, false); err != nil {
		logOutcome("create-terminal", "error", attrs...)
		return runner.Terminal{}, err
	}
	logOutcome("create-terminal", "ok", attrs...)
	return runner.Terminal{ID: pane.ID, Title: title}, nil
}

// startNode applies the stable pane label and submits the harness command.
// A pane that cannot be prepared is closed so no half-built node pane is left
// behind for label recovery to adopt.
func (a *adapter) startNode(ctx context.Context, paneID, title string, command runner.Command, labelled bool) error {
	if !labelled {
		if err := a.cli.RenamePane(ctx, paneID, title); err != nil {
			_ = a.cli.ClosePane(ctx, paneID)
			return err
		}
	}
	if err := a.cli.RunPane(ctx, paneID, shellCommand(command)); err != nil {
		_ = a.cli.ClosePane(ctx, paneID)
		return err
	}
	return nil
}

// EnsureTerminal reuses a live pane, then recovers a pane whose creation was
// acknowledged but not persisted, and only then creates a replacement.
func (a *adapter) EnsureTerminal(ctx context.Context, env runner.Environment, stored runner.Terminal, title string, command runner.Command) (runner.Terminal, error) {
	attrs := []any{"envID", env.ID, "title", title}
	logCall("ensure-terminal", attrs...)
	if terminal, ok, err := a.FindTerminal(ctx, stored); err != nil {
		logOutcome("ensure-terminal", "error", attrs...)
		return runner.Terminal{}, err
	} else if ok {
		logOutcome("ensure-terminal", "exists", attrs...)
		return terminal, nil
	}
	if stored.ID != "" {
		if err := a.CloseTerminal(ctx, runner.Terminal{ID: stored.ID, Title: title}); err != nil {
			logOutcome("ensure-terminal", "error", attrs...)
			return runner.Terminal{}, err
		}
	}

	// A create acknowledgement can be lost before the pane handle is
	// persisted. The tab label is the recovery marker until pane rename
	// completes; the pane label is used afterward.
	recovered, labelled, err := a.recoverTerminal(ctx, env.ID, title)
	if err != nil {
		logOutcome("ensure-terminal", "error", attrs...)
		return runner.Terminal{}, err
	}
	if recovered != "" && recovered != stored.ID {
		if terminal, ok, err := a.FindTerminal(ctx, runner.Terminal{ID: recovered, Title: title}); err != nil {
			logOutcome("ensure-terminal", "error", attrs...)
			return runner.Terminal{}, err
		} else if ok {
			logOutcome("ensure-terminal", "exists", attrs...)
			return terminal, nil
		}
		// The recovered pane is the root shell left between tab creation and
		// command submission: finish that launch instead of duplicating it.
		if err := a.startNode(ctx, recovered, title, command, labelled); err != nil {
			logOutcome("ensure-terminal", "error", attrs...)
			return runner.Terminal{}, err
		}
		logOutcome("ensure-terminal", "exists", attrs...)
		return runner.Terminal{ID: recovered, Title: title}, nil
	}

	terminal, err := a.CreateTerminal(ctx, env, title, command)
	if err != nil {
		logOutcome("ensure-terminal", "error", attrs...)
		return runner.Terminal{}, err
	}
	logOutcome("ensure-terminal", "created", attrs...)
	return terminal, nil
}

// recoverTerminal finds a pane belonging to the node's stable label, either
// directly or through its tab. labelled reports whether the pane already
// carries the node label.
func (a *adapter) recoverTerminal(ctx context.Context, workspaceID, title string) (string, bool, error) {
	if workspaceID == "" || title == "" {
		return "", false, nil
	}
	tabs, err := a.cli.ListTabs(ctx, workspaceID)
	if err != nil {
		if errors.Is(err, herdrcli.ErrWorkspaceNotFound) {
			return "", false, nil
		}
		return "", false, err
	}
	panes, err := a.cli.ListPanes(ctx, workspaceID)
	if err != nil {
		if errors.Is(err, herdrcli.ErrWorkspaceNotFound) {
			return "", false, nil
		}
		return "", false, err
	}
	ownedTabs := make(map[string]bool)
	for _, tab := range tabs {
		if tab.WorkspaceID == workspaceID && tab.Label == title {
			ownedTabs[tab.ID] = true
		}
	}
	candidates := make([]herdrcli.Pane, 0, len(panes))
	for _, pane := range panes {
		if pane.WorkspaceID != workspaceID || pane.ID == "" {
			continue
		}
		if pane.Label == title || ownedTabs[pane.TabID] {
			candidates = append(candidates, pane)
		}
	}
	if len(candidates) == 0 {
		return "", false, nil
	}
	sort.Slice(candidates, func(i, j int) bool { return candidates[i].ID < candidates[j].ID })
	return candidates[0].ID, candidates[0].Label == title, nil
}

func (a *adapter) SendTerminal(ctx context.Context, terminal runner.Terminal, text string) error {
	attrs := []any{"title", terminal.Title, "handle", terminal.ID}
	logCall("send-terminal", attrs...)
	if err := a.cli.RunPane(ctx, terminal.ID, text); err != nil {
		logOutcome("send-terminal", "error", attrs...)
		return err
	}
	logOutcome("send-terminal", "ok", attrs...)
	return nil
}

// CloseTerminal closes one pane by handle and tolerates an already-closed
// pane.
func (a *adapter) CloseTerminal(ctx context.Context, terminal runner.Terminal) error {
	attrs := []any{"title", terminal.Title, "handle", terminal.ID}
	logCall("close-terminal", attrs...)
	if terminal.ID == "" {
		logOutcome("close-terminal", "absent", attrs...)
		return nil
	}
	if err := a.cli.ClosePane(ctx, terminal.ID); err != nil {
		if errors.Is(err, herdrcli.ErrPaneNotFound) {
			logOutcome("close-terminal", "absent", attrs...)
			return nil
		}
		logOutcome("close-terminal", "error", attrs...)
		return err
	}
	logOutcome("close-terminal", "ok", attrs...)
	return nil
}

// --- Cleanup ---

// CloseTerminals closes the run's node panes inside the ticket worktree
// workspace while preserving the workspace, the worktree, and any pane the
// user opened there. Run-owned panes carry the stable "<ticket>:" prefix on
// the pane label or its tab label.
func (a *adapter) CloseTerminals(ctx context.Context, spec runner.RunSpec) error {
	attrs := []any{"ticket", spec.TicketKey, "runID", string(spec.RunID)}
	logCall("close-terminals", attrs...)
	workspaceID, found, err := a.ticketWorkspace(ctx, spec)
	if err != nil {
		logOutcome("close-terminals", "error", attrs...)
		return err
	}
	if !found {
		logOutcome("close-terminals", "no-environment", attrs...)
		return nil
	}
	tabs, err := a.cli.ListTabs(ctx, workspaceID)
	if err != nil {
		if errors.Is(err, herdrcli.ErrWorkspaceNotFound) {
			logOutcome("close-terminals", "no-environment", attrs...)
			return nil
		}
		logOutcome("close-terminals", "error", attrs...)
		return err
	}
	panes, err := a.cli.ListPanes(ctx, workspaceID)
	if err != nil {
		if errors.Is(err, herdrcli.ErrWorkspaceNotFound) {
			logOutcome("close-terminals", "no-environment", attrs...)
			return nil
		}
		logOutcome("close-terminals", "error", attrs...)
		return err
	}

	prefix := spec.TicketKey + ":"
	ownedTabs := make(map[string]bool)
	for _, tab := range tabs {
		if tab.WorkspaceID == workspaceID && strings.HasPrefix(tab.Label, prefix) {
			ownedTabs[tab.ID] = true
		}
	}
	closed := 0
	for _, pane := range panes {
		if pane.WorkspaceID != workspaceID || pane.ID == "" {
			continue
		}
		if !strings.HasPrefix(pane.Label, prefix) && !ownedTabs[pane.TabID] {
			continue
		}
		if err := a.CloseTerminal(ctx, runner.Terminal{ID: pane.ID, Title: pane.Label}); err != nil {
			logOutcome("close-terminals", "error", attrs...)
			return fmt.Errorf("herdr: close ticket pane %q: %w", pane.ID, err)
		}
		closed++
	}
	args := append([]any(nil), attrs...)
	args = append(args, "closed", closed)
	logOutcome("close-terminals", "ok", args...)
	return nil
}

// CleanupRun releases the runner-owned resources for the run: node panes and
// the ticket workspace. The Git worktree, its branch, and its files are
// deliberately preserved; a later run reopens the same checkout.
func (a *adapter) CleanupRun(ctx context.Context, spec runner.RunSpec) error {
	attrs := []any{"ticket", spec.TicketKey, "runID", string(spec.RunID)}
	logCall("cleanup-run", attrs...)
	if err := a.CloseTerminals(ctx, spec); err != nil {
		logOutcome("cleanup-run", "error", attrs...)
		return err
	}
	workspaceID, found, err := a.ticketWorkspace(ctx, spec)
	if err != nil {
		logOutcome("cleanup-run", "error", attrs...)
		return err
	}
	if !found {
		logOutcome("cleanup-run", "no-environment", attrs...)
		return nil
	}
	if err := a.cli.CloseWorkspace(ctx, workspaceID); err != nil {
		if errors.Is(err, herdrcli.ErrWorkspaceNotFound) {
			logOutcome("cleanup-run", "no-environment", attrs...)
			return nil
		}
		logOutcome("cleanup-run", "error", attrs...)
		return err
	}
	logOutcome("cleanup-run", "ok", attrs...)
	return nil
}

// --- Command rendering ---

// shellCommand renders the structured command as one shell line with env
// assignments. Binding the environment to the launch rather than to the pane
// keeps a reused pane from inheriting a previous run's values.
func shellCommand(command runner.Command) string {
	var b strings.Builder
	for _, key := range sortedKeys(command.Env) {
		b.WriteString(key)
		b.WriteString("=")
		b.WriteString(shellQuote(command.Env[key]))
		b.WriteString(" ")
	}
	b.WriteString(shellQuote(command.Executable))
	for _, arg := range command.Args {
		b.WriteString(" ")
		b.WriteString(shellQuote(arg))
	}
	return b.String()
}

func shellQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", `'\''`) + "'"
}

func sortedKeys(values map[string]string) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func normalizePath(path string) string {
	if path == "" {
		return ""
	}
	absolute, err := filepath.Abs(path)
	if err != nil {
		return filepath.Clean(path)
	}
	resolved, err := filepath.EvalSymlinks(absolute)
	if err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			return filepath.Clean(absolute)
		}
		return filepath.Clean(absolute)
	}
	return filepath.Clean(resolved)
}

var _ runner.Runner = (*adapter)(nil)
