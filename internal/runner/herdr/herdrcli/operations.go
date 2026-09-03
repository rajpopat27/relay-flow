package herdrcli

import "context"

type workspaceResponse struct {
	ID       string             `json:"workspace_id"`
	Label    string             `json:"label"`
	Worktree *worktreeRefwithID `json:"worktree"`
}

type worktreeRefwithID struct {
	CheckoutPath string `json:"checkout_path"`
	RepoName     string `json:"repo_name"`
	RepoRoot     string `json:"repo_root"`
	IsLinked     bool   `json:"is_linked_worktree"`
}

type worktreeResponse struct {
	Path            string `json:"path"`
	Branch          string `json:"branch"`
	IsLinked        bool   `json:"is_linked_worktree"`
	OpenWorkspaceID string `json:"open_workspace_id"`
}

type worktreeSourceResponse struct {
	RepoName           string `json:"repo_name"`
	RepoRoot           string `json:"repo_root"`
	SourceCheckoutPath string `json:"source_checkout_path"`
}

type tabResponse struct {
	ID          string `json:"tab_id"`
	WorkspaceID string `json:"workspace_id"`
	Label       string `json:"label"`
}

type paneResponse struct {
	ID          string `json:"pane_id"`
	TerminalID  string `json:"terminal_id"`
	WorkspaceID string `json:"workspace_id"`
	TabID       string `json:"tab_id"`
	Label       string `json:"label"`
	CWD         string `json:"cwd"`
}

type processInfoResponse struct {
	PaneID              string                      `json:"pane_id"`
	ShellPID            *uint32                     `json:"shell_pid"`
	ForegroundProcesses []foregroundProcessResponse `json:"foreground_processes"`
}

type foregroundProcessResponse struct {
	PID  uint32 `json:"pid"`
	Name string `json:"name"`
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

func (c *CLI) WorktreeList(ctx context.Context, repoPath string) (WorktreeListing, error) {
	var response struct {
		Source    worktreeSourceResponse `json:"source"`
		Worktrees []worktreeResponse     `json:"worktrees"`
	}
	if err := c.runJSON(ctx, "worktree list", &response, "worktree", "list", "--cwd", repoPath); err != nil {
		return WorktreeListing{}, err
	}
	return WorktreeListing{
		Source: WorktreeSource{
			RepoName:           response.Source.RepoName,
			RepoRoot:           response.Source.RepoRoot,
			SourceCheckoutPath: response.Source.SourceCheckoutPath,
		},
		Worktrees: convertWorktrees(response.Worktrees),
	}, nil
}

// WorktreeCreate creates the ticket branch checkout and opens it as its own
// workspace. Herdr reuses an existing branch, so base applies only when the
// branch does not exist yet and prior agent commits are never discarded.
func (c *CLI) WorktreeCreate(ctx context.Context, repoPath, branch, base, label string) (Workspace, error) {
	args := []string{
		"worktree", "create",
		"--cwd", repoPath,
		"--branch", branch,
		"--base", base,
		"--label", label,
		"--no-focus",
	}
	return c.workspaceFromWorktree(ctx, "worktree create", args)
}

// WorktreeOpen opens the existing ticket checkout, reusing the workspace when
// it is already open. It returns ErrWorktreeNotFound when no checkout exists
// for the branch.
func (c *CLI) WorktreeOpen(ctx context.Context, repoPath, branch, label string) (Workspace, error) {
	args := []string{
		"worktree", "open",
		"--cwd", repoPath,
		"--branch", branch,
		"--label", label,
		"--no-focus",
	}
	return c.workspaceFromWorktree(ctx, "worktree open", args)
}

func (c *CLI) workspaceFromWorktree(ctx context.Context, operation string, args []string) (Workspace, error) {
	var response struct {
		Workspace workspaceResponse `json:"workspace"`
	}
	if err := c.runJSON(ctx, operation, &response, args...); err != nil {
		return Workspace{}, err
	}
	return convertWorkspace(response.Workspace), nil
}

func (c *CLI) CreateTab(ctx context.Context, workspaceID, cwd, label string) (Tab, Pane, error) {
	args := []string{
		"tab", "create",
		"--workspace", workspaceID,
		"--cwd", cwd,
		"--label", label,
		"--no-focus",
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

func (c *CLI) CloseWorkspace(ctx context.Context, workspaceID string) error {
	return c.runCommand(ctx, "workspace close", "workspace", "close", workspaceID)
}

func convertWorkspace(value workspaceResponse) Workspace {
	workspace := Workspace{ID: value.ID, Label: value.Label}
	if value.Worktree != nil {
		workspace.Worktree = WorkspaceWorktree{
			CheckoutPath: value.Worktree.CheckoutPath,
			RepoName:     value.Worktree.RepoName,
			RepoRoot:     value.Worktree.RepoRoot,
			IsLinked:     value.Worktree.IsLinked,
		}
	}
	return workspace
}

func convertWorkspaces(values []workspaceResponse) []Workspace {
	out := make([]Workspace, len(values))
	for i, value := range values {
		out[i] = convertWorkspace(value)
	}
	return out
}

func convertWorktrees(values []worktreeResponse) []Worktree {
	out := make([]Worktree, len(values))
	for i, value := range values {
		out[i] = Worktree{
			Path:            value.Path,
			Branch:          value.Branch,
			IsLinked:        value.IsLinked,
			OpenWorkspaceID: value.OpenWorkspaceID,
		}
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
		ID:          value.ID,
		TerminalID:  value.TerminalID,
		WorkspaceID: value.WorkspaceID,
		TabID:       value.TabID,
		Label:       value.Label,
		CWD:         value.CWD,
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
		processes[i] = ForegroundProcess{PID: process.PID, Name: process.Name}
	}
	return ProcessInfo{
		PaneID:              value.PaneID,
		ShellPID:            value.ShellPID,
		ForegroundProcesses: processes,
	}
}
