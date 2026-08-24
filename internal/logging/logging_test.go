package logging

import (
	"bytes"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// 9.1: level resolution — --debug flag wins over RELAY_FLOW_LOG_LEVEL;
// default is info.
func TestLevelFor(t *testing.T) {
	cases := []struct {
		name string
		opts Options
		want slog.Level
	}{
		{"default info", Options{}, slog.LevelInfo},
		{"flag debug", Options{Debug: true}, slog.LevelDebug},
		{"flag wins over env info", Options{Debug: true, Env: "info"}, slog.LevelDebug},
		{"flag wins over env error", Options{Debug: true, Env: "error"}, slog.LevelDebug},
		{"env debug", Options{Env: "debug"}, slog.LevelDebug},
		{"env warn", Options{Env: "warn"}, slog.LevelWarn},
		{"env error", Options{Env: "ERROR"}, slog.LevelError},
		{"env unknown ignored", Options{Env: "bogus"}, slog.LevelInfo},
		{"env empty", Options{Env: ""}, slog.LevelInfo},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := levelFor(c.opts); got != c.want {
				t.Fatalf("levelFor(%+v) = %v, want %v", c.opts, got, c.want)
			}
		})
	}
}

// 9.1: Setup writes to BOTH server.log and stderr, creates the log file
// 0600, and honors the configured level.
func TestSetupWritesFileAndStderr(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "server.log")

	// Capture stderr during Setup + one log call.
	oldStderr := os.Stderr
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stderr = w
	defer func() { os.Stderr = oldStderr }()

	closer, err := Setup(logPath, Options{Debug: true})
	if err != nil {
		t.Fatalf("Setup: %v", err)
	}
	defer closer.Close()

	slog.Debug("hello-debug", "ticket", "PAY-1")
	slog.Info("hello-info", "ticket", "PAY-2")

	_ = w.Close()
	os.Stderr = oldStderr
	var stderrBuf bytes.Buffer
	_, _ = io.Copy(&stderrBuf, r)
	stderrOut := stderrBuf.String()

	if !strings.Contains(stderrOut, "hello-debug") || !strings.Contains(stderrOut, "hello-info") {
		t.Fatalf("stderr missing expected lines; got:\n%s", stderrOut)
	}

	fi, err := os.Stat(logPath)
	if err != nil {
		t.Fatalf("server.log not created: %v", err)
	}
	if fi.Mode().Perm() != 0o600 {
		t.Fatalf("server.log mode = %o, want 0600", fi.Mode().Perm())
	}
	body, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	text := string(body)
	if !strings.Contains(text, "hello-debug") || !strings.Contains(text, "hello-info") {
		t.Fatalf("server.log missing expected lines; got:\n%s", text)
	}
	if !strings.Contains(text, "ticket=PAY-1") || !strings.Contains(text, "ticket=PAY-2") {
		t.Fatalf("server.log missing structured ticket attr; got:\n%s", text)
	}
}

// 9.1: default (no flag, no env) is info — debug lines are suppressed.
func TestSetupDefaultInfoSuppressesDebug(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "server.log")
	closer, err := Setup(logPath, Options{})
	if err != nil {
		t.Fatalf("Setup: %v", err)
	}
	defer closer.Close()
	slog.Debug("suppressed-debug")
	slog.Info("kept-info")
	body, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	text := string(body)
	if strings.Contains(text, "suppressed-debug") {
		t.Fatalf("debug line should be suppressed at info level; got:\n%s", text)
	}
	if !strings.Contains(text, "kept-info") {
		t.Fatalf("info line missing; got:\n%s", text)
	}
}
