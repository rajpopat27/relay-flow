package orcacli

import (
	"os/exec"
	"strings"
	"testing"
)

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
