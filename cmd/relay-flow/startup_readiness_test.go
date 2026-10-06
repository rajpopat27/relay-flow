package main

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/rajpopat27/relay-flow/internal/config"
	"github.com/rajpopat27/relay-flow/internal/harness"
	"github.com/rajpopat27/relay-flow/internal/runner"
	"github.com/rajpopat27/relay-flow/internal/server"
	"github.com/rajpopat27/relay-flow/internal/task"
)

func TestServePrerequisitesRequireSelectedPluginProbeSupport(t *testing.T) {
	for _, stage := range []string{"task", "runner", "harness"} {
		t.Run(stage, func(t *testing.T) {
			f := newPrerequisiteFixture(t, nil)
			if stage == "task" {
				name := fmt.Sprintf("missing-startup-task-%d", prerequisitePluginSequence.Add(1))
				task.Register(name, task.Factory{NewLocal: func(context.Context, task.RepoSpec) (task.System, error) {
					return newScenarioTaskSystem(f.log), nil
				}})
				f.cfg.TaskPlugin = name
			} else if stage == "runner" {
				name := fmt.Sprintf("missing-startup-runner-%d", prerequisitePluginSequence.Add(1))
				runner.Register(name, func(config.RawValues) (runner.Runner, error) {
					// Embedding the runtime interface deliberately does not expose
					// the separate startup boundary of the concrete fake.
					return struct{ runner.Runner }{f.rnr}, nil
				})
				f.cfg.RunnerPlugin = name
			} else {
				name := fmt.Sprintf("missing-startup-harness-%d", prerequisitePluginSequence.Add(1))
				harness.Register(name, harness.Factory{New: func(config.RawValues) (harness.Harness, error) {
					return struct{ harness.Harness }{f.hrn}, nil
				}})
				f.cfg.HarnessPlugin = name
			}
			f.save(t)
			err := serveRoot(context.Background(), f.paths, false)
			if err == nil || !strings.Contains(err.Error(), stage+" plugin") || !strings.Contains(err.Error(), "startup prob") {
				t.Fatalf("selected dependency was silently skipped: %v", err)
			}
			if serverResponding(server.NewClient(f.paths.Socket), 50*time.Millisecond) {
				t.Fatal("unsupported probe published readiness")
			}
		})
	}
}

func TestForegroundAndDetachedServeNeverReportReadinessOnDependencyFailure(t *testing.T) {
	binary := buildCLIBinary(t)
	for _, stage := range []string{"task", "runner", "harness"} {
		for _, foreground := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/foreground=%v", stage, foreground), func(t *testing.T) {
				root := filepath.Join(t.TempDir(), "relay-flow")
				t.Setenv("RELAY_FLOW_HOME", root)
				init := exec.Command(binary, initArgs()...)
				init.Env = os.Environ()
				if out, err := init.CombinedOutput(); err != nil {
					t.Fatalf("init: %v\n%s", err, out)
				}
				installHealthyServePrerequisites(t, root)
				bin := strings.Split(os.Getenv("PATH"), string(os.PathListSeparator))[0]
				// The child's own environment has only the isolated executables.
				// Removing one must not resolve a real installed dependency.
				t.Setenv("PATH", bin)
				missing := filepath.Join(root, "credentials.yaml")
				if stage == "runner" {
					launcher := "orca"
					if runtime.GOOS == "linux" {
						launcher = "orca-ide"
					}
					missing = filepath.Join(bin, launcher)
				} else if stage == "harness" {
					missing = filepath.Join(bin, "opencode")
				}
				if err := os.Remove(missing); err != nil {
					t.Fatal(err)
				}
				args := []string{"serve"}
				if foreground {
					args = append(args, "--foreground")
				}
				ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
				defer cancel()
				serve := exec.CommandContext(ctx, binary, args...)
				serve.Env = os.Environ()
				out, err := serve.CombinedOutput()
				if err == nil || strings.Contains(string(out), "server started") {
					t.Fatalf("failed dependency reported readiness: err=%v, output=%s", err, out)
				}
				log := readFile(t, filepath.Join(root, "server.log"))
				if !strings.Contains(log, "startup prerequisite "+stage+" plugin") {
					t.Fatalf("server did not report the selected dependency: %s", log)
				}
				if !strings.Contains(string(out), "startup prerequisite "+stage+" plugin") {
					t.Fatalf("startup failure lost dependency identity: %s", out)
				}
				if !strings.Contains(string(out), filepath.Join(root, "server.log")) {
					t.Fatalf("startup failure lost log location: %s", out)
				}
				if serverResponding(server.NewClient(filepath.Join(root, "server.sock")), 50*time.Millisecond) {
					t.Fatal("failed dependency left a ready server")
				}
			})
		}
	}
}
