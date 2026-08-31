package herdrclicli

import (
	"context"
	"sort"
)

type workspaceResponse struct {
	ID    string `json:"workspace_id"`
	Label string `json:"label"`
}

type tabResponse struct {
	ID          string `json:"tab_id"`
	WorkspaceID string `json:"workspace_id"`
	Label       string `json:"label"`
}

type paneResponse struct {
	ID            string `json:"pane_id"`
	TerminalID    string `json:"terminal_id"`
	WorkspaceID   string `json:"workspace_id"`
	TabID         string `json:"tab_id"`
	Label         string `json:"label"`
	CWD           string `json:"cwd"`
	ForegroundCWD string `json:"foreground_cwd"`
}

type processInfoResponse struct {
	PaneID                   string                      `json:"pane_id"`
	ShellPID                 *uint32                     `json:"shell_pid"`
	ForegroundProcessGroupID *uint32                     `json:"foreground_process_group_id"`
	ForegroundProcesses      []foregroundProcessResponse `json:"foreground_processes"`
}

type foregroundProcessResponse struct {
	PID     uint32   `json:"pid"`
	Name    string   `json:"name"`
	Cmdline string   `json:"cmdline"`
	CWD     string   `json:"cwd"`
	Argv0   string   `json:"argv0"`
	Argv    []string `json:"argv"`
}

func (c *CLI) Snapshot(ctx context.Context) (Snapshot, error) {
	var response struct {
		Snapshot struct {
			Workspaces []workspaceResponse `json:"workspaces"`
			Tabs       []tabResponse       `json:"tabs"`
			Panes      []paneResponse      `json:"panes"`
		} `json:"snapshot"`
	}
	if err := c.runJSON(ctx, "api snapshot", &response, "api", "snapshot"); err != nil {
		return Snapshot{}, err
	}
	return Snapshot{
		Workspaces: convertWorkspaces(response.Snapshot.Workspaces),
		Tabs:       convertTabs(response.Snapshot.Tabs),
		Panes:      convertPanes(response.Snapshot.Panes),
	}, nil
}

func (c *CLI) CreateTab(ctx context.Context, workspaceID, cwd, label string, env map[string]string) (Tab, Pane, error) {
	args := []string{
		"tab", "create",
		"--workspace", workspaceID,
		"--cwd", cwd,
		"--label", label,
		"--no-focus",
	}
	keys := make([]string, 0, len(env))
	for key := range env {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		args = append(args, "--env", key+"="+env[key])
	}

	var response struct {
		Tab      tabResponse  `json:"tab"`
		RootPane paneResponse `json:"root_pane"`
	}
	if err := c.runJSON(ctx, "tab create", &response, args...); err != nil {
		return Tab{}, Pane{}, err
	}
	return convertTab(response.Tab), convertPane(response.RootPane), nil
}

func (c *CLI) ListTabs(ctx context.Context, workspaceID string) ([]Tab, error) {
	var response struct {
		Tabs []tabResponse `json:"tabs"`
	}
	if err := c.runJSON(ctx, "tab list", &response, "tab", "list", "--workspace", workspaceID); err != nil {
		return nil, err
	}
	return convertTabs(response.Tabs), nil
}

func (c *CLI) ListPanes(ctx context.Context, workspaceID string) ([]Pane, error) {
	var response struct {
		Panes []paneResponse `json:"panes"`
	}
	if err := c.runJSON(ctx, "pane list", &response, "pane", "list", "--workspace", workspaceID); err != nil {
		return nil, err
	}
	return convertPanes(response.Panes), nil
}

func (c *CLI) GetPane(ctx context.Context, paneID string) (Pane, error) {
	var response struct {
		Pane paneResponse `json:"pane"`
	}
	if err := c.runJSON(ctx, "pane get", &response, "pane", "get", paneID); err != nil {
		return Pane{}, err
	}
	return convertPane(response.Pane), nil
}

func (c *CLI) ProcessInfo(ctx context.Context, paneID string) (ProcessInfo, error) {
	var response struct {
		ProcessInfo processInfoResponse `json:"process_info"`
	}
	if err := c.runJSON(ctx, "pane process-info", &response, "pane", "process-info", "--pane", paneID); err != nil {
		return ProcessInfo{}, err
	}
	return convertProcessInfo(response.ProcessInfo), nil
}

func (c *CLI) RenamePane(ctx context.Context, paneID, label string) error {
	return c.runCommand(ctx, "pane rename", "pane", "rename", paneID, label)
}

func (c *CLI) RunPane(ctx context.Context, paneID, command string) error {
	return c.runCommand(ctx, "pane run", "pane", "run", paneID, command)
}

func (c *CLI) ClosePane(ctx context.Context, paneID string) error {
	return c.runCommand(ctx, "pane close", "pane", "close", paneID)
}

func convertWorkspaces(values []workspaceResponse) []Workspace {
	out := make([]Workspace, len(values))
	for i, value := range values {
		out[i] = Workspace{ID: value.ID, Label: value.Label}
	}
	return out
}

func convertTab(value tabResponse) Tab {
	return Tab{ID: value.ID, WorkspaceID: value.WorkspaceID, Label: value.Label}
}

func convertTabs(values []tabResponse) []Tab {
	out := make([]Tab, len(values))
	for i, value := range values {
		out[i] = convertTab(value)
	}
	return out
}

func convertPane(value paneResponse) Pane {
	return Pane{
		ID:            value.ID,
		TerminalID:    value.TerminalID,
		WorkspaceID:   value.WorkspaceID,
		TabID:         value.TabID,
		Label:         value.Label,
		CWD:           value.CWD,
		ForegroundCWD: value.ForegroundCWD,
	}
}

func convertPanes(values []paneResponse) []Pane {
	out := make([]Pane, len(values))
	for i, value := range values {
		out[i] = convertPane(value)
	}
	return out
}

func convertProcessInfo(value processInfoResponse) ProcessInfo {
	processes := make([]ForegroundProcess, len(value.ForegroundProcesses))
	for i, process := range value.ForegroundProcesses {
		processes[i] = ForegroundProcess{
			PID:     process.PID,
			Name:    process.Name,
			Cmdline: process.Cmdline,
			CWD:     process.CWD,
			Argv0:   process.Argv0,
			Argv:    process.Argv,
		}
	}
	return ProcessInfo{
		PaneID:                   value.PaneID,
		ShellPID:                 value.ShellPID,
		ForegroundProcessGroupID: value.ForegroundProcessGroupID,
		ForegroundProcesses:      processes,
	}
}
