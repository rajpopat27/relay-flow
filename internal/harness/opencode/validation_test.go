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

// TestValidateAgentUsesRepositoryWorkingDirectory verifies that
// `opencode debug agents` is invoked in the correct repository directory,
// so per-repo agent definitions are resolved independently.
func TestValidateAgentUsesRepositoryWorkingDirectory(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("fake opencode uses a POSIX shell")
	}

	repoA := t.TempDir()
	repoB := t.TempDir()
	fakeDir := t.TempDir()
	fake := filepath.Join(fakeDir, "opencode")
	// Fake opencode that responds to `opencode debug agents` with a
	// per-directory JSON agent list matching the OpenCode v2 output format.
	script := `#!/bin/sh
set -eu
[ "$#" -eq 2 ] && [ "$1" = "debug" ] && [ "$2" = "agents" ] || exit 2
actual=$(pwd)
case "$actual" in
  "$OPENCODE_REPO_A")
    printf '[{"id":"repo-a-agent"}]\n'
    ;;
  "$OPENCODE_REPO_B")
    printf '[{"id":"repo-b-agent"}]\n'
    ;;
  *)
    printf 'unexpected working directory: %s\n' "$actual" >&2
    exit 3
    ;;
esac
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

// TestValidateAgentIncludesGlobalBuiltins verifies that agents returned by
// `opencode debug agents` (which merges global built-ins with repo-local
// definitions) are accepted without requiring a .opencode/agents/ directory.
func TestValidateAgentIncludesGlobalBuiltins(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("fake opencode uses a POSIX shell")
	}

	fakeDir := t.TempDir()
	fake := filepath.Join(fakeDir, "opencode")
	// Simulate the OpenCode v2 built-in agents (build, plan, etc.) with no
	// repo-local .opencode/agents/ directory present.
	script := `#!/bin/sh
set -eu
[ "$#" -eq 2 ] && [ "$1" = "debug" ] && [ "$2" = "agents" ] || exit 2
printf '[{"id":"build"},{"id":"plan"},{"id":"explore"},{"id":"general"}]\n'
`
	if err := os.WriteFile(fake, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", fakeDir)

	h := New()
	for _, agent := range []string{"build", "plan", "explore", "general"} {
		if err := h.ValidateAgent(context.Background(), t.TempDir(), agent); err != nil {
			t.Fatalf("built-in agent %q rejected: %v", agent, err)
		}
	}
	if err := h.ValidateAgent(context.Background(), t.TempDir(), "nonexistent"); err == nil {
		t.Fatal("nonexistent agent was accepted")
	}
}

// TestValidateAgentPropagatesDebugCommandFailure verifies that a non-zero
// exit from `opencode debug agents` is propagated with "opencode agent list"
// context for consistent error messaging.
func TestValidateAgentPropagatesDebugCommandFailure(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("fake opencode uses a POSIX shell")
	}

	fakeDir := t.TempDir()
	fake := filepath.Join(fakeDir, "opencode")
	script := `#!/bin/sh
set -eu
[ "$#" -eq 2 ] && [ "$1" = "debug" ] && [ "$2" = "agents" ] || exit 2
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
		t.Fatal("ValidateAgent succeeded after opencode debug agents failed")
	}
	if !strings.Contains(err.Error(), "opencode agent list") {
		t.Fatalf("error = %q, want opencode agent list context", err)
	}
}
