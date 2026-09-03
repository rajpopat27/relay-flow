package herdr

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/rajpopat27/relay-flow/internal/config"
	"github.com/rajpopat27/relay-flow/internal/runner"
	"github.com/rajpopat27/relay-flow/internal/runner/herdr/herdrcli"
)

// fakeClient is the typed CLI seam used for adapter decision tests. Command
// shapes themselves are covered by the strict executable tests in herdrcli.
type fakeClient struct {
	mu sync.Mutex

	snapshot    herdrcli.Snapshot
	snapshotErr error

	listing    herdrcli.WorktreeListing
	listingErr error

	openWorkspace herdrcli.Workspace
	openErr       error

	createWorkspace herdrcli.Workspace
	createErr       error

	tabs    []herdrcli.Tab
	tabsErr error

	panes    []herdrcli.Pane
	panesErr error

	pane    herdrcli.Pane
	paneErr error

	info    herdrcli.ProcessInfo
	infoErr error

	createdTab   herdrcli.Tab
	createdPane  herdrcli.Pane
	createTabErr error

	renameErr         error
	runErr            error
	closePaneErr      error
	closeWorkspaceErr error

	openCalls        []worktreeCall
	createCalls      []worktreeCall
	tabCreateCalls   []tabCreateCall
	renameCalls      []renameCall
	runCalls         []runCall
	closedPanes      []string
	closedWorkspaces []string
}

type worktreeCall struct{ RepoPath, Branch, Base, Label string }
type tabCreateCall struct{ WorkspaceID, CWD, Label string }
type renameCall struct{ PaneID, Label string }
type runCall struct{ PaneID, Command string }

var _ herdrcli.Client = (*fakeClient)(nil)

func (f *fakeClient) Snapshot(context.Context) (herdrcli.Snapshot, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.snapshot, f.snapshotErr
}

func (f *fakeClient) WorktreeList(_ context.Context, _ string) (herdrcli.WorktreeListing, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.listing, f.listingErr
}

func (f *fakeClient) WorktreeCreate(_ context.Context, repoPath, branch, base, label string) (herdrcli.Workspace, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.createCalls = append(f.createCalls, worktreeCall{RepoPath: repoPath, Branch: branch, Base: base, Label: label})
	return f.createWorkspace, f.createErr
}

func (f *fakeClient) WorktreeOpen(_ context.Context, repoPath, branch, label string) (herdrcli.Workspace, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.openCalls = append(f.openCalls, worktreeCall{RepoPath: repoPath, Branch: branch, Label: label})
	return f.openWorkspace, f.openErr
}

func (f *fakeClient) CreateTab(_ context.Context, workspaceID, cwd, label string) (herdrcli.Tab, herdrcli.Pane, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.tabCreateCalls = append(f.tabCreateCalls, tabCreateCall{WorkspaceID: workspaceID, CWD: cwd, Label: label})
	return f.createdTab, f.createdPane, f.createTabErr
}

func (f *fakeClient) ListTabs(context.Context, string) ([]herdrcli.Tab, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.tabs, f.tabsErr
}

func (f *fakeClient) ListPanes(context.Context, string) ([]herdrcli.Pane, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.panes, f.panesErr
}

func (f *fakeClient) GetPane(context.Context, string) (herdrcli.Pane, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.pane, f.paneErr
}

func (f *fakeClient) ProcessInfo(context.Context, string) (herdrcli.ProcessInfo, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.info, f.infoErr
}

func (f *fakeClient) RenamePane(_ context.Context, paneID, label string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.renameCalls = append(f.renameCalls, renameCall{PaneID: paneID, Label: label})
	return f.renameErr
}

func (f *fakeClient) RunPane(_ context.Context, paneID, command string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.runCalls = append(f.runCalls, runCall{PaneID: paneID, Command: command})
	return f.runErr
}

func (f *fakeClient) ClosePane(_ context.Context, paneID string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.closedPanes = append(f.closedPanes, paneID)
	return f.closePaneErr
}

func (f *fakeClient) CloseWorkspace(_ context.Context, workspaceID string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.closedWorkspaces = append(f.closedWorkspaces, workspaceID)
	return f.closeWorkspaceErr
}

func uint32Pointer(v uint32) *uint32 { return &v }

const (
	repoPath     = "/work/payments"
	checkoutPath = "/work/worktrees/payments/pay-101"
)

func ticketWorkspaceValue() herdrcli.Workspace {
	return herdrcli.Workspace{
		ID:    "w2",
		Label: "PAY-101",
		Worktree: herdrcli.WorkspaceWorktree{
			CheckoutPath: checkoutPath,
			RepoName:     "payments",
			RepoRoot:     repoPath,
			IsLinked:     true,
		},
	}
}

func runSpec() runner.RunSpec {
	return runner.RunSpec{RunID: "run-1", RepoName: "payments", RepoPath: repoPath, TicketKey: "PAY-101"}
}

func liveProcessInfo(paneID string) herdrcli.ProcessInfo {
	return herdrcli.ProcessInfo{
		PaneID:              paneID,
		ShellPID:            uint32Pointer(100),
		ForegroundProcesses: []herdrcli.ForegroundProcess{{PID: 200, Name: "opencode"}},
	}
}

func shellProcessInfo(paneID string) herdrcli.ProcessInfo {
	return herdrcli.ProcessInfo{
		PaneID:              paneID,
		ShellPID:            uint32Pointer(100),
		ForegroundProcesses: []herdrcli.ForegroundProcess{{PID: 100, Name: "zsh"}},
	}
}

func nodeCommand(runID string) runner.Command {
	return runner.Command{
		Executable: "opencode",
		Args:       []string{"--agent", "build", "--prompt", "line one\nline two"},
		Env:        map[string]string{"RELAY_FLOW_RUN_ID": runID, "RELAY_FLOW_NODE": "coding"},
	}
}

// --- Construction ---

func TestNewRejectsUnknownRunnerConfigKeys(t *testing.T) {
	if _, err := New(config.RawValues{"session": "relay-flow"}); err != nil {
		t.Fatalf("New(valid) = %v", err)
	}
	if _, err := New(config.RawValues{"workspace": "w1"}); err == nil {
		t.Fatal("New accepted an unknown runnerConfig key")
	}
}

// --- Repos ---

func TestDiscoverReposDeduplicatesRepositoryRoots(t *testing.T) {
	cli := &fakeClient{snapshot: herdrcli.Snapshot{Workspaces: []herdrcli.Workspace{
		{ID: "w1", Label: "payments", Worktree: herdrcli.WorkspaceWorktree{CheckoutPath: repoPath, RepoName: "payments", RepoRoot: repoPath}},
		ticketWorkspaceValue(),
		{ID: "w3", Label: "billing", Worktree: herdrcli.WorkspaceWorktree{CheckoutPath: "/work/billing", RepoName: "billing", RepoRoot: "/work/billing"}},
		{ID: "w4", Label: "scratch"},
	}}}
	got, err := newAdapter(cli).DiscoverRepos(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	want := []runner.RepoCandidate{
		{Name: "billing", Path: "/work/billing"},
		{Name: "payments", Path: repoPath},
	}
	if len(got) != len(want) {
		t.Fatalf("DiscoverRepos = %+v, want %+v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("DiscoverRepos[%d] = %+v, want %+v", i, got[i], want[i])
		}
	}
}

func TestValidateRepoAcceptsRepositoryRootAndRejectsInnerPaths(t *testing.T) {
	cli := &fakeClient{listing: herdrcli.WorktreeListing{
		Source: herdrcli.WorktreeSource{RepoName: "payments", RepoRoot: repoPath, SourceCheckoutPath: repoPath},
	}}
	a := newAdapter(cli)
	if err := a.ValidateRepo(context.Background(), "payments", repoPath); err != nil {
		t.Fatalf("ValidateRepo(root) = %v", err)
	}

	err := a.ValidateRepo(context.Background(), "payments", repoPath+"/internal/api")
	if err == nil {
		t.Fatal("ValidateRepo accepted a path inside the repository")
	}
	if !strings.Contains(err.Error(), repoPath) {
		t.Fatalf("ValidateRepo error = %v, want the repository root in the message", err)
	}
}

func TestValidateRepoRejectsNonGitPath(t *testing.T) {
	cli := &fakeClient{listingErr: herdrcli.ErrNotGitWorktree}
	err := newAdapter(cli).ValidateRepo(context.Background(), "payments", "/work/not-a-repo")
	if !errors.Is(err, herdrcli.ErrNotGitWorktree) {
		t.Fatalf("ValidateRepo(non-git) = %v, want ErrNotGitWorktree", err)
	}
}

func TestValidateRepoCreatesNothing(t *testing.T) {
	cli := &fakeClient{listing: herdrcli.WorktreeListing{
		Source: herdrcli.WorktreeSource{RepoRoot: repoPath, SourceCheckoutPath: repoPath},
	}}
	if err := newAdapter(cli).ValidateRepo(context.Background(), "payments", repoPath); err != nil {
		t.Fatal(err)
	}
	if len(cli.createCalls) != 0 || len(cli.openCalls) != 0 || len(cli.tabCreateCalls) != 0 {
		t.Fatalf("ValidateRepo created Herdr resources: %+v %+v %+v", cli.createCalls, cli.openCalls, cli.tabCreateCalls)
	}
}

// --- Environment ---

func TestEnsureEnvironmentReusesExistingTicketWorktree(t *testing.T) {
	cli := &fakeClient{openWorkspace: ticketWorkspaceValue()}
	env, err := newAdapter(cli).EnsureEnvironment(context.Background(), runSpec())
	if err != nil {
		t.Fatal(err)
	}
	if env != (runner.Environment{ID: "w2", Path: checkoutPath}) {
		t.Fatalf("EnsureEnvironment = %+v", env)
	}
	if len(cli.createCalls) != 0 {
		t.Fatalf("worktree create calls = %+v, want none for an existing checkout", cli.createCalls)
	}
	if len(cli.openCalls) != 1 || cli.openCalls[0].Branch != "PAY-101" || cli.openCalls[0].Label != "PAY-101" {
		t.Fatalf("worktree open calls = %+v", cli.openCalls)
	}
}

func TestEnsureEnvironmentCreatesTicketWorktreeFromOriginBase(t *testing.T) {
	repo := newGitRepo(t, "main")
	git(t, repo, "remote", "add", "origin", repo)
	git(t, repo, "update-ref", "refs/remotes/origin/main", "HEAD")
	git(t, repo, "symbolic-ref", "refs/remotes/origin/HEAD", "refs/remotes/origin/main")

	cli := &fakeClient{
		openErr:         herdrcli.ErrWorktreeNotFound,
		createWorkspace: ticketWorkspaceValue(),
	}
	spec := runSpec()
	spec.RepoPath = repo
	env, err := newAdapter(cli).EnsureEnvironment(context.Background(), spec)
	if err != nil {
		t.Fatal(err)
	}
	if env.ID != "w2" || env.Path != checkoutPath {
		t.Fatalf("EnsureEnvironment = %+v", env)
	}
	if len(cli.createCalls) != 1 {
		t.Fatalf("worktree create calls = %+v", cli.createCalls)
	}
	call := cli.createCalls[0]
	if call.Branch != "PAY-101" || call.Label != "PAY-101" || call.Base != "origin/main" {
		t.Fatalf("worktree create call = %+v, want ticket branch based on origin/main", call)
	}
}

func TestEnsureEnvironmentPropagatesOpenFailures(t *testing.T) {
	cli := &fakeClient{openErr: errors.New("herdr server unavailable")}
	if _, err := newAdapter(cli).EnsureEnvironment(context.Background(), runSpec()); err == nil {
		t.Fatal("EnsureEnvironment hid a transport failure")
	}
	if len(cli.createCalls) != 0 {
		t.Fatalf("worktree create calls = %+v, want none after a transport failure", cli.createCalls)
	}
}

func TestSetEnvironmentStatusIsSuccessfulNoOp(t *testing.T) {
	cli := &fakeClient{}
	err := newAdapter(cli).SetEnvironmentStatus(context.Background(), runner.Environment{ID: "w2"}, runner.WorkspaceStatusInProgress)
	if err != nil {
		t.Fatalf("SetEnvironmentStatus = %v", err)
	}
	if len(cli.closedWorkspaces) != 0 || len(cli.createCalls) != 0 {
		t.Fatal("SetEnvironmentStatus touched Herdr state")
	}
}

// --- Base ref ladder ---

func TestOriginBaseRefLadder(t *testing.T) {
	t.Run("origin/HEAD", func(t *testing.T) {
		repo := newGitRepo(t, "trunk")
		git(t, repo, "remote", "add", "origin", repo)
		git(t, repo, "update-ref", "refs/remotes/origin/trunk", "HEAD")
		git(t, repo, "symbolic-ref", "refs/remotes/origin/HEAD", "refs/remotes/origin/trunk")
		assertBaseRef(t, repo, "origin/trunk")
	})
	t.Run("origin/main", func(t *testing.T) {
		repo := newGitRepo(t, "work")
		git(t, repo, "remote", "add", "origin", repo)
		git(t, repo, "update-ref", "refs/remotes/origin/main", "HEAD")
		assertBaseRef(t, repo, "origin/main")
	})
	t.Run("origin/master", func(t *testing.T) {
		repo := newGitRepo(t, "work")
		git(t, repo, "remote", "add", "origin", repo)
		git(t, repo, "update-ref", "refs/remotes/origin/master", "HEAD")
		assertBaseRef(t, repo, "origin/master")
	})
	t.Run("local main without origin", func(t *testing.T) {
		assertBaseRef(t, newGitRepo(t, "main"), "main")
	})
	t.Run("local master without origin", func(t *testing.T) {
		assertBaseRef(t, newGitRepo(t, "master"), "master")
	})
	t.Run("no usable base", func(t *testing.T) {
		repo := newGitRepo(t, "wip")
		if _, err := originBaseRef(repo); err == nil {
			t.Fatal("originBaseRef accepted a repository with no origin or main/master")
		} else if !strings.Contains(err.Error(), "remote set-head") {
			t.Fatalf("originBaseRef error = %v, want actionable guidance", err)
		}
	})
}

func assertBaseRef(t *testing.T, repo, want string) {
	t.Helper()
	got, err := originBaseRef(repo)
	if err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Fatalf("originBaseRef = %q, want %q", got, want)
	}
}

func newGitRepo(t *testing.T, branch string) string {
	t.Helper()
	dir := t.TempDir()
	git(t, dir, "init", "-q", "-b", branch, ".")
	git(t, dir, "config", "user.email", "test@relay-flow.test")
	git(t, dir, "config", "user.name", "test")
	if err := os.WriteFile(filepath.Join(dir, "README.md"), []byte("x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	git(t, dir, "add", "-A")
	git(t, dir, "commit", "-qm", "init")
	return dir
}

func git(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v: %s", args, err, out)
	}
}

// --- Terminals ---

func TestCreateTerminalUsesPublicPaneIDAndStableLabel(t *testing.T) {
	cli := &fakeClient{
		createdTab:  herdrcli.Tab{ID: "w2:t2", WorkspaceID: "w2", Label: "PAY-101:coding"},
		createdPane: herdrcli.Pane{ID: "w2:p2", TerminalID: "term_ephemeral", WorkspaceID: "w2", TabID: "w2:t2"},
	}
	env := runner.Environment{ID: "w2", Path: checkoutPath}
	terminal, err := newAdapter(cli).CreateTerminal(context.Background(), env, "PAY-101:coding", nodeCommand("run-1"))
	if err != nil {
		t.Fatal(err)
	}
	if terminal != (runner.Terminal{ID: "w2:p2", Title: "PAY-101:coding"}) {
		t.Fatalf("CreateTerminal = %+v, want the public pane ID", terminal)
	}
	if len(cli.tabCreateCalls) != 1 || cli.tabCreateCalls[0] != (tabCreateCall{WorkspaceID: "w2", CWD: checkoutPath, Label: "PAY-101:coding"}) {
		t.Fatalf("tab create calls = %+v, want the worktree checkout as cwd", cli.tabCreateCalls)
	}
	if len(cli.renameCalls) != 1 || cli.renameCalls[0].Label != "PAY-101:coding" {
		t.Fatalf("rename calls = %+v", cli.renameCalls)
	}
	if len(cli.runCalls) != 1 {
		t.Fatalf("run calls = %+v", cli.runCalls)
	}
	command := cli.runCalls[0].Command
	for _, want := range []string{"RELAY_FLOW_RUN_ID='run-1'", "RELAY_FLOW_NODE='coding'", "'opencode'", "'line one\nline two'"} {
		if !strings.Contains(command, want) {
			t.Fatalf("command %q missing %q", command, want)
		}
	}
	for _, forbidden := range []string{"nodeVisit", "run-1:coding", "--env"} {
		if strings.Contains(cli.tabCreateCalls[0].Label, forbidden) {
			t.Fatalf("label %q contains %q", cli.tabCreateCalls[0].Label, forbidden)
		}
	}
}

func TestFindTerminalAcceptsLiveAgentAndRejectsRestoredShell(t *testing.T) {
	live := &fakeClient{
		pane: herdrcli.Pane{ID: "w2:p2", TerminalID: "term_1", WorkspaceID: "w2", Label: "PAY-101:coding"},
		info: liveProcessInfo("w2:p2"),
	}
	terminal, ok, err := newAdapter(live).FindTerminal(context.Background(), runner.Terminal{ID: "w2:p2", Title: "PAY-101:coding"})
	if err != nil || !ok || terminal.ID != "w2:p2" {
		t.Fatalf("FindTerminal(live) = %+v, %v, %v", terminal, ok, err)
	}

	restored := &fakeClient{
		pane: herdrcli.Pane{ID: "w2:p2", TerminalID: "term_after_restart", WorkspaceID: "w2", Label: "PAY-101:coding"},
		info: shellProcessInfo("w2:p2"),
	}
	if _, ok, err := newAdapter(restored).FindTerminal(context.Background(), runner.Terminal{ID: "w2:p2"}); err != nil || ok {
		t.Fatalf("FindTerminal(restored shell) = %v, %v, want absent", ok, err)
	}
}

func TestFindTerminalDistinguishesMissingPaneFromTransportFailure(t *testing.T) {
	missing := &fakeClient{paneErr: herdrcli.ErrPaneNotFound}
	if _, ok, err := newAdapter(missing).FindTerminal(context.Background(), runner.Terminal{ID: "w2:p9"}); err != nil || ok {
		t.Fatalf("FindTerminal(missing) = %v, %v, want absent without error", ok, err)
	}

	broken := &fakeClient{paneErr: errors.New("herdr server unavailable")}
	if _, ok, err := newAdapter(broken).FindTerminal(context.Background(), runner.Terminal{ID: "w2:p2"}); err == nil || ok {
		t.Fatalf("FindTerminal(transport failure) = %v, %v, want the error to propagate", ok, err)
	}
}

func TestEnsureTerminalReusesLivePaneWithoutRelaunching(t *testing.T) {
	cli := &fakeClient{
		pane: herdrcli.Pane{ID: "w2:p2", TerminalID: "term_1", WorkspaceID: "w2", Label: "PAY-101:coding"},
		info: liveProcessInfo("w2:p2"),
	}
	env := runner.Environment{ID: "w2", Path: checkoutPath}
	stored := runner.Terminal{ID: "w2:p2", Title: "PAY-101:coding"}
	got, err := newAdapter(cli).EnsureTerminal(context.Background(), env, stored, "PAY-101:coding", nodeCommand("run-1"))
	if err != nil || got.ID != "w2:p2" {
		t.Fatalf("EnsureTerminal = %+v, %v", got, err)
	}
	if len(cli.tabCreateCalls) != 0 || len(cli.runCalls) != 0 || len(cli.closedPanes) != 0 {
		t.Fatal("EnsureTerminal disturbed a live pane")
	}
}

func TestEnsureTerminalReplacesMissingPane(t *testing.T) {
	cli := &fakeClient{
		paneErr:     herdrcli.ErrPaneNotFound,
		createdTab:  herdrcli.Tab{ID: "w2:t3", WorkspaceID: "w2", Label: "PAY-101:coding"},
		createdPane: herdrcli.Pane{ID: "w2:p3", TerminalID: "term_new", WorkspaceID: "w2", TabID: "w2:t3"},
	}
	env := runner.Environment{ID: "w2", Path: checkoutPath}
	stored := runner.Terminal{ID: "w2:p2", Title: "PAY-101:coding"}
	got, err := newAdapter(cli).EnsureTerminal(context.Background(), env, stored, "PAY-101:coding", nodeCommand("run-1"))
	if err != nil || got.ID != "w2:p3" {
		t.Fatalf("EnsureTerminal = %+v, %v", got, err)
	}
	if len(cli.tabCreateCalls) != 1 {
		t.Fatalf("tab create calls = %+v, want exactly one replacement", cli.tabCreateCalls)
	}
}

func TestEnsureTerminalRecoversLostCreateThroughTabLabel(t *testing.T) {
	cli := &fakeClient{
		paneErr: herdrcli.ErrPaneNotFound,
		tabs:    []herdrcli.Tab{{ID: "w2:t2", WorkspaceID: "w2", Label: "PAY-101:coding"}},
		panes:   []herdrcli.Pane{{ID: "w2:p2", WorkspaceID: "w2", TabID: "w2:t2"}},
	}
	env := runner.Environment{ID: "w2", Path: checkoutPath}
	got, err := newAdapter(cli).EnsureTerminal(context.Background(), env, runner.Terminal{}, "PAY-101:coding", nodeCommand("run-2"))
	if err != nil || got.ID != "w2:p2" {
		t.Fatalf("EnsureTerminal = %+v, %v", got, err)
	}
	if len(cli.tabCreateCalls) != 0 {
		t.Fatalf("tab create calls = %+v, want no duplicate pane", cli.tabCreateCalls)
	}
	if len(cli.renameCalls) != 1 || cli.renameCalls[0].PaneID != "w2:p2" {
		t.Fatalf("rename calls = %+v, want the recovered pane labelled", cli.renameCalls)
	}
	if len(cli.runCalls) != 1 || !strings.Contains(cli.runCalls[0].Command, "RELAY_FLOW_RUN_ID='run-2'") {
		t.Fatalf("run calls = %+v, want the current run's environment", cli.runCalls)
	}
}

func TestEnsureTerminalRecoversLabelledPaneWithoutRenaming(t *testing.T) {
	cli := &fakeClient{
		paneErr: herdrcli.ErrPaneNotFound,
		tabs:    []herdrcli.Tab{{ID: "w2:t2", WorkspaceID: "w2", Label: "PAY-101:coding"}},
		panes:   []herdrcli.Pane{{ID: "w2:p2", WorkspaceID: "w2", TabID: "w2:t2", Label: "PAY-101:coding"}},
	}
	env := runner.Environment{ID: "w2", Path: checkoutPath}
	got, err := newAdapter(cli).EnsureTerminal(context.Background(), env, runner.Terminal{}, "PAY-101:coding", nodeCommand("run-2"))
	if err != nil || got.ID != "w2:p2" {
		t.Fatalf("EnsureTerminal = %+v, %v", got, err)
	}
	if len(cli.renameCalls) != 0 {
		t.Fatalf("rename calls = %+v, want none for an already labelled pane", cli.renameCalls)
	}
	if len(cli.runCalls) != 1 {
		t.Fatalf("run calls = %+v", cli.runCalls)
	}
}

// A pane left by an earlier run must be relaunched with the current run's
// environment: Herdr binds environment to the launched command, not the tab.
func TestAdoptedPaneNeverInheritsPreviousRunEnvironment(t *testing.T) {
	cli := &fakeClient{
		paneErr: herdrcli.ErrPaneNotFound,
		tabs:    []herdrcli.Tab{{ID: "w2:t2", WorkspaceID: "w2", Label: "PAY-101:coding"}},
		panes:   []herdrcli.Pane{{ID: "w2:p2", WorkspaceID: "w2", TabID: "w2:t2", Label: "PAY-101:coding"}},
	}
	env := runner.Environment{ID: "w2", Path: checkoutPath}
	if _, err := newAdapter(cli).EnsureTerminal(context.Background(), env, runner.Terminal{}, "PAY-101:coding", nodeCommand("run-9")); err != nil {
		t.Fatal(err)
	}
	command := cli.runCalls[0].Command
	if !strings.Contains(command, "RELAY_FLOW_RUN_ID='run-9'") || strings.Contains(command, "run-1") {
		t.Fatalf("command %q must carry only the current run identity", command)
	}
}

func TestSendTerminalForwardsMultilineTextUnchanged(t *testing.T) {
	cli := &fakeClient{}
	text := "feedback line one\nfeedback line two\n"
	if err := newAdapter(cli).SendTerminal(context.Background(), runner.Terminal{ID: "w2:p2"}, text); err != nil {
		t.Fatal(err)
	}
	if len(cli.runCalls) != 1 || cli.runCalls[0].Command != text {
		t.Fatalf("run calls = %+v, want the exact text", cli.runCalls)
	}
}

func TestCloseTerminalIsIdempotentForMissingPane(t *testing.T) {
	cli := &fakeClient{closePaneErr: herdrcli.ErrPaneNotFound}
	if err := newAdapter(cli).CloseTerminal(context.Background(), runner.Terminal{ID: "w2:p9"}); err != nil {
		t.Fatalf("CloseTerminal(missing) = %v, want nil", err)
	}
	cli.closePaneErr = errors.New("herdr server unavailable")
	if err := newAdapter(cli).CloseTerminal(context.Background(), runner.Terminal{ID: "w2:p2"}); err == nil {
		t.Fatal("CloseTerminal hid a transport failure")
	}
}

// --- Cleanup ---

func openTicketWorktreeClient() *fakeClient {
	return &fakeClient{
		listing: herdrcli.WorktreeListing{
			Source: herdrcli.WorktreeSource{RepoName: "payments", RepoRoot: repoPath, SourceCheckoutPath: repoPath},
			Worktrees: []herdrcli.Worktree{
				{Path: repoPath, Branch: "main", OpenWorkspaceID: "w1"},
				{Path: checkoutPath, Branch: "PAY-101", IsLinked: true, OpenWorkspaceID: "w2"},
			},
		},
		tabs: []herdrcli.Tab{
			{ID: "w2:t1", WorkspaceID: "w2", Label: "1"},
			{ID: "w2:t2", WorkspaceID: "w2", Label: "PAY-101:coding"},
		},
		panes: []herdrcli.Pane{
			{ID: "w2:p1", WorkspaceID: "w2", TabID: "w2:t1"},
			{ID: "w2:p2", WorkspaceID: "w2", TabID: "w2:t2", Label: "PAY-101:coding"},
			{ID: "w2:p3", WorkspaceID: "w2", TabID: "w2:t3", Label: "PAY-202:review"},
		},
	}
}

func TestCloseTerminalsClosesOnlyTicketPanesAndKeepsWorkspace(t *testing.T) {
	cli := openTicketWorktreeClient()
	if err := newAdapter(cli).CloseTerminals(context.Background(), runSpec()); err != nil {
		t.Fatal(err)
	}
	if len(cli.closedPanes) != 1 || cli.closedPanes[0] != "w2:p2" {
		t.Fatalf("closed panes = %v, want only the ticket node pane", cli.closedPanes)
	}
	if len(cli.closedWorkspaces) != 0 {
		t.Fatalf("closed workspaces = %v, want the workspace preserved", cli.closedWorkspaces)
	}
}

func TestCleanupRunClosesTicketWorkspaceAndKeepsWorktree(t *testing.T) {
	cli := openTicketWorktreeClient()
	if err := newAdapter(cli).CleanupRun(context.Background(), runSpec()); err != nil {
		t.Fatal(err)
	}
	if len(cli.closedPanes) != 1 || cli.closedPanes[0] != "w2:p2" {
		t.Fatalf("closed panes = %v", cli.closedPanes)
	}
	if len(cli.closedWorkspaces) != 1 || cli.closedWorkspaces[0] != "w2" {
		t.Fatalf("closed workspaces = %v, want the ticket workspace", cli.closedWorkspaces)
	}
}

func TestCleanupRollsForwardWhenTicketWorktreeIsGone(t *testing.T) {
	cases := map[string]*fakeClient{
		"repository is not a git work tree": {listingErr: herdrcli.ErrNotGitWorktree},
		"ticket checkout removed": {listing: herdrcli.WorktreeListing{
			Source:    herdrcli.WorktreeSource{RepoRoot: repoPath},
			Worktrees: []herdrcli.Worktree{{Path: repoPath, Branch: "main", OpenWorkspaceID: "w1"}},
		}},
		"ticket workspace closed": {listing: herdrcli.WorktreeListing{
			Source:    herdrcli.WorktreeSource{RepoRoot: repoPath},
			Worktrees: []herdrcli.Worktree{{Path: checkoutPath, Branch: "PAY-101", IsLinked: true}},
		}},
	}
	for name, cli := range cases {
		t.Run(name, func(t *testing.T) {
			a := newAdapter(cli)
			if err := a.CloseTerminals(context.Background(), runSpec()); err != nil {
				t.Fatalf("CloseTerminals = %v, want roll-forward success", err)
			}
			if err := a.CleanupRun(context.Background(), runSpec()); err != nil {
				t.Fatalf("CleanupRun = %v, want roll-forward success", err)
			}
			if len(cli.closedPanes) != 0 || len(cli.closedWorkspaces) != 0 {
				t.Fatal("cleanup touched Herdr state for an absent environment")
			}
		})
	}
}

func TestCleanupPropagatesTransportFailures(t *testing.T) {
	cli := &fakeClient{listingErr: errors.New("herdr server unavailable")}
	if err := newAdapter(cli).CloseTerminals(context.Background(), runSpec()); err == nil {
		t.Fatal("CloseTerminals hid a transport failure")
	}
	if err := newAdapter(cli).CleanupRun(context.Background(), runSpec()); err == nil {
		t.Fatal("CleanupRun hid a transport failure")
	}
}
