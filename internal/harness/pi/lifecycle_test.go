package pi

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/rajpopat27/relay-flow/internal/harness"
)

func TestPiSetupRepoIsSideEffectFree(t *testing.T) {
	repoPath := t.TempDir()
	sentinelPath := filepath.Join(repoPath, "existing.txt")
	sentinel := []byte("leave this repository unchanged")
	if err := os.WriteFile(sentinelPath, sentinel, 0o640); err != nil {
		t.Fatal(err)
	}

	h := newPiHarness(t)
	if err := h.SetupRepo(context.Background(), repoPath); err != nil {
		t.Fatalf("SetupRepo: %v", err)
	}

	data, err := os.ReadFile(sentinelPath)
	if err != nil {
		t.Fatalf("read sentinel: %v", err)
	}
	if !reflect.DeepEqual(data, sentinel) {
		t.Fatalf("SetupRepo changed existing repository content: %q", data)
	}
	for _, name := range []string{"opencode.json", "opencode.jsonc", ".pi"} {
		if _, err := os.Stat(filepath.Join(repoPath, name)); !os.IsNotExist(err) {
			t.Fatalf("SetupRepo created %q: %v", name, err)
		}
	}
}

func TestPiFindSessionDoesNotDiscoverByTitle(t *testing.T) {
	missingRepo := filepath.Join(t.TempDir(), "not-a-repository")
	h := newPiHarness(t)

	session, ok, err := h.FindSession(context.Background(), missingRepo, "PAY-101:implement")
	if err != nil {
		t.Fatalf("FindSession: %v", err)
	}
	if ok {
		t.Fatalf("FindSession found an unrequested session: %+v", session)
	}
	if session != (harness.Session{}) {
		t.Fatalf("FindSession returned session data: %+v", session)
	}
}

func TestPiResumeUsesOnlyPersistedLaunchSpecSessionID(t *testing.T) {
	spec := launchSpec(t)
	spec.RepoPath = filepath.Join(t.TempDir(), "missing-ticket-environment")
	spec.ResumeID = "session-123"

	cmd, err := newPiHarness(t).BuildCommand(spec)
	if err != nil {
		t.Fatalf("BuildCommand: %v", err)
	}
	want := []string{"--name", spec.Title, "--session-id", spec.ResumeID, spec.Prompt}
	if !reflect.DeepEqual(cmd.Args, want) {
		t.Fatalf("Args = %#v, want %#v", cmd.Args, want)
	}
}
