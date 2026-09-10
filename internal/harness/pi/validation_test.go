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

func TestPiValidateAgentAcceptsNamedPromptTemplateAndPiOnPath(t *testing.T) {
	fakeDir := strictPiAvailabilityCLI(t)
	t.Setenv("PATH", fakeDir)
	repoPath := t.TempDir()

	h := newPiHarness(t)
	if err := h.ValidateAgent(context.Background(), repoPath, "coder"); err != nil {
		t.Fatalf("prompt template rejected: %v", err)
	}
	if calls := readAvailabilityCalls(t, fakeDir); len(calls) != 0 {
		// ValidateAgent only performs executable lookup; it must not execute Pi.
		t.Fatalf("unexpected Pi execution during prompt-template validation: %v", calls)
	}
}

func TestPiValidateAgentDoesNotInspectPromptTemplateBeforePiLookup(t *testing.T) {
	fakeDir := strictPiAvailabilityCLI(t)
	t.Setenv("PATH", fakeDir)

	h := newPiHarness(t)
	err := h.ValidateAgent(context.Background(), t.TempDir(), "reviewer")
	if err != nil {
		t.Fatalf("missing prompt template rejected: %v", err)
	}
	if calls := readAvailabilityCalls(t, fakeDir); len(calls) != 0 {
		t.Fatalf("prompt-template validation executed Pi: %v", calls)
	}
}

func TestPiValidateAgentRejectsUnsafePromptTemplateName(t *testing.T) {
	fakeDir := strictPiAvailabilityCLI(t)
	t.Setenv("PATH", fakeDir)

	h := newPiHarness(t)
	for _, name := range []string{"../coder", "subdir/coder", `subdir\\coder`, " ", "."} {
		if err := h.ValidateAgent(context.Background(), t.TempDir(), name); err == nil {
			t.Errorf("unsafe prompt template name %q accepted", name)
		}
	}
	if calls := readAvailabilityCalls(t, fakeDir); len(calls) != 0 {
		t.Fatalf("unsafe prompt template names invoked Pi lookup: %v", calls)
	}
}

func TestPiValidateAgentAcceptsMissingAndNonRegularPromptTemplateFiles(t *testing.T) {
	fakeDir := strictPiAvailabilityCLI(t)
	t.Setenv("PATH", fakeDir)

	h := newPiHarness(t)
	if err := h.ValidateAgent(context.Background(), t.TempDir(), "coder"); err != nil {
		t.Fatalf("missing prompt template error = %v", err)
	}
	if err := h.ValidateAgent(context.Background(), t.TempDir(), "reviewer"); err != nil {
		t.Fatalf("non-regular prompt template error = %v", err)
	}
	if calls := readAvailabilityCalls(t, fakeDir); len(calls) != 0 {
		t.Fatalf("prompt-template validation executed Pi: %v", calls)
	}
}

func TestPiValidateAgentAcceptsAnySafePromptTemplateNameWithoutAgentDiscovery(t *testing.T) {
	fakeDir := strictPiAvailabilityCLI(t)
	t.Setenv("PATH", fakeDir)

	h := newPiHarness(t)
	for _, name := range []string{"build", "plan", "gpt-4o"} {
		if err := h.ValidateAgent(context.Background(), "/srv/payments", name); err != nil {
			t.Errorf("safe prompt template name %q rejected: %v", name, err)
		}
	}
	if err := h.ValidateAgent(context.Background(), "/srv/payments", ""); err == nil {
		t.Error("empty prompt template name accepted")
	}
	if calls := readAvailabilityCalls(t, fakeDir); len(calls) != 0 {
		t.Fatalf("prompt-template validation executed Pi: %v", calls)
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
