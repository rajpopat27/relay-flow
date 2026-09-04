package pi

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPiValidateAgentAcceptsDefaultWithPiOnPATH(t *testing.T) {
	fakeDir := strictPiAvailabilityCLI(t)
	t.Setenv("PATH", fakeDir)

	h := newPiHarness(t)
	if err := h.ValidateAgent(context.Background(), "/srv/payments", "default"); err != nil {
		t.Fatalf("default agent rejected when pi is available: %v", err)
	}
}

func TestPiValidateAgentRejectsUnavailablePi(t *testing.T) {
	directory := t.TempDir()
	// Keep PATH controlled and empty of both the fake and any host-installed Pi.
	t.Setenv("PATH", directory)

	h := newPiHarness(t)
	err := h.ValidateAgent(context.Background(), "/srv/payments", "default")
	if err == nil {
		t.Fatal("default agent accepted when pi is unavailable")
	}
	if !strings.Contains(err.Error(), "pi") {
		t.Fatalf("error = %q, want Pi availability context", err)
	}
}

func TestPiValidateAgentAcceptsExistingRoleAndPiOnPath(t *testing.T) {
	fakeDir := strictPiAvailabilityCLI(t)
	t.Setenv("PATH", fakeDir)
	repoPath := t.TempDir()
	rolePath := writePiRole(t, repoPath, "coder", "You are the coder.\n")

	h := newPiHarness(t)
	if err := h.ValidateAgent(context.Background(), repoPath, "coder"); err != nil {
		t.Fatalf("existing role %q rejected: %v", rolePath, err)
	}
	if calls := readAvailabilityCalls(t, fakeDir); len(calls) != 0 {
		t.Fatalf("role validation unexpectedly executed Pi: %v", calls)
	}
}

func TestPiValidateAgentRejectsMissingRoleBeforePiLookup(t *testing.T) {
	fakeDir := strictPiAvailabilityCLI(t)
	t.Setenv("PATH", fakeDir)

	h := newPiHarness(t)
	err := h.ValidateAgent(context.Background(), t.TempDir(), "reviewer")
	if err == nil || !strings.Contains(err.Error(), ".pi/roles/reviewer.md") {
		t.Fatalf("missing role error = %v, want role path", err)
	}
	if calls := readAvailabilityCalls(t, fakeDir); len(calls) != 0 {
		t.Fatalf("missing role invoked Pi lookup: %v", calls)
	}
}

func TestPiValidateAgentRejectsUnsafeRolePath(t *testing.T) {
	fakeDir := strictPiAvailabilityCLI(t)
	t.Setenv("PATH", fakeDir)

	h := newPiHarness(t)
	for _, role := range []string{"../coder", "subdir/coder", `subdir\\coder`, " ", "."} {
		if err := h.ValidateAgent(context.Background(), t.TempDir(), role); err == nil {
			t.Errorf("unsafe role %q accepted", role)
		}
	}
	if calls := readAvailabilityCalls(t, fakeDir); len(calls) != 0 {
		t.Fatalf("unsafe roles invoked Pi lookup: %v", calls)
	}
}

func TestPiValidateAgentRejectsEmptyAndNonRegularRoleFiles(t *testing.T) {
	fakeDir := strictPiAvailabilityCLI(t)
	t.Setenv("PATH", fakeDir)

	emptyRepo := t.TempDir()
	emptyPath := filepath.Join(emptyRepo, ".pi", "roles", "coder.md")
	if err := os.MkdirAll(filepath.Dir(emptyPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(emptyPath, nil, 0o644); err != nil {
		t.Fatal(err)
	}

	directoryRepo := t.TempDir()
	directoryPath := filepath.Join(directoryRepo, ".pi", "roles", "reviewer.md")
	if err := os.MkdirAll(directoryPath, 0o755); err != nil {
		t.Fatal(err)
	}

	h := newPiHarness(t)
	if err := h.ValidateAgent(context.Background(), emptyRepo, "coder"); err == nil || !strings.Contains(err.Error(), "empty") {
		t.Fatalf("empty role error = %v, want empty-role error", err)
	}
	if err := h.ValidateAgent(context.Background(), directoryRepo, "reviewer"); err == nil || !strings.Contains(err.Error(), "regular file") {
		t.Fatalf("directory role error = %v, want non-regular-role error", err)
	}
	if calls := readAvailabilityCalls(t, fakeDir); len(calls) != 0 {
		t.Fatalf("invalid role files invoked Pi lookup: %v", calls)
	}
}

func TestPiValidateAgentRejectsUnsupportedLabelsWithoutAgentDiscovery(t *testing.T) {
	fakeDir := strictPiAvailabilityCLI(t)
	t.Setenv("PATH", fakeDir)

	h := newPiHarness(t)
	for _, label := range []string{"build", "plan", "gpt-4o", ""} {
		if err := h.ValidateAgent(context.Background(), "/srv/payments", label); err == nil {
			t.Errorf("unsupported agent label %q accepted", label)
		}
	}
	if calls := readAvailabilityCalls(t, fakeDir); len(calls) != 0 {
		t.Fatalf("unsupported labels invoked pi instead of rejecting directly: %v", calls)
	}
}

func strictPiAvailabilityCLI(t *testing.T) string {
	t.Helper()
	directory := t.TempDir()
	executable := filepath.Join(directory, "pi")
	calls := filepath.Join(directory, "calls")
	script := `#!/bin/sh
set -eu
printf '%s\000' "$*" >> "${PI_FAKE_CALLS:?}"
case "$#:$*" in
  1:--version) printf '0.84.1\n' ;;
  *) exit 2 ;;
esac
`
	if err := os.WriteFile(executable, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PI_FAKE_CALLS", calls)
	return directory
}

func readAvailabilityCalls(t *testing.T, directory string) []string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(directory, "calls"))
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		t.Fatal(err)
	}
	return strings.Split(strings.TrimSuffix(string(data), "\x00"), "\x00")
}

func writePiRole(t *testing.T, repoPath, name, content string) string {
	t.Helper()
	path := filepath.Join(repoPath, ".pi", "roles", name+".md")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}
