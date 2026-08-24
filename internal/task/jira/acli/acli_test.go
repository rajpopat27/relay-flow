package acli

import (
	"errors"
	"strings"
	"testing"
)

// 9.5: info-level outcome logs must never embed argv payloads. sanitizeErr
// strips the leading "acli [args...]:" prefix while keeping the wrapped
// exit error and trailing stderr fragment that carry the failure reason.
func TestSanitizeErr(t *testing.T) {
	argvShape := errors.New(`acli [jira workitem comment create --key PAY-1 --body SECRET-BODY --json]: exit status 1: permission denied`)
	got := sanitizeErr(argvShape)
	if strings.Contains(got, "SECRET-BODY") || strings.Contains(got, "--body") {
		t.Fatalf("sanitizeErr leaked argv payload: %q", got)
	}
	if !strings.Contains(got, "exit status 1") || !strings.Contains(got, "permission denied") {
		t.Fatalf("sanitizeErr dropped failure reason: %q", got)
	}

	parseShape := errors.New(`acli create subtask: parse json: invalid character 'x'`)
	got = sanitizeErr(parseShape)
	if !strings.Contains(got, "parse json") {
		t.Fatalf("sanitizeErr dropped parse context: %q", got)
	}

	if got := sanitizeErr(nil); got != "" {
		t.Fatalf("sanitizeErr(nil) = %q, want empty", got)
	}

	plain := errors.New("some other error")
	if got := sanitizeErr(plain); got != "some other error" {
		t.Fatalf("sanitizeErr(plain) = %q", got)
	}
}
