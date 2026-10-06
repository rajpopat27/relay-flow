package main

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/rajpopat27/relay-flow/internal/config"
	"github.com/rajpopat27/relay-flow/internal/execution/goworkflows"
	"github.com/rajpopat27/relay-flow/internal/harness"
	"github.com/rajpopat27/relay-flow/internal/paths"
	"github.com/rajpopat27/relay-flow/internal/runner"
	"github.com/rajpopat27/relay-flow/internal/server"
	"github.com/rajpopat27/relay-flow/internal/task"
	"github.com/rajpopat27/relay-flow/internal/workflow"
)

// Existing CLI lifecycle tests use production plugin selection with isolated
// prerequisite dependencies. These fake executables accept only the startup
// read; neither a desktop runner nor an agent process may be invoked.
func installHealthyServePrerequisites(t *testing.T, root string) {
	t.Helper()
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		user, token, ok := r.BasicAuth()
		if !ok || user != "startup@example.invalid" || token != "fixture-token" || r.Method != "GET" || r.URL.Path != "/rest/api/3/myself" {
			t.Errorf("unexpected startup request: %s %s", r.Method, r.URL.Path)
			http.Error(w, "unexpected request", http.StatusBadRequest)
			return
		}
		_, _ = w.Write([]byte(`{"accountId":"startup-account"}`))
	}))
	t.Cleanup(s.Close)
	credentials := fmt.Sprintf("site: %q\nemail: startup@example.invalid\ntoken: fixture-token\n", s.URL)
	if err := os.WriteFile(filepath.Join(root, "credentials.yaml"), []byte(credentials), 0o600); err != nil {
		t.Fatal(err)
	}
	bin := t.TempDir()
	launcher := "orca"
	if runtime.GOOS == "linux" {
		launcher = "orca-ide"
	}
	for name, script := range map[string]string{
		launcher:   "#!/bin/sh\n[ \"$*\" = 'repo list --json' ] || exit 99\nprintf '%s' '{\"ok\":true,\"result\":{\"repos\":[]}}'\n",
		"opencode": "#!/bin/sh\necho 'startup must not execute harness commands' >&2\nexit 99\n",
	} {
		if err := os.WriteFile(filepath.Join(bin, name), []byte(script), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("ORCA_CLI_COMMAND", "")
	t.Setenv("ORCA_DEV_REPO_ROOT", "")
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
}

type prerequisiteTask struct{ *scenarioTaskSystem }

func (s *prerequisiteTask) Poll(context.Context) ([]task.Ticket, error) {
	s.log.add("poll")
	return nil, nil
}

type prerequisiteRunner struct {
	*scenarioRunner
	probe func(context.Context) error
}

func (r *prerequisiteRunner) ProbeStartup(ctx context.Context) error {
	r.log.add("startup:runner")
	if r.probe != nil {
		return r.probe(ctx)
	}
	return ctx.Err()
}

type prerequisiteHarness struct {
	*scenarioHarness
	probe func(context.Context) error
}

func (h *prerequisiteHarness) ProbeStartup(ctx context.Context) error {
	h.log.add("startup:harness")
	if h.probe != nil {
		return h.probe(ctx)
	}
	return ctx.Err()
}

var prerequisitePluginSequence atomic.Int64

type prerequisiteFixture struct {
	paths paths.Paths
	cfg   *config.Machine
	log   *scenarioLog
	rnr   *prerequisiteRunner
	hrn   *prerequisiteHarness
}

func newPrerequisiteFixture(t *testing.T, taskProbe func(context.Context) error) *prerequisiteFixture {
	t.Helper()
	f := &prerequisiteFixture{paths: pathsForRoot(filepath.Join(t.TempDir(), "relay-flow")), log: newScenarioLog()}
	t.Setenv("RELAY_FLOW_HOME", f.paths.Root)
	if err := paths.Ensure(f.paths); err != nil {
		t.Fatal(err)
	}
	name := fmt.Sprintf("prerequisite-test-%d", prerequisitePluginSequence.Add(1))
	f.cfg = &config.Machine{TaskPlugin: name + "-task", RunnerPlugin: name + "-runner", HarnessPlugin: name + "-harness", PollIntervalSeconds: 1}
	sys := &prerequisiteTask{newScenarioTaskSystem(f.log)}
	f.rnr = &prerequisiteRunner{scenarioRunner: newScenarioRunner(f.log)}
	f.hrn = &prerequisiteHarness{scenarioHarness: newScenarioHarness(f.log)}
	constructTask := func(context.Context, task.RepoSpec) (task.System, error) {
		f.log.add("construct:task")
		return sys, nil
	}
	task.Register(f.cfg.TaskPlugin, task.Factory{
		RequiredRepoKeys: func() []string { return nil },
		TaskScopeKey:     func(config.RawValues, config.RawValues) (string, error) { return name, nil },
		New:              constructTask, NewLocal: constructTask,
		ProbeStartup: func(ctx context.Context, _ config.RawValues, _ map[string]config.Repo) error {
			f.log.add("startup:task")
			if taskProbe != nil {
				return taskProbe(ctx)
			}
			return ctx.Err()
		},
	})
	runner.Register(f.cfg.RunnerPlugin, func(config.RawValues) (runner.Runner, error) { return f.rnr, nil })
	harness.Register(f.cfg.HarnessPlugin, harness.Factory{New: func(config.RawValues) (harness.Harness, error) { return f.hrn, nil }})
	if err := goworkflows.InitDatabase(f.paths.Database); err != nil {
		t.Fatal(err)
	}
	f.save(t)
	return f
}

func (f *prerequisiteFixture) save(t *testing.T) {
	t.Helper()
	if err := config.SaveMachine(f.paths.Config, f.cfg); err != nil {
		t.Fatal(err)
	}
}

func (f *prerequisiteFixture) start(t *testing.T) *server.Client {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- serveRoot(ctx, f.paths, false) }()
	t.Cleanup(func() {
		cancel()
		select {
		case err := <-done:
			if err != nil {
				t.Errorf("serveRoot shutdown: %v", err)
			}
		case <-time.After(10 * time.Second):
			t.Error("serveRoot did not stop")
		}
	})
	client := server.NewClient(f.paths.Socket)
	waitForServerOrError(t, client, done)
	return client
}

func TestServePrerequisitesFailBeforeWorkersPollingRecoveryAndReadiness(t *testing.T) {
	for _, stage := range []string{"task", "runner", "harness"} {
		for _, recover := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/recover=%v", stage, recover), func(t *testing.T) {
				failure := errors.New("selected dependency unavailable")
				var taskProbe func(context.Context) error
				if stage == "task" {
					taskProbe = func(context.Context) error { return failure }
				}
				f := newPrerequisiteFixture(t, taskProbe)
				if stage == "runner" {
					f.rnr.probe = func(context.Context) error { return failure }
				}
				if stage == "harness" {
					f.hrn.probe = func(context.Context) error { return failure }
				}
				f.cfg.Repos = map[string]config.Repo{"app": {Path: "/work/app"}}
				f.save(t)
				before, err := os.ReadFile(f.paths.Database)
				if err != nil {
					t.Fatal(err)
				}
				err = serveRoot(context.Background(), f.paths, recover)
				if !errors.Is(err, failure) || !strings.Contains(err.Error(), stage+" plugin") {
					t.Fatalf("prerequisite failure = %v", err)
				}
				want := []string{"construct:task", "startup:task"}
				if stage != "task" {
					want = append(want, "startup:runner")
				}
				if stage == "harness" {
					want = append(want, "startup:harness")
				}
				if got := f.log.all(); !reflect.DeepEqual(got, want) {
					t.Fatalf("unexpected work before prerequisites: %v, want %v", got, want)
				}
				after, readErr := os.ReadFile(f.paths.Database)
				backups, _ := filepath.Glob(f.paths.Database + ".recover-*")
				if readErr != nil || string(before) != string(after) || len(backups) != 0 {
					t.Fatalf("prerequisite failure changed execution/recovery state: %v, backups=%v", readErr, backups)
				}
				if serverResponding(server.NewClient(f.paths.Socket), 50*time.Millisecond) {
					t.Fatal("failed prerequisites published readiness")
				}
				if _, err := os.Stat(f.paths.Socket); !os.IsNotExist(err) {
					t.Fatalf("socket created before prerequisites: %v", err)
				}
			})
		}
	}
}

func TestServePrerequisitesAcceptZeroReposAndProbeOnlySelectedPlugins(t *testing.T) {
	f := newPrerequisiteFixture(t, nil)
	client := f.start(t)
	repos, err := client.ListRepos(context.Background())
	if err != nil || len(repos) != 0 {
		t.Fatalf("zero-repo readiness = %v, %v", repos, err)
	}
	if got := f.log.all(); !reflect.DeepEqual(got, []string{"startup:task", "startup:runner", "startup:harness"}) {
		t.Fatalf("zero-repo startup queried workflows, repos or unselected plugins: %v", got)
	}
}

func TestServePrerequisitesDoNotRepeatAcceptedWorkflowPreflightAndIsolateMalformedWorkflow(t *testing.T) {
	f := newPrerequisiteFixture(t, nil)
	f.cfg.Repos = map[string]config.Repo{scenarioRepo: {Path: scenarioRepoPath}}
	f.save(t)
	store := &workflow.Store{Dir: f.paths.Workflows}
	if err := store.Put(scenarioWorkflowName, scenarioWorkflowYAML); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(f.paths.Workflows, "broken.yaml"), []byte("name: [broken\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	client := f.start(t)
	definitions, err := client.ListWorkflows(context.Background())
	if err != nil || len(definitions) != 2 {
		t.Fatalf("stored definitions = %v, %v", definitions, err)
	}
	for _, definition := range definitions {
		if definition.Name == scenarioWorkflowName && !definition.IsRoutable() {
			t.Fatalf("accepted workflow lost routing: %+v", definition)
		}
		if definition.Name == "broken" && definition.IsRoutable() {
			t.Fatal("malformed workflow published routes")
		}
	}
	waitScenario(t, time.Second, func() bool { return f.log.countPrefix("poll") > 0 })
	for _, stage := range []string{"task", "runner", "harness"} {
		if f.log.countPrefix("startup:"+stage) != 1 {
			t.Fatalf("prerequisite repeated per workflow: %v", f.log.all())
		}
		assertBefore(t, f.log.all(), "startup:"+stage, "poll")
	}
	for _, fullCheck := range []string{"task-config-validated", "runner-repo-validated", "harness-agent-validated", "environment:", "terminal-created:"} {
		if f.log.countPrefix(fullCheck) != 0 {
			t.Fatalf("startup repeated full preflight or launched work: %v", f.log.all())
		}
	}
}

func TestPrerequisiteGateUsesTenSecondAggregateAndEarlierCallerBudget(t *testing.T) {
	start := time.Now()
	f := newPrerequisiteFixture(t, func(ctx context.Context) error {
		deadline, ok := ctx.Deadline()
		if !ok || deadline.After(time.Now().Add(10*time.Second)) {
			t.Fatalf("missing aggregate ceiling: %v", deadline)
		}
		return nil
	})
	f.rnr.probe = func(ctx context.Context) error {
		deadline, ok := ctx.Deadline()
		if !ok || deadline.After(time.Now().Add(5*time.Second)) {
			t.Fatalf("missing runner ceiling: %v", deadline)
		}
		return nil
	}
	if err := checkStartupPrerequisites(context.Background(), f.cfg, f.rnr, f.hrn); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Millisecond)
	defer cancel()
	f.rnr.probe = func(ctx context.Context) error { <-ctx.Done(); return ctx.Err() }
	start = time.Now()
	err := checkStartupPrerequisites(ctx, f.cfg, f.rnr, f.hrn)
	if !errors.Is(err, context.DeadlineExceeded) || time.Since(start) > time.Second || f.log.countPrefix("startup:harness") != 1 {
		t.Fatalf("gate extended deadline or continued after failure: %v, %v", err, f.log.all())
	}
}

func TestPrerequisiteGateAggregateIncludesMultipleTaskConnectionsAndRunner(t *testing.T) {
	f := newPrerequisiteFixture(t, func(ctx context.Context) error {
		// Model two successful task connections, each strictly below five
		// seconds. The runner must receive the aggregate's remaining budget,
		// not a fresh five seconds beyond the ten-second prerequisite phase.
		for i := 0; i < 2; i++ {
			select {
			case <-time.After(4 * time.Second):
			case <-ctx.Done():
				return ctx.Err()
			}
		}
		return nil
	})
	f.rnr.probe = func(ctx context.Context) error { <-ctx.Done(); return ctx.Err() }
	start := time.Now()
	err := checkStartupPrerequisites(context.Background(), f.cfg, f.rnr, f.hrn)
	if !errors.Is(err, context.DeadlineExceeded) || time.Since(start) > 11*time.Second || f.log.countPrefix("startup:harness") != 0 {
		t.Fatalf("aggregate prerequisite budget not enforced: %v (%v), events=%v", err, time.Since(start), f.log.all())
	}
}
