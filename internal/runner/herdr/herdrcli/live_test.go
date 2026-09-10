package herdrcli

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// TestLiveInstalledHerdr drives the production wrapper against the installed
// Herdr binary. It is skipped unless RELAY_FLOW_HERDR_LIVE=1 so default CI,
// which has no Herdr, stays green; the fixtures in this package are captured
// from exactly this flow. It never touches the default Herdr session.
//
//	RELAY_FLOW_HERDR_LIVE=1 go test ./internal/runner/herdr/herdrcli/ -run Live -v
func TestLiveInstalledHerdr(t *testing.T) {
	if os.Getenv("RELAY_FLOW_HERDR_LIVE") != "1" {
		t.Skip("set RELAY_FLOW_HERDR_LIVE=1 to run against the installed Herdr binary")
	}
	if _, err := exec.LookPath("herdr"); err != nil {
		t.Fatalf("herdr executable not found: %v", err)
	}

	ctx := context.Background()
	session := "relay-flow-live-" + filepath.Base(t.TempDir())
	repo := t.TempDir()
	runGit(t, repo, "init", "-q", "-b", "main", ".")
	runGit(t, repo, "config", "user.email", "live@relay-flow.test")
	runGit(t, repo, "config", "user.name", "live")
	if err := os.WriteFile(filepath.Join(repo, "README.md"), []byte("live\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, repo, "add", "-A")
	runGit(t, repo, "commit", "-qm", "init")

	server := exec.Command("herdr", "--session", session, "server")
	if err := server.Start(); err != nil {
		t.Fatalf("start herdr server: %v", err)
	}
	cli := New(Options{Session: session})
	t.Cleanup(func() {
		_ = exec.Command("herdr", "--session", session, "server", "stop").Run()
		_ = server.Wait()
		_ = exec.Command("herdr", "session", "delete", session, "--json").Run()
	})

	var ready bool
	for i := 0; i < 200; i++ {
		if _, err := cli.Snapshot(ctx); err == nil {
			ready = true
			break
		}
	}
	if !ready {
		t.Fatal("herdr session never became ready through the production wrapper")
	}

	// Missing branch must be reported as ErrWorktreeNotFound so the adapter
	// can create instead of failing the run.
	if _, err := cli.WorktreeOpen(ctx, repo, "LIVE-1", "LIVE-1"); !errors.Is(err, ErrWorktreeNotFound) {
		t.Fatalf("WorktreeOpen(missing) = %v, want ErrWorktreeNotFound", err)
	}
	workspace, err := cli.WorktreeCreate(ctx, repo, "LIVE-1", "main", "LIVE-1")
	if err != nil {
		t.Fatalf("WorktreeCreate: %v", err)
	}
	t.Cleanup(func() {
		_ = os.RemoveAll(workspace.Worktree.CheckoutPath)
		// Herdr groups checkouts under a per-repository directory; remove it
		// too when this test's checkout was the only one in it.
		_ = os.Remove(filepath.Dir(workspace.Worktree.CheckoutPath))
	})
	if workspace.ID == "" || workspace.Worktree.CheckoutPath == "" || workspace.Worktree.RepoRoot == "" {
		t.Fatalf("WorktreeCreate workspace = %+v", workspace)
	}
	if reopened, err := cli.WorktreeOpen(ctx, repo, "LIVE-1", "LIVE-1"); err != nil || reopened.ID != workspace.ID {
		t.Fatalf("WorktreeOpen = %+v, %v", reopened, err)
	}

	listing, err := cli.WorktreeList(ctx, repo)
	if err != nil {
		t.Fatalf("WorktreeList: %v", err)
	}
	if listing.Source.RepoRoot == "" || len(listing.Worktrees) < 2 {
		t.Fatalf("WorktreeList = %+v", listing)
	}

	_, pane, err := cli.CreateTab(ctx, workspace.ID, workspace.Worktree.CheckoutPath, "LIVE-1:coding")
	if err != nil {
		t.Fatalf("CreateTab: %v", err)
	}
	if err := cli.RenamePane(ctx, pane.ID, "LIVE-1:coding"); err != nil {
		t.Fatalf("RenamePane: %v", err)
	}
	// A real multiline, env-carrying command line, exactly as the adapter
	// renders it.
	if err := cli.RunPane(ctx, pane.ID, "RELAY_FLOW_NODE='coding' sleep 30"); err != nil {
		t.Fatalf("RunPane: %v", err)
	}
	if got, err := cli.GetPane(ctx, pane.ID); err != nil || got.Label != "LIVE-1:coding" {
		t.Fatalf("GetPane = %+v, %v", got, err)
	}
	if _, err := cli.ProcessInfo(ctx, pane.ID); err != nil {
		t.Fatalf("ProcessInfo: %v", err)
	}
	if _, err := cli.GetPane(ctx, "w99:p99"); !errors.Is(err, ErrPaneNotFound) {
		t.Fatalf("GetPane(missing) = %v, want ErrPaneNotFound", err)
	}
	if _, err := cli.ListTabs(ctx, "w99"); !errors.Is(err, ErrWorkspaceNotFound) {
		t.Fatalf("ListTabs(missing) = %v, want ErrWorkspaceNotFound", err)
	}
	if _, err := cli.WorktreeList(ctx, t.TempDir()); !errors.Is(err, ErrNotGitWorktree) {
		t.Fatalf("WorktreeList(non-repo) = %v, want ErrNotGitWorktree", err)
	}
	if err := cli.ClosePane(ctx, pane.ID); err != nil {
		t.Fatalf("ClosePane: %v", err)
	}
	if err := cli.CloseWorkspace(ctx, workspace.ID); err != nil {
		t.Fatalf("CloseWorkspace: %v", err)
	}
}

func runGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v: %s", args, err, out)
	}
}
