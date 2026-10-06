// Package orcacli wraps the Orca CLI behind a small fakeable interface.
// Every call is real; there is no dry-run mode.
package orcacli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"time"
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

// CLI retains the selected launcher and its resolved path for all operations.
// Construction is local; runtime connectivity is checked by ListRepos.
type CLI struct {
	executable string
	lookupErr  error
}

// RepoRegistrar is the optional repository-provisioning seam used by the
// runner adapter. Keeping it separate from Client lets existing read-only
// test clients remain focused on the runtime operations they exercise.
type RepoRegistrar interface {
	AddRepo(ctx context.Context, path string) error
}

func New() *CLI {
	command := selectCommand(runtime.GOOS, os.Getenv("ORCA_CLI_COMMAND"), os.Getenv("ORCA_DEV_REPO_ROOT"))
	path, err := exec.LookPath(command)
	if err == nil {
		path, err = filepath.Abs(path)
	}
	if err != nil {
		path = command
	}
	return &CLI{executable: path, lookupErr: err}
}

// Orca's explicit session selector takes precedence over the dev launcher.
// Linux defaults to orca-ide, never the unrelated GNOME screen reader or a
// legacy alias/shim. Select once; there is no executable fallback.
func selectCommand(goos, command, devRoot string) string {
	if command != "" {
		return command
	}
	if devRoot != "" {
		return "orca-dev"
	}
	if goos == "linux" {
		return "orca-ide"
	}
	return "orca"
}

// AddRepo registers an existing local repository with Orca. Orca derives its
// display name from the path; relay-flow keeps the user-facing registration
// name separately.
func (c CLI) AddRepo(ctx context.Context, path string) error {
	return c.run(ctx, "repo", "add", "--path", path, "--json")
}

func (c CLI) ListRepos(ctx context.Context) ([]Repo, error) {
	var res struct {
		Result *struct {
			Repos []Repo `json:"repos"`
		} `json:"result"`
	}
	if err := c.runJSON(ctx, &res, "repo", "list", "--json"); err != nil {
		return nil, fmt.Errorf("orca repo list: %w", err)
	}
	if res.Result == nil || res.Result.Repos == nil {
		return nil, fmt.Errorf("orca repo list (%s): malformed response: missing result.repos array", c.executable)
	}
	return res.Result.Repos, nil
}

func (c CLI) ListWorktrees(ctx context.Context) ([]Worktree, error) {
	var res struct {
		Result struct {
			Worktrees []Worktree `json:"worktrees"`
		} `json:"result"`
	}
	if err := c.runJSON(ctx, &res, "worktree", "list", "--json"); err != nil {
		return nil, fmt.Errorf("orca worktree list: %w", err)
	}
	return res.Result.Worktrees, nil
}

// CreateWorktree always sets --parent-worktree and --base-branch explicitly
// (never Orca's inferred defaults) so every ticket worktree has a
// deliberate, known ancestry.
func (c CLI) CreateWorktree(ctx context.Context, ticketKey, repoID, parentWorktreeID, baseBranch string) error {
	return c.run(ctx, "worktree", "create", "--name", ticketKey, "--repo", "id:"+repoID,
		"--parent-worktree", "worktree:"+parentWorktreeID, "--base-branch", baseBranch, "--json")
}

func (c CLI) SetWorktreeStatus(ctx context.Context, worktreeID, status string) error {
	return c.run(ctx, "worktree", "set", "--worktree", "id:"+worktreeID, "--workspace-status", status, "--json")
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

func (c CLI) DeleteWorktree(ctx context.Context, worktreeID string) error {
	return c.run(ctx, "worktree", "rm", "--worktree", "id:"+worktreeID, "--json")
}

func (c CLI) ShowTerminal(ctx context.Context, handle string) (Terminal, error) {
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
	if err := c.runJSON(ctx, &res, "terminal", "show", "--terminal", handle, "--json"); err != nil {
		if strings.Contains(err.Error(), "terminal_handle_stale") {
			return Terminal{}, ErrTerminalUnavailable
		}
		return Terminal{}, fmt.Errorf("orca terminal show: %w", err)
	}
	t := res.Result.Terminal
	return Terminal{Handle: t.Handle, Title: t.Title, Connected: t.Connected && t.Writable}, nil
}

func (c CLI) SendTerminal(ctx context.Context, handle, text string) error {
	return c.run(ctx, "terminal", "send", "--terminal", handle, "--text", text, "--enter", "--json")
}

// ListTerminals returns tabs (with their persistent tab-level title) for a
// worktree, e.g. "name:PAY-101". --include-visual-layouts is mandatory:
// orca omits visualLayouts from JSON without it.
func (c CLI) ListTerminals(ctx context.Context, worktree string) ([]Terminal, error) {
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
	if err := c.runJSON(ctx, &res, "terminal", "list", "--worktree", worktree, "--include-visual-layouts", "--json"); err != nil {
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
func (c CLI) CreateTerminal(ctx context.Context, ticketKey, title, command string) (string, error) {
	var res struct {
		Result struct {
			Terminal struct {
				Handle string `json:"handle"`
			} `json:"terminal"`
		} `json:"result"`
	}
	if err := c.runJSON(ctx, &res, "terminal", "create",
		"--worktree", "name:"+ticketKey,
		"--title", title,
		"--command", command,
		"--json"); err != nil {
		return "", fmt.Errorf("orca terminal create: %w", err)
	}
	return res.Result.Terminal.Handle, nil
}

func (c CLI) CloseTerminal(ctx context.Context, handle string) error {
	if err := c.run(ctx, "terminal", "close", "--terminal", handle, "--json"); err != nil {
		if strings.Contains(err.Error(), "terminal_handle_stale") {
			return ErrTerminalUnavailable
		}
		return err
	}
	return nil
}

func (c CLI) run(ctx context.Context, args ...string) error {
	if c.lookupErr != nil {
		return c.commandError(ctx, args, nil, c.lookupErr)
	}
	cmd := exec.CommandContext(ctx, c.executable, args...)
	cmd.WaitDelay = 100 * time.Millisecond
	out, err := cmd.CombinedOutput()
	if err != nil {
		return c.commandError(ctx, args, out, err)
	}
	return nil
}

func (c CLI) runJSON(ctx context.Context, dest any, args ...string) error {
	if c.lookupErr != nil {
		return c.commandError(ctx, args, nil, c.lookupErr)
	}
	cmd := exec.CommandContext(ctx, c.executable, args...)
	cmd.WaitDelay = 100 * time.Millisecond
	out, err := cmd.Output()
	if err != nil {
		// Orca prints JSON errors to stdout; launcher failures may use stderr.
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			out = append(out, exitErr.Stderr...)
		}
		return c.commandError(ctx, args, out, err)
	}
	if err := json.Unmarshal(out, dest); err != nil {
		return fmt.Errorf("orca %s (%s): malformed JSON response: %w", strings.Join(args[:2], " "), c.executable, err)
	}
	return nil
}

// Report safe operation identity, not argv containing commands or prompts.
func (c CLI) commandError(ctx context.Context, args []string, out []byte, err error) error {
	operation := strings.Join(args[:2], " ")
	if ctx.Err() != nil {
		return fmt.Errorf("orca %s (%s): timeout/cancellation: %w", operation, c.executable, ctx.Err())
	}
	if c.lookupErr != nil && (errors.Is(err, exec.ErrNotFound) || errors.Is(err, os.ErrNotExist)) {
		return fmt.Errorf("orca %s: missing executable %q: %w", operation, c.executable, err)
	}
	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) {
		return fmt.Errorf("orca %s (%s): launch failure: %w", operation, c.executable, err)
	}
	return fmt.Errorf("orca %s (%s): runtime failure: %w: %s", operation, c.executable, err, strings.TrimSpace(string(out)))
}
