// Package orcacli wraps the Orca CLI behind a small fakeable interface.
// Every call is real; there is no dry-run mode.
package orcacli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"regexp"
	"strings"
)

var ErrTerminalUnavailable = errors.New("terminal unavailable")

// Repo is an Orca-registered repository.
type Repo struct {
	ID          string `json:"id"`
	DisplayName string `json:"displayName"`
	Path        string `json:"path"`
}

// Worktree is an Orca worktree (a runner environment).
type Worktree struct {
	ID             string `json:"id"`
	RepoID         string `json:"repoId"`
	DisplayName    string `json:"displayName"`
	Branch         string `json:"branch"`
	Path           string `json:"path"`
	IsMainWorktree bool   `json:"isMainWorktree"`
}

// Terminal is a tab's persistent identity: Title is the tab-level title set
// via --title, which persists — unlike the pane-level title, which the
// running program resets.
type Terminal struct {
	Handle    string
	Title     string
	Connected bool
}

// Client is the fakeable Orca CLI seam used by the Orca runner adapter.
type Client interface {
	ListRepos(ctx context.Context) ([]Repo, error)
	ListWorktrees(ctx context.Context) ([]Worktree, error)
	CreateWorktree(ctx context.Context, ticketKey, repoID, parentWorktreeID, baseBranch string) error
	SetWorktreeStatus(ctx context.Context, worktreeID, status string) error
	DeleteWorktree(ctx context.Context, worktreeID string) error
	ShowTerminal(ctx context.Context, handle string) (Terminal, error)
	SendTerminal(ctx context.Context, handle, text string) error
	ListTerminals(ctx context.Context, worktree string) ([]Terminal, error)
	CreateTerminal(ctx context.Context, ticketKey, title, command string) (handle string, err error)
	CloseTerminal(ctx context.Context, handle string) error
}

// CLI is the production Client backed by the orca binary.
type CLI struct{}

func New() *CLI { return &CLI{} }

func (CLI) ListRepos(ctx context.Context) ([]Repo, error) {
	var res struct {
		Result struct {
			Repos []Repo `json:"repos"`
		} `json:"result"`
	}
	if err := runJSON(ctx, &res, "repo", "list", "--json"); err != nil {
		return nil, fmt.Errorf("orca repo list: %w", err)
	}
	return res.Result.Repos, nil
}

func (CLI) ListWorktrees(ctx context.Context) ([]Worktree, error) {
	var res struct {
		Result struct {
			Worktrees []Worktree `json:"worktrees"`
		} `json:"result"`
	}
	if err := runJSON(ctx, &res, "worktree", "list", "--json"); err != nil {
		return nil, fmt.Errorf("orca worktree list: %w", err)
	}
	return res.Result.Worktrees, nil
}

// CreateWorktree always sets --parent-worktree and --base-branch explicitly
// (never Orca's inferred defaults) so every ticket worktree has a
// deliberate, known ancestry.
func (CLI) CreateWorktree(ctx context.Context, ticketKey, repoID, parentWorktreeID, baseBranch string) error {
	return run(ctx, "worktree", "create", "--name", ticketKey, "--repo", "id:"+repoID,
		"--parent-worktree", "worktree:"+parentWorktreeID, "--base-branch", baseBranch, "--json")
}

func (CLI) SetWorktreeStatus(ctx context.Context, worktreeID, status string) error {
	return run(ctx, "worktree", "set", "--worktree", "id:"+worktreeID, "--workspace-status", status, "--json")
}

// FindExistingBranch returns the first local or remote-tracking branch whose
// short ref contains ticketKey. The returned ref is suitable for Orca's
// --base-branch argument.
func FindExistingBranch(repoPath, ticketKey string) (string, bool, error) {
	out, err := exec.Command("git", "-C", repoPath, "branch", "-a", "--list", "--format=%(refname:short)").CombinedOutput()
	if err != nil {
		return "", false, fmt.Errorf("git branch --list: %w: %s", err, strings.TrimSpace(string(out)))
	}
	match := regexp.MustCompile(regexp.QuoteMeta(ticketKey))
	for _, line := range strings.Split(string(out), "\n") {
		branch := strings.TrimSpace(line)
		if branch != "" && match.MatchString(branch) {
			return branch, true, nil
		}
	}
	return "", false, nil
}

func (CLI) DeleteWorktree(ctx context.Context, worktreeID string) error {
	return run(ctx, "worktree", "rm", "--worktree", "id:"+worktreeID, "--json")
}

func (CLI) ShowTerminal(ctx context.Context, handle string) (Terminal, error) {
	var res struct {
		Result struct {
			Terminal struct {
				Handle    string `json:"handle"`
				Title     string `json:"title"`
				Connected bool   `json:"connected"`
				Writable  bool   `json:"writable"`
			} `json:"terminal"`
		} `json:"result"`
	}
	if err := runJSON(ctx, &res, "terminal", "show", "--terminal", handle, "--json"); err != nil {
		if strings.Contains(err.Error(), "terminal_handle_stale") {
			return Terminal{}, ErrTerminalUnavailable
		}
		return Terminal{}, fmt.Errorf("orca terminal show: %w", err)
	}
	t := res.Result.Terminal
	return Terminal{Handle: t.Handle, Title: t.Title, Connected: t.Connected && t.Writable}, nil
}

func (CLI) SendTerminal(ctx context.Context, handle, text string) error {
	return run(ctx, "terminal", "send", "--terminal", handle, "--text", text, "--enter", "--json")
}

// ListTerminals returns tabs (with their persistent tab-level title) for a
// worktree, e.g. "name:PAY-101". --include-visual-layouts is mandatory:
// orca omits visualLayouts from JSON without it.
func (CLI) ListTerminals(ctx context.Context, worktree string) ([]Terminal, error) {
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
	if err := runJSON(ctx, &res, "terminal", "list", "--worktree", worktree, "--include-visual-layouts", "--json"); err != nil {
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

// CreateTerminal launches the given shell command in a fresh terminal on
// the ticket's worktree.
func (CLI) CreateTerminal(ctx context.Context, ticketKey, title, command string) (string, error) {
	var res struct {
		Result struct {
			Terminal struct {
				Handle string `json:"handle"`
			} `json:"terminal"`
		} `json:"result"`
	}
	if err := runJSON(ctx, &res, "terminal", "create",
		"--worktree", "name:"+ticketKey,
		"--title", title,
		"--command", command,
		"--json"); err != nil {
		return "", fmt.Errorf("orca terminal create: %w", err)
	}
	return res.Result.Terminal.Handle, nil
}

func (CLI) CloseTerminal(ctx context.Context, handle string) error {
	return run(ctx, "terminal", "close", "--terminal", handle, "--json")
}

func run(ctx context.Context, args ...string) error {
	cmd := exec.CommandContext(ctx, "orca", args...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("orca %v: %w: %s", args, err, strings.TrimSpace(string(out)))
	}
	return nil
}

func runJSON(ctx context.Context, dest any, args ...string) error {
	cmd := exec.CommandContext(ctx, "orca", args...)
	out, err := cmd.Output()
	if err != nil {
		// orca prints its JSON error envelope to stdout even on failure.
		return fmt.Errorf("orca %v: %w: %s", args, err, strings.TrimSpace(string(out)))
	}
	if err := json.Unmarshal(out, dest); err != nil {
		return fmt.Errorf("orca %v: parse json: %w", args, err)
	}
	return nil
}
