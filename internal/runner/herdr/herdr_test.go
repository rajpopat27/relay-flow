package herdr

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/rajpopat27/relay-flow/internal/identity"
	"github.com/rajpopat27/relay-flow/internal/runner"
	"github.com/rajpopat27/relay-flow/internal/runner/herdr/herdrclicli"
)

// fakeClient is the typed adapter seam from design.md section 1a. The
// adapter tests deliberately provide only captured Herdr value shapes; strict
// executable tests cover the production CLI wrapper separately.
type fakeClient struct {
	mu sync.Mutex

	snapshot    herdrclicli.Snapshot
	snapshotErr error

	snapshotDelay       time.Duration
	snapshotCalls       int
	activeSnapshots     int
	maxConcurrentLookup int
}

var _ herdrclicli.Client = (*fakeClient)(nil)

func (f *fakeClient) Snapshot(context.Context) (herdrclicli.Snapshot, error) {
	f.mu.Lock()
	f.snapshotCalls++
	f.activeSnapshots++
	if f.activeSnapshots > f.maxConcurrentLookup {
		f.maxConcurrentLookup = f.activeSnapshots
	}
	snapshot := f.snapshot
	snapshotErr := f.snapshotErr
	delay := f.snapshotDelay
	f.mu.Unlock()

	if delay > 0 {
		time.Sleep(delay)
	}

	f.mu.Lock()
	f.activeSnapshots--
	f.mu.Unlock()
	return snapshot, snapshotErr
}

func (*fakeClient) CreateTab(context.Context, string, string, string, map[string]string) (herdrclicli.Tab, herdrclicli.Pane, error) {
	return herdrclicli.Tab{}, herdrclicli.Pane{}, errors.New("unexpected CreateTab call")
}

func (*fakeClient) ListTabs(context.Context, string) ([]herdrclicli.Tab, error) {
	return nil, errors.New("unexpected ListTabs call")
}

func (*fakeClient) ListPanes(context.Context, string) ([]herdrclicli.Pane, error) {
	return nil, errors.New("unexpected ListPanes call")
}

func (*fakeClient) GetPane(context.Context, string) (herdrclicli.Pane, error) {
	return herdrclicli.Pane{}, errors.New("unexpected GetPane call")
}

func (*fakeClient) ProcessInfo(context.Context, string) (herdrclicli.ProcessInfo, error) {
	return herdrclicli.ProcessInfo{}, errors.New("unexpected ProcessInfo call")
}

func (*fakeClient) RenamePane(context.Context, string, string) error {
	return errors.New("unexpected RenamePane call")
}

func (*fakeClient) RunPane(context.Context, string, string) error {
	return errors.New("unexpected RunPane call")
}

func (*fakeClient) ClosePane(context.Context, string) error {
	return errors.New("unexpected ClosePane call")
}

type createTabCall struct {
	workspaceID string
	cwd         string
	label       string
	env         map[string]string
}

type renamePaneCall struct {
	paneID string
	label  string
}

type closePaneCall struct {
	paneID string
}

// terminalFakeClient enables the terminal operations needed by Tasks 2.5 and
// 2.6 without making the workspace-resolution fake above permissive by
// default.
// The strict executable tests exercise the production CLI command shapes.
type terminalFakeClient struct {
	fakeClient

	createdTab  herdrclicli.Tab
	createdPane herdrclicli.Pane
	createCalls []createTabCall
	renameCalls []renamePaneCall
	runPaneIDs  []string
	runTexts    []string
	getPaneIDs  []string
	processIDs  []string
	listTabIDs  []string
	listPaneIDs []string
	closeCalls  []closePaneCall

	recoveryTabs   []herdrclicli.Tab
	recoveryPanes  []herdrclicli.Pane
	getPaneErr     error
	processInfoErr error
	closePaneErr   error

	pane        herdrclicli.Pane
	processInfo herdrclicli.ProcessInfo

	mu sync.Mutex
}

var _ herdrclicli.Client = (*terminalFakeClient)(nil)

func (f *terminalFakeClient) CreateTab(_ context.Context, workspaceID, cwd, label string, env map[string]string) (herdrclicli.Tab, herdrclicli.Pane, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	copyEnv := make(map[string]string, len(env))
	for key, value := range env {
		copyEnv[key] = value
	}
	f.createCalls = append(f.createCalls, createTabCall{
		workspaceID: workspaceID,
		cwd:         cwd,
		label:       label,
		env:         copyEnv,
	})
	return f.createdTab, f.createdPane, nil
}

func (f *terminalFakeClient) RenamePane(_ context.Context, paneID, label string) error {
	f.mu.Lock()
	f.renameCalls = append(f.renameCalls, renamePaneCall{paneID: paneID, label: label})
	f.mu.Unlock()
	return nil
}

func (f *terminalFakeClient) RunPane(_ context.Context, paneID, text string) error {
	f.mu.Lock()
	f.runPaneIDs = append(f.runPaneIDs, paneID)
	f.runTexts = append(f.runTexts, text)
	f.mu.Unlock()
	return nil
}

func (f *terminalFakeClient) ListTabs(_ context.Context, workspaceID string) ([]herdrclicli.Tab, error) {
	f.mu.Lock()
	f.listTabIDs = append(f.listTabIDs, workspaceID)
	tabs := append([]herdrclicli.Tab(nil), f.recoveryTabs...)
	f.mu.Unlock()
	return tabs, nil
}

func (f *terminalFakeClient) ListPanes(_ context.Context, workspaceID string) ([]herdrclicli.Pane, error) {
	f.mu.Lock()
	f.listPaneIDs = append(f.listPaneIDs, workspaceID)
	panes := append([]herdrclicli.Pane(nil), f.recoveryPanes...)
	f.mu.Unlock()
	return panes, nil
}

func (f *terminalFakeClient) GetPane(_ context.Context, paneID string) (herdrclicli.Pane, error) {
	f.mu.Lock()
	f.getPaneIDs = append(f.getPaneIDs, paneID)
	pane := f.pane
	err := f.getPaneErr
	f.mu.Unlock()
	return pane, err
}

func (f *terminalFakeClient) ProcessInfo(_ context.Context, paneID string) (herdrclicli.ProcessInfo, error) {
	f.mu.Lock()
	f.processIDs = append(f.processIDs, paneID)
	processInfo := f.processInfo
	err := f.processInfoErr
	f.mu.Unlock()
	return processInfo, err
}

func (f *terminalFakeClient) ClosePane(_ context.Context, paneID string) error {
	f.mu.Lock()
	f.closeCalls = append(f.closeCalls, closePaneCall{paneID: paneID})
	err := f.closePaneErr
	if err == nil {
		remaining := f.recoveryPanes[:0]
		for _, pane := range f.recoveryPanes {
			if pane.ID != paneID {
				remaining = append(remaining, pane)
			}
		}
		f.recoveryPanes = remaining
	}
	f.mu.Unlock()
	return err
}

func newTerminalFakeClient() *terminalFakeClient {
	return &terminalFakeClient{
		createdTab: herdrclicli.Tab{
			ID:          "tab-payments",
			WorkspaceID: "workspace-payments",
		},
		createdPane: herdrclicli.Pane{
			ID:          "pane-public",
			TerminalID:  "terminal-created",
			WorkspaceID: "workspace-payments",
			TabID:       "tab-payments",
		},
		pane: herdrclicli.Pane{
			ID:          "pane-public",
			TerminalID:  "terminal-after-restart",
			WorkspaceID: "workspace-payments",
			TabID:       "tab-payments",
			Label:       "PAY-101:coding",
		},
		processInfo: herdrclicli.ProcessInfo{
			PaneID:                   "pane-public",
			ShellPID:                 uint32Pointer(999),
			ForegroundProcessGroupID: uint32Pointer(1234),
			ForegroundProcesses: []herdrclicli.ForegroundProcess{{
				PID:   1234,
				Name:  "opencode",
				Argv0: "opencode",
				Argv:  []string{"opencode"},
			}},
		},
	}
}

func newCleanupTestClient(t *testing.T) (*terminalFakeClient, runner.RunSpec) {
	t.Helper()
	canonical, paneCWD, _ := newWorkspaceTestPaths(t)
	cli := newTerminalFakeClient()
	cli.fakeClient.snapshot = newWorkspaceSnapshot(
		[]herdrclicli.Workspace{{ID: "workspace-payments", Label: "payments"}},
		[]herdrclicli.Pane{{ID: "pane-root", WorkspaceID: "workspace-payments", CWD: paneCWD}},
	)
	cli.recoveryTabs = []herdrclicli.Tab{
		{ID: "tab-owned-label", WorkspaceID: "workspace-payments", Label: "PAY-101:coding"},
		{ID: "tab-owned-pane", WorkspaceID: "workspace-payments", Label: "PAY-101:review"},
		{ID: "tab-other", WorkspaceID: "workspace-payments", Label: "PAY-202:coding"},
		{ID: "tab-neutral", WorkspaceID: "workspace-payments", Label: ""},
	}
	cli.recoveryPanes = []herdrclicli.Pane{
		{ID: "pane-owned-label", WorkspaceID: "workspace-payments", TabID: "tab-owned-label", Label: "PAY-101:coding"},
		// This pane was created before pane rename completed; its containing
		// tab remains the ticket ownership marker.
		{ID: "pane-owned-tab", WorkspaceID: "workspace-payments", TabID: "tab-owned-pane", Label: ""},
		{ID: "pane-other", WorkspaceID: "workspace-payments", TabID: "tab-other", Label: "PAY-202:coding"},
		{ID: "pane-similar-ticket", WorkspaceID: "workspace-payments", TabID: "tab-neutral", Label: "PAY-1010:coding"},
		{ID: "pane-neutral", WorkspaceID: "workspace-payments", TabID: "tab-neutral", Label: ""},
	}
	return cli, runSpec("payments", canonical)
}

func closePaneIDs(client *terminalFakeClient) []string {
	client.mu.Lock()
	defer client.mu.Unlock()
	ids := make([]string, 0, len(client.closeCalls))
	for _, call := range client.closeCalls {
		ids = append(ids, call.paneID)
	}
	return ids
}

func remainingPaneIDs(client *terminalFakeClient) []string {
	client.mu.Lock()
	defer client.mu.Unlock()
	ids := make([]string, 0, len(client.recoveryPanes))
	for _, pane := range client.recoveryPanes {
		ids = append(ids, pane.ID)
	}
	return ids
}

func assertPaneIDs(t *testing.T, got []string, want ...string) {
	t.Helper()
	gotSet := make(map[string]bool, len(got))
	for _, id := range got {
		gotSet[id] = true
	}
	wantSet := make(map[string]bool, len(want))
	for _, id := range want {
		wantSet[id] = true
	}
	if len(gotSet) != len(wantSet) {
		t.Fatalf("closed pane IDs = %v, want %v", got, want)
	}
	for id := range wantSet {
		if !gotSet[id] {
			t.Fatalf("closed pane IDs = %v, missing %q; want %v", got, id, want)
		}
	}
}

func TestCloseTerminalsClosesOnlyExactTicketPanesAndPreservesWorkspace(t *testing.T) {
	cli, spec := newCleanupTestClient(t)
	a := newAdapter(cli, Config{})

	if err := a.CloseTerminals(context.Background(), spec); err != nil {
		t.Fatal(err)
	}
	assertPaneIDs(t, closePaneIDs(cli), "pane-owned-label", "pane-owned-tab")
	assertPaneIDs(t, remainingPaneIDs(cli), "pane-other", "pane-similar-ticket", "pane-neutral")

	if env, err := a.EnsureEnvironment(context.Background(), spec); err != nil {
		t.Fatalf("workspace was not reusable after pane cleanup: %v", err)
	} else if env.ID != "workspace-payments" || env.Path != spec.RepoPath {
		t.Fatalf("environment after cleanup = %+v, want shared workspace-payments at %q", env, spec.RepoPath)
	}
	cli.mu.Lock()
	workspaceCount := len(cli.snapshot.Workspaces)
	cli.mu.Unlock()
	if workspaceCount != 1 {
		t.Fatalf("workspace count after cleanup = %d, want shared workspace preserved", workspaceCount)
	}
}

func TestCloseTerminalsIsIdempotentWhenTicketPanesAreMissing(t *testing.T) {
	cli, spec := newCleanupTestClient(t)
	a := newAdapter(cli, Config{})

	if err := a.CloseTerminals(context.Background(), spec); err != nil {
		t.Fatal(err)
	}
	firstCloseIDs := closePaneIDs(cli)
	if err := a.CloseTerminals(context.Background(), spec); err != nil {
		t.Fatalf("second CloseTerminals after panes disappeared: %v", err)
	}
	secondCloseIDs := closePaneIDs(cli)
	assertPaneIDs(t, firstCloseIDs, "pane-owned-label", "pane-owned-tab")
	if len(secondCloseIDs) != len(firstCloseIDs) {
		t.Fatalf("second cleanup issued new close calls: first=%v second=%v", firstCloseIDs, secondCloseIDs)
	}
}

func TestCleanupRunClosesTicketPanesAndPreservesSharedWorkspace(t *testing.T) {
	cli, spec := newCleanupTestClient(t)
	a := newAdapter(cli, Config{})

	if err := a.CleanupRun(context.Background(), spec); err != nil {
		t.Fatal(err)
	}
	assertPaneIDs(t, closePaneIDs(cli), "pane-owned-label", "pane-owned-tab")
	assertPaneIDs(t, remainingPaneIDs(cli), "pane-other", "pane-similar-ticket", "pane-neutral")

	env, err := a.EnsureEnvironment(context.Background(), spec)
	if err != nil {
		t.Fatalf("shared workspace unavailable after CleanupRun: %v", err)
	}
	if env.ID != "workspace-payments" || env.Path != spec.RepoPath {
		t.Fatalf("environment after CleanupRun = %+v, want workspace-payments at %q", env, spec.RepoPath)
	}
	cli.mu.Lock()
	workspaces := append([]herdrclicli.Workspace(nil), cli.snapshot.Workspaces...)
	cli.mu.Unlock()
	if len(workspaces) != 1 || workspaces[0].ID != "workspace-payments" {
		t.Fatalf("workspaces after CleanupRun = %+v, want shared workspace preserved", workspaces)
	}
}

func TestCloseTerminalToleratesAlreadyMissingPane(t *testing.T) {
	cli := newTerminalFakeClient()
	cli.closePaneErr = errors.New("pane pane-missing not found")
	a := newAdapter(cli, Config{})

	if err := a.CloseTerminal(context.Background(), runner.Terminal{ID: "pane-missing", Title: "PAY-101:coding"}); err != nil {
		t.Fatalf("CloseTerminal(missing pane) = %v, want nil", err)
	}
}

func TestSetEnvironmentStatusIsSuccessfulNoOp(t *testing.T) {
	cli, spec := newCleanupTestClient(t)
	a := newAdapter(cli, Config{})

	for _, status := range []string{
		runner.WorkspaceStatusInProgress,
		runner.WorkspaceStatusInReview,
		runner.WorkspaceStatusCompleted,
	} {
		if err := a.SetEnvironmentStatus(context.Background(), runner.Environment{ID: "workspace-payments", Path: spec.RepoPath}, status); err != nil {
			t.Fatalf("SetEnvironmentStatus(%q) = %v, want nil", status, err)
		}
	}
	cli.mu.Lock()
	workspaces := append([]herdrclicli.Workspace(nil), cli.snapshot.Workspaces...)
	cli.mu.Unlock()
	if len(workspaces) != 1 || workspaces[0] != (herdrclicli.Workspace{ID: "workspace-payments", Label: "payments"}) {
		t.Fatalf("workspaces after status updates = %+v, want unchanged shared workspace", workspaces)
	}
}

func uint32Pointer(value uint32) *uint32 {
	return &value
}

func TestCreateTerminalStoresPublicPaneIDNotTerminalID(t *testing.T) {
	cli := newTerminalFakeClient()
	a := newAdapter(cli, Config{})

	got, err := a.CreateTerminal(context.Background(), runner.Environment{ID: "workspace-payments", Path: "/work/payments"}, "PAY-101:coding", runner.Command{
		Executable: "opencode",
		Args:       []string{"--agent", "build"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if got.ID != "pane-public" {
		t.Fatalf("terminal ID = %q, want public pane_id pane-public", got.ID)
	}
	if got.ID == "terminal-created" {
		t.Fatal("terminal ID contains ephemeral terminal_id")
	}

	cli.mu.Lock()
	defer cli.mu.Unlock()
	if len(cli.createCalls) != 1 {
		t.Fatalf("CreateTab calls = %d, want 1", len(cli.createCalls))
	}
	if cli.createCalls[0].label != "PAY-101:coding" {
		t.Fatalf("CreateTab label = %q, want PAY-101:coding", cli.createCalls[0].label)
	}
}

func TestFindTerminalIgnoresChangedTerminalID(t *testing.T) {
	cli := newTerminalFakeClient()
	a := newAdapter(cli, Config{})

	got, ok, err := a.FindTerminal(context.Background(), runner.Terminal{
		ID:    "pane-public",
		Title: "PAY-101:coding",
	})
	if err != nil {
		t.Fatal(err)
	}
	if !ok {
		t.Fatal("FindTerminal reported restarted pane as unusable")
	}
	if got.ID != "pane-public" {
		t.Fatalf("terminal ID = %q, want public pane_id pane-public", got.ID)
	}
	if got.ID == "terminal-after-restart" {
		t.Fatal("FindTerminal returned ephemeral terminal_id")
	}

	cli.mu.Lock()
	defer cli.mu.Unlock()
	if len(cli.getPaneIDs) != 1 || cli.getPaneIDs[0] != "pane-public" {
		t.Fatalf("GetPane IDs = %v, want [pane-public]", cli.getPaneIDs)
	}
	if len(cli.processIDs) != 1 || cli.processIDs[0] != "pane-public" {
		t.Fatalf("ProcessInfo IDs = %v, want [pane-public]", cli.processIDs)
	}
}

func TestCreateTerminalUsesExactStableLabelWithoutRunMetadata(t *testing.T) {
	cli := newTerminalFakeClient()
	a := newAdapter(cli, Config{})
	visit := identity.NewNodeVisitID()
	runID := identity.NewRunID("payments", "basicFlow", "PAY-101")
	title := "PAY-101:coding"
	command := runner.Command{
		Executable: "opencode",
		Args: []string{
			"--agent", "build",
			"--workflow", "basicFlow",
			"--node-visit", string(visit),
		},
		Env: map[string]string{
			"RELAY_FLOW_RUN_ID":        string(runID),
			"RELAY_FLOW_WORKFLOW":      "basicFlow",
			"RELAY_FLOW_AGENT":         "build",
			"RELAY_FLOW_NODE_VISIT_ID": string(visit),
		},
	}

	got, err := a.CreateTerminal(context.Background(), runner.Environment{ID: "workspace-payments"}, title, command)
	if err != nil {
		t.Fatal(err)
	}
	if got.Title != title {
		t.Fatalf("terminal title = %q, want %q", got.Title, title)
	}

	cli.mu.Lock()
	defer cli.mu.Unlock()
	if len(cli.createCalls) != 1 {
		t.Fatalf("CreateTab calls = %d, want 1", len(cli.createCalls))
	}
	gotLabel := cli.createCalls[0].label
	if gotLabel != title {
		t.Fatalf("CreateTab label = %q, want exact stable title %q", gotLabel, title)
	}
	for _, forbidden := range []string{string(runID), "basicFlow", "build", string(visit)} {
		if strings.Contains(got.Title, forbidden) {
			t.Fatalf("terminal title %q contains forbidden metadata %q", got.Title, forbidden)
		}
		if strings.Contains(gotLabel, forbidden) {
			t.Fatalf("CreateTab label %q contains forbidden metadata %q", gotLabel, forbidden)
		}
	}
}

func TestCreateTerminalRenamesPaneToExactStableLabel(t *testing.T) {
	cli := newTerminalFakeClient()
	a := newAdapter(cli, Config{})
	title := "PAY-101:coding"

	if _, err := a.CreateTerminal(context.Background(), runner.Environment{ID: "workspace-payments"}, title, runner.Command{Executable: "opencode"}); err != nil {
		t.Fatal(err)
	}

	cli.mu.Lock()
	defer cli.mu.Unlock()
	if len(cli.renameCalls) != 1 {
		t.Fatalf("RenamePane calls = %d, want 1", len(cli.renameCalls))
	}
	if got := cli.renameCalls[0]; got.paneID != "pane-public" || got.label != title {
		t.Fatalf("RenamePane call = %+v, want pane-public and %q", got, title)
	}
}

func TestEnsureTerminalReusesLivePane(t *testing.T) {
	cli := newTerminalFakeClient()
	a := newAdapter(cli, Config{})
	stored := runner.Terminal{ID: "pane-public", Title: "PAY-101:coding"}

	got, err := a.EnsureTerminal(context.Background(), runner.Environment{ID: "workspace-payments"}, stored, stored.Title, runner.Command{Executable: "opencode"})
	if err != nil {
		t.Fatal(err)
	}
	if got != stored {
		t.Fatalf("EnsureTerminal = %+v, want stored live terminal %+v", got, stored)
	}

	cli.mu.Lock()
	defer cli.mu.Unlock()
	if len(cli.createCalls) != 0 {
		t.Fatalf("CreateTab calls = %d, want no replacement for live pane", len(cli.createCalls))
	}
	if len(cli.listTabIDs) != 0 || len(cli.listPaneIDs) != 0 {
		t.Fatalf("recovery lookup calls = tabs %v panes %v, want none for stored live pane", cli.listTabIDs, cli.listPaneIDs)
	}
}

func TestFindTerminalRejectsRestoredShellOnlyPane(t *testing.T) {
	cli := newTerminalFakeClient()
	cli.processInfo = herdrclicli.ProcessInfo{
		PaneID:                   "pane-public",
		ShellPID:                 uint32Pointer(4567),
		ForegroundProcessGroupID: uint32Pointer(4567),
		ForegroundProcesses: []herdrclicli.ForegroundProcess{{
			PID:     4567,
			Name:    "bash",
			Cmdline: "bash",
			Argv0:   "bash",
			Argv:    []string{"bash"},
		}},
	}
	a := newAdapter(cli, Config{})

	if _, ok, err := a.FindTerminal(context.Background(), runner.Terminal{ID: "pane-public", Title: "PAY-101:coding"}); err != nil {
		t.Fatal(err)
	} else if ok {
		t.Fatal("FindTerminal accepted a restored shell-only pane as a live agent")
	}
}

func TestEnsureTerminalReplacesMissingPane(t *testing.T) {
	cli := newTerminalFakeClient()
	cli.getPaneErr = errors.New("pane pane-stale not found")
	cli.createdPane = herdrclicli.Pane{
		ID:          "pane-replacement",
		TerminalID:  "terminal-replacement",
		WorkspaceID: "workspace-payments",
		TabID:       "tab-replacement",
	}
	a := newAdapter(cli, Config{})

	got, err := a.EnsureTerminal(context.Background(), runner.Environment{ID: "workspace-payments", Path: "/work/payments"}, runner.Terminal{ID: "pane-stale", Title: "PAY-101:coding"}, "PAY-101:coding", runner.Command{Executable: "opencode"})
	if err != nil {
		t.Fatal(err)
	}
	if got.ID != "pane-replacement" {
		t.Fatalf("replacement terminal ID = %q, want pane-replacement", got.ID)
	}
	if got.ID == "terminal-replacement" {
		t.Fatal("replacement terminal used ephemeral terminal_id")
	}

	cli.mu.Lock()
	defer cli.mu.Unlock()
	if len(cli.createCalls) != 1 {
		t.Fatalf("CreateTab calls = %d, want 1", len(cli.createCalls))
	}
	if len(cli.renameCalls) != 1 || cli.renameCalls[0].paneID != "pane-replacement" {
		t.Fatalf("RenamePane calls = %+v, want replacement pane", cli.renameCalls)
	}
	if len(cli.runPaneIDs) != 1 || cli.runPaneIDs[0] != "pane-replacement" {
		t.Fatalf("RunPane IDs = %v, want [pane-replacement]", cli.runPaneIDs)
	}
}

func TestEnsureTerminalRecoversLostCreateAckFromTabLabelBeforePaneRename(t *testing.T) {
	cli := newTerminalFakeClient()
	cli.recoveryTabs = []herdrclicli.Tab{{
		ID:          "tab-recovered",
		WorkspaceID: "workspace-payments",
		Label:       "PAY-101:coding",
	}}
	cli.recoveryPanes = []herdrclicli.Pane{{
		ID:          "pane-recovered",
		TerminalID:  "terminal-recovered",
		WorkspaceID: "workspace-payments",
		TabID:       "tab-recovered",
		// The tab is labelled, but pane rename was not acknowledged yet.
		Label: "",
	}}
	cli.pane = cli.recoveryPanes[0]
	cli.processInfo.PaneID = "pane-recovered"
	a := newAdapter(cli, Config{})

	got, err := a.EnsureTerminal(context.Background(), runner.Environment{ID: "workspace-payments"}, runner.Terminal{}, "PAY-101:coding", runner.Command{Executable: "opencode"})
	if err != nil {
		t.Fatal(err)
	}
	if got.ID != "pane-recovered" {
		t.Fatalf("recovered terminal ID = %q, want pane-recovered", got.ID)
	}
	if got.Title != "PAY-101:coding" {
		t.Fatalf("recovered terminal title = %q, want PAY-101:coding", got.Title)
	}

	cli.mu.Lock()
	defer cli.mu.Unlock()
	if len(cli.createCalls) != 0 {
		t.Fatalf("CreateTab calls = %d, want no duplicate after tab-label recovery", len(cli.createCalls))
	}
	if len(cli.listTabIDs) != 1 || cli.listTabIDs[0] != "workspace-payments" {
		t.Fatalf("ListTabs workspace IDs = %v, want [workspace-payments]", cli.listTabIDs)
	}
	if len(cli.listPaneIDs) != 1 || cli.listPaneIDs[0] != "workspace-payments" {
		t.Fatalf("ListPanes workspace IDs = %v, want [workspace-payments]", cli.listPaneIDs)
	}
}

func TestEnsureTerminalRecoversLostCreateAckFromPaneLabelAfterRename(t *testing.T) {
	cli := newTerminalFakeClient()
	cli.recoveryPanes = []herdrclicli.Pane{{
		ID:          "pane-recovered",
		TerminalID:  "terminal-recovered",
		WorkspaceID: "workspace-payments",
		TabID:       "tab-recovered",
		Label:       "PAY-101:coding",
	}}
	cli.pane = cli.recoveryPanes[0]
	cli.processInfo.PaneID = "pane-recovered"
	a := newAdapter(cli, Config{})

	got, err := a.EnsureTerminal(context.Background(), runner.Environment{ID: "workspace-payments"}, runner.Terminal{}, "PAY-101:coding", runner.Command{Executable: "opencode"})
	if err != nil {
		t.Fatal(err)
	}
	if got.ID != "pane-recovered" {
		t.Fatalf("recovered terminal ID = %q, want pane-recovered", got.ID)
	}

	cli.mu.Lock()
	defer cli.mu.Unlock()
	if len(cli.createCalls) != 0 {
		t.Fatalf("CreateTab calls = %d, want no duplicate after pane-label recovery", len(cli.createCalls))
	}
	if len(cli.listTabIDs) != 1 || cli.listTabIDs[0] != "workspace-payments" {
		t.Fatalf("ListTabs workspace IDs = %v, want [workspace-payments]", cli.listTabIDs)
	}
	if len(cli.listPaneIDs) != 1 || cli.listPaneIDs[0] != "workspace-payments" {
		t.Fatalf("ListPanes workspace IDs = %v, want [workspace-payments]", cli.listPaneIDs)
	}
}

func TestSendTerminalPreservesMultilineText(t *testing.T) {
	cli := newTerminalFakeClient()
	a := newAdapter(cli, Config{})
	text := "first line\nsecond line\n\nfinal line"

	if err := a.SendTerminal(context.Background(), runner.Terminal{ID: "pane-public"}, text); err != nil {
		t.Fatal(err)
	}

	cli.mu.Lock()
	defer cli.mu.Unlock()
	if len(cli.runPaneIDs) != 1 || cli.runPaneIDs[0] != "pane-public" {
		t.Fatalf("RunPane IDs = %v, want [pane-public]", cli.runPaneIDs)
	}
	if len(cli.runTexts) != 1 || cli.runTexts[0] != text {
		t.Fatalf("RunPane text = %q, want exact multiline text %q", cli.runTexts, text)
	}
}

func TestCreateTerminalForwardsOpaqueHarnessCommandAndEnvironment(t *testing.T) {
	cli := newTerminalFakeClient()
	a := newAdapter(cli, Config{})
	command := runner.Command{
		Executable: "custom harness",
		Args: []string{
			"--session", "opaque session",
			"--prompt", "line one\nline two",
			"--agent", "review",
			"--quote=it's preserved",
		},
		Env: map[string]string{
			"RELAY_FLOW_RUN_ID": "payments/basic/PAY-101",
			"RELAY_FLOW_NODE":   "coding",
			"OPAQUE_VALUE":      "value with spaces",
		},
	}
	title := "PAY-101:coding"

	if _, err := a.CreateTerminal(context.Background(), runner.Environment{ID: "workspace-payments", Path: "/work/payments"}, title, command); err != nil {
		t.Fatal(err)
	}

	cli.mu.Lock()
	defer cli.mu.Unlock()
	if len(cli.createCalls) != 1 {
		t.Fatalf("CreateTab calls = %d, want 1", len(cli.createCalls))
	}
	call := cli.createCalls[0]
	if call.workspaceID != "workspace-payments" || call.cwd != "/work/payments" || call.label != title {
		t.Fatalf("CreateTab call = %+v, want workspace/path/title preserved", call)
	}
	if len(call.env) != len(command.Env) {
		t.Fatalf("CreateTab environment = %+v, want %+v", call.env, command.Env)
	}
	for key, want := range command.Env {
		if got := call.env[key]; got != want {
			t.Fatalf("CreateTab environment %s = %q, want %q", key, got, want)
		}
	}
	if len(cli.runTexts) != 1 {
		t.Fatalf("RunPane calls = %d, want 1", len(cli.runTexts))
	}
	runText := cli.runTexts[0]
	wantCommand := "'custom harness' '--session' 'opaque session' '--prompt' 'line one\nline two' '--agent' 'review' '--quote=it'\\''s preserved'"
	if runText != wantCommand {
		t.Fatalf("RunPane command = %q, want exact POSIX-shell-quoted command %q", runText, wantCommand)
	}
}

func newWorkspaceTestPaths(t *testing.T) (canonical, paneCWD, registered string) {
	t.Helper()
	root := t.TempDir()
	separator := string(filepath.Separator)
	canonical = filepath.Join(root, "payments")
	if err := os.Mkdir(canonical, 0o755); err != nil {
		t.Fatal(err)
	}
	paneCWD = root + separator + "payments" + separator + "."
	registered = root + separator + "payments" + separator + ".." + separator + "payments"
	return canonical, paneCWD, registered
}

func newWorkspaceSnapshot(workspaces []herdrclicli.Workspace, panes []herdrclicli.Pane) herdrclicli.Snapshot {
	return herdrclicli.Snapshot{Workspaces: workspaces, Panes: panes}
}

func runSpec(name, path string) runner.RunSpec {
	return runner.RunSpec{RepoName: name, RepoPath: path, TicketKey: "PAY-101"}
}

func TestDiscoverReposDerivesCandidatesFromWorkspacePanes(t *testing.T) {
	canonical, paneCWD, _ := newWorkspaceTestPaths(t)
	cli := &fakeClient{snapshot: newWorkspaceSnapshot(
		[]herdrclicli.Workspace{
			{ID: "workspace-payments", Label: "payments"},
		},
		[]herdrclicli.Pane{
			{ID: "pane-root", WorkspaceID: "workspace-payments", CWD: paneCWD},
			// A repository workspace can contain multiple panes; it still
			// produces one repository candidate.
			{ID: "pane-ticket", WorkspaceID: "workspace-payments", CWD: canonical},
		},
	)}
	a := newAdapter(cli, Config{})

	got, err := a.DiscoverRepos(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	want := []runner.RepoCandidate{{Name: "payments", Path: canonical}}
	if len(got) != len(want) || got[0] != want[0] {
		t.Fatalf("DiscoverRepos = %+v, want %+v", got, want)
	}
}

func TestEnsureEnvironmentMatchesNormalizedPanePath(t *testing.T) {
	canonical, paneCWD, registered := newWorkspaceTestPaths(t)
	cli := &fakeClient{snapshot: newWorkspaceSnapshot(
		[]herdrclicli.Workspace{{ID: "workspace-payments", Label: "payments"}},
		[]herdrclicli.Pane{{ID: "pane-root", WorkspaceID: "workspace-payments", CWD: paneCWD}},
	)}
	a := newAdapter(cli, Config{})

	if err := a.ValidateRepo(context.Background(), "payments", registered); err != nil {
		t.Fatalf("ValidateRepo with normalized path: %v", err)
	}
	env, err := a.EnsureEnvironment(context.Background(), runSpec("payments", registered))
	if err != nil {
		t.Fatal(err)
	}
	if env.ID != "workspace-payments" {
		t.Fatalf("environment ID = %q, want workspace-payments", env.ID)
	}
	if env.Path != registered {
		t.Fatalf("environment path = %q, want registered path %q", env.Path, registered)
	}
	if filepath.Clean(paneCWD) != canonical || filepath.Clean(registered) != canonical {
		t.Fatalf("test paths did not normalize to %q: pane=%q registered=%q", canonical, paneCWD, registered)
	}
}

func TestEnsureEnvironmentUsesUniqueWorkspaceLabelTieBreaker(t *testing.T) {
	canonical, paneCWD, _ := newWorkspaceTestPaths(t)
	cli := &fakeClient{snapshot: newWorkspaceSnapshot(
		[]herdrclicli.Workspace{
			{ID: "workspace-copy", Label: "payments-copy"},
			{ID: "workspace-payments", Label: "payments"},
		},
		[]herdrclicli.Pane{
			{ID: "pane-copy", WorkspaceID: "workspace-copy", CWD: paneCWD},
			{ID: "pane-payments", WorkspaceID: "workspace-payments", CWD: canonical},
		},
	)}
	a := newAdapter(cli, Config{})

	env, err := a.EnsureEnvironment(context.Background(), runSpec("payments", canonical))
	if err != nil {
		t.Fatal(err)
	}
	if env.ID != "workspace-payments" {
		t.Fatalf("environment ID = %q, want label-matched workspace-payments", env.ID)
	}
}

func TestEnsureEnvironmentRejectsAmbiguousWorkspacePath(t *testing.T) {
	canonical, paneCWD, _ := newWorkspaceTestPaths(t)
	cli := &fakeClient{snapshot: newWorkspaceSnapshot(
		[]herdrclicli.Workspace{
			{ID: "workspace-one", Label: "payments-copy-a"},
			{ID: "workspace-two", Label: "payments-copy-b"},
		},
		[]herdrclicli.Pane{
			{ID: "pane-one", WorkspaceID: "workspace-one", CWD: paneCWD},
			{ID: "pane-two", WorkspaceID: "workspace-two", CWD: canonical},
		},
	)}
	a := newAdapter(cli, Config{})

	err := a.ValidateRepo(context.Background(), "payments", canonical)
	if err == nil {
		t.Fatal("ValidateRepo accepted an ambiguous workspace path")
	}
	if !strings.Contains(strings.ToLower(err.Error()), "ambiguous") {
		t.Fatalf("ValidateRepo error = %v, want ambiguous workspace error", err)
	}

	if _, err := a.EnsureEnvironment(context.Background(), runSpec("payments", canonical)); err == nil {
		t.Fatal("EnsureEnvironment accepted an ambiguous workspace path")
	}
}

func TestEnsureEnvironmentReusesExistingWorkspace(t *testing.T) {
	canonical, paneCWD, _ := newWorkspaceTestPaths(t)
	cli := &fakeClient{snapshot: newWorkspaceSnapshot(
		[]herdrclicli.Workspace{{ID: "workspace-payments", Label: "payments"}},
		[]herdrclicli.Pane{{ID: "pane-root", WorkspaceID: "workspace-payments", CWD: paneCWD}},
	)}
	a := newAdapter(cli, Config{})
	spec := runSpec("payments", canonical)

	first, err := a.EnsureEnvironment(context.Background(), spec)
	if err != nil {
		t.Fatal(err)
	}
	second, err := a.EnsureEnvironment(context.Background(), spec)
	if err != nil {
		t.Fatal(err)
	}
	if first != second {
		t.Fatalf("EnsureEnvironment changed an existing workspace: first=%+v second=%+v", first, second)
	}
}

func TestEnsureEnvironmentReportsMissingWorkspace(t *testing.T) {
	canonical, paneCWD, _ := newWorkspaceTestPaths(t)
	cli := &fakeClient{snapshot: newWorkspaceSnapshot(
		[]herdrclicli.Workspace{{ID: "workspace-other", Label: "other"}},
		[]herdrclicli.Pane{{ID: "pane-other", WorkspaceID: "workspace-other", CWD: paneCWD + "-other"}},
	)}
	a := newAdapter(cli, Config{})

	err := a.ValidateRepo(context.Background(), "payments", canonical)
	if err == nil {
		t.Fatal("ValidateRepo accepted a missing workspace")
	}
	if !strings.Contains(strings.ToLower(err.Error()), "workspace") {
		t.Fatalf("ValidateRepo error = %v, want missing workspace error", err)
	}

	err = func() error {
		_, err := a.EnsureEnvironment(context.Background(), runSpec("payments", canonical))
		return err
	}()
	if err == nil {
		t.Fatal("EnsureEnvironment accepted a missing workspace")
	}
	if !strings.Contains(strings.ToLower(err.Error()), "workspace") {
		t.Fatalf("EnsureEnvironment error = %v, want missing workspace error", err)
	}
}

func TestValidateRepoRejectsMissingLocalPathEvenWhenWorkspaceMatches(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "missing-repository")
	cli := &fakeClient{snapshot: newWorkspaceSnapshot(
		[]herdrclicli.Workspace{{ID: "workspace-payments", Label: "payments"}},
		[]herdrclicli.Pane{{ID: "pane-root", WorkspaceID: "workspace-payments", CWD: missing}},
	)}
	a := newAdapter(cli, Config{})

	err := a.ValidateRepo(context.Background(), "payments", missing)
	if err == nil {
		t.Fatal("ValidateRepo accepted a missing local path despite a matching workspace pane")
	}
	if !strings.Contains(strings.ToLower(err.Error()), "does not exist") {
		t.Fatalf("ValidateRepo error = %v, want missing local path error", err)
	}
	cli.mu.Lock()
	snapshotCalls := cli.snapshotCalls
	cli.mu.Unlock()
	if snapshotCalls != 0 {
		t.Fatalf("Snapshot calls = %d, want no Herdr lookup for missing local path", snapshotCalls)
	}
}

func TestConcurrentWorkspaceLookupIsSerializedAndIdempotent(t *testing.T) {
	canonical, paneCWD, _ := newWorkspaceTestPaths(t)
	cli := &fakeClient{
		snapshot: newWorkspaceSnapshot(
			[]herdrclicli.Workspace{{ID: "workspace-payments", Label: "payments"}},
			[]herdrclicli.Pane{{ID: "pane-root", WorkspaceID: "workspace-payments", CWD: paneCWD}},
		),
		snapshotDelay: 2 * time.Millisecond,
	}
	a := newAdapter(cli, Config{})
	spec := runSpec("payments", canonical)

	const lookupCount = 12
	start := make(chan struct{})
	results := make(chan struct {
		env runner.Environment
		err error
	}, lookupCount)
	var wg sync.WaitGroup
	for i := 0; i < lookupCount; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			env, err := a.EnsureEnvironment(context.Background(), spec)
			results <- struct {
				env runner.Environment
				err error
			}{env: env, err: err}
		}()
	}
	close(start)
	wg.Wait()
	close(results)

	for result := range results {
		if result.err != nil {
			t.Fatal(result.err)
		}
		if result.env.ID != "workspace-payments" || result.env.Path != canonical {
			t.Fatalf("concurrent EnsureEnvironment result = %+v", result.env)
		}
	}

	cli.mu.Lock()
	maxConcurrent := cli.maxConcurrentLookup
	cli.mu.Unlock()
	if maxConcurrent != 1 {
		t.Fatalf("maximum concurrent workspace lookups = %d, want serialized lookups", maxConcurrent)
	}
}
