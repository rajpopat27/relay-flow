package orca

import (
	"errors"
	"strings"
	"testing"
)

// 9.5: info-level outcome logs must never embed argv payloads. sanitizeErr
// strips the "[args...]" middle from orcacli errors so the agent prompt and
// RELAY_FLOW_* env carried by --command are never written to server.log.
func TestSanitizeErr(t *testing.T) {
	// Shape from runJSON/run: "orca [terminal create ... --command 'PAYLOAD']: exit status 1: boom"
	// wrapped by CreateTerminal as "orca terminal create: %w".
	wrapped := errors.New("orca terminal create: orca [terminal create --worktree name:PAY-1 --title PAY-1:coding --command 'PAYLOAD']: exit status 1: boom")
	got := sanitizeErr(wrapped)
	if strings.Contains(got, "PAYLOAD") || strings.Contains(got, "--command") {
		t.Fatalf("sanitizeErr leaked argv payload: %q", got)
	}
	if !strings.Contains(got, "boom") {
		t.Fatalf("sanitizeErr dropped failure reason: %q", got)
	}

	if got := sanitizeErr(nil); got != "" {
		t.Fatalf("sanitizeErr(nil) = %q, want empty", got)
	}

	plain := errors.New("unwrapped failure")
	if got := sanitizeErr(plain); got != "unwrapped failure" {
		t.Fatalf("sanitizeErr(plain) = %q", got)
	}
}
