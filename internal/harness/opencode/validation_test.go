package opencode

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestValidateAgentPassesRepositoryToListAgentsSeam(t *testing.T) {
	repoPath := t.TempDir()
	var gotRepoPath string
	h := New()
	h.listAgents = func(_ context.Context, path string) ([]string, error) {
		gotRepoPath = path
		return []string{"build"}, nil
	}

	if err := h.ValidateAgent(context.Background(), repoPath, "build"); err != nil {
		t.Fatalf("ValidateAgent: %v", err)
	}
	if gotRepoPath != repoPath {
		t.Fatalf("listAgents repoPath = %q, want %q", gotRepoPath, repoPath)
	}
}

func TestValidateAgentUsesRepositoryWorkingDirectory(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("fake opencode uses a POSIX shell")
	}

	repoA := t.TempDir()
	repoB := t.TempDir()
	fakeDir := t.TempDir()
	fake := filepath.Join(fakeDir, "opencode")
	script := `#!/bin/sh
set -eu
[ "$#" -eq 2 ] && [ "$1" = "agent" ] && [ "$2" = "list" ] || exit 2
actual=$(pwd)
case "$actual" in
  "$OPENCODE_REPO_A")
    printf 'repo-a-agent details\n'
    ;;
  "$OPENCODE_REPO_B")
    printf 'repo-b-agent details\n'
    ;;
  *)
    printf 'unexpected working directory: %s\n' "$actual" >&2
    exit 3
    ;;
esac
printf '  ignored-indented\n'
printf '[ignored-json]\n'
printf '{"ignored":true}\n'
`
	if err := os.WriteFile(fake, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", fakeDir)
	t.Setenv("OPENCODE_REPO_A", repoA)
	t.Setenv("OPENCODE_REPO_B", repoB)

	h := New()
	if err := h.ValidateAgent(context.Background(), repoA, "repo-a-agent"); err != nil {
		t.Fatalf("agent from repository A rejected: %v", err)
	}
	if err := h.ValidateAgent(context.Background(), repoB, "repo-b-agent"); err != nil {
		t.Fatalf("agent from repository B rejected: %v", err)
	}
	if err := h.ValidateAgent(context.Background(), repoA, "repo-b-agent"); err == nil {
		t.Fatal("repository B agent accepted while validating repository A")
	}
	if err := h.ValidateAgent(context.Background(), repoB, "repo-a-agent"); err == nil {
		t.Fatal("repository A agent accepted while validating repository B")
	}
}

func TestValidateAgentPropagatesListCommandFailure(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("fake opencode uses a POSIX shell")
	}

	fakeDir := t.TempDir()
	fake := filepath.Join(fakeDir, "opencode")
	script := `#!/bin/sh
set -eu
[ "$#" -eq 2 ] && [ "$1" = "agent" ] && [ "$2" = "list" ] || exit 2
printf 'agent list failed\n' >&2
exit 17
`
	if err := os.WriteFile(fake, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", fakeDir)

	h := New()
	err := h.ValidateAgent(context.Background(), t.TempDir(), "build")
	if err == nil {
		t.Fatal("ValidateAgent succeeded after opencode agent list failed")
	}
	if !strings.Contains(err.Error(), "opencode agent list") {
		t.Fatalf("error = %q, want opencode agent list context", err)
	}
}
