// Package orcacli wraps the `orca` CLI for worktree/terminal management.
// Every call is skippable via DryRun for safe local testing. All real
// `orca ... --json` output is wrapped as {id, ok, result: {...}}.
package orcacli

import (
	"encoding/json"
	"fmt"
	"log"
	"os/exec"
	"regexp"
	"strings"
)

type Client struct {
	DryRun bool
}

func New(dryRun bool) *Client {
	return &Client{DryRun: dryRun}
}

type Repo struct {
	ID          string `json:"id"`
	DisplayName string `json:"displayName"`
}

type Worktree struct {
	ID             string `json:"id"`
	RepoID         string `json:"repoId"`
	DisplayName    string `json:"displayName"`
	Branch         string `json:"branch"`
	Path           string `json:"path"`
	IsMainWorktree bool   `json:"isMainWorktree"`
}

// Terminal is a tab's persistent identity: Title is the tab-level title we
// set via --title (visualLayouts[].root.tabs[].title), which persists —
// unlike the pane-level title, which the running program (opencode) resets.
type Terminal struct {
	Handle    string
	Title     string
	Connected bool
}

// ListRepos returns all Orca-registered repos, used to resolve a Jira
// ticket's component name to a repoId (component name == repo displayName).
func (c *Client) ListRepos() ([]Repo, error) {
	if c.DryRun {
		log.Printf("[dry-run] orca repo list --json (skipped, returning empty)")
		return nil, nil
	}
	var res struct {
		Result struct {
			Repos []Repo `json:"repos"`
		} `json:"result"`
	}
	if err := runOrcaJSON(&res, "repo", "list", "--json"); err != nil {
		return nil, fmt.Errorf("orca repo list: %w", err)
	}
	return res.Result.Repos, nil
}

func (c *Client) WorktreeList() ([]Worktree, error) {
	if c.DryRun {
		log.Printf("[dry-run] orca worktree list --json (skipped, returning empty)")
		return nil, nil
	}
	var res struct {
		Result struct {
			Worktrees []Worktree `json:"worktrees"`
		} `json:"result"`
	}
	if err := runOrcaJSON(&res, "worktree", "list", "--json"); err != nil {
		return nil, fmt.Errorf("orca worktree list: %w", err)
	}
	return res.Result.Worktrees, nil
}

// WorktreeCreate always explicitly sets --parent-worktree and --base-branch
// (never relies on Orca's inferred defaults) so every ticket's worktree has
// a deliberate, known ancestry: main by default, or an explicit parent
// ticket's worktree/branch for subtasks.
func (c *Client) WorktreeCreate(ticketKey, repoID, parentWorktreeID, baseBranch string) error {
	if c.DryRun {
		log.Printf("[dry-run] orca worktree create --name %s --repo id:%s --parent-worktree worktree:%s --base-branch %s --json (skipped)", ticketKey, repoID, parentWorktreeID, baseBranch)
		return nil
	}
	return runOrca("worktree", "create", "--name", ticketKey, "--repo", "id:"+repoID,
		"--parent-worktree", "worktree:"+parentWorktreeID, "--base-branch", baseBranch, "--json")
}

// FindWorktree returns the worktree in repoID with the given displayName.
func (c *Client) FindWorktree(repoID, displayName string) (Worktree, bool, error) {
	wts, err := c.WorktreeList()
	if err != nil {
		return Worktree{}, false, err
	}
	for _, w := range wts {
		if w.RepoID == repoID && w.DisplayName == displayName {
			return w, true, nil
		}
	}
	return Worktree{}, false, nil
}

// FindExistingBranch looks for a branch containing ticketKey
// (prefix-agnostic — e.g. "Raj-Popat/KCC-1374" or "someone-else/KCC-1374"
// both match) in the repo checked out at repoPath, checking BOTH local and
// remote-tracking branches: Orca's worktree-create name-collision logic
// consults remote branches too, so a leftover origin/Raj-Popat/KCC-1377
// (pushed by an agent, local copy long deleted) would otherwise make Orca
// silently suffix the new worktree (-2, -3, ...) and desync every
// ticket-key-based lookup we do afterward.
//
// Return value is the ref to pass as --base-branch:
//   - local-only branch  -> its name as-is ("Raj-Popat/KCC-1377")
//   - remote-only branch -> the remote-qualified ref ("origin/Raj-Popat/KCC-1377");
//     Orca recognizes this as "create the worktree on this existing branch"
//     and does NOT suffix the worktree name (verified empirically).
func FindExistingBranch(repoPath, ticketKey string) (string, bool, error) {
	out, err := exec.Command("git", "-C", repoPath, "branch", "-a", "--list", "--format=%(refname:short)").CombinedOutput()
	if err != nil {
		return "", false, fmt.Errorf("git branch --list: %w: %s", err, out)
	}
	re := regexp.MustCompile(regexp.QuoteMeta(ticketKey))
	for _, line := range strings.Split(string(out), "\n") {
		branch := strings.TrimSpace(line)
		if branch != "" && re.MatchString(branch) {
			return branch, true, nil
		}
	}
	return "", false, nil
}

// MainWorktree returns repoID's main worktree (the one checked out on the
// repo's primary branch, e.g. main).
func (c *Client) MainWorktree(repoID string) (Worktree, bool, error) {
	wts, err := c.WorktreeList()
	if err != nil {
		return Worktree{}, false, err
	}
	for _, w := range wts {
		if w.RepoID == repoID && w.IsMainWorktree {
			return w, true, nil
		}
	}
	return Worktree{}, false, nil
}

// TerminalList returns tabs (with their persistent tab-level title) for a
// given worktree, e.g. "name:KCC-1373". --include-visual-layouts is
// mandatory: orca omits visualLayouts from JSON without it, which would
// make every lookup return zero tabs and the daemon would spawn duplicate
// terminals on every poll.
func (c *Client) TerminalList(worktree string) ([]Terminal, error) {
	if c.DryRun {
		log.Printf("[dry-run] orca terminal list --worktree %s --json (skipped, returning empty)", worktree)
		return nil, nil
	}
	var res struct {
		Result struct {
			VisualLayouts []struct {
				Root struct {
					Tabs []struct {
						Title string `json:"title"`
						Panes struct {
							Handle    string `json:"handle"`
							Connected bool   `json:"connected"`
						} `json:"panes"`
					} `json:"tabs"`
				} `json:"root"`
			} `json:"visualLayouts"`
		} `json:"result"`
	}
	if err := runOrcaJSON(&res, "terminal", "list", "--worktree", worktree, "--include-visual-layouts", "--json"); err != nil {
		return nil, fmt.Errorf("orca terminal list: %w", err)
	}
	var terms []Terminal
	for _, vl := range res.Result.VisualLayouts {
		for _, tab := range vl.Root.Tabs {
			terms = append(terms, Terminal{Handle: tab.Panes.Handle, Title: tab.Title, Connected: tab.Panes.Connected})
		}
	}
	return terms, nil
}

// TerminalCreate launches the given shell command in a fresh terminal on
// the ticket's worktree. (orca terminal create has no --agent/--prompt
// flags — those exist only on `worktree create` — so the opencode
// invocation, with its RELAY_* env markers, is a --command line.)
func (c *Client) TerminalCreate(ticketKey, title, command string) (string, error) {
	if c.DryRun {
		log.Printf("[dry-run] orca terminal create --worktree name:%s --title %q --command %q --json (skipped)", ticketKey, title, command)
		return "dry-run-handle", nil
	}
	var res struct {
		Result struct {
			Terminal struct {
				Handle string `json:"handle"`
			} `json:"terminal"`
		} `json:"result"`
	}
	if err := runOrcaJSON(&res, "terminal", "create",
		"--worktree", "name:"+ticketKey,
		"--title", title,
		"--command", command,
		"--json"); err != nil {
		return "", fmt.Errorf("orca terminal create: %w", err)
	}
	return res.Result.Terminal.Handle, nil
}

func (c *Client) TerminalWait(handle, forState string, timeoutMs int) error {
	if c.DryRun {
		log.Printf("[dry-run] orca terminal wait --terminal %s --for %s --timeout-ms %d --json (skipped)", handle, forState, timeoutMs)
		return nil
	}
	return runOrca("terminal", "wait",
		"--terminal", handle,
		"--for", forState,
		"--timeout-ms", fmt.Sprintf("%d", timeoutMs),
		"--json")
}

func (c *Client) TerminalClose(handle string) error {
	if c.DryRun {
		log.Printf("[dry-run] orca terminal close --terminal %s (skipped)", handle)
		return nil
	}
	return runOrca("terminal", "close", "--terminal", handle)
}

// TerminalSend types text into an existing terminal and presses Enter.
// text must be pre-flattened to a single line by the caller — send works
// via keystroke simulation, so an embedded newline would submit early.
func (c *Client) TerminalSend(handle, text string) error {
	if c.DryRun {
		log.Printf("[dry-run] orca terminal send --terminal %s --text %q --enter --json (skipped)", handle, text)
		return nil
	}
	return runOrca("terminal", "send", "--terminal", handle, "--text", text, "--enter", "--json")
}

func runOrca(args ...string) error {
	out, err := exec.Command("orca", args...).CombinedOutput()
	if err != nil {
		return fmt.Errorf("orca %v: %w: %s", args, err, string(out))
	}
	return nil
}

func runOrcaJSON(dest any, args ...string) error {
	out, err := exec.Command("orca", args...).Output()
	if err != nil {
		// orca prints its JSON error envelope to stdout even on failure —
		// surface it so callers can match on codes like selector_not_found.
		return fmt.Errorf("%w: %s", err, strings.TrimSpace(string(out)))
	}
	if err := json.Unmarshal(out, dest); err != nil {
		return fmt.Errorf("parse json: %w", err)
	}
	return nil
}
