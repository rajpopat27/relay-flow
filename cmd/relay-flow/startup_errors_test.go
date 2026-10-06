package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestStartupDiagnosticReadsOnlyCurrentAttemptAndChild(t *testing.T) {
	path := filepath.Join(t.TempDir(), "server.log")
	file, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	log := slog.New(slog.NewTextHandler(file, nil))
	log.Error("server startup failed", "pid", 123, "error", errors.New("stale same-pid error"))
	offset := startupLogOffset(path)
	log.Error("server startup failed", "pid", 456, "error", errors.New("another child's error"))
	log.Warn("unrelated warning", "error", "private warning payload")
	if got := readStartupFailure(path, offset, 123); got != "" {
		t.Fatalf("replayed stale or unrelated diagnostic: %q", got)
	}
	failure := `startup prerequisite runner plugin "orca": orca repo list: connection refused`
	log.Error("server startup failed", "pid", 123, "error", errors.New(failure))
	if got := readStartupFailure(path, offset, 123); got != failure {
		t.Fatalf("current child diagnostic = %q, want %q", got, failure)
	}
	if got := readStartupFailure(path, offset, 12); got != "" {
		t.Fatalf("PID prefix matched another child: %q", got)
	}
}

func TestStartupDiagnosticIsBoundedAndDoesNotReplayTruncatedOrUnreadableLogs(t *testing.T) {
	path := filepath.Join(t.TempDir(), "server.log")
	old := fmt.Sprintf("msg=\"server startup failed\" pid=123 error=%q\n", "stale error")
	if err := os.WriteFile(path, []byte(old+strings.Repeat("unrelated log data\n", 10000)), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := readStartupFailure(path, 0, 123); got != "" {
		t.Fatalf("reader scanned beyond bounded tail: %q", got)
	}
	offset := startupLogOffset(path)
	if err := os.WriteFile(path, []byte(old), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := readStartupFailure(path, offset, 123); got != "" {
		t.Fatalf("replayed truncated log: %q", got)
	}
	for _, offset := range []int64{-1, 1000} {
		if got := readStartupFailure(path, offset, 123); got != "" {
			t.Fatalf("invalid boundary replayed old error: %q", got)
		}
	}
	if got := readStartupFailure(path+"-absent", 0, 123); got != "" {
		t.Fatalf("missing log returned diagnostic: %q", got)
	}
}

func TestStartupDiagnosticRemovesTerminalControlCharacters(t *testing.T) {
	path := filepath.Join(t.TempDir(), "server.log")
	line := fmt.Sprintf("msg=\"server startup failed\" pid=123 error=%q\n", "orca repo list: \x1b[31mconnection\nrefused")
	if err := os.WriteFile(path, []byte(line), 0o600); err != nil {
		t.Fatal(err)
	}
	got := readStartupFailure(path, 0, 123)
	if strings.ContainsAny(got, "\x1b\n\r") || !strings.Contains(got, "connection refused") {
		t.Fatalf("unsafe diagnostic: %q", got)
	}
}

func TestGuidedStartupPropagatesInProcessFailureAndWritesOwnedLogRecord(t *testing.T) {
	failure := errors.New("task connection refused")
	f := newPrerequisiteFixture(t, func(context.Context) error { return failure })
	err := ensureFirstRunServer(f.paths)
	if err == nil || !strings.Contains(err.Error(), "task connection refused") || !strings.Contains(err.Error(), f.cfg.TaskPlugin) || !strings.Contains(err.Error(), f.paths.ServerLog) {
		t.Fatalf("guided startup lost dependency or log location: %v", err)
	}
	got := readStartupFailure(f.paths.ServerLog, 0, os.Getpid())
	if !strings.Contains(got, "startup prerequisite task plugin") || !strings.Contains(got, "task connection refused") {
		t.Fatalf("in-process startup did not write owned failure record: %q", got)
	}
}
