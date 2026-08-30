package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/rajpopat27/relay-flow/internal/config"
	"github.com/rajpopat27/relay-flow/internal/execution/goworkflows"
	"github.com/rajpopat27/relay-flow/internal/harness"
	"github.com/rajpopat27/relay-flow/internal/identity"
	"github.com/rajpopat27/relay-flow/internal/paths"
	"github.com/rajpopat27/relay-flow/internal/repo"
	runsvc "github.com/rajpopat27/relay-flow/internal/run"
	"github.com/rajpopat27/relay-flow/internal/runner"
	"github.com/rajpopat27/relay-flow/internal/server"
	"github.com/rajpopat27/relay-flow/internal/task"
	"github.com/rajpopat27/relay-flow/internal/workflow"
)

const (
	scenarioTaskPlugin    = "scenario-e2e-task"
	scenarioRunnerPlugin  = "scenario-e2e-runner"
	scenarioHarnessPlugin = "scenario-e2e-harness"
)

var (
	scenarioFactoryMu      sync.Mutex
	scenarioFactorySystem  task.System
	scenarioFactoryRunner  runner.Runner
	scenarioFactoryHarness harness.Harness
)

func init() {
	task.Register(scenarioTaskPlugin, task.Factory{
		RequiredRepoKeys: func() []string { return nil },
		TaskScopeKey: func(config.RawValues, config.RawValues) (string, error) {
			return "scenario-scope", nil
		},
		New: func(context.Context, task.RepoSpec) (task.System, error) {
			scenarioFactoryMu.Lock()
			defer scenarioFactoryMu.Unlock()
			if scenarioFactorySystem == nil {
				return nil, errors.New("scenario task system not configured")
			}
			if fake, ok := scenarioFactorySystem.(*scenarioTaskSystem); ok {
				fake.log.add("factory:task:" + scenarioTaskPlugin)
			}
			return scenarioFactorySystem, nil
		},
	})
	runner.Register(scenarioRunnerPlugin, func(config.RawValues) (runner.Runner, error) {
		scenarioFactoryMu.Lock()
		defer scenarioFactoryMu.Unlock()
		if scenarioFactoryRunner == nil {
			return nil, errors.New("scenario runner not configured")
		}
		if fake, ok := scenarioFactoryRunner.(*scenarioRunner); ok {
			fake.log.add("factory:runner:" + scenarioRunnerPlugin)
		}
		return scenarioFactoryRunner, nil
	})
	harness.Register(scenarioHarnessPlugin, func(config.RawValues) (harness.Harness, error) {
		scenarioFactoryMu.Lock()
		defer scenarioFactoryMu.Unlock()
		if scenarioFactoryHarness == nil {
			return nil, errors.New("scenario harness not configured")
		}
		if fake, ok := scenarioFactoryHarness.(*scenarioHarness); ok {
			fake.log.add("factory:harness:" + scenarioHarnessPlugin)
		}
		return scenarioFactoryHarness, nil
	})
}

func setScenarioFactorySystem(system task.System) {
	scenarioFactoryMu.Lock()
	scenarioFactorySystem = system
	scenarioFactoryMu.Unlock()
}

func setScenarioFactoryAdapters(system task.System, rnr runner.Runner, hrn harness.Harness) {
	scenarioFactoryMu.Lock()
	scenarioFactorySystem = system
	scenarioFactoryRunner = rnr
	scenarioFactoryHarness = hrn
	scenarioFactoryMu.Unlock()
}

// These scenarios exercise the same composition chain as serve:
// RepoPoller/handleBatch -> RunManager -> real go-workflows SQLite engine.
// The only replacements are the documented task, runner, and harness seams.

func TestScenarioHappyPath(t *testing.T) {
	f := newScenarioFixture(t)
	f.pollOnce()
	f.waitNode("implement")

	wantID := identity.NewRunID(scenarioRepo, scenarioWorkflowName, scenarioTicket)
	if f.runID != wantID {
		t.Fatalf("run ID = %q, want deterministic %q", f.runID, wantID)
	}
	assertBefore(t, f.log.all(), "claim:TEST-1:scenarioFlow", "run-created:"+string(f.runID))
	assertMailboxDefinitions(t, f.tasks)
	assertLaunch(t, f, "implement", workflow.NodeAgent)

	f.submit(workflow.OutcomeSuccess, "verify")
	f.waitNode("verify")
	assertTransitionOrder(t, f.log.all(), "implement", "verify")
	assertLaunch(t, f, "verify", workflow.NodeAgent)

	f.submit(workflow.OutcomeSuccess, "pr-review")
	f.waitNode("pr-review")
	assertTransitionOrder(t, f.log.all(), "verify", "pr-review")
	// The real launch metadata selects the production plugin's HITL silence
	// path. The TypeScript plugin tests drive that path directly; this Go
	// scenario does not invent a second nudge implementation.
	assertLaunch(t, f, "pr-review", workflow.NodeHITL)

	f.submit(workflow.OutcomeSuccess, "end")
	f.waitCompleted()
	if got := f.runner.cleanupCount(); got != 1 {
		t.Fatalf("CleanupRun calls = %d, want 1 with explicit cleanup policy", got)
	}
	assertExactHappyEffects(t, f)
}

func TestCompositionRootSelectsAlternatePluginsForDurableRun(t *testing.T) {
	log := newScenarioLog()
	tasks := newScenarioTaskSystem(log)
	rnr := newScenarioRunner(log)
	hrn := newScenarioHarness(log)
	setScenarioFactoryAdapters(tasks, rnr, hrn)

	root := filepath.Join(t.TempDir(), ".relay-flow")
	t.Setenv("RELAY_FLOW_HOME", root)
	p, err := home()
	if err != nil {
		t.Fatal(err)
	}
	if err := paths.Ensure(p); err != nil {
		t.Fatal(err)
	}
	if err := config.SaveMachine(p.Config, &config.Machine{
		PollIntervalSeconds: 1,
		TaskPlugin:          scenarioTaskPlugin,
		TaskConfig:          config.RawValues{"provider": "alternate"},
		RunnerPlugin:        scenarioRunnerPlugin,
		RunnerConfig:        config.RawValues{"transport": "opaque"},
		HarnessPlugin:       scenarioHarnessPlugin,
		HarnessConfig:       config.RawValues{"runtime": "alternate"},
		Repos: map[string]config.Repo{
			scenarioRepo: {Path: scenarioRepoPath, TaskConfig: config.RawValues{"scope": "test"}},
		},
	}); err != nil {
		t.Fatal(err)
	}
	if err := (&workflow.Store{Dir: p.Workflows}).Put(scenarioWorkflowName, scenarioWorkflowYAML); err != nil {
		t.Fatal(err)
	}
	if err := goworkflows.InitDatabase(p.Database); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	stopped := false
	go func() { done <- serveRoot(ctx, p, false) }()
	t.Cleanup(func() {
		if stopped {
			return
		}
		cancel()
		select {
		case <-done:
		case <-time.After(10 * time.Second):
		}
	})

	client := server.NewClient(p.Socket)
	waitForServer(t, client)
	var active runsvc.Run
	waitScenario(t, 10*time.Second, func() bool {
		var lookupErr error
		active, lookupErr = client.GetRunByTicket(context.Background(), scenarioTicket)
		return lookupErr == nil && active.State == runsvc.StateWaiting && active.CurrentNode == "implement"
	})

	launch := hrn.launch("implement")
	wantPrompt := "Task system: " + scenarioTaskPlugin + "\nUse the " + scenarioTaskPlugin + " tools to read the parent ticket " + scenarioTicket + "."
	if !strings.Contains(launch.Prompt, wantPrompt) {
		t.Fatalf("selected task system did not reach launch prompt: %q", launch.Prompt)
	}
	command := rnr.command(scenarioTicket + ":implement")
	wantArgs := []string{"opaque", launch.Prompt}
	if command.Executable != "fake-harness" || !reflect.DeepEqual(command.Args, wantArgs) {
		t.Fatalf("runner command = %#v, want opaque harness command args %#v", command, wantArgs)
	}
	for _, event := range []string{
		"factory:task:" + scenarioTaskPlugin,
		"factory:runner:" + scenarioRunnerPlugin,
		"factory:harness:" + scenarioHarnessPlugin,
		"task-config-validated",
		"runner-repo-validated",
		"harness-agent-validated:implementer",
		"harness-launched:" + scenarioTicket + ":implement",
		"terminal-created:" + scenarioTicket + ":implement",
	} {
		if log.countPrefix(event) == 0 {
			t.Fatalf("composition did not call selected interface boundary %q; events=%v", event, log.all())
		}
	}

	for i, next := range []string{"verify", "pr-review", "end"} {
		current, getErr := client.GetRunByTicket(context.Background(), scenarioTicket)
		if getErr != nil {
			t.Fatal(getErr)
		}
		ack, submitErr := client.SubmitReport(context.Background(), runsvc.ReportRequest{
			RunID: current.ID, Node: current.CurrentNode,
			ReportID: "alternate-session:message-" + fmt.Sprint(i+1),
			Report:   scenarioReport(workflow.OutcomeSuccess, next),
		})
		if submitErr != nil || !ack.Accepted {
			t.Fatalf("submit durable report %s -> %s: ack=%+v err=%v", current.CurrentNode, next, ack, submitErr)
		}
		if next != "end" {
			waitScenario(t, 10*time.Second, func() bool {
				advanced, advanceErr := client.GetRunByTicket(context.Background(), scenarioTicket)
				return advanceErr == nil && advanced.State == runsvc.StateWaiting && advanced.CurrentNode == next
			})
		}
	}
	waitScenario(t, 10*time.Second, func() bool {
		finished, getErr := client.GetRunByTicket(context.Background(), scenarioTicket)
		return getErr == nil && finished.State == runsvc.StateCompleted
	})
	if err := client.Stop(context.Background()); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("serveRoot: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("serveRoot did not stop")
	}
	stopped = true
}

func TestScenarioHITLRejectLoop(t *testing.T) {
	f := newScenarioFixture(t)
	f.pollOnce()
	f.waitNode("implement")
	f.submit(workflow.OutcomeSuccess, "verify")
	f.waitNode("verify")
	f.submit(workflow.OutcomeSuccess, "pr-review")
	f.waitNode("pr-review")

	firstImplementVisit := f.harness.launch("implement").NodeVisitID
	f.submit(workflow.OutcomeFailure, "implement")
	f.waitNodeWithNewVisit("implement", firstImplementVisit)
	if f.tasks.mailboxCreateCount("implement") != 1 {
		t.Fatal("reject loop created a second implement mailbox")
	}
	if got := f.tasks.commentCount("implement", "feedback"); got != 1 {
		t.Fatalf("reject feedback on reopened implement mailbox = %d, want 1", got)
	}
	if got := f.runner.launchCount("TEST-1:implement"); got != 2 {
		t.Fatalf("implement terminal launches = %d, want 2 with explicit terminal checkpointing", got)
	}

	f.submit(workflow.OutcomeSuccess, "verify")
	f.waitNode("verify")
	f.submit(workflow.OutcomeSuccess, "pr-review")
	f.waitNode("pr-review")
	f.submit(workflow.OutcomeSuccess, "end")
	f.waitCompleted()

	if got := f.tasks.commentCount("implement", "summary"); got != 2 {
		t.Fatalf("implement summaries = %d, want one per pass", got)
	}
	if got := f.tasks.commentCount("verify", "summary"); got != 2 {
		t.Fatalf("verify summaries = %d, want one per pass", got)
	}
	if got := f.tasks.commentCount("pr-review", "summary"); got != 2 {
		t.Fatalf("pr-review summaries = %d, want one per pass", got)
	}
	for node, want := range map[string]int{"implement": 1, "verify": 2, "pr-review": 2, "parent": 0} {
		if got := f.tasks.commentCount(node, "feedback"); got != want {
			t.Fatalf("%s feedback comments = %d, want exactly %d", node, got, want)
		}
	}
	if got := f.tasks.totalComments(); got != 11 {
		t.Fatalf("loop comments = %d, want exactly 11 (one summary and selected feedback per pass)", got)
	}
	if got := f.tasks.totalMailboxCreates(); got != 3 {
		t.Fatalf("mailboxes created = %d, want three reusable mailboxes", got)
	}
}

func TestScenarioAgentFailureRoutingAndInvalidNudge(t *testing.T) {
	f := newScenarioFixture(t)
	f.pollOnce()
	f.waitNode("implement")

	before := f.projection()
	bad := scenarioReport(workflow.OutcomeFailure, "verify") // success-only target
	ack, err := f.engine.SubmitReport(context.Background(), runsvc.ReportRequest{
		RunID: f.runID, Node: before.CurrentNode, ReportID: "invalid-route", Report: bad,
	})
	if err == nil || ack.Accepted {
		t.Fatalf("failure report naming success-only target accepted: ack=%+v err=%v", ack, err)
	}
	// Production plugin tests exercise invalid output -> session API nudge.
	// Here the real server boundary must reject it without persistence.
	after := f.projection()
	if after.CurrentNodeVisitID != before.CurrentNodeVisitID || after.CurrentNode != "implement" {
		t.Fatalf("invalid report changed projection: before=%+v after=%+v", before, after)
	}
	if got := f.tasks.totalComments(); got != 0 {
		t.Fatalf("invalid report persisted graph effects: %d comments", got)
	}

	f.submit(workflow.OutcomeFailure, "pr-review")
	f.waitNode("pr-review")
	if got := f.tasks.commentCount("pr-review", "feedback"); got != 1 {
		t.Fatalf("failure feedback on configured failure target = %d, want 1", got)
	}
	if got := f.runner.launchCount("TEST-1:verify"); got != 0 {
		t.Fatalf("failure followed success route and launched verify %d times", got)
	}
	f.submit(workflow.OutcomeSuccess, "end")
	f.waitCompleted()
}

func TestScenarioCrashMidTransitionRollsForward(t *testing.T) {
	f := newScenarioFixture(t)
	f.pollOnce()
	f.waitNode("implement")
	f.tasks.setCompleteFailures(100)
	f.submitWithoutWaiting(workflow.OutcomeSuccess, "verify")
	f.waitEvent("complete-failed:TEST-1:implement")
	if got := f.tasks.commentCount("implement", "summary"); got != 1 {
		t.Fatalf("pre-crash summaries = %d, want 1", got)
	}
	if got := f.tasks.commentCount("verify", "feedback"); got != 1 {
		t.Fatalf("pre-crash feedback = %d, want 1", got)
	}

	f.restart()
	f.tasks.setCompleteFailures(0)
	f.waitNode("verify")
	if got := f.tasks.commentCount("implement", "summary"); got != 1 {
		t.Fatalf("summary duplicated after restart: %d", got)
	}
	if got := f.tasks.commentCount("verify", "feedback"); got != 1 {
		t.Fatalf("feedback duplicated after restart: %d", got)
	}
	if got := f.tasks.completeCount("implement"); got != 1 {
		t.Fatalf("successful implement completions = %d, want 1", got)
	}

	f.submit(workflow.OutcomeSuccess, "pr-review")
	f.waitNode("pr-review")
	f.submit(workflow.OutcomeSuccess, "end")
	f.waitCompleted()
	assertNoDuplicateTransitionCalls(t, f.tasks.transitionCalls())
}

func TestScenarioLateRegisterAndLateSubmit(t *testing.T) {
	log := newScenarioLog()
	sys := newScenarioTaskSystem(log)
	fr := newScenarioRunner(log)
	fh := newScenarioHarness(log)
	reg := repo.NewRegistry()
	db := filepath.Join(t.TempDir(), "state.db")
	engine, err := goworkflows.New(db, goworkflows.Dependencies{Repos: reg, Runner: fr, Harness: fh})
	if err != nil {
		t.Fatal(err)
	}
	if err := engine.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { shutdownScenarioEngine(engine) })

	gate := &sync.Mutex{}
	manager := &runsvc.RunManager{Executor: scenarioExecutor{inner: engine, log: log}, Runs: engine, Gate: gate}
	pollers := repo.NewPollerGroup(10, handleBatch(manager))
	pollers.Interval = 100 * time.Millisecond
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { pollers.Run(ctx); close(done) }()
	t.Cleanup(func() { cancel(); <-done })

	repoLookup := scenarioRepoLookup{reg: reg}
	store := &workflow.Store{Dir: filepath.Join(t.TempDir(), "workflows")}
	wfService := workflow.NewService(store, engine, repoLookup)
	wfService.Gate = gate
	wfService.ValidateTaskConfig = workflowConfigValidator(reg)
	wfService.Rebind = func() error { return reg.BindWorkflows(wfService.Registry().List()) }
	configPath := filepath.Join(t.TempDir(), "config.yaml")
	if err := config.SaveMachine(configPath, &config.Machine{
		TaskPlugin: scenarioTaskPlugin, RunnerPlugin: "unused", HarnessPlugin: "unused",
		Repos: map[string]config.Repo{},
	}); err != nil {
		t.Fatal(err)
	}
	repoService := repo.NewServiceWithRegistry(repo.ServiceConfig{
		ConfigPath: configPath, TaskPlugin: scenarioTaskPlugin, Runner: fr,
		Active: engine, Workflows: wfService.Registry(),
	}, reg)
	deps := &serveDeps{
		repos:          repoService,
		onReposChanged: func() { pollers.ReplaceRepos(repoService.Registry().List()) },
	}

	// Normative inverse case: submission before registration is rejected
	// completely; no definition, binding, claim, or run leaks through.
	if _, err := wfService.Submit(context.Background(), scenarioWorkflowYAML); err == nil || !strings.Contains(err.Error(), "unregistered repo") {
		t.Fatalf("submit before registration error = %v, want unregistered repo rejection", err)
	}
	if len(wfService.List()) != 0 || log.countPrefix("claim:") != 0 {
		t.Fatal("rejected workflow submission left observable state")
	}

	// Runtime registration goes through the real repo service and the same
	// serve callback that updates the already-running poller group.
	setScenarioFactorySystem(sys)
	if _, err := deps.RegisterRepo(context.Background(), repo.RegisterInput{
		Name: scenarioRepo, Path: scenarioRepoPath,
	}); err != nil {
		t.Fatalf("register repo: %v", err)
	}
	waitScenario(t, 2*time.Second, func() bool { return log.countPrefix("poll") > 0 })
	if log.countPrefix("claim:") != 0 {
		t.Fatal("repo registration claimed before a workflow was submitted")
	}

	// Registration first, then submission: the real Service rebuilds the
	// live repo binding, and the next poll claims without a server restart.
	if _, err := wfService.Submit(context.Background(), scenarioWorkflowYAML); err != nil {
		t.Fatalf("submit after registration: %v", err)
	}
	log.add("workflow:submitted")
	wantID := identity.NewRunID(scenarioRepo, scenarioWorkflowName, scenarioTicket)
	waitScenario(t, 5*time.Second, func() bool {
		r, err := engine.GetRun(context.Background(), wantID)
		return err == nil && r.CurrentNode == "implement"
	})
	assertBefore(t, log.all(), "workflow:submitted", "claim:TEST-1:scenarioFlow")
	assertBefore(t, log.all(), "claim:TEST-1:scenarioFlow", "run-created:"+string(wantID))

	finishScenarioRun(t, engine, wantID)
	r, err := engine.GetRun(context.Background(), wantID)
	if err != nil || r.State != runsvc.StateCompleted {
		t.Fatalf("late-register run = %+v err=%v, want completed", r, err)
	}
}

const (
	scenarioRepo         = "fake-repo"
	scenarioRepoPath     = "/tmp/fake-repo"
	scenarioWorkflowName = "scenarioFlow"
	scenarioTicket       = "TEST-1"
)

var scenarioWorkflowYAML = []byte(`name: scenarioFlow
repos: [fake-repo]
cleanupRunnerOnEnd: true
nodes:
  start:
    onSuccess: [{target: implement}]
  implement:
    type: agent
    agent: implementer
    description: Implement the requested change.
    onSuccess: [{target: verify}]
    onFailure: [{target: pr-review, when: implementation cannot proceed}]
  verify:
    type: agent
    agent: verifier
    description: Verify the implementation.
    onSuccess: [{target: pr-review}]
    onFailure: [{target: implement, when: verification fails}]
  pr-review:
    type: hitl
    agent: reviewer
    description: Review and approve the pull request.
    onSuccess: [{target: end}]
    onFailure: [{target: implement, when: changes are requested}]
  end: {}
`)

func scenarioWorkflow() workflow.Workflow {
	wf, err := workflow.Parse("", scenarioWorkflowYAML)
	if err != nil {
		panic(err)
	}
	if err := wf.Validate(); err != nil {
		panic(err)
	}
	return *wf
}

type scenarioFixture struct {
	t       *testing.T
	log     *scenarioLog
	tasks   *scenarioTaskSystem
	runner  *scenarioRunner
	harness *scenarioHarness
	reg     *repo.Registry
	repo    *repo.Repo
	wf      workflow.Workflow
	db      string
	engine  *goworkflows.Engine
	manager *runsvc.RunManager
	runID   runsvc.ID
}

func newScenarioFixture(t *testing.T) *scenarioFixture {
	t.Helper()
	f := &scenarioFixture{t: t, log: newScenarioLog(), wf: scenarioWorkflow()}
	f.tasks = newScenarioTaskSystem(f.log)
	f.runner = newScenarioRunner(f.log)
	f.harness = newScenarioHarness(f.log)
	f.reg = repo.NewRegistry()
	f.repo = &repo.Repo{Name: scenarioRepo, Path: scenarioRepoPath, TaskSystem: f.tasks}
	f.reg.Replace(f.repo)
	if err := f.reg.BindWorkflows([]*workflow.Workflow{&f.wf}); err != nil {
		t.Fatal(err)
	}
	f.db = filepath.Join(t.TempDir(), "state.db")
	f.engine = f.openEngine()
	f.manager = &runsvc.RunManager{Executor: scenarioExecutor{inner: f.engine, log: f.log}, Runs: f.engine, Gate: &sync.Mutex{}}
	f.runID = identity.NewRunID(scenarioRepo, scenarioWorkflowName, scenarioTicket)
	t.Cleanup(func() { shutdownScenarioEngine(f.engine) })
	return f
}

func (f *scenarioFixture) openEngine() *goworkflows.Engine {
	f.t.Helper()
	e, err := goworkflows.New(f.db, goworkflows.Dependencies{
		Repos: f.reg, Runner: f.runner, Harness: f.harness,
		Runtime: &runsvc.RuntimePolicy{},
	})
	if err != nil {
		f.t.Fatal(err)
	}
	if err := e.Start(context.Background()); err != nil {
		f.t.Fatal(err)
	}
	return e
}

func (f *scenarioFixture) pollOnce() {
	f.t.Helper()
	batch, err := f.tasks.Poll(context.Background())
	if err != nil {
		f.t.Fatal(err)
	}
	handleBatch(f.manager)(context.Background(), f.repo, batch)
}

func (f *scenarioFixture) projection() runsvc.Run {
	f.t.Helper()
	r, err := f.engine.GetRun(context.Background(), f.runID)
	if err != nil {
		f.t.Fatal(err)
	}
	return r
}

func (f *scenarioFixture) waitNode(node string) {
	f.t.Helper()
	waitScenario(f.t, 10*time.Second, func() bool {
		r, err := f.engine.GetRun(context.Background(), f.runID)
		return err == nil && r.CurrentNode == node && r.CurrentNodeVisitID != ""
	})
}

func (f *scenarioFixture) waitNodeWithNewVisit(node string, old runsvc.NodeVisitID) {
	f.t.Helper()
	waitScenario(f.t, 10*time.Second, func() bool {
		r, err := f.engine.GetRun(context.Background(), f.runID)
		return err == nil && r.CurrentNode == node && r.CurrentNodeVisitID != "" && r.CurrentNodeVisitID != old
	})
}

func (f *scenarioFixture) firstVisit(node string) runsvc.NodeVisitID {
	f.t.Helper()
	r := f.projection()
	if r.CurrentNode != node {
		f.t.Fatalf("current node = %q, want %q", r.CurrentNode, node)
	}
	return r.CurrentNodeVisitID
}

func (f *scenarioFixture) submit(status workflow.Outcome, next string) {
	f.t.Helper()
	current := f.projection().CurrentNode
	f.submitWithoutWaiting(status, next)
	if next == "end" {
		return
	}
	waitScenario(f.t, 10*time.Second, func() bool {
		r, err := f.engine.GetRun(context.Background(), f.runID)
		return err == nil && r.CurrentNode == next && r.CurrentNode != current
	})
}

func (f *scenarioFixture) submitWithoutWaiting(status workflow.Outcome, next string) {
	f.t.Helper()
	r := f.projection()
	ack, err := f.engine.SubmitReport(context.Background(), runsvc.ReportRequest{
		RunID: f.runID, Node: r.CurrentNode,
		ReportID: string(r.CurrentNodeVisitID) + ":scenario", Report: scenarioReport(status, next),
	})
	if err != nil || !ack.Accepted {
		f.t.Fatalf("submit %s -> %s: ack=%+v err=%v", r.CurrentNode, next, ack, err)
	}
	// A successful ack is the public guarantee that report+route persistence
	// happened. Recording it here lets the cross-boundary ordering assertion
	// compare that observable ack with subsequent adapter effects.
	f.log.add("report-persisted:" + r.CurrentNode)
}

func (f *scenarioFixture) waitCompleted() {
	f.t.Helper()
	waitScenario(f.t, 15*time.Second, func() bool {
		r, err := f.engine.GetRun(context.Background(), f.runID)
		return err == nil && r.State == runsvc.StateCompleted
	})
}

func (f *scenarioFixture) waitEvent(event string) {
	f.t.Helper()
	waitScenario(f.t, 10*time.Second, func() bool { return f.log.count(event) > 0 })
}

func (f *scenarioFixture) restart() {
	f.t.Helper()
	shutdownScenarioEngine(f.engine)
	f.engine = f.openEngine()
	f.manager.Executor = f.engine
	f.manager.Runs = f.engine
}

func shutdownScenarioEngine(e *goworkflows.Engine) {
	if e == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_ = e.Shutdown(ctx)
}

func finishScenarioRun(t *testing.T, engine *goworkflows.Engine, id runsvc.ID) {
	t.Helper()
	for _, next := range []string{"verify", "pr-review", "end"} {
		var r runsvc.Run
		waitScenario(t, 10*time.Second, func() bool {
			var err error
			r, err = engine.GetRun(context.Background(), id)
			return err == nil && r.CurrentNodeVisitID != "" && r.State == runsvc.StateWaiting
		})
		ack, err := engine.SubmitReport(context.Background(), runsvc.ReportRequest{
			RunID: id, Node: r.CurrentNode,
			ReportID: string(r.CurrentNodeVisitID) + ":finish", Report: scenarioReport(workflow.OutcomeSuccess, next),
		})
		if err != nil || !ack.Accepted {
			t.Fatalf("finish %s -> %s: ack=%+v err=%v", r.CurrentNode, next, ack, err)
		}
		if next != "end" {
			want := next
			waitScenario(t, 10*time.Second, func() bool {
				n, err := engine.GetRun(context.Background(), id)
				return err == nil && n.CurrentNode == want
			})
		}
	}
	waitScenario(t, 15*time.Second, func() bool {
		r, err := engine.GetRun(context.Background(), id)
		return err == nil && r.State == runsvc.StateCompleted
	})
}

func scenarioReport(status workflow.Outcome, next string) workflow.Report {
	none := "None"
	r := workflow.Report{
		Status: status, NextStep: next,
		Summary: workflow.Summary{
			Completed: "work completed", Commits: "abc123", NotCompleted: none, IssuesDiscovered: none,
			Verification: "checks passed", Notes: none,
		},
		Feedback: workflow.Feedback{
			ReasonForNextStep: "continue", RequiredActions: "process this mailbox",
			RelevantContext: "scenario context", ExpectedResult: "successful next visit",
		},
	}
	if next == "end" {
		r.Feedback = workflow.Feedback{
			ReasonForNextStep: none, RequiredActions: none, RelevantContext: none, ExpectedResult: none,
		}
	}
	return r
}

type scenarioLog struct {
	mu     sync.Mutex
	events []string
}

type scenarioExecutor struct {
	inner runsvc.Executor
	log   *scenarioLog
}

func (e scenarioExecutor) EnsureRun(ctx context.Context, start runsvc.Start) (bool, error) {
	created, err := e.inner.EnsureRun(ctx, start)
	if err == nil && created {
		e.log.add("run-created:" + string(start.ID))
	}
	return created, err
}

func (e scenarioExecutor) SubmitReport(ctx context.Context, report runsvc.ReportRequest) (runsvc.ReportAck, error) {
	return e.inner.SubmitReport(ctx, report)
}

func (e scenarioExecutor) CancelRun(ctx context.Context, id runsvc.ID, reason string) error {
	return e.inner.CancelRun(ctx, id, reason)
}

func newScenarioLog() *scenarioLog { return &scenarioLog{} }

func (l *scenarioLog) add(event string) {
	l.mu.Lock()
	l.events = append(l.events, event)
	l.mu.Unlock()
}

func (l *scenarioLog) all() []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]string(nil), l.events...)
}

func (l *scenarioLog) count(event string) int {
	n := 0
	for _, got := range l.all() {
		if got == event {
			n++
		}
	}
	return n
}

func (l *scenarioLog) countPrefix(prefix string) int {
	n := 0
	for _, got := range l.all() {
		if strings.HasPrefix(got, prefix) {
			n++
		}
	}
	return n
}

type scenarioComment struct {
	node string
	kind string
	body string
}

type scenarioTaskSystem struct {
	log *scenarioLog
	mu  sync.Mutex

	claimed          bool
	mailboxes        map[string]task.Mailbox
	specs            map[string]task.MailboxSpec
	mailboxStatus    map[string]string
	parentStatus     string
	comments         map[string]scenarioComment
	transitions      []string
	creates          map[string]int
	completions      map[string]int
	completeFailures int
}

var _ task.System = (*scenarioTaskSystem)(nil)

func newScenarioTaskSystem(log *scenarioLog) *scenarioTaskSystem {
	return &scenarioTaskSystem{
		log: log, mailboxes: map[string]task.Mailbox{}, specs: map[string]task.MailboxSpec{},
		mailboxStatus: map[string]string{}, comments: map[string]scenarioComment{},
		creates: map[string]int{}, completions: map[string]int{},
	}
}

func (s *scenarioTaskSystem) Poll(context.Context) ([]task.Ticket, error) {
	s.log.add("poll")
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.claimed {
		return nil, nil
	}
	return []task.Ticket{{ID: "ticket-1", Key: scenarioTicket, Title: "Scenario ticket"}}, nil
}

func (s *scenarioTaskSystem) CompileFilter(config.RawValues) (func(task.Ticket) bool, error) {
	return func(t task.Ticket) bool { return t.Key == scenarioTicket }, nil
}

func (s *scenarioTaskSystem) Claim(_ context.Context, ref task.TicketRef, workflowName string) error {
	s.log.add("claim:" + ref.Key + ":" + workflowName)
	s.mu.Lock()
	s.claimed = true
	s.mu.Unlock()
	return nil
}

func (s *scenarioTaskSystem) ValidateConfig(context.Context, config.RawValues, map[string]config.RawValues) error {
	s.log.add("task-config-validated")
	return nil
}

func (s *scenarioTaskSystem) EnsureMailboxes(_ context.Context, parent task.TicketRef, _ string, specs []task.MailboxSpec) (map[string]task.Mailbox, error) {
	s.log.add("mailboxes:ensure")
	s.mu.Lock()
	defer s.mu.Unlock()
	out := map[string]task.Mailbox{}
	for _, spec := range specs {
		s.specs[spec.Node] = spec
		mb, ok := s.mailboxes[spec.Node]
		if !ok {
			mb = task.Mailbox{ID: "mb-" + spec.Node, Key: spec.Title, Node: spec.Node}
			s.mailboxes[spec.Node] = mb
			s.mailboxStatus[spec.Node] = "To Do"
			s.creates[spec.Node]++
			s.log.add("mailbox-created:" + spec.Title)
		}
		out[spec.Node] = mb
	}
	return out, nil
}

func (s *scenarioTaskSystem) StartDefaults() config.RawValues {
	return config.RawValues{"transitionTo": map[string]any{"parentStatus": "In Progress"}}
}

func (s *scenarioTaskSystem) WorkDefaults() config.RawValues {
	return config.RawValues{"transitionTo": map[string]any{"taskStatus": "In Progress"}}
}

func (s *scenarioTaskSystem) EndDefaults() config.RawValues {
	return config.RawValues{"transitionTo": map[string]any{"parentStatus": "Done"}}
}

func (s *scenarioTaskSystem) ApplyTaskConfig(_ context.Context, target task.Target, cfg config.RawValues) error {
	transition, _ := cfg["transitionTo"].(map[string]any)
	s.mu.Lock()
	defer s.mu.Unlock()
	if target.Mailbox == nil {
		status, _ := transition["parentStatus"].(string)
		s.parentStatus = status
		call := "parent:" + status
		s.transitions = append(s.transitions, call)
		s.log.add("transition:" + call)
		return nil
	}
	status, _ := transition["taskStatus"].(string)
	s.mailboxStatus[target.Mailbox.Node] = status
	call := target.Mailbox.Node + ":" + status
	s.transitions = append(s.transitions, call)
	s.log.add("transition:" + call)
	return nil
}

func (s *scenarioTaskSystem) CompleteMailbox(_ context.Context, mailbox task.Mailbox) error {
	s.mu.Lock()
	if s.completeFailures > 0 {
		s.completeFailures--
		s.mu.Unlock()
		s.log.add("complete-failed:" + mailbox.Key)
		return errors.New("temporary completion failure")
	}
	s.mailboxStatus[mailbox.Node] = "Done"
	s.completions[mailbox.Node]++
	s.mu.Unlock()
	s.log.add("complete:" + mailbox.Key)
	return nil
}

func (s *scenarioTaskSystem) HasComment(_ context.Context, _ task.Target, marker string) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, ok := s.comments[marker]
	return ok, nil
}

func (s *scenarioTaskSystem) Comment(_ context.Context, target task.Target, body, marker string) error {
	s.mu.Lock()
	if _, exists := s.comments[marker]; exists {
		s.mu.Unlock()
		s.log.add("comment-existing:" + marker)
		return nil
	}
	node := "parent"
	if target.Mailbox != nil {
		node = target.Mailbox.Node
	}
	kind := marker[strings.LastIndex(marker, ":")+1:]
	s.comments[marker] = scenarioComment{node: node, kind: kind, body: body}
	s.mu.Unlock()
	s.log.add("comment:" + scenarioTicket + ":" + node + ":" + kind)
	return nil
}

func (s *scenarioTaskSystem) ResetForRecovery(context.Context, task.TicketRef, []task.Mailbox, config.RawValues) error {
	return nil
}

func (s *scenarioTaskSystem) setCompleteFailures(n int) {
	s.mu.Lock()
	s.completeFailures = n
	s.mu.Unlock()
}

func (s *scenarioTaskSystem) mailboxCreateCount(node string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.creates[node]
}

func (s *scenarioTaskSystem) totalMailboxCreates() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	n := 0
	for _, count := range s.creates {
		n += count
	}
	return n
}

func (s *scenarioTaskSystem) commentCount(node, kind string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	n := 0
	for _, comment := range s.comments {
		if comment.node == node && comment.kind == kind {
			n++
		}
	}
	return n
}

func (s *scenarioTaskSystem) totalComments() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.comments)
}

func (s *scenarioTaskSystem) completeCount(node string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.completions[node]
}

func (s *scenarioTaskSystem) transitionCalls() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.transitions...)
}

type scenarioTerminal struct {
	terminal runner.Terminal
	live     bool
}

type scenarioRunner struct {
	log *scenarioLog
	mu  sync.Mutex

	environments map[string]runner.Environment
	terminals    map[string]*scenarioTerminal
	commands     map[string][]runner.Command
	launches     map[string]int
	cleanups     int
}

var _ runner.Runner = (*scenarioRunner)(nil)

func newScenarioRunner(log *scenarioLog) *scenarioRunner {
	return &scenarioRunner{
		log: log, environments: map[string]runner.Environment{}, terminals: map[string]*scenarioTerminal{},
		commands: map[string][]runner.Command{}, launches: map[string]int{},
	}
}

func (r *scenarioRunner) DiscoverRepos(context.Context) ([]runner.RepoCandidate, error) {
	return []runner.RepoCandidate{{Name: scenarioRepo, Path: scenarioRepoPath}}, nil
}

func (r *scenarioRunner) ValidateRepo(context.Context, string, string) error {
	r.log.add("runner-repo-validated")
	return nil
}

func (r *scenarioRunner) EnsureEnvironment(_ context.Context, spec runner.RunSpec) (runner.Environment, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if env, ok := r.environments[string(spec.RunID)]; ok {
		return env, nil
	}
	env := runner.Environment{ID: "env-" + string(spec.RunID), Path: scenarioRepoPath}
	r.environments[string(spec.RunID)] = env
	r.log.add("environment:" + string(spec.RunID))
	return env, nil
}

func (r *scenarioRunner) SetEnvironmentStatus(_ context.Context, _ runner.Environment, status string) error {
	r.log.add("workspace-status:" + status)
	return nil
}

func (r *scenarioRunner) FindTerminal(_ context.Context, terminal runner.Terminal) (runner.Terminal, bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, current := range r.terminals {
		if current.terminal.ID == terminal.ID && current.live {
			return current.terminal, true, nil
		}
	}
	return runner.Terminal{}, false, nil
}
func (r *scenarioRunner) SendTerminal(context.Context, runner.Terminal, string) error { return nil }
func (r *scenarioRunner) CreateTerminal(_ context.Context, _ runner.Environment, title string, command runner.Command) (runner.Terminal, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	terminal := runner.Terminal{ID: "terminal-" + title, Title: title}
	r.terminals[title] = &scenarioTerminal{terminal: terminal, live: true}
	r.commands[title] = append(r.commands[title], command)
	r.launches[title]++
	r.log.add("terminal-created:" + title)
	return terminal, nil
}

func (r *scenarioRunner) CloseTerminal(_ context.Context, terminal runner.Terminal) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if t, ok := r.terminals[terminal.Title]; ok {
		t.live = false
	}
	r.log.add("terminal-closed:" + terminal.Title)
	return nil
}

func (r *scenarioRunner) EnsureTerminal(ctx context.Context, env runner.Environment, stored runner.Terminal, title string, command runner.Command) (runner.Terminal, error) {
	if terminal, ok, err := r.FindTerminal(ctx, stored); err != nil {
		return runner.Terminal{}, err
	} else if ok {
		return terminal, nil
	}
	return r.CreateTerminal(ctx, env, title, command)
}

func (r *scenarioRunner) CloseTerminals(context.Context, runner.RunSpec) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, terminal := range r.terminals {
		terminal.live = false
	}
	return nil
}

func (r *scenarioRunner) CleanupRun(context.Context, runner.RunSpec) error {
	r.mu.Lock()
	r.cleanups++
	r.terminals = map[string]*scenarioTerminal{}
	r.mu.Unlock()
	r.log.add("runner-cleanup")
	return nil
}

func (r *scenarioRunner) launchCount(title string) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.launches[title]
}

func (r *scenarioRunner) command(title string) runner.Command {
	r.mu.Lock()
	defer r.mu.Unlock()
	commands := r.commands[title]
	if len(commands) == 0 {
		return runner.Command{}
	}
	return commands[len(commands)-1]
}

func (r *scenarioRunner) cleanupCount() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.cleanups
}

type scenarioHarness struct {
	log *scenarioLog
	mu  sync.Mutex

	sessions map[string]harness.Session
	launches map[string][]harness.LaunchSpec
}

var _ harness.Harness = (*scenarioHarness)(nil)

func newScenarioHarness(log *scenarioLog) *scenarioHarness {
	return &scenarioHarness{log: log, sessions: map[string]harness.Session{}, launches: map[string][]harness.LaunchSpec{}}
}

func (h *scenarioHarness) ValidateAgent(_ context.Context, _, agent string) error {
	h.log.add("harness-agent-validated:" + agent)
	return nil
}

func (h *scenarioHarness) FindSession(_ context.Context, _ string, title string) (harness.Session, bool, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	session, ok := h.sessions[title]
	return session, ok, nil
}

func (h *scenarioHarness) BuildCommand(spec harness.LaunchSpec) (runner.Command, error) {
	h.mu.Lock()
	h.launches[spec.Node] = append(h.launches[spec.Node], spec)
	h.sessions[spec.Title] = harness.Session{ID: "session-" + spec.Title, Title: spec.Title}
	h.mu.Unlock()
	h.log.add("harness-launched:" + spec.Title)
	return runner.Command{
		Executable: "fake-harness",
		Args:       []string{"opaque", spec.Prompt},
		Env: map[string]string{
			"RELAY_FLOW_RUN_ID":          string(spec.RunID),
			"RELAY_FLOW_WORKFLOW":        spec.Workflow,
			"RELAY_FLOW_REPO":            spec.RepoName,
			"RELAY_FLOW_TICKET":          spec.Ticket,
			"RELAY_FLOW_NODE":            spec.Node,
			"RELAY_FLOW_NODE_TYPE":       string(spec.NodeType),
			"RELAY_FLOW_NUDGE_PROMPT":    spec.NudgePrompt,
			"RELAY_FLOW_NEXT_STEPS_JSON": routesJSON(spec.NextSteps),
		},
	}, nil
}

func (h *scenarioHarness) launch(node string) harness.LaunchSpec {
	h.mu.Lock()
	defer h.mu.Unlock()
	launches := h.launches[node]
	if len(launches) == 0 {
		return harness.LaunchSpec{}
	}
	return launches[len(launches)-1]
}

func routesJSON(routes []workflow.Route) string {
	b, _ := json.Marshal(routes)
	return string(b)
}

type scenarioRepoLookup struct{ reg *repo.Registry }

func (r scenarioRepoLookup) Exists(name string) bool {
	_, ok := r.reg.Get(name)
	return ok
}

func waitScenario(t *testing.T, timeout time.Duration, condition func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if condition() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("condition not met within " + timeout.String())
}

func eventIndex(events []string, want string) int {
	for i, event := range events {
		if event == want {
			return i
		}
	}
	return -1
}

func assertBefore(t *testing.T, events []string, first, second string) {
	t.Helper()
	a, b := eventIndex(events, first), eventIndex(events, second)
	if a < 0 || b < 0 || a >= b {
		t.Fatalf("want %q before %q; events=%v", first, second, events)
	}
}

func assertTransitionOrder(t *testing.T, events []string, current, next string) {
	t.Helper()
	order := []string{
		"report-persisted:" + current,
		"comment:TEST-1:" + current + ":summary",
		"comment:TEST-1:" + next + ":feedback",
		"complete:TEST-1:" + current,
		"transition:" + next + ":In Progress",
		"terminal-created:TEST-1:" + next,
	}
	for i := 0; i+1 < len(order); i++ {
		assertBefore(t, events, order[i], order[i+1])
	}
}

func assertMailboxDefinitions(t *testing.T, tasks *scenarioTaskSystem) {
	t.Helper()
	tasks.mu.Lock()
	defer tasks.mu.Unlock()
	for node, description := range map[string]string{
		"implement": "Implement the requested change.",
		"verify":    "Verify the implementation.",
		"pr-review": "Review and approve the pull request.",
	} {
		spec, ok := tasks.specs[node]
		if !ok {
			t.Fatalf("mailbox spec %q missing", node)
		}
		if spec.Title != "TEST-1:"+node || !strings.Contains(spec.Description, description) {
			t.Fatalf("mailbox %q = %+v, want stable title and node description", node, spec)
		}
	}
	if len(tasks.specs) != 3 {
		t.Fatalf("mailbox specs = %d, want 3 (no start/end mailbox)", len(tasks.specs))
	}
}

func assertLaunch(t *testing.T, f *scenarioFixture, node string, nodeType workflow.NodeType) {
	t.Helper()
	title := "TEST-1:" + node
	launch := f.harness.launch(node)
	if launch.Title != title || launch.NodeType != nodeType {
		t.Fatalf("launch %q = %+v", node, launch)
	}
	cmd := f.runner.command(title)
	for _, key := range []string{
		"RELAY_FLOW_RUN_ID", "RELAY_FLOW_WORKFLOW", "RELAY_FLOW_REPO",
		"RELAY_FLOW_TICKET", "RELAY_FLOW_NODE", "RELAY_FLOW_NODE_TYPE", "RELAY_FLOW_NEXT_STEPS_JSON",
	} {
		if cmd.Env[key] == "" {
			t.Errorf("%s missing from %s launch env", key, node)
		}
	}
	if _, ok := cmd.Env["RELAY_FLOW_NUDGE_PROMPT"]; !ok {
		t.Errorf("RELAY_FLOW_NUDGE_PROMPT missing from %s launch env", node)
	}
}

func assertExactHappyEffects(t *testing.T, f *scenarioFixture) {
	t.Helper()
	if got := f.tasks.totalMailboxCreates(); got != 3 {
		t.Fatalf("mailbox creates = %d, want 3", got)
	}
	if got := f.tasks.totalComments(); got != 5 {
		t.Fatalf("comments = %d, want 5", got)
	}
	for _, node := range []string{"implement", "verify", "pr-review"} {
		if got := f.tasks.commentCount(node, "summary"); got != 1 {
			t.Errorf("%s summaries = %d, want 1", node, got)
		}
		if got := f.tasks.completeCount(node); got != 1 {
			t.Errorf("%s completions = %d, want 1", node, got)
		}
	}
	if got := f.tasks.commentCount("verify", "feedback"); got != 1 {
		t.Errorf("verify feedback = %d, want 1", got)
	}
	if got := f.tasks.commentCount("pr-review", "feedback"); got != 1 {
		t.Errorf("pr-review feedback = %d, want 1", got)
	}
	if got := f.tasks.commentCount("parent", "feedback"); got != 0 {
		t.Errorf("feedback written for end = %d", got)
	}
	assertNoDuplicateTransitionCalls(t, f.tasks.transitionCalls())
}

func assertNoDuplicateTransitionCalls(t *testing.T, calls []string) {
	t.Helper()
	want := []string{
		"parent:In Progress", "implement:In Progress", "verify:In Progress",
		"pr-review:In Progress", "parent:Done",
	}
	if strings.Join(calls, "|") != strings.Join(want, "|") {
		t.Fatalf("transition calls = %v, want exactly %v", calls, want)
	}
}
