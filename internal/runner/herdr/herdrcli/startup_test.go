package herdrcli

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func installSnapshotLauncher(t *testing.T, script string) {
	t.Helper()
	bin := t.TempDir()
	if err := os.WriteFile(filepath.Join(bin, "herdr"), []byte("#!/bin/sh\n"+script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin)
}

func TestSnapshotAcceptsEmptyRuntimeAndRejectsMalformedResponses(t *testing.T) {
	for _, tc := range []struct {
		response string
		valid    bool
	}{
		{`{"result":{"snapshot":{"workspaces":[],"tabs":[],"panes":[]}}}`, true},
		{`not-json`, false},
		{`{}`, false},
		{`{"result":null}`, false},
		{`{"result":{}}`, false},
		{`{"result":{"snapshot":null}}`, false},
		{`{"result":{"snapshot":{}}}`, false},
		{`{"result":{"snapshot":{"workspaces":null,"tabs":[],"panes":[]}}}`, false},
	} {
		t.Run(tc.response, func(t *testing.T) {
			installSnapshotLauncher(t, "printf '%s' '"+tc.response+"'\n")
			snapshot, err := New(Options{}).Snapshot(context.Background())
			if tc.valid {
				if err != nil || len(snapshot.Workspaces) != 0 || len(snapshot.Tabs) != 0 || len(snapshot.Panes) != 0 {
					t.Fatalf("empty runtime = %+v, %v", snapshot, err)
				}
			} else if err == nil {
				t.Fatal("accepted malformed snapshot")
			}
		})
	}
}

func TestSnapshotPassesSelectorsWithExactlyOneRead(t *testing.T) {
	log := filepath.Join(t.TempDir(), "calls")
	t.Setenv("SNAPSHOT_CALL_LOG", log)
	t.Setenv("HERDR_SESSION", "ambient-session")
	t.Setenv("HERDR_SOCKET_PATH", "/ambient/socket")
	installSnapshotLauncher(t, `
[ "$*" = 'api snapshot' ] || exit 2
[ "$HERDR_SESSION" = 'configured-session' ] || exit 3
[ "$HERDR_SOCKET_PATH" = '/configured/socket' ] || exit 4
printf '%s\n' "$*" >> "$SNAPSHOT_CALL_LOG"
printf '%s' '{"result":{"snapshot":{"workspaces":[],"tabs":[],"panes":[]}}}'
`)
	_, err := New(Options{Session: "configured-session", SocketPath: "/configured/socket"}).Snapshot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	calls, err := os.ReadFile(log)
	if err != nil || string(calls) != "api snapshot\n" {
		t.Fatalf("snapshot calls = %q, %v", calls, err)
	}
}

func TestSnapshotReportsMissingExecutableAndSocketFailure(t *testing.T) {
	t.Run("executable", func(t *testing.T) {
		t.Setenv("PATH", t.TempDir())
		_, err := New(Options{}).Snapshot(context.Background())
		if err == nil || !strings.Contains(err.Error(), "missing executable") || !strings.Contains(err.Error(), "herdr") {
			t.Fatalf("missing executable = %v", err)
		}
	})
	t.Run("socket", func(t *testing.T) {
		installSnapshotLauncher(t, "printf '%s' '{\"error\":{\"code\":\"connection_refused\",\"message\":\"socket unavailable\"}}' >&2\nexit 1\n")
		_, err := New(Options{SocketPath: "/configured/socket"}).Snapshot(context.Background())
		if err == nil || !strings.Contains(err.Error(), "connection_refused") || !strings.Contains(err.Error(), "api snapshot") {
			t.Fatalf("socket failure = %v", err)
		}
	})
}

func TestSnapshotCancellationBoundsInheritedOutputPipes(t *testing.T) {
	installSnapshotLauncher(t, "/bin/sleep 2 &\nwait\n")
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Millisecond)
	defer cancel()
	start := time.Now()
	_, err := New(Options{}).Snapshot(ctx)
	if !errors.Is(err, context.DeadlineExceeded) || !strings.Contains(err.Error(), "timeout") {
		t.Fatalf("timeout = %v", err)
	}
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Fatalf("inherited pipes defeated deadline: %v", elapsed)
	}
}
