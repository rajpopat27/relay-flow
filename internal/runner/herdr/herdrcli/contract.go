// Package herdrcli defines the narrow internal boundary between the Herdr
// runner adapter and the public Herdr CLI.
package herdrcli

import (
	"context"
	"errors"
)

// Herdr reports failures as JSON error envelopes on stderr with a stable
// code. These sentinels expose the codes the adapter must react to; every
// other failure is returned unchanged so it can be retried.
var (
	ErrPaneNotFound      = errors.New("pane not found")
	ErrWorkspaceNotFound = errors.New("workspace not found")
	ErrWorktreeNotFound  = errors.New("worktree not found")
	ErrNotGitWorktree    = errors.New("path is not inside a Git work tree")
)

// Client is the adapter-facing Herdr CLI contract. The production adapter
// receives a *CLI; tests may provide a client implementing this interface.
type Client interface {
	Snapshot(ctx context.Context) (Snapshot, error)
	WorktreeList(ctx context.Context, repoPath string) (WorktreeListing, error)
	WorktreeCreate(ctx context.Context, repoPath, branch, base, label string) (Workspace, error)
	WorktreeOpen(ctx context.Context, repoPath, branch, label string) (Workspace, error)
	CreateTab(ctx context.Context, workspaceID, cwd, label string) (Tab, Pane, error)
	ListTabs(ctx context.Context, workspaceID string) ([]Tab, error)
	ListPanes(ctx context.Context, workspaceID string) ([]Pane, error)
	GetPane(ctx context.Context, paneID string) (Pane, error)
	ProcessInfo(ctx context.Context, paneID string) (ProcessInfo, error)
	RenamePane(ctx context.Context, paneID, label string) error
	RunPane(ctx context.Context, paneID, command string) error
	ClosePane(ctx context.Context, paneID string) error
	CloseWorkspace(ctx context.Context, workspaceID string) error
}

// Options selects the Herdr session or explicit socket used by the CLI.
type Options struct {
	Session    string
	SocketPath string
}

// CLI is the production client backed by the herdr executable.
type CLI struct {
	options Options
}

// New constructs a production Herdr CLI client.
func New(options Options) *CLI {
	return &CLI{options: options}
}

// WorkspaceWorktree is the Git identity Herdr reports for a workspace. The
// source checkout of a repository has IsLinked false; every ticket worktree
// workspace has IsLinked true and the same RepoRoot.
type WorkspaceWorktree struct {
	CheckoutPath string
	RepoName     string
	RepoRoot     string
	IsLinked     bool
}

// Workspace is the subset of Herdr workspace output used by relay-flow.
type Workspace struct {
	ID       string
	Label    string
	Worktree WorkspaceWorktree
}

// Worktree is one checkout reported by worktree list. OpenWorkspaceID is
// empty when the checkout exists but is not open as a workspace.
type Worktree struct {
	Path            string
	Branch          string
	IsLinked        bool
	OpenWorkspaceID string
}

// WorktreeSource identifies the repository a worktree listing belongs to.
type WorktreeSource struct {
	RepoName           string
	RepoRoot           string
	SourceCheckoutPath string
}

// WorktreeListing is the response of worktree list for one repository.
type WorktreeListing struct {
	Source    WorktreeSource
	Worktrees []Worktree
}

// Tab is the subset of Herdr tab output used by relay-flow.
type Tab struct {
	ID          string
	WorkspaceID string
	Label       string
}

// Pane is the subset of Herdr pane output used by relay-flow.
type Pane struct {
	ID          string
	TerminalID  string
	WorkspaceID string
	TabID       string
	Label       string
	CWD         string
}

// Snapshot contains the workspace, tab, and pane records returned by Herdr.
type Snapshot struct {
	Workspaces []Workspace
	Tabs       []Tab
	Panes      []Pane
}

// ProcessInfo contains the process records returned for a pane.
type ProcessInfo struct {
	PaneID              string
	ShellPID            *uint32
	ForegroundProcesses []ForegroundProcess
}

// ForegroundProcess is the observed process shape returned by Herdr.
type ForegroundProcess struct {
	PID  uint32
	Name string
}
