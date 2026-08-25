package acli

import (
	"context"
	"errors"
	"os"
	"path/filepath"
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
		t.Fatalf("sanitizeErr(plain) = %q, got %q", "some other error", got)
	}
}

// 9.12 command-shape regression: acli's workitem create/edit take the
// description via --description (there is no --body flag on those
// subcommands); only comment create takes --body. A fake `acli` binary on
// PATH captures argv so the exact wire shape is asserted.
func TestCommandShape(t *testing.T) {
	dir := t.TempDir()
	argvFile := filepath.Join(dir, "argv.json")
	fake := "#!/bin/sh\n" +
		"for a in \"$@\"; do printf '<%s>' \"$a\"; done > " + argvFile + "\n" +
		"printf '{\"id\":\"1\",\"key\":\"PAY-2\"}'\n"
	bin := filepath.Join(dir, "acli")
	if err := os.WriteFile(bin, []byte(fake), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))

	readArgv := func() string {
		raw, err := os.ReadFile(argvFile)
		if err != nil {
			t.Fatal(err)
		}
		return string(raw)
	}
	assertShape := func(argv, wantFlag, forbidFlag string) {
		if !strings.Contains(argv, wantFlag) {
			t.Fatalf("argv %q missing %s", argv, wantFlag)
		}
		if strings.Contains(argv, forbidFlag) {
			t.Fatalf("argv %q contains unsupported %s", argv, forbidFlag)
		}
	}

	ctx := context.Background()
	c := New()

	if _, _, err := c.CreateSubtask(ctx, "PAY-1", "t", "d"); err != nil {
		t.Fatal(err)
	}
	argv := readArgv()
	assertShape(argv, "<--description><d>", "--body")
	// 9.14: acli rejects workitem create when --summary/--type are set
	// without --project; the project is derived from the parent key prefix.
	if !strings.Contains(argv, "<--project><PAY>") {
		t.Fatalf("argv %q missing <--project><PAY> derived from parent key PAY-1", argv)
	}
	// 9.15: live GHCOS acli accepts the issue-type name "Sub-task" and
	// rejects "Subtask" with its allowed issue-type list.
	for _, want := range []string{"<--summary><t>", "<--type><Sub-task>", "<--parent><PAY-1>"} {
		if !strings.Contains(argv, want) {
			t.Fatalf("argv %q missing %s", argv, want)
		}
	}

	if err := c.UpdateDescription(ctx, "PAY-1", "d"); err != nil {
		t.Fatal(err)
	}
	assertShape(readArgv(), "<--description><d>", "--body")

	if err := c.AddComment(ctx, "PAY-1", "b"); err != nil {
		t.Fatal(err)
	}
	assertShape(readArgv(), "<--body><b>", "--description")
}
