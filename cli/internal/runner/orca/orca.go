// Package orca is the built-in Runner adapter: it executes agent sessions
// as Orca terminals on per-ticket worktrees. It knows nothing about
// trackers — tickets arrive as tasks.Ticket values.
package orca

import (
	"fmt"
	"strings"
	"time"

	"github.com/rajpopat27/relayflow/cli/internal/opencode"
	"github.com/rajpopat27/relayflow/cli/internal/orcacli"
	"github.com/rajpopat27/relayflow/cli/internal/runner"
	"github.com/rajpopat27/relayflow/cli/internal/tasks"
)

func init() {
	runner.Register("orca", runner.Factory{
		UnmarshalConfig: unmarshalConfig,
		New: func(cfg any) (runner.Runner, error) {
			c, ok := cfg.(Config)
			if !ok {
				return nil, fmt.Errorf("internal: orca factory received %T", cfg)
			}
			return NewRunner(c, nil), nil
		},
	})
}

// Config is the strictly-unmarshalled runner.config for type orca.
// Empty today — worktree ancestry details (repo, parent) are passed by
// the server at construction, not committed in YAML.
type Config struct{}

func unmarshalConfig(m map[string]any) (any, error) {
	if len(m) > 0 {
		for k := range m {
			return nil, fmt.Errorf("unknown field %q (orca runner takes no config)", k)
		}
	}
	return Config{}, nil
}

// orcaCLI is the seam to the orca CLI. *orcacli.Client satisfies it;
// tests fake it.
type orcaCLI interface {
	WorktreeList() ([]orcacli.Worktree, error)
	WorktreeCreate(ticketKey, repoID, parentWorktreeID, baseBranch string) error
	FindWorktree(repoID, displayName string) (orcacli.Worktree, bool, error)
	MainWorktree(repoID string) (orcacli.Worktree, bool, error)
	TerminalList(worktree string) ([]orcacli.Terminal, error)
	TerminalCreate(ticketKey, title, command string) (string, error)
	TerminalWait(handle, forState string, timeoutMs int) error
	TerminalClose(handle string) error
	TerminalSend(handle, text string) error
}

type orcaRunner struct {
	repoID     string
	repoName   string // Jira component name; unused by the runner itself
	dryRun     bool
	orca       orcaCLI
	exists     func(string) (bool, error)
	findBranch func(repoPath, key string) (string, bool, error)
	sleep      func(time.Duration) // test seam for ensureWorktree retries
}

// NewRunner builds the orca runner. oc nil → real orcacli client (dryRun
// plumbed through). repoID is set by WithRepo at submit time.
func NewRunner(_ Config, oc orcaCLI) runner.Runner {
	r := &orcaRunner{
		exists:     opencode.Exists,
		findBranch: orcacli.FindExistingBranch,
		sleep:      time.Sleep,
	}
	if oc != nil {
		r.orca = oc
	}
	return r
}

// WithRepo binds the repo this runner serves (resolved server-side from
// the submitting client's cwd) and the dry-run flag.
func (r *orcaRunner) WithRepo(repoID, repoName string, dryRun bool) {
	r.repoID, r.repoName, r.dryRun = repoID, repoName, dryRun
	if r.orca == nil {
		r.orca = orcacli.New(dryRun)
	}
}

// title is the session identity: <key>:<agent>:<node>. Bounce and Close
// both match on it.
func title(t tasks.Ticket, node, agent string) string {
	return fmt.Sprintf("%s:%s:%s", t.Key, agent, node)
}

// Spawn ensures the ticket's worktree exists, then creates a fresh
// terminal titled key:agent:node running opencode with the RELAYFLOW_* env
// markers and the initial prompt. A fresh terminal per node visit:
// reusing an old session would leak the previous node's context.
func (r *orcaRunner) Spawn(t tasks.Ticket, node, agent, prompt string, env map[string]string) error {
	if ok, err := r.exists(agent); err != nil {
		return fmt.Errorf("verify opencode agent %q: %w", agent, err)
	} else if !ok {
		return fmt.Errorf("opencode agent %q does not exist", agent)
	}
	if err := r.ensureWorktree(t); err != nil {
		return fmt.Errorf("ensure worktree: %w", err)
	}
	command := buildCommand(env, agent, prompt)
	handle, err := r.orca.TerminalCreate(t.Key, title(t, node, agent), command)
	if err != nil {
		return fmt.Errorf("terminal create: %w", err)
	}
	// Best-effort: the wait only bounds how long Spawn blocks; the plugin
	// report is the real synchronization.
	_ = r.orca.TerminalWait(handle, "tui-idle", 10*60*1000)
	return nil
}

// buildCommand renders the opencode invocation typed into the new
// terminal, with RELAYFLOW_* env markers so the plugin can report back. A
// developer's own opencode session never has these set, so it never
// reports.
func buildCommand(env map[string]string, agent, prompt string) string {
	parts := make([]string, 0, len(env))
	// Deterministic order for logs/tests.
	for _, k := range []string{"RELAYFLOW_WORKFLOW", "RELAYFLOW_TICKET", "RELAYFLOW_NODE", "RELAYFLOW_AGENT"} {
		if v, ok := env[k]; ok {
			parts = append(parts, k+"="+shellQuote(v))
		}
	}
	return fmt.Sprintf("%s opencode --agent %s --prompt %s",
		strings.Join(parts, " "), shellQuote(agent), shellQuote(prompt))
}

func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// Find locates the live session for ticket+node by exact title. A
// missing worktree (selector_not_found — e.g. claim label survived a
// crash but the worktree didn't) means "no session", not an error.
func (r *orcaRunner) Find(t tasks.Ticket, node string) (runner.Session, bool, error) {
	terms, err := r.orca.TerminalList("name:" + t.Key)
	if err != nil {
		if strings.Contains(err.Error(), "selector_not_found") {
			return runner.Session{}, false, nil
		}
		return runner.Session{}, false, fmt.Errorf("terminal list: %w", err)
	}
	want := t.Key + ":"
	for _, term := range terms {
		if strings.HasPrefix(term.Title, want) && strings.HasSuffix(term.Title, ":"+node) {
			return runner.Session{ID: term.Handle, Title: term.Title}, true, nil
		}
	}
	return runner.Session{}, false, nil
}

// Nudge types a prompt into an existing session. The caller must have
// waited for idle first — typed text mid-turn corrupts the input box.
func (r *orcaRunner) Nudge(s runner.Session, prompt string) error {
	flat := strings.Join(strings.Fields(prompt), " ")
	if err := r.orca.TerminalWait(s.ID, "tui-idle", 3000); err != nil {
		return fmt.Errorf("session %q busy, nudge not delivered", s.Title)
	}
	return r.orca.TerminalSend(s.ID, flat)
}

// Close tears down every terminal titled <key>:* on the ticket's
// worktree. Scaffolding tabs ("Terminal 1", "Setup") are not ours and
// survive.
func (r *orcaRunner) Close(t tasks.Ticket) error {
	terms, err := r.orca.TerminalList("name:" + t.Key)
	if err != nil {
		return fmt.Errorf("terminal list: %w", err)
	}
	prefix := t.Key + ":"
	for _, term := range terms {
		if strings.HasPrefix(term.Title, prefix) {
			if err := r.orca.TerminalClose(term.Handle); err != nil {
				return fmt.Errorf("close %q: %w", term.Title, err)
			}
		}
	}
	return nil
}

// ensureWorktree creates the ticket's worktree if missing, verifying the
// exact name landed (Orca silently auto-suffixes on collisions).
func (r *orcaRunner) ensureWorktree(t tasks.Ticket) error {
	for attempt := 0; attempt < 3; attempt++ {
		if _, ok, err := r.orca.FindWorktree(r.repoID, t.Key); err != nil {
			return err
		} else if ok {
			return nil
		}
		if attempt < 2 {
			r.sleep(2 * time.Second)
		}
	}
	parentID, baseBranch, err := r.resolveWorktreeParent(t)
	if err != nil {
		return err
	}
	if err := r.orca.WorktreeCreate(t.Key, r.repoID, parentID, baseBranch); err != nil {
		return err
	}
	for attempt := 0; attempt < 3; attempt++ {
		if _, ok, err := r.orca.FindWorktree(r.repoID, t.Key); err != nil {
			return err
		} else if ok {
			return nil
		}
		if attempt < 2 {
			r.sleep(2 * time.Second)
		}
	}
	return fmt.Errorf("worktree %q not found after create (Orca likely auto-suffixed it on a name/branch collision) — clean up the suffixed worktree/branch manually", t.Key)
}

// resolveWorktreeParent picks the ancestry for a new ticket worktree:
// main worktree's branch, reused ticket branch if one exists. (The
// baseBranch-label and subtask-parent rules from v3 need ticket labels/
// parent info that tasks.Ticket doesn't carry — deferred; YAGNI until a
// tracker adapter exposes them.)
func (r *orcaRunner) resolveWorktreeParent(t tasks.Ticket) (parentWorktreeID, baseBranch string, err error) {
	w, ok, err := r.orca.MainWorktree(r.repoID)
	if err != nil {
		return "", "", err
	}
	if !ok {
		return "", "", fmt.Errorf("could not find main worktree for repo %s", r.repoID)
	}
	// A branch for this ticket may already exist (left over from a
	// removed worktree). Reuse it — Orca silently renames the worktree on
	// branch collision, desyncing every ticket-key lookup.
	if existing, ok, err := r.findBranch(w.Path, t.Key); err == nil && ok {
		return w.ID, existing, nil
	}
	return w.ID, w.Branch, nil
}
