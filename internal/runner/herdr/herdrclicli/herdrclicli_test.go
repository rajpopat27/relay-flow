package herdrclicli

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
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
