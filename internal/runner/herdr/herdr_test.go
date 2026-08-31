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

// terminalFakeClient enables the terminal operations needed by Task 2.5
// without making the workspace-resolution fake above permissive by default.
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

func (f *terminalFakeClient) GetPane(_ context.Context, paneID string) (herdrclicli.Pane, error) {
	f.mu.Lock()
	f.getPaneIDs = append(f.getPaneIDs, paneID)
	pane := f.pane
	f.mu.Unlock()
	return pane, nil
}

func (f *terminalFakeClient) ProcessInfo(_ context.Context, paneID string) (herdrclicli.ProcessInfo, error) {
	f.mu.Lock()
	f.processIDs = append(f.processIDs, paneID)
	processInfo := f.processInfo
	f.mu.Unlock()
	return processInfo, nil
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
			PaneID: "pane-public",
			ForegroundProcesses: []herdrclicli.ForegroundProcess{{
				PID:   1234,
				Name:  "opencode",
				Argv0: "opencode",
				Argv:  []string{"opencode"},
			}},
		},
	}
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
