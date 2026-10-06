package beads

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/rajpopat27/relay-flow/internal/config"
	"github.com/rajpopat27/relay-flow/internal/task"
)

func installStartupBD(t *testing.T, script string) string {
	t.Helper()
	bin := t.TempDir()
	log := filepath.Join(t.TempDir(), "calls")
	t.Setenv("BD_STARTUP_LOG", log)
	t.Setenv("PATH", bin)
	t.Setenv("BEADS_DIR", "/ambient/workspace")
	t.Setenv("BEADS_DB", "/ambient/database")
	t.Setenv("BD_DB", "/ambient/legacy")
	if err := os.WriteFile(filepath.Join(bin, "bd"), []byte("#!/bin/sh\n"+script), 0o755); err != nil {
		t.Fatal(err)
	}
	return log
}

const startupReadScript = `
[ "$*" = 'list --ready --limit 1 --no-parent --json' ] || exit 2
[ -z "${BEADS_DB+x}" ] || exit 3
[ -z "${BD_DB+x}" ] || exit 4
printf '%s|%s|%s\n' "$PWD" "$BEADS_DIR" "$*" >> "$BD_STARTUP_LOG"
printf '%s' '[]'
`

func TestBeadsStartupWithoutWorkspaceChecksOnlyExecutableAndReportsDeferred(t *testing.T) {
	log := installStartupBD(t, "printf invoked >> \"$BD_STARTUP_LOG\"\nexit 99\n")
	var output bytes.Buffer
	original := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&output, nil)))
	t.Cleanup(func() { slog.SetDefault(original) })
	if err := task.ProbeStartup(context.Background(), "beads", nil, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(log); !os.IsNotExist(err) {
		t.Fatalf("startup probed ambient workspace: %v", err)
	}
	if !strings.Contains(output.String(), "deferred to registration") {
		t.Fatalf("deferred verification not reported: %s", output.String())
	}
	t.Setenv("PATH", t.TempDir())
	if err := task.ProbeStartup(context.Background(), "beads", nil, nil); err == nil || !strings.Contains(err.Error(), "missing executable") {
		t.Fatalf("missing bd was silently skipped: %v", err)
	}
}

func TestBeadsStartupDeduplicatesExplicitWorkspaceAliasesAndPreservesCWD(t *testing.T) {
	log := installStartupBD(t, startupReadScript)
	shared, other := t.TempDir(), t.TempDir()
	alias := filepath.Join(t.TempDir(), "workspace-alias")
	if err := os.Symlink(shared, alias); err != nil {
		t.Fatal(err)
	}
	codeOne, codeTwo, codeThree := t.TempDir(), t.TempDir(), t.TempDir()
	repos := map[string]config.Repo{
		"one":   {Path: codeOne, TaskConfig: config.RawValues{"beadsDir": shared}},
		"two":   {Path: codeTwo, TaskConfig: config.RawValues{"beadsDir": alias}},
		"three": {Path: codeThree, TaskConfig: config.RawValues{"beadsDir": other}},
	}
	if err := task.ProbeStartup(context.Background(), "beads", config.RawValues{"beadsDir": shared}, repos); err != nil {
		t.Fatal(err)
	}
	calls, err := os.ReadFile(log)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(string(calls)), "\n")
	if len(lines) != 2 || !strings.Contains(string(calls), codeOne+"|"+shared+"|") || !strings.Contains(string(calls), codeThree+"|"+other+"|") || strings.Contains(string(calls), codeTwo+"|") {
		t.Fatalf("unexpected deduplicated probes: %s", calls)
	}
}

func TestBeadsStartupDoesNotSkipConfiguredRootWorkspaceWithoutRepos(t *testing.T) {
	log := installStartupBD(t, startupReadScript)
	workspace := t.TempDir()
	if err := task.ProbeStartup(context.Background(), "beads", config.RawValues{"beadsDir": workspace}, nil); err != nil {
		t.Fatal(err)
	}
	calls, err := os.ReadFile(log)
	if err != nil || !strings.HasPrefix(string(calls), workspace+"|"+workspace+"|") || strings.Count(string(calls), "\n") != 1 {
		t.Fatalf("configured root connection not probed explicitly: %q, %v", calls, err)
	}
}

func TestBeadsStartupRejectsInvalidWorkspaceAndNeverFallsBackToRoot(t *testing.T) {
	log := installStartupBD(t, startupReadScript)
	for _, tc := range []struct {
		name string
		repo config.RawValues
	}{
		{"missing-repo-key", nil},
		{"missing-directory", config.RawValues{"beadsDir": filepath.Join(t.TempDir(), "missing")}},
		{"wrong-type", config.RawValues{"beadsDir": 123}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			repos := map[string]config.Repo{"app": {Path: t.TempDir(), TaskConfig: tc.repo}}
			if err := task.ProbeStartup(context.Background(), "beads", config.RawValues{"beadsDir": t.TempDir()}, repos); err == nil {
				t.Fatal("invalid configured workspace accepted")
			}
		})
	}
	if _, err := os.Stat(log); !os.IsNotExist(err) {
		t.Fatalf("invalid config caused an ambient/root fallback read: %v", err)
	}
}

func TestBeadsStartupReportsConnectionAndResponseFailuresWithoutRetry(t *testing.T) {
	for _, tc := range []struct {
		name, script, want string
	}{
		{"connection", "printf '%s' 'Dolt connection refused' >&2\nexit 1\n", "connection refused"},
		{"malformed", "printf '%s' 'not-json'\n", "JSON"},
		{"null", "printf '%s' 'null'\n", "JSON"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			log := installStartupBD(t, "printf '%s\\n' called >> \"$BD_STARTUP_LOG\"\n"+tc.script)
			repos := map[string]config.Repo{"app": {Path: t.TempDir(), TaskConfig: config.RawValues{"beadsDir": t.TempDir()}}}
			err := task.ProbeStartup(context.Background(), "beads", nil, repos)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("%s = %v", tc.name, err)
			}
			calls, err := os.ReadFile(log)
			if err != nil || string(calls) != "called\n" {
				t.Fatalf("workspace probe retried: %q, %v", calls, err)
			}
		})
	}
}

func TestBeadsStartupHonorsEarlierAggregateDeadline(t *testing.T) {
	installStartupBD(t, "/bin/sleep 2 &\nwait\n")
	repos := map[string]config.Repo{"app": {Path: t.TempDir(), TaskConfig: config.RawValues{"beadsDir": t.TempDir()}}}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	start := time.Now()
	err := task.ProbeStartup(ctx, "beads", nil, repos)
	if !errors.Is(err, context.DeadlineExceeded) || !strings.Contains(err.Error(), "timeout") || time.Since(start) > time.Second {
		t.Fatalf("startup exceeded caller budget: %v (%v)", err, time.Since(start))
	}
}
