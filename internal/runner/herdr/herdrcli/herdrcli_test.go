package herdrcli

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestCLIUsesExactProductionCommandShapes(t *testing.T) {
	installStrictFakeHerdr(t)
	cli := newTestCLI()
	ctx := context.Background()

	if _, err := cli.Snapshot(ctx); err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	if _, err := cli.WorktreeList(ctx, "/work/payments"); err != nil {
		t.Fatalf("WorktreeList: %v", err)
	}
	if _, err := cli.WorktreeOpen(ctx, "/work/payments", "PAY-101", "PAY-101"); err != nil {
		t.Fatalf("WorktreeOpen: %v", err)
	}
	if _, err := cli.WorktreeCreate(ctx, "/work/payments", "PAY-101", "origin/main", "PAY-101"); err != nil {
		t.Fatalf("WorktreeCreate: %v", err)
	}
	if _, _, err := cli.CreateTab(ctx, "w2", "/work/worktrees/payments/pay-101", "PAY-101:coding"); err != nil {
		t.Fatalf("CreateTab: %v", err)
	}
	if _, err := cli.ListTabs(ctx, "w2"); err != nil {
		t.Fatalf("ListTabs: %v", err)
	}
	if _, err := cli.ListPanes(ctx, "w2"); err != nil {
		t.Fatalf("ListPanes: %v", err)
	}
	if _, err := cli.GetPane(ctx, "w2:p2"); err != nil {
		t.Fatalf("GetPane: %v", err)
	}
	if _, err := cli.ProcessInfo(ctx, "w2:p2"); err != nil {
		t.Fatalf("ProcessInfo: %v", err)
	}
	if err := cli.RenamePane(ctx, "w2:p2", "PAY-101:coding"); err != nil {
		t.Fatalf("RenamePane: %v", err)
	}
	if err := cli.RunPane(ctx, "w2:p2", "RELAY_FLOW_NODE='coding' 'opencode' '--prompt' 'line one\nline two'"); err != nil {
		t.Fatalf("RunPane: %v", err)
	}
	if err := cli.ClosePane(ctx, "w2:p2"); err != nil {
		t.Fatalf("ClosePane: %v", err)
	}
	if err := cli.CloseWorkspace(ctx, "w2"); err != nil {
		t.Fatalf("CloseWorkspace: %v", err)
	}
}

func TestCLIDecodesCapturedResponseLocations(t *testing.T) {
	installStrictFakeHerdr(t)
	cli := newTestCLI()
	ctx := context.Background()

	snapshot, err := cli.Snapshot(ctx)
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	if len(snapshot.Workspaces) != 2 {
		t.Fatalf("Snapshot workspaces = %+v", snapshot.Workspaces)
	}
	source := snapshot.Workspaces[0]
	if source.ID != "w1" || source.Worktree.RepoRoot != "/work/payments" || source.Worktree.IsLinked {
		t.Fatalf("source workspace = %+v", source)
	}
	ticket := snapshot.Workspaces[1]
	if ticket.ID != "w2" || ticket.Label != "PAY-101" || !ticket.Worktree.IsLinked ||
		ticket.Worktree.CheckoutPath != "/work/worktrees/payments/pay-101" ||
		ticket.Worktree.RepoRoot != "/work/payments" {
		t.Fatalf("ticket workspace = %+v", ticket)
	}
	if len(snapshot.Tabs) != 3 || snapshot.Tabs[2] != (Tab{ID: "w2:t2", WorkspaceID: "w2", Label: "PAY-101:coding"}) {
		t.Fatalf("Snapshot tabs = %+v", snapshot.Tabs)
	}
	if len(snapshot.Panes) != 3 || snapshot.Panes[2].ID != "w2:p2" || snapshot.Panes[2].TerminalID == "" {
		t.Fatalf("Snapshot panes = %+v", snapshot.Panes)
	}

	listing, err := cli.WorktreeList(ctx, "/work/payments")
	if err != nil {
		t.Fatalf("WorktreeList: %v", err)
	}
	if listing.Source.RepoRoot != "/work/payments" || listing.Source.RepoName != "repo" {
		t.Fatalf("WorktreeList source = %+v", listing.Source)
	}
	if len(listing.Worktrees) != 2 {
		t.Fatalf("WorktreeList worktrees = %+v", listing.Worktrees)
	}
	if listing.Worktrees[1] != (Worktree{
		Path:            "/work/worktrees/payments/pay-101",
		Branch:          "PAY-101",
		IsLinked:        true,
		OpenWorkspaceID: "w2",
	}) {
		t.Fatalf("ticket worktree = %+v", listing.Worktrees[1])
	}

	created, err := cli.WorktreeCreate(ctx, "/work/payments", "PAY-101", "origin/main", "PAY-101")
	if err != nil {
		t.Fatalf("WorktreeCreate: %v", err)
	}
	if created.ID != "w2" || created.Worktree.CheckoutPath != "/work/worktrees/payments/pay-101" {
		t.Fatalf("WorktreeCreate workspace = %+v", created)
	}
	opened, err := cli.WorktreeOpen(ctx, "/work/payments", "PAY-101", "PAY-101")
	if err != nil {
		t.Fatalf("WorktreeOpen: %v", err)
	}
	if opened.ID != "w2" || opened.Worktree.CheckoutPath != "/work/worktrees/payments/pay-101" {
		t.Fatalf("WorktreeOpen workspace = %+v", opened)
	}

	tab, rootPane, err := cli.CreateTab(ctx, "w2", "/work/worktrees/payments/pay-101", "PAY-101:coding")
	if err != nil {
		t.Fatalf("CreateTab: %v", err)
	}
	if tab != (Tab{ID: "w2:t2", WorkspaceID: "w2", Label: "PAY-101:coding"}) {
		t.Fatalf("CreateTab tab = %+v", tab)
	}
	if rootPane.ID != "w2:p2" || rootPane.TabID != "w2:t2" || rootPane.CWD != "/work/worktrees/payments/pay-101" {
		t.Fatalf("CreateTab root pane = %+v", rootPane)
	}

	tabs, err := cli.ListTabs(ctx, "w2")
	if err != nil || len(tabs) != 2 || tabs[1].Label != "PAY-101:coding" {
		t.Fatalf("ListTabs = %+v, %v", tabs, err)
	}
	panes, err := cli.ListPanes(ctx, "w2")
	if err != nil || len(panes) != 2 || panes[1].Label != "PAY-101:coding" {
		t.Fatalf("ListPanes = %+v, %v", panes, err)
	}
	pane, err := cli.GetPane(ctx, "w2:p2")
	if err != nil || pane.ID != "w2:p2" || pane.Label != "PAY-101:coding" {
		t.Fatalf("GetPane = %+v, %v", pane, err)
	}

	info, err := cli.ProcessInfo(ctx, "w2:p2")
	if err != nil || info.PaneID != "w2:p2" || info.ShellPID == nil || len(info.ForegroundProcesses) != 1 {
		t.Fatalf("ProcessInfo = %+v, %v", info, err)
	}
	if info.ForegroundProcesses[0].PID == *info.ShellPID {
		t.Fatalf("running command fixture must not report the shell as foreground: %+v", info)
	}
	shellInfo, err := cli.ProcessInfo(ctx, "shell")
	if err != nil || shellInfo.ShellPID == nil || len(shellInfo.ForegroundProcesses) != 1 {
		t.Fatalf("ProcessInfo(shell) = %+v, %v", shellInfo, err)
	}
	if shellInfo.ForegroundProcesses[0].PID != *shellInfo.ShellPID {
		t.Fatalf("restored shell fixture must report the shell as foreground: %+v", shellInfo)
	}
}

func TestCLIHandlesEmptyResults(t *testing.T) {
	installStrictFakeHerdr(t)
	cli := newTestCLI()
	ctx := context.Background()

	tabs, err := cli.ListTabs(ctx, "empty")
	if err != nil || tabs == nil || len(tabs) != 0 {
		t.Fatalf("ListTabs(empty) = %+v, %v", tabs, err)
	}
	panes, err := cli.ListPanes(ctx, "empty")
	if err != nil || panes == nil || len(panes) != 0 {
		t.Fatalf("ListPanes(empty) = %+v, %v", panes, err)
	}
}

func TestCLIMapsHerdrErrorCodesToSentinels(t *testing.T) {
	installStrictFakeHerdr(t)
	cli := newTestCLI()
	ctx := context.Background()

	if _, err := cli.GetPane(ctx, "missing"); !errors.Is(err, ErrPaneNotFound) {
		t.Fatalf("GetPane(missing) = %v, want ErrPaneNotFound", err)
	}
	if _, err := cli.ProcessInfo(ctx, "missing"); !errors.Is(err, ErrPaneNotFound) {
		t.Fatalf("ProcessInfo(missing) = %v, want ErrPaneNotFound", err)
	}
	if err := cli.ClosePane(ctx, "missing"); !errors.Is(err, ErrPaneNotFound) {
		t.Fatalf("ClosePane(missing) = %v, want ErrPaneNotFound", err)
	}
	if _, err := cli.ListTabs(ctx, "missing"); !errors.Is(err, ErrWorkspaceNotFound) {
		t.Fatalf("ListTabs(missing) = %v, want ErrWorkspaceNotFound", err)
	}
	if _, err := cli.ListPanes(ctx, "missing"); !errors.Is(err, ErrWorkspaceNotFound) {
		t.Fatalf("ListPanes(missing) = %v, want ErrWorkspaceNotFound", err)
	}
	if err := cli.CloseWorkspace(ctx, "missing"); !errors.Is(err, ErrWorkspaceNotFound) {
		t.Fatalf("CloseWorkspace(missing) = %v, want ErrWorkspaceNotFound", err)
	}
	if _, err := cli.WorktreeOpen(ctx, "/work/payments", "MISSING-1", "MISSING-1"); !errors.Is(err, ErrWorktreeNotFound) {
		t.Fatalf("WorktreeOpen(missing) = %v, want ErrWorktreeNotFound", err)
	}
	if _, err := cli.WorktreeList(ctx, "/work/not-a-repo"); !errors.Is(err, ErrNotGitWorktree) {
		t.Fatalf("WorktreeList(non-repo) = %v, want ErrNotGitWorktree", err)
	}
	if _, err := cli.GetPane(ctx, "missing"); !strings.Contains(err.Error(), "pane w9:p9 not found") {
		t.Fatalf("error message = %v, want Herdr's own message", err)
	}
}

func TestCLIReportsMalformedOutputAndToleratesStderrWarnings(t *testing.T) {
	installStrictFakeHerdr(t)
	cli := newTestCLI()
	ctx := context.Background()

	if _, err := cli.GetPane(ctx, "malformed"); err == nil {
		t.Fatal("GetPane accepted malformed JSON")
	}
	if pane, err := cli.GetPane(ctx, "warning"); err != nil || pane.ID != "w2:p2" {
		t.Fatalf("GetPane(warning) = %+v, %v", pane, err)
	}
}

func TestCLIRejectsRelativeCWD(t *testing.T) {
	installStrictFakeHerdr(t)
	cli := newTestCLI()
	if _, err := cli.WorktreeList(context.Background(), "relative/path"); err == nil {
		t.Fatal("WorktreeList accepted a relative --cwd")
	}
}

func TestStrictFakeHerdrRejectsUnsupportedProductionShapes(t *testing.T) {
	fake := installStrictFakeHerdr(t)
	unsupported := [][]string{
		{"workspace", "create", "--cwd", "/work/payments", "--label", "payments", "--no-focus"},
		{"worktree", "remove", "--workspace", "w2"},
		{"terminal", "create", "--pane", "w2:p2"},
		{"pane", "get", "--pane", "w2:p2"},
		{"pane", "process-info", "w2:p2"},
		{"tab", "list", "w2"},
		{"tab", "create", "--workspace", "w2", "--cwd", "/work/payments", "--label", "x", "--no-focus", "--env", "K=V"},
	}
	for _, args := range unsupported {
		if err := exec.Command(fake, args...).Run(); err == nil {
			t.Errorf("strict fake accepted unsupported invocation %v", args)
		}
	}
}

func newTestCLI() *CLI {
	return New(Options{Session: "relay-flow", SocketPath: "/tmp/relay-flow-herdr.sock"})
}

func installStrictFakeHerdr(t *testing.T) string {
	t.Helper()
	binDir := t.TempDir()
	fake := filepath.Join(binDir, "herdr")
	script, err := os.ReadFile(filepath.Join("testdata", "strict-herdr.sh"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(fake, script, 0o755); err != nil {
		t.Fatal(err)
	}
	fixtureDir, err := filepath.Abs("testdata")
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("HERDR_FIXTURE_DIR", fixtureDir)
	t.Setenv("HERDR_EXPECT_SESSION", "relay-flow")
	t.Setenv("HERDR_EXPECT_SOCKET_PATH", "/tmp/relay-flow-herdr.sock")
	return fake
}
