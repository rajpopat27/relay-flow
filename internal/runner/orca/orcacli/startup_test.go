package orcacli

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestLauncherSelection(t *testing.T) {
	for _, tc := range []struct {
		os, selector, devRoot, want string
	}{
		{"linux", "", "", "orca-ide"},
		{"darwin", "", "", "orca"},
		{"windows", "", "", "orca"},
		{"linux", "", "/dev/orca", "orca-dev"},
		{"darwin", "", "/dev/orca", "orca-dev"},
		{"linux", "/session/orca.exe", "/dev/orca", "/session/orca.exe"},
		{"darwin", "session-orca", "", "session-orca"},
	} {
		if got := selectCommand(tc.os, tc.selector, tc.devRoot); got != tc.want {
			t.Errorf("selectCommand(%q, %q, %q) = %q, want %q", tc.os, tc.selector, tc.devRoot, got, tc.want)
		}
	}
}

func installStartupLauncher(t *testing.T, name, script string) string {
	t.Helper()
	bin := t.TempDir()
	path := filepath.Join(bin, name)
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"+script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin)
	t.Setenv("ORCA_CLI_COMMAND", name)
	t.Setenv("ORCA_DEV_REPO_ROOT", "")
	return path
}

func TestSelectedLauncherIsResolvedOnceForAllCalls(t *testing.T) {
	fake := installStrictFakeOrca(t)
	cli := New()
	if cli.executable != fake {
		t.Fatalf("resolved launcher = %q, want %q", cli.executable, fake)
	}
	// Neither a client-side selector nor PATH change replaces the server's CLI.
	t.Setenv("ORCA_CLI_COMMAND", "nonexistent-other-orca")
	t.Setenv("PATH", t.TempDir()+string(os.PathListSeparator)+os.Getenv("PATH"))
	if _, err := cli.ListRepos(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := cli.ListWorktrees(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := cli.SendTerminal(context.Background(), "term-1", "hello"); err != nil {
		t.Fatal(err)
	}
}

func TestMissingSelectedLauncherNeverFallsBack(t *testing.T) {
	installStartupLauncher(t, "orca", "printf '%s' '{\"result\":{\"repos\":[]}}'\n")
	t.Setenv("ORCA_CLI_COMMAND", "missing-session-launcher")
	_, err := New().ListRepos(context.Background())
	if err == nil || !strings.Contains(err.Error(), "missing executable") || !strings.Contains(err.Error(), "missing-session-launcher") {
		t.Fatalf("missing selector = %v", err)
	}
	if runtime.GOOS == "linux" {
		t.Setenv("ORCA_CLI_COMMAND", "")
		_, err = New().ListRepos(context.Background())
		if err == nil || !strings.Contains(err.Error(), "orca-ide") {
			t.Fatalf("Linux must not invoke bare orca: %v", err)
		}
	}
}

func TestRepoListAcceptsEmptyArrayAndRejectsMalformedResponses(t *testing.T) {
	for _, tc := range []struct {
		response string
		valid    bool
	}{
		{`{"result":{"repos":[]}}`, true},
		{`{}`, false},
		{`null`, false},
		{`{"result":null}`, false},
		{`{"result":{}}`, false},
		{`{"result":{"repos":null}}`, false},
		{`{"result":{"repos":{}}}`, false},
		{`not-json`, false},
	} {
		t.Run(tc.response, func(t *testing.T) {
			installStartupLauncher(t, "selected-orca", "printf '%s' '"+tc.response+"'\n")
			repos, err := New().ListRepos(context.Background())
			if tc.valid {
				if err != nil || repos == nil || len(repos) != 0 {
					t.Fatalf("empty runtime = %v, %v", repos, err)
				}
			} else if err == nil || !strings.Contains(err.Error(), "malformed") {
				t.Fatalf("malformed response = %v", err)
			}
		})
	}
}

func TestRepoListReportsLaunchAndRuntimeFailures(t *testing.T) {
	t.Run("launch", func(t *testing.T) {
		path := installStartupLauncher(t, "broken-orca", "")
		if err := os.WriteFile(path, []byte("#!/nonexistent/interpreter\n"), 0o755); err != nil {
			t.Fatal(err)
		}
		_, err := New().ListRepos(context.Background())
		// The selected executable exists, but its interpreter cannot launch.
		if err == nil || !strings.Contains(err.Error(), "launch failure") || !strings.Contains(err.Error(), path) {
			t.Fatalf("launch error = %v", err)
		}
	})
	t.Run("connection", func(t *testing.T) {
		installStartupLauncher(t, "selected-orca", "printf '%s' '{\"error\":{\"code\":\"connection_refused\",\"message\":\"runtime unavailable\"}}'\nexit 1\n")
		_, err := New().ListRepos(context.Background())
		if err == nil || !strings.Contains(err.Error(), "connection_refused") || !strings.Contains(err.Error(), "repo list") {
			t.Fatalf("connection error = %v", err)
		}
	})
	t.Run("stderr", func(t *testing.T) {
		installStartupLauncher(t, "selected-orca", "printf '%s' 'authentication required' >&2\nexit 1\n")
		_, err := New().ListRepos(context.Background())
		if err == nil || !strings.Contains(err.Error(), "authentication required") {
			t.Fatalf("stderr error = %v", err)
		}
	})
}

func TestRepoListCancellationBoundsInheritedOutputPipes(t *testing.T) {
	// The background descendant retains both pipes after the launcher is killed.
	installStartupLauncher(t, "selected-orca", "/bin/sleep 2 &\nwait\n")
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Millisecond)
	defer cancel()
	start := time.Now()
	_, err := New().ListRepos(ctx)
	if !errors.Is(err, context.DeadlineExceeded) || !strings.Contains(err.Error(), "timeout") {
		t.Fatalf("timeout = %v", err)
	}
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Fatalf("inherited pipes defeated deadline: %v", elapsed)
	}
}

func TestRuntimeFailureDoesNotExposeCommandArguments(t *testing.T) {
	installStartupLauncher(t, "selected-orca", "exit 1\n")
	err := New().SendTerminal(context.Background(), "term-1", "SECRET PROMPT")
	if err == nil || strings.Contains(err.Error(), "SECRET PROMPT") || !strings.Contains(err.Error(), "terminal send") {
		t.Fatalf("unsafe operation error = %v", err)
	}
}
