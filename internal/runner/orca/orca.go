// Package orca is the Orca runner adapter. It owns ticket-scoped worktrees
// (environments), terminals, liveness, and cleanup. It does not know
// task-system fields, workflow routes, report contents, or agent command
// syntax.
//
// 9.5 external-call logging: every adapter boundary emits one debug line
// BEFORE the call (operation, ticket/runID, title when applicable) and one
// info line AFTER with only the outcome (ok/error), never payloads.
package orca

import (
	"context"
	"fmt"
	"log/slog"
	"strings"

	"github.com/rajpopat27/relay-flow/internal/config"
	"github.com/rajpopat27/relay-flow/internal/runner"
	"github.com/rajpopat27/relay-flow/internal/runner/orca/orcacli"
)

// Config is the adapter-owned root runnerConfig.
type Config struct {
	// BaseRef is the base branch for ticket worktrees (e.g. "main").
	BaseRef string `yaml:"baseRef,omitempty"`
}

func init() {
	runner.Register("orca", func(raw config.RawValues) (runner.Runner, error) {
		return New(orcacli.New(), raw)
	})
}

// adapter is the Orca runner.Runner. It is safe for concurrent use; the CLI
// client owns subprocess serialization.
type adapter struct {
	cli orcacli.Client
	cfg Config
}

// New constructs the adapter from root runnerConfig around an explicit CLI
// seam (tests inject a fake Client).
func New(cli orcacli.Client, raw config.RawValues) (runner.Runner, error) {
	var cfg Config
	if err := config.DecodeStrict(raw, &cfg); err != nil {
		return nil, fmt.Errorf("orca runnerConfig: %w", err)
	}
	return &adapter{cli: cli, cfg: cfg}, nil
}

// --- Repos ---

// DiscoverRepos returns Orca-registered repos as registration candidates.
func (a *adapter) DiscoverRepos(ctx context.Context) ([]runner.RepoCandidate, error) {
	repos, err := a.cli.ListRepos(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]runner.RepoCandidate, 0, len(repos))
	for _, r := range repos {
		out = append(out, runner.RepoCandidate{Name: r.DisplayName, Path: r.Path})
	}
	return out, nil
}

// ValidateRepo verifies the named repo exists in Orca at the given path.
func (a *adapter) ValidateRepo(ctx context.Context, name, path string) error {
	if _, err := a.repoID(ctx, name, path); err != nil {
		return err
	}
	return nil
}

// repoID resolves a registered repo to its Orca repo ID. The repo path is
// the stable identity (the Orca repo ID is an internal detail that can
// change across machines); name is matched as a secondary check.
func (a *adapter) repoID(ctx context.Context, name, path string) (string, error) {
	repos, err := a.cli.ListRepos(ctx)
	if err != nil {
		return "", err
	}
	for _, r := range repos {
		if r.Path == path && r.DisplayName == name {
			return r.ID, nil
		}
	}
	return "", fmt.Errorf("orca: repo %q at %q not registered", name, path)
}

// --- Environment ---

// EnsureEnvironment returns the ticket-scoped worktree, creating it from the
// repo's main worktree and configured base ref when absent.
func (a *adapter) EnsureEnvironment(ctx context.Context, spec runner.RunSpec) (runner.Environment, error) {
	slog.Debug("orca call",
		"op", "ensure-environment", "ticket", spec.TicketKey,
		"runID", string(spec.RunID), "repo", spec.RepoName)
	env, reused, err := a.ensureEnvironment(ctx, spec)
	attrs := []any{
		"op", "ensure-environment", "ticket", spec.TicketKey,
		"runID", string(spec.RunID), "repo", spec.RepoName,
	}
	if err != nil {
		attrs = append(attrs, "result", "error", "error", sanitizeErr(err))
	} else if reused {
		attrs = append(attrs, "result", "exists")
	} else {
		attrs = append(attrs, "result", "created")
	}
	slog.Info("orca outcome", attrs...)
	return env, err
}

// ensureEnvironment is the unlogged body factored out so the public method
// can emit one outcome line (created/exists/error) without duplicate
// logging on the inner re-reads.
func (a *adapter) ensureEnvironment(ctx context.Context, spec runner.RunSpec) (runner.Environment, bool, error) {
	repoID, err := a.repoID(ctx, spec.RepoName, spec.RepoPath)
	if err != nil {
		return runner.Environment{}, false, err
	}
	wts, err := a.cli.ListWorktrees(ctx)
	if err != nil {
		return runner.Environment{}, false, err
	}
	var main *orcacli.Worktree
	for i := range wts {
		w := &wts[i]
		if w.RepoID != repoID {
			continue
		}
		if w.DisplayName == spec.TicketKey {
			return runner.Environment{ID: w.ID, Path: w.Path}, true, nil
		}
		if w.IsMainWorktree {
			main = w
		}
	}
	if main == nil {
		return runner.Environment{}, false, fmt.Errorf("orca: repo %q has no main worktree", spec.RepoName)
	}
	baseRef := a.cfg.BaseRef
	if baseRef == "" {
		baseRef = "main"
	}
	if err := a.cli.CreateWorktree(ctx, spec.TicketKey, repoID, main.ID, baseRef); err != nil {
		return runner.Environment{}, false, err
	}
	// Re-read to return the created worktree's identity.
	wts, err = a.cli.ListWorktrees(ctx)
	if err != nil {
		return runner.Environment{}, false, err
	}
	for _, w := range wts {
		if w.RepoID == repoID && w.DisplayName == spec.TicketKey {
			return runner.Environment{ID: w.ID, Path: w.Path}, false, nil
		}
	}
	return runner.Environment{}, false, fmt.Errorf("orca: worktree %q not found after create", spec.TicketKey)
}

// --- Terminals ---

// FindTerminal returns the terminal titled exactly title in the environment
// when it is live and usable; stale/disconnected records are treated as
// absent.
func (a *adapter) FindTerminal(ctx context.Context, env runner.Environment, title string) (runner.Terminal, bool, error) {
	slog.Debug("orca call", "op", "find-terminal", "title", title, "envID", env.ID)
	terms, err := a.cli.ListTerminals(ctx, "id:"+env.ID)
	if err != nil {
		slog.Info("orca outcome", "op", "find-terminal", "title", title, "result", "error", "error", sanitizeErr(err))
		return runner.Terminal{}, false, err
	}
	for _, t := range terms {
		if t.Title == title && t.Connected {
			slog.Info("orca outcome", "op", "find-terminal", "title", title, "result", "found")
			return runner.Terminal{ID: t.Handle, Title: t.Title}, true, nil
		}
	}
	slog.Info("orca outcome", "op", "find-terminal", "title", title, "result", "absent")
	return runner.Terminal{}, false, nil
}

// CloseTerminal closes one terminal by handle.
func (a *adapter) CloseTerminal(ctx context.Context, terminal runner.Terminal) error {
	slog.Debug("orca call", "op", "close-terminal", "title", terminal.Title, "handle", terminal.ID)
	err := a.cli.CloseTerminal(ctx, terminal.ID)
	if err != nil {
		slog.Info("orca outcome", "op", "close-terminal", "title", terminal.Title, "result", "error", "error", sanitizeErr(err))
	} else {
		slog.Info("orca outcome", "op", "close-terminal", "title", terminal.Title, "result", "ok")
	}
	return err
}

// EnsureTerminal is idempotent: it returns the live terminal with the stable
// title when present, otherwise creates one running command. The title
// contains only <ticket>:<node>; visit metadata lives in the command's
// environment, never the title.
func (a *adapter) EnsureTerminal(ctx context.Context, env runner.Environment, title string, command runner.Command) (runner.Terminal, error) {
	slog.Debug("orca call", "op", "ensure-terminal", "title", title, "envID", env.ID)
	if t, ok, err := a.FindTerminal(ctx, env, title); err != nil {
		slog.Info("orca outcome", "op", "ensure-terminal", "title", title, "result", "error", "error", sanitizeErr(err))
		return runner.Terminal{}, err
	} else if ok {
		slog.Info("orca outcome", "op", "ensure-terminal", "title", title, "result", "exists")
		return t, nil
	}
	// The worktree display name is the ticket key (EnsureEnvironment names
	// it after the ticket).
	name := strings.SplitN(title, ":", 2)[0]
	handle, err := a.cli.CreateTerminal(ctx, name, title, shellCommand(command))
	if err != nil {
		slog.Info("orca outcome", "op", "ensure-terminal", "title", title, "result", "error", "error", sanitizeErr(err))
		return runner.Terminal{}, err
	}
	slog.Info("orca outcome", "op", "ensure-terminal", "title", title, "result", "created")
	return runner.Terminal{ID: handle, Title: title}, nil
}

// sanitizeErr strips the leading "orca [args...]:" prefix from orcacli
// errors so info-level outcome lines never leak argv payloads (notably the
// --command string built by shellCommand, which carries the agent prompt
// and RELAY_FLOW_* env). Keeps the trailing stderr/exit fragment.
func sanitizeErr(err error) string {
	if err == nil {
		return ""
	}
	s := err.Error()
	// "orca terminal create: orca [args...]: <err>: <out>" — drop the
	// "[args...]" middle.
	if i := strings.Index(s, "]: "); i >= 0 {
		// Keep any "orca <words>:" prefix before the "[" (e.g. the
		// "orca terminal create:" wrap), then append the post-] tail.
		prefix := ""
		if j := strings.Index(s, "["); j > 0 {
			prefix = strings.TrimSuffix(s[:j], " ")
		}
		if prefix != "" {
			return prefix + "]: " + s[i+3:]
		}
		return s[i+3:]
	}
	return s
}

// shellCommand renders the structured command as one shell line with env
// assignments; the runner executes it but never constructs it.
func shellCommand(c runner.Command) string {
	var b strings.Builder
	for _, k := range sortedKeys(c.Env) {
		b.WriteString(k)
		b.WriteString("=")
		b.WriteString(shellQuote(c.Env[k]))
		b.WriteString(" ")
	}
	b.WriteString(shellQuote(c.Executable))
	for _, arg := range c.Args {
		b.WriteString(" ")
		b.WriteString(shellQuote(arg))
	}
	return b.String()
}

func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

func sortedKeys(m map[string]string) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	for i := 1; i < len(keys); i++ {
		for j := i; j > 0 && keys[j] < keys[j-1]; j-- {
			keys[j], keys[j-1] = keys[j-1], keys[j]
		}
	}
	return keys
}

// --- Cleanup ---

// CloseTerminals closes the run's agent terminals while preserving the
// worktree and any non-run tabs (setup, user shells). Run-owned terminals
// are identified by the stable <ticket>:<node> title prefix.
func (a *adapter) CloseTerminals(ctx context.Context, spec runner.RunSpec) error {
	slog.Debug("orca call", "op", "close-terminals", "ticket", spec.TicketKey, "runID", string(spec.RunID))
	env, ok, err := a.findEnvironment(ctx, spec)
	if err != nil || !ok {
		if err != nil {
			slog.Info("orca outcome", "op", "close-terminals", "ticket", spec.TicketKey, "result", "error", "error", sanitizeErr(err))
		} else {
			slog.Info("orca outcome", "op", "close-terminals", "ticket", spec.TicketKey, "result", "no-environment")
		}
		return err
	}
	terms, err := a.cli.ListTerminals(ctx, "id:"+env.ID)
	if err != nil {
		slog.Info("orca outcome", "op", "close-terminals", "ticket", spec.TicketKey, "result", "error", "error", sanitizeErr(err))
		return err
	}
	prefix := spec.TicketKey + ":"
	closed := 0
	for _, t := range terms {
		if !strings.HasPrefix(t.Title, prefix) {
			continue
		}
		// 9.5: one outcome line per actual terminal close, with its title.
		slog.Debug("orca call", "op", "close-terminal", "title", t.Title, "handle", t.Handle)
		if err := a.cli.CloseTerminal(ctx, t.Handle); err != nil {
			slog.Info("orca outcome", "op", "close-terminal", "title", t.Title, "result", "error", "error", sanitizeErr(err))
			slog.Info("orca outcome", "op", "close-terminals", "ticket", spec.TicketKey, "result", "error", "error", sanitizeErr(err))
			return fmt.Errorf("close terminal %q: %w", t.Title, err)
		}
		slog.Info("orca outcome", "op", "close-terminal", "title", t.Title, "result", "ok")
		closed++
	}
	slog.Info("orca outcome", "op", "close-terminals", "ticket", spec.TicketKey, "result", "ok", "closed", closed)
	return nil
}

// CleanupRun removes all runner-owned run resources: terminals, then the
// ticket worktree itself.
func (a *adapter) CleanupRun(ctx context.Context, spec runner.RunSpec) error {
	slog.Debug("orca call", "op", "cleanup-run", "ticket", spec.TicketKey, "runID", string(spec.RunID))
	if err := a.CloseTerminals(ctx, spec); err != nil {
		slog.Info("orca outcome", "op", "cleanup-run", "ticket", spec.TicketKey, "result", "error", "error", sanitizeErr(err))
		return err
	}
	env, ok, err := a.findEnvironment(ctx, spec)
	if err != nil || !ok {
		if err != nil {
			slog.Info("orca outcome", "op", "cleanup-run", "ticket", spec.TicketKey, "result", "error", "error", sanitizeErr(err))
		} else {
			slog.Info("orca outcome", "op", "cleanup-run", "ticket", spec.TicketKey, "result", "no-environment")
		}
		return err
	}
	err = a.cli.DeleteWorktree(ctx, env.ID)
	if err != nil {
		slog.Info("orca outcome", "op", "cleanup-run", "ticket", spec.TicketKey, "result", "error", "error", sanitizeErr(err))
	} else {
		slog.Info("orca outcome", "op", "cleanup-run", "ticket", spec.TicketKey, "result", "ok")
	}
	return err
}

// findEnvironment locates the ticket worktree without creating it.
func (a *adapter) findEnvironment(ctx context.Context, spec runner.RunSpec) (runner.Environment, bool, error) {
	repoID, err := a.repoID(ctx, spec.RepoName, spec.RepoPath)
	if err != nil {
		return runner.Environment{}, false, err
	}
	wts, err := a.cli.ListWorktrees(ctx)
	if err != nil {
		return runner.Environment{}, false, err
	}
	for _, w := range wts {
		if w.RepoID == repoID && w.DisplayName == spec.TicketKey {
			return runner.Environment{ID: w.ID, Path: w.Path}, true, nil
		}
	}
	return runner.Environment{}, false, nil
}
