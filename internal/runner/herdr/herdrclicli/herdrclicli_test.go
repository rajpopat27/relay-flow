package herdrclicli

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestCLIUsesExactProductionCommandShapes(t *testing.T) {
	installStrictFakeHerdr(t)
	cli := New(Options{Session: "relay-flow", SocketPath: "/tmp/relay-flow-herdr.sock"})
	ctx := context.Background()

	if _, err := cli.Snapshot(ctx); err != nil {
		t.Fatalf("Snapshot: %v", err)
	}

	if _, _, err := cli.CreateTab(ctx, "workspace-1", "/work/payments", "PAY-101:coding", map[string]string{
		"RELAY_FLOW_NODE":   "coding",
		"RELAY_FLOW_TICKET": "PAY-101",
	}); err != nil {
		t.Fatalf("CreateTab: %v", err)
	}

	if _, err := cli.ListTabs(ctx, "workspace-1"); err != nil {
		t.Fatalf("ListTabs: %v", err)
	}
	if _, err := cli.ListPanes(ctx, "workspace-1"); err != nil {
		t.Fatalf("ListPanes: %v", err)
	}
	if _, err := cli.GetPane(ctx, "pane-1"); err != nil {
		t.Fatalf("GetPane: %v", err)
	}
	if _, err := cli.ProcessInfo(ctx, "pane-1"); err != nil {
		t.Fatalf("ProcessInfo: %v", err)
	}

	if err := cli.RenamePane(ctx, "pane-1", "PAY-101:coding"); err != nil {
		t.Fatalf("RenamePane: %v", err)
	}
	command := "opencode --agent build --prompt 'line one\nline two'"
	if err := cli.RunPane(ctx, "pane-1", command); err != nil {
		t.Fatalf("RunPane: %v", err)
	}
	if err := cli.ClosePane(ctx, "pane-1"); err != nil {
		t.Fatalf("ClosePane: %v", err)
	}
}

func TestCLIDecodesCapturedResponseLocations(t *testing.T) {
	installStrictFakeHerdr(t)
	cli := New(Options{Session: "relay-flow", SocketPath: "/tmp/relay-flow-herdr.sock"})
	ctx := context.Background()

	snapshot, err := cli.Snapshot(ctx)
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	if len(snapshot.Workspaces) != 1 || snapshot.Workspaces[0] != (Workspace{ID: "workspace-1", Label: "payments"}) {
		t.Fatalf("Snapshot workspaces = %+v", snapshot.Workspaces)
	}
	if len(snapshot.Tabs) != 1 || snapshot.Tabs[0] != (Tab{ID: "tab-1", WorkspaceID: "workspace-1", Label: "PAY-101:coding"}) {
		t.Fatalf("Snapshot tabs = %+v", snapshot.Tabs)
	}
	if len(snapshot.Panes) != 1 || snapshot.Panes[0].ID != "pane-1" || snapshot.Panes[0].TerminalID != "terminal-1" {
		t.Fatalf("Snapshot panes = %+v", snapshot.Panes)
	}

	tab, rootPane, err := cli.CreateTab(ctx, "workspace-1", "/work/payments", "PAY-101:coding", map[string]string{
		"RELAY_FLOW_NODE":   "coding",
		"RELAY_FLOW_TICKET": "PAY-101",
	})
	if err != nil {
		t.Fatalf("CreateTab: %v", err)
	}
	if tab != (Tab{ID: "tab-created", WorkspaceID: "workspace-1", Label: "PAY-101:coding"}) {
		t.Fatalf("CreateTab tab = %+v", tab)
	}
	if rootPane.ID != "pane-created" || rootPane.TerminalID != "terminal-created" || rootPane.TabID != "tab-created" || rootPane.CWD != "/work/payments" {
		t.Fatalf("CreateTab root pane = %+v", rootPane)
	}

	tabs, err := cli.ListTabs(ctx, "workspace-1")
	if err != nil || len(tabs) != 1 || tabs[0].ID != "tab-1" || tabs[0].Label != "PAY-101:coding" {
		t.Fatalf("ListTabs = %+v, %v", tabs, err)
	}
	panes, err := cli.ListPanes(ctx, "workspace-1")
	if err != nil || len(panes) != 1 || panes[0].ID != "pane-1" || panes[0].CWD != "/work/payments" {
		t.Fatalf("ListPanes = %+v, %v", panes, err)
	}
	pane, err := cli.GetPane(ctx, "pane-1")
	if err != nil || pane.ID != "pane-1" || pane.Label != "PAY-101:coding" || pane.ForegroundCWD != "/work/payments" {
		t.Fatalf("GetPane = %+v, %v", pane, err)
	}
	processInfo, err := cli.ProcessInfo(ctx, "pane-1")
	if err != nil || processInfo.PaneID != "pane-1" || processInfo.ShellPID == nil || *processInfo.ShellPID != 1234 || len(processInfo.ForegroundProcesses) != 1 {
		t.Fatalf("ProcessInfo = %+v, %v", processInfo, err)
	}
	process := processInfo.ForegroundProcesses[0]
	if process.PID != 2345 || process.Name != "opencode" || process.Argv0 != "opencode" || len(process.Argv) != 3 {
		t.Fatalf("ForegroundProcess = %+v", process)
	}
}

func TestCLIHandlesEmptyResults(t *testing.T) {
	installStrictFakeHerdr(t)
	cli := New(Options{Session: "relay-flow", SocketPath: "/tmp/relay-flow-herdr.sock"})
	ctx := context.Background()

	tabs, err := cli.ListTabs(ctx, "empty")
	if err != nil {
		t.Fatalf("ListTabs(empty): %v", err)
	}
	if tabs == nil || len(tabs) != 0 {
		t.Fatalf("ListTabs(empty) = %+v, want non-nil empty result", tabs)
	}
	panes, err := cli.ListPanes(ctx, "empty")
	if err != nil {
		t.Fatalf("ListPanes(empty): %v", err)
	}
	if panes == nil || len(panes) != 0 {
		t.Fatalf("ListPanes(empty) = %+v, want non-nil empty result", panes)
	}
}

func TestCLIReportsMalformedOutputStderrAndNonzeroErrors(t *testing.T) {
	installStrictFakeHerdr(t)
	cli := New(Options{Session: "relay-flow", SocketPath: "/tmp/relay-flow-herdr.sock"})
	ctx := context.Background()

	if _, err := cli.GetPane(ctx, "malformed"); err == nil {
		t.Fatal("GetPane accepted malformed JSON")
	}
	if pane, err := cli.GetPane(ctx, "stderr"); err != nil || pane.ID != "pane-1" {
		t.Fatalf("GetPane(stderr) = %+v, %v", pane, err)
	}
	if _, err := cli.GetPane(ctx, "error"); err == nil {
		t.Fatal("GetPane accepted a nonzero Herdr error")
	} else if !strings.Contains(err.Error(), "pane pane-error not found") {
		t.Fatalf("GetPane(error) = %v, want API error message", err)
	}
}

func TestStrictFakeHerdrRejectsUnsupportedProductionShapes(t *testing.T) {
	fake := installStrictFakeHerdr(t)
	unsupported := [][]string{
		{"workspace", "create", "--cwd", "/work/payments", "--label", "payments", "--no-focus"},
		{"terminal", "create", "--pane", "pane-1"},
		{"pane", "get", "--pane", "pane-1"},
		{"pane", "process-info", "pane-1"},
		{"tab", "list", "workspace-1"},
	}
	for _, args := range unsupported {
		cmd := exec.Command(fake, args...)
		if err := cmd.Run(); err == nil {
			t.Errorf("strict fake accepted unsupported invocation %v", args)
		}
	}
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
	t.Setenv("HERDR_EXPECT_CWD", "/work/payments")
	t.Setenv("HERDR_EXPECT_ENVS", "RELAY_FLOW_NODE=coding;RELAY_FLOW_TICKET=PAY-101")
	t.Setenv("HERDR_EXPECT_SESSION", "relay-flow")
	t.Setenv("HERDR_EXPECT_SOCKET_PATH", "/tmp/relay-flow-herdr.sock")
	return fake
}
