package orcacli

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestCLIContractsAgainstCapturedRealOutput(t *testing.T) {
	installStrictFakeOrca(t)
	ctx := context.Background()
	cli := New()

	repos, err := cli.ListRepos(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(repos) != 1 || repos[0].ID != "repo-1" || repos[0].DisplayName != "app" || repos[0].Path != "/srv/app" {
		t.Fatalf("ListRepos = %+v", repos)
	}

	worktrees, err := cli.ListWorktrees(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(worktrees) != 1 || worktrees[0].ID != "wt-main" || worktrees[0].Branch != "refs/heads/master" || !worktrees[0].IsMainWorktree {
		t.Fatalf("ListWorktrees = %+v", worktrees)
	}

	if err := cli.CreateWorktree(ctx, "PAY-101", "repo-1", "wt-main", "origin/alice/PAY-101"); err != nil {
		t.Fatal(err)
	}
	if err := cli.SetWorktreeStatus(ctx, "wt-PAY-101", "in-review"); err != nil {
		t.Fatal(err)
	}
	if err := cli.DeleteWorktree(ctx, "wt-PAY-101"); err != nil {
		t.Fatal(err)
	}

	terminals, err := cli.ListTerminals(ctx, "id:wt-PAY-101")
	if err != nil {
		t.Fatal(err)
	}
	if len(terminals) != 1 {
		t.Fatalf("ListTerminals = %+v", terminals)
	}
	if got := terminals[0]; got.Handle != "term-1" || got.Title != "PAY-101:implement" || !got.Connected {
		t.Fatalf("ListTerminals[0] = %+v", got)
	}
	if terminals[0].Title == "opencode" {
		t.Fatal("ListTerminals used mutable pane title instead of stable visualLayouts tab title")
	}
	direct, err := cli.ShowTerminal(ctx, "term-1")
	if err != nil || direct.Handle != "term-1" || !direct.Connected {
		t.Fatalf("ShowTerminal = %+v, %v", direct, err)
	}
	if err := cli.SendTerminal(ctx, "term-1", "hello"); err != nil {
		t.Fatal(err)
	}

	handle, err := cli.CreateTerminal(ctx, "PAY-101", "PAY-101:implement", "echo hello")
	if err != nil {
		t.Fatal(err)
	}
	if handle != "term-1" {
		t.Fatalf("CreateTerminal handle = %q", handle)
	}
	if err := cli.CloseTerminal(ctx, handle); err != nil {
		t.Fatal(err)
	}
}

func TestStrictFakeOrcaRejectsMissingFlags(t *testing.T) {
	fake := installStrictFakeOrca(t)
	if err := exec.Command(fake, "terminal", "list", "--worktree", "id:wt-PAY-101", "--json").Run(); err == nil {
		t.Fatal("strict fake accepted terminal list without --include-visual-layouts")
	}
}

func TestFindExistingBranchLocalOnly(t *testing.T) {
	repo := newGitRepo(t)
	runGit(t, repo, "branch", "alice/PAY-101")

	got, ok, err := FindExistingBranch(repo, "PAY-101")
	if err != nil {
		t.Fatal(err)
	}
	if !ok || got != "alice/PAY-101" {
		t.Fatalf("FindExistingBranch = %q, %v; want alice/PAY-101, true", got, ok)
	}
}

func TestFindExistingBranchRemoteOnly(t *testing.T) {
	repo := newGitRepo(t)
	runGit(t, repo, "update-ref", "refs/remotes/origin/alice/PAY-102", "HEAD")

	got, ok, err := FindExistingBranch(repo, "PAY-102")
	if err != nil {
		t.Fatal(err)
	}
	if !ok || got != "origin/alice/PAY-102" {
		t.Fatalf("FindExistingBranch = %q, %v; want origin/alice/PAY-102, true", got, ok)
	}
}

func TestFindExistingBranchMissing(t *testing.T) {
	repo := newGitRepo(t)
	runGit(t, repo, "branch", "alice/OTHER-1")

	got, ok, err := FindExistingBranch(repo, "PAY-103")
	if err != nil {
		t.Fatal(err)
	}
	if ok || got != "" {
		t.Fatalf("FindExistingBranch = %q, %v; want empty, false", got, ok)
	}
}

func newGitRepo(t *testing.T) string {
	t.Helper()
	repo := t.TempDir()
	runGit(t, repo, "init", "--quiet")
	runGit(t, repo, "-c", "user.name=Relay Flow", "-c", "user.email=relay-flow@example.invalid", "commit", "--allow-empty", "--quiet", "-m", "initial")
	return repo
}

func runGit(t *testing.T, repo string, args ...string) {
	t.Helper()
	cmdArgs := append([]string{"-C", repo}, args...)
	out, err := exec.Command("git", cmdArgs...).CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v: %s", strings.Join(args, " "), err, out)
	}
}

func installStrictFakeOrca(t *testing.T) string {
	t.Helper()
	binDir := t.TempDir()
	fake := filepath.Join(binDir, "orca")
	script, err := os.ReadFile(filepath.Join("testdata", "strict-orca.sh"))
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
	t.Setenv("ORCA_FIXTURE_DIR", fixtureDir)
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	return fake
}
