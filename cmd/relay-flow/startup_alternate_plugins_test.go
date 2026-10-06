package main

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/rajpopat27/relay-flow/internal/config"
	"github.com/rajpopat27/relay-flow/internal/server"
)

func TestServeModesSupportBeadsHerdrPiWithoutReposAndRejectConfiguredConnectionFailures(t *testing.T) {
	binary := buildCLIBinary(t)
	for _, failure := range []string{"", "beads", "herdr"} {
		for _, foreground := range []bool{false, true} {
			name := failure
			if name == "" {
				name = "empty-healthy"
			}
			if foreground {
				name += "-foreground"
			}
			t.Run(name, func(t *testing.T) {
				root := filepath.Join(t.TempDir(), "relay-flow")
				t.Setenv("RELAY_FLOW_HOME", root)
				init := exec.Command(binary, "init", "--task-plugin", "beads", "--runner-plugin", "herdr", "--harness-plugin", "pi")
				if out, err := init.CombinedOutput(); err != nil {
					t.Fatalf("init: %v\n%s", err, out)
				}
				bin := t.TempDir()
				logPath := filepath.Join(t.TempDir(), "calls")
				t.Setenv("STARTUP_CALLS", logPath)
				t.Setenv("HERDR_SESSION", "ambient-session")
				t.Setenv("HERDR_SOCKET_PATH", "/ambient/socket")
				t.Setenv("BEADS_DIR", "/ambient/beads")
				t.Setenv("BEADS_DB", "/ambient/db")
				p := pathsForRoot(root)
				cfg, err := config.LoadMachine(p.Config)
				if err != nil {
					t.Fatal(err)
				}
				cfg.RunnerConfig = config.RawValues{"session": "configured-session", "socketPath": "/configured/socket"}
				bdScript := "#!/bin/sh\necho 'unexpected ambient workspace read' >&2\nexit 99\n"
				if failure == "beads" {
					code, workspace := t.TempDir(), t.TempDir()
					cfg.Repos = map[string]config.Repo{"app": {Path: code, TaskConfig: config.RawValues{"beadsDir": workspace}}}
					t.Setenv("EXPECTED_CWD", code)
					t.Setenv("EXPECTED_BEADS_DIR", workspace)
					bdScript = `#!/bin/sh
[ "$*" = 'list --ready --limit 1 --no-parent --json' ] || exit 99
[ "$PWD" = "$EXPECTED_CWD" ] || exit 98
[ "$BEADS_DIR" = "$EXPECTED_BEADS_DIR" ] || exit 97
[ -z "${BEADS_DB+x}" ] || exit 96
printf '%s\n' 'bd probe' >> "$STARTUP_CALLS"
printf '%s' 'Dolt connection refused' >&2
exit 1
`
				}
				herdrScript := `#!/bin/sh
[ "$*" = 'api snapshot' ] || exit 99
[ "$HERDR_SESSION" = 'configured-session' ] || exit 98
[ "$HERDR_SOCKET_PATH" = '/configured/socket' ] || exit 97
printf '%s\n' 'herdr snapshot' >> "$STARTUP_CALLS"
`
				if failure == "herdr" {
					herdrScript += "printf '%s' '{\"error\":{\"code\":\"connection_refused\",\"message\":\"socket unavailable\"}}' >&2\nexit 1\n"
				} else {
					herdrScript += "printf '%s' '{\"result\":{\"snapshot\":{\"workspaces\":[],\"tabs\":[],\"panes\":[]}}}'\n"
				}
				for name, script := range map[string]string{"bd": bdScript, "herdr": herdrScript, "pi": "#!/bin/sh\nexit 99\n"} {
					if err := os.WriteFile(filepath.Join(bin, name), []byte(script), 0o755); err != nil {
						t.Fatal(err)
					}
				}
				if err := config.SaveMachine(p.Config, cfg); err != nil {
					t.Fatal(err)
				}
				t.Setenv("PATH", bin)
				args := []string{"serve"}
				if foreground {
					args = append(args, "--foreground")
				}
				ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
				defer cancel()
				serve := exec.CommandContext(ctx, binary, args...)
				if failure != "" {
					out, err := serve.CombinedOutput()
					operation := "api snapshot"
					if failure == "beads" {
						operation = "Dolt connection refused"
					}
					if err == nil || !strings.Contains(string(out), operation) || !strings.Contains(string(out), p.ServerLog) || strings.Contains(string(out), "server started") {
						t.Fatalf("configured connection failure skipped: %v\n%s", err, out)
					}
				} else {
					client := server.NewClient(p.Socket)
					if foreground {
						if err := serve.Start(); err != nil {
							t.Fatal(err)
						}
						wait := make(chan error, 1)
						go func() { wait <- serve.Wait() }()
						t.Cleanup(func() { _ = serve.Process.Kill() })
						waitForServer(t, client)
						if err := client.Stop(context.Background()); err != nil {
							t.Fatal(err)
						}
						if err := <-wait; err != nil {
							t.Fatal(err)
						}
					} else {
						if out, err := serve.CombinedOutput(); err != nil || !strings.Contains(string(out), "server started") {
							t.Fatalf("zero-repo readiness: %v\n%s", err, out)
						}
						if err := client.Stop(context.Background()); err != nil {
							t.Fatal(err)
						}
					}
					waitForServerStop(t, client)
					if log := readFile(t, p.ServerLog); !strings.Contains(log, "deferred to registration") {
						t.Fatalf("unconfigured Beads connection not reported deferred: %s", log)
					}
				}
				wantCalls := "herdr snapshot\n"
				if failure == "beads" {
					wantCalls = "bd probe\n"
				}
				if calls := readFile(t, logPath); calls != wantCalls {
					t.Fatalf("unselected/duplicate/full-discovery startup calls: %q", calls)
				}
			})
		}
	}
}
