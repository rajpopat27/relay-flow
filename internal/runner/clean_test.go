package runner

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestCheckCleanCheckoutRejectsGitChanges(t *testing.T) {
	cases := map[string]func(t *testing.T, repo string){
		"untracked": func(t *testing.T, repo string) {
			writeFile(t, filepath.Join(repo, "untracked.txt"), "new\n")
		},
		"unstaged": func(t *testing.T, repo string) {
			writeFile(t, filepath.Join(repo, "tracked.txt"), "changed\n")
		},
		"staged": func(t *testing.T, repo string) {
			writeFile(t, filepath.Join(repo, "tracked.txt"), "changed\n")
			runGit(t, repo, "add", "tracked.txt")
		},
	}
	for name, makeDirty := range cases {
		t.Run(name, func(t *testing.T) {
			repo := newCleanCheckout(t)
			makeDirty(t, repo)
			err := CheckCleanCheckout(context.Background(), "PAY-101", repo)
			if err == nil || !strings.Contains(err.Error(), "PAY-101") || !strings.Contains(err.Error(), "commit required") {
				t.Fatalf("CheckCleanCheckout error = %v, want ticket and commit-required message", err)
			}
		})
	}
}

func TestCheckCleanCheckoutAcceptsCleanAndMissingCheckouts(t *testing.T) {
	repo := newCleanCheckout(t)
	if err := CheckCleanCheckout(context.Background(), "PAY-101", repo); err != nil {
		t.Fatalf("clean checkout returned %v", err)
	}
	if err := CheckCleanCheckout(context.Background(), "PAY-101", filepath.Join(t.TempDir(), "removed")); err != nil {
		t.Fatalf("missing checkout returned %v, want idempotent success", err)
	}
}

func TestCheckCleanCheckoutPropagatesGitStatusFailure(t *testing.T) {
	dir := t.TempDir()
	err := CheckCleanCheckout(context.Background(), "PAY-101", dir)
	if err == nil {
		t.Fatal("non-Git checkout was treated as clean")
	}
	if strings.Contains(err.Error(), "commit required") {
		t.Fatalf("Git status failure was reported as dirty state: %v", err)
	}
}

func newCleanCheckout(t *testing.T) string {
	t.Helper()
	repo := t.TempDir()
	runGit(t, repo, "init", "-q", "-b", "main")
	runGit(t, repo, "config", "user.email", "relay-flow@example.invalid")
	runGit(t, repo, "config", "user.name", "Relay Flow")
	writeFile(t, filepath.Join(repo, "tracked.txt"), "clean\n")
	runGit(t, repo, "add", "tracked.txt")
	runGit(t, repo, "commit", "-qm", "initial")
	return repo
}

func writeFile(t *testing.T, path, contents string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(contents), 0o644); err != nil {
		t.Fatal(err)
	}
}

func runGit(t *testing.T, repo string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = repo
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v: %s", args, err, output)
	}
}
