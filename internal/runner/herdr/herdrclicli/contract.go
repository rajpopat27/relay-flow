// Package herdrclicli defines the narrow internal boundary between the Herdr
// runner adapter and the public Herdr CLI.
package herdrclicli

import "context"

// Client is the adapter-facing Herdr CLI contract. The production adapter
// receives a *CLI; tests may provide a client implementing this interface.
type Client interface {
	Snapshot(context.Context) (Snapshot, error)
	CreateTab(context.Context, string, string, string, map[string]string) (Tab, Pane, error)
	ListTabs(context.Context, string) ([]Tab, error)
	ListPanes(context.Context, string) ([]Pane, error)
	GetPane(context.Context, string) (Pane, error)
	ProcessInfo(context.Context, string) (ProcessInfo, error)
	RenamePane(context.Context, string, string) error
	RunPane(context.Context, string, string) error
	ClosePane(context.Context, string) error
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
func New(options ...Options) *CLI {
	var option Options
	if len(options) > 0 {
		option = options[0]
	}
	return &CLI{options: option}
}

// Workspace is the subset of Herdr workspace output used by relay-flow.
type Workspace struct {
	ID    string
	Label string
}

// Tab is the subset of Herdr tab output used by relay-flow.
type Tab struct {
	ID          string
	WorkspaceID string
	Label       string
}

// Pane is the subset of Herdr pane output used by relay-flow.
type Pane struct {
	ID            string
	TerminalID    string
	WorkspaceID   string
	TabID         string
	Label         string
	CWD           string
	ForegroundCWD string
}

// Snapshot contains the workspace, tab, and pane records returned by Herdr.
type Snapshot struct {
	Workspaces []Workspace
	Tabs       []Tab
	Panes      []Pane
}

// ProcessInfo contains the process records returned for a pane.
type ProcessInfo struct {
	PaneID                   string
	ShellPID                 *uint32
	ForegroundProcessGroupID *uint32
	ForegroundProcesses      []ForegroundProcess
}

// ForegroundProcess is the observed process shape returned by Herdr.
type ForegroundProcess struct {
	PID     uint32
	Name    string
	Cmdline string
	CWD     string
	Argv0   string
	Argv    []string
}
