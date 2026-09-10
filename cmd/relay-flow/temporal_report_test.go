package main

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/rajpopat27/relay-flow/internal/config"
	"github.com/rajpopat27/relay-flow/internal/execution/projection"
	temporalexec "github.com/rajpopat27/relay-flow/internal/execution/temporal"
	"github.com/rajpopat27/relay-flow/internal/identity"
	"github.com/rajpopat27/relay-flow/internal/paths"
	runsvc "github.com/rajpopat27/relay-flow/internal/run"
	"github.com/rajpopat27/relay-flow/internal/runner"
	"github.com/rajpopat27/relay-flow/internal/server"
	"github.com/rajpopat27/relay-flow/internal/task"
	"github.com/rajpopat27/relay-flow/internal/workflow"
	enumspb "go.temporal.io/api/enums/v1"
	workflowservice "go.temporal.io/api/workflowservice/v1"
	"go.temporal.io/sdk/client"
	temporalworker "go.temporal.io/sdk/worker"
	temporalworkflow "go.temporal.io/sdk/workflow"
	_ "modernc.org/sqlite"
)

type temporalScenarioFixture struct {
	tasks     *scenarioTaskSystem
	runner    *scenarioRunner
	log       *scenarioLog
	path      string
	namespace string
	paths     paths.Paths
	cli       *server.Client
	ctx       context.Context
	stop      context.CancelFunc
	done      chan error
}

func newTemporalScenarioFixture(t *testing.T) *temporalScenarioFixture {
	t.Helper()
	log := newScenarioLog()
	tasks := newScenarioTaskSystem(log)
	rnr := newScenarioRunner(log)
	setScenarioFactoryAdapters(tasks, rnr, newScenarioHarness(log))
	root := filepath.Join(t.TempDir(), ".relay-flow")
	t.Setenv("RELAY_FLOW_HOME", root)
	p := pathsForRoot(root)
	namespace := fmt.Sprintf("relay-flow-report-%d", time.Now().UnixNano())
	if err := ensureTemporalNamespace(context.Background(), "localhost:7233", namespace, 30); err != nil {
		t.Fatal(err)
	}
	time.Sleep(5 * time.Second)
	if err := paths.Ensure(p); err != nil {
		t.Fatal(err)
	}
	if err := config.SaveMachine(p.Config, &config.Machine{
		PollIntervalSeconds:       1,
		CompletedRunRetentionDays: 30,
		TaskPlugin:                scenarioTaskPlugin,
		RunnerPlugin:              scenarioRunnerPlugin,
		HarnessPlugin:             scenarioHarnessPlugin,
		ExecutorPlugin:            "temporal",
		TemporalAddress:           "localhost:7233",
		TemporalNamespace:         namespace,
		Repos: map[string]config.Repo{
			scenarioRepo: {Path: scenarioRepoPath},
		},
	}); err != nil {
		t.Fatal(err)
	}
	if err := (&workflow.Store{Dir: p.Workflows}).Put(scenarioWorkflowName, scenarioWorkflowYAML); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.LoadMachine(p.Config)
	if err != nil {
		t.Fatal(err)
	}
	if err := projection.InitDatabaseWithIdentity(p.Database, projection.ExecutorIdentity{
		ExecutorPlugin: "temporal", TemporalAddress: cfg.TemporalAddress, TemporalNamespace: cfg.TemporalNamespace,
	}); err != nil {
		t.Fatal(err)
	}
	ctx, stop := context.WithCancel(context.Background())
	fixture := &temporalScenarioFixture{
		tasks: tasks, runner: rnr, log: log, path: p.Database, namespace: namespace, paths: p, cli: server.NewClient(p.Socket),
		ctx: ctx, stop: stop, done: make(chan error, 1),
	}
	go func() { fixture.done <- serveRoot(ctx, p, false) }()
	waitForServerOrError(t, fixture.cli, fixture.done)
	assertNoEmbeddedExecutionTables(t, p.Database)
	return fixture
}

func (f *temporalScenarioFixture) close(t *testing.T) {
	t.Helper()
	if err := f.cli.Stop(context.Background()); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-f.done:
		if err != nil {
			t.Fatalf("serveRoot: %v", err)
		}
	case <-time.After(15 * time.Second):
		f.stop()
		t.Fatal("Temporal serve fixture did not stop")
	}
}

func TestTemporalCompositionServesCommonWorkflowAndRunQueries(t *testing.T) {
	if os.Getenv("RELAY_FLOW_TEMPORAL_LIVE") != "1" {
		t.Skip("set RELAY_FLOW_TEMPORAL_LIVE=1 to run common Temporal service API coverage")
	}
	fixture := newTemporalScenarioFixture(t)
	defer fixture.close(t)
	waitScenario(t, 20*time.Second, func() bool {
		run, err := fixture.cli.GetRunByTicket(context.Background(), scenarioTicket)
		return err == nil && run.State == runsvc.StateWaiting && run.CurrentNode == "implement"
	})
	if repos, err := fixture.cli.ListRepos(context.Background()); err != nil || len(repos) != 1 || repos[0].Name != scenarioRepo {
		t.Fatalf("ListRepos = %#v, %v", repos, err)
	}
	if workflows, err := fixture.cli.ListWorkflows(context.Background()); err != nil || len(workflows) != 1 || workflows[0].Name != scenarioWorkflowName {
		t.Fatalf("ListWorkflows = %#v, %v", workflows, err)
	}
	if got, err := fixture.cli.GetWorkflow(context.Background(), scenarioWorkflowName); err != nil || got.Name != scenarioWorkflowName {
		t.Fatalf("GetWorkflow = %#v, %v", got, err)
	}
	if runs, err := fixture.cli.ListRuns(context.Background(), runsvc.Filter{Active: boolPointer(true)}); err != nil || len(runs) != 1 || runs[0].CurrentNode != "implement" {
		t.Fatalf("ListRuns = %#v, %v", runs, err)
	}
}

func boolPointer(value bool) *bool { return &value }

func TestTemporalReportAckSurvivesMissingProjectionReceipt(t *testing.T) {
	if os.Getenv("RELAY_FLOW_TEMPORAL_LIVE") != "1" {
		t.Skip("set RELAY_FLOW_TEMPORAL_LIVE=1 to run Temporal report delivery against the local server")
	}
	fixture := newTemporalScenarioFixture(t)
	defer fixture.close(t)
	temporalClient, err := client.Dial(client.Options{HostPort: "localhost:7233", Namespace: fixture.namespace})
	if err != nil {
		t.Fatal(err)
	}
	defer temporalClient.Close()

	var current runsvc.Run
	waitScenario(t, 20*time.Second, func() bool {
		var err error
		current, err = fixture.cli.GetRunByTicket(context.Background(), scenarioTicket)
		return err == nil && current.State == runsvc.StateWaiting && current.CurrentNode == "implement"
	})
	var beforeState temporalexec.RunStateSnapshot
	encoded, err := temporalClient.QueryWorkflow(context.Background(), string(current.ID), "", "relay-flow/run-state-v1")
	if err != nil || encoded.Get(&beforeState) != nil || beforeState.Run.State != runsvc.StateWaiting || beforeState.Run.CurrentNode != "implement" || beforeState.Run.CurrentNodeVisitID == "" {
		t.Fatalf("initial run-state query = %#v, %v", beforeState, err)
	}
	var beforeReport temporalexec.ReportStateSnapshot
	encoded, err = temporalClient.QueryWorkflow(context.Background(), string(current.ID), "", "relay-flow/report-state-v1", temporalexec.ReportStateQuery{ReportID: "temporal-report-1"})
	if err != nil || encoded.Get(&beforeReport) != nil || beforeReport.Processed || beforeReport.CurrentNodeVisitID != beforeState.Run.CurrentNodeVisitID {
		t.Fatalf("initial report-state query = %#v, %v", beforeReport, err)
	}
	report := runsvc.ReportRequest{
		RunID: current.ID, Node: current.CurrentNode, ReportID: "temporal-report-1",
		Report: scenarioReport(workflow.OutcomeSuccess, "verify"),
	}
	ack, err := fixture.cli.SubmitReport(context.Background(), report)
	if err != nil || !ack.Accepted || ack.Duplicate {
		t.Fatalf("first Temporal report ack = %+v, err %v", ack, err)
	}
	waitScenario(t, 20*time.Second, func() bool {
		r, err := fixture.cli.GetRunByTicket(context.Background(), scenarioTicket)
		return err == nil && r.CurrentNode == "verify" && r.State == runsvc.StateWaiting
	})
	var afterReport temporalexec.ReportStateSnapshot
	encoded, err = temporalClient.QueryWorkflow(context.Background(), string(current.ID), "", "relay-flow/report-state-v1", temporalexec.ReportStateQuery{ReportID: report.ReportID})
	if err != nil || encoded.Get(&afterReport) != nil || !afterReport.Processed || afterReport.CurrentNode != "verify" || afterReport.State != runsvc.StateWaiting {
		t.Fatalf("consumed report-state query = %#v, %v", afterReport, err)
	}

	db, err := sql.Open("sqlite", fixture.path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`DELETE FROM relay_processed_reports WHERE run_id = ? AND report_id = ?`, string(report.RunID), report.ReportID); err != nil {
		db.Close()
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	duplicate, err := fixture.cli.SubmitReport(context.Background(), report)
	if err != nil || !duplicate.Accepted || !duplicate.Duplicate {
		t.Fatalf("replayed report without projection receipt = %+v, err %v", duplicate, err)
	}
	if got := fixture.tasks.commentCount("implement", "summary"); got != 1 {
		t.Fatalf("Temporal duplicate repeated summary effects: %d", got)
	}
	if got := fixture.tasks.commentCount("verify", "feedback"); got != 1 {
		t.Fatalf("Temporal duplicate repeated feedback effects: %d", got)
	}
}

func TestTemporalReportAndCancelRaceIsReconciled(t *testing.T) {
	if os.Getenv("RELAY_FLOW_TEMPORAL_LIVE") != "1" {
		t.Skip("set RELAY_FLOW_TEMPORAL_LIVE=1 to run the Temporal report/cancel race against the local server")
	}
	fixture := newTemporalScenarioFixture(t)
	defer fixture.close(t)
	var current runsvc.Run
	waitScenario(t, 20*time.Second, func() bool {
		var err error
		current, err = fixture.cli.GetRunByTicket(context.Background(), scenarioTicket)
		return err == nil && current.State == runsvc.StateWaiting && current.CurrentNode == "implement"
	})
	report := runsvc.ReportRequest{RunID: current.ID, Node: current.CurrentNode, ReportID: "race-report", Report: scenarioReport(workflow.OutcomeSuccess, "verify")}
	results := make(chan error, 2)
	go func() {
		_, err := fixture.cli.SubmitReport(context.Background(), report)
		results <- err
	}()
	go func() { results <- fixture.cli.CancelRun(context.Background(), scenarioTicket, "race cancellation") }()
	for i := 0; i < 2; i++ {
		if err := <-results; err != nil {
			t.Fatalf("report/cancel race returned error: %v", err)
		}
	}
	waitScenario(t, 30*time.Second, func() bool {
		run, err := fixture.cli.GetRunByTicket(context.Background(), scenarioTicket)
		return err == nil && (run.State == runsvc.StateCanceled || run.State == runsvc.StateCompleted)
	})
}

func TestTemporalCancellationClosesTerminalsAndPreservesWorkspace(t *testing.T) {
	if os.Getenv("RELAY_FLOW_TEMPORAL_LIVE") != "1" {
		t.Skip("set RELAY_FLOW_TEMPORAL_LIVE=1 to run Temporal cancellation against the local server")
	}
	fixture := newTemporalScenarioFixture(t)
	defer fixture.close(t)
	waitScenario(t, 20*time.Second, func() bool {
		run, err := fixture.cli.GetRunByTicket(context.Background(), scenarioTicket)
		return err == nil && run.State == runsvc.StateWaiting && run.CurrentNode == "implement"
	})
	if err := fixture.cli.CancelRun(context.Background(), scenarioTicket, "cancellation test"); err != nil {
		t.Fatal(err)
	}
	waitScenario(t, 30*time.Second, func() bool {
		run, err := fixture.cli.GetRunByTicket(context.Background(), scenarioTicket)
		return err == nil && run.State == runsvc.StateCanceled
	})
	runnerState := snapshotScenarioRunnerState(fixture.runner)
	if len(runnerState.environments) != 1 {
		t.Fatalf("cancellation removed workspace: %+v", runnerState)
	}
	for title, terminal := range runnerState.terminals {
		if terminal.live {
			t.Fatalf("cancellation left terminal %q live", title)
		}
	}
	if got := fixture.tasks.commentCount("parent", "cancellation"); got != 1 {
		t.Fatalf("cancellation comments = %d, want one", got)
	}
	fixture.tasks.mu.Lock()
	var cancellationBody string
	for _, comment := range fixture.tasks.comments {
		if comment.kind == "cancellation" {
			cancellationBody = comment.body
		}
	}
	fixture.tasks.mu.Unlock()
	if cancellationBody != "Run canceled: cancellation test" {
		t.Fatalf("cancellation reason = %q", cancellationBody)
	}
}

func TestTemporalRestartFencesStaleReport(t *testing.T) {
	if os.Getenv("RELAY_FLOW_TEMPORAL_LIVE") != "1" {
		t.Skip("set RELAY_FLOW_TEMPORAL_LIVE=1 to run Temporal restart fencing against the local server")
	}
	fixture := newTemporalScenarioFixture(t)
	defer fixture.close(t)
	var oldRun runsvc.Run
	waitScenario(t, 20*time.Second, func() bool {
		var err error
		oldRun, err = fixture.cli.GetRunByTicket(context.Background(), scenarioTicket)
		return err == nil && oldRun.State == runsvc.StateWaiting && oldRun.CurrentNode == "implement"
	})
	if err := fixture.cli.CancelRun(context.Background(), scenarioTicket, "restart fencing"); err != nil {
		t.Fatal(err)
	}
	waitScenario(t, 20*time.Second, func() bool {
		run, err := fixture.cli.GetRunByTicket(context.Background(), scenarioTicket)
		return err == nil && run.ID == oldRun.ID && run.State == runsvc.StateCanceled
	})
	newRun, err := fixture.cli.RestartRun(context.Background(), scenarioTicket)
	if err != nil {
		t.Fatal(err)
	}
	if newRun.ID == oldRun.ID || newRun.AttemptID <= oldRun.AttemptID {
		t.Fatalf("restart reused old attempt: old=%+v new=%+v", oldRun, newRun)
	}
	waitScenario(t, 20*time.Second, func() bool {
		run, err := fixture.cli.GetRunByTicket(context.Background(), scenarioTicket)
		return err == nil && run.ID == newRun.ID && run.State == runsvc.StateWaiting && run.CurrentNode == "implement"
	})
	stale, err := fixture.cli.SubmitReport(context.Background(), runsvc.ReportRequest{
		RunID: oldRun.ID, Node: oldRun.CurrentNode, ReportID: "stale-attempt-report",
		Report: scenarioReport(workflow.OutcomeSuccess, "verify"),
	})
	if err != nil || !stale.Accepted || !stale.Duplicate {
		t.Fatalf("stale report ack = %+v, err %v", stale, err)
	}
	current, err := fixture.cli.GetRunByTicket(context.Background(), scenarioTicket)
	if err != nil {
		t.Fatal(err)
	}
	if current.ID != newRun.ID || current.CurrentNode != "implement" || current.State != runsvc.StateWaiting {
		t.Fatalf("stale report affected new attempt: %+v", current)
	}
	staleClient, err := client.Dial(client.Options{HostPort: "localhost:7233", Namespace: fixture.namespace})
	if err != nil {
		t.Fatal(err)
	}
	// A reconcile request addressed to the old, canceled execution is allowed
	// to be rejected by Temporal, but must never be redirected to the new ID.
	_ = staleClient.SignalWorkflow(context.Background(), string(oldRun.ID), "", "reconcile", struct{}{})
	staleClient.Close()
	current, err = fixture.cli.GetRunByTicket(context.Background(), scenarioTicket)
	if err != nil {
		t.Fatal(err)
	}
	if current.ID != newRun.ID || current.CurrentNode != "implement" || current.State != runsvc.StateWaiting {
		t.Fatalf("stale reconcile affected new attempt: %+v", current)
	}
}

func TestTemporalProjectionRecoveryLeavesMailboxesUntouched(t *testing.T) {
	if os.Getenv("RELAY_FLOW_TEMPORAL_LIVE") != "1" {
		t.Skip("set RELAY_FLOW_TEMPORAL_LIVE=1 to run Temporal recovery against the local server")
	}
	fixture := newTemporalScenarioFixture(t)
	var current runsvc.Run
	waitScenario(t, 20*time.Second, func() bool {
		var err error
		current, err = fixture.cli.GetRunByTicket(context.Background(), scenarioTicket)
		return err == nil && current.State == runsvc.StateWaiting && current.CurrentNode == "implement"
	})
	before := snapshotScenarioTaskState(fixture.tasks)
	beforeRunner := snapshotScenarioRunnerState(fixture.runner)
	fixture.close(t)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- serveRoot(ctx, fixture.paths, true) }()
	recoveryClient := server.NewClient(fixture.paths.Socket)
	waitForServerOrError(t, recoveryClient, done)
	assertNoEmbeddedExecutionTables(t, fixture.paths.Database)
	backups, err := filepath.Glob(fixture.paths.Database + ".recover-*.bak")
	if err != nil || len(backups) == 0 {
		t.Fatalf("Temporal recovery backup files = %v, err %v", backups, err)
	}
	after := snapshotScenarioTaskState(fixture.tasks)
	afterRunner := snapshotScenarioRunnerState(fixture.runner)
	if !reflect.DeepEqual(before, after) {
		t.Fatalf("Temporal projection recovery mutated task-system state: before=%+v after=%+v", before, after)
	}
	if !reflect.DeepEqual(beforeRunner, afterRunner) {
		t.Fatalf("Temporal projection recovery mutated runner state: before=%+v after=%+v", beforeRunner, afterRunner)
	}
	if err := recoveryClient.Stop(context.Background()); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("recovery serveRoot: %v", err)
		}
	case <-time.After(15 * time.Second):
		t.Fatal("recovery serveRoot did not stop")
	}
}

func TestTemporalRecoveryRestoresOpenAndClosedExecutions(t *testing.T) {
	if os.Getenv("RELAY_FLOW_TEMPORAL_LIVE") != "1" {
		t.Skip("set RELAY_FLOW_TEMPORAL_LIVE=1 to run Temporal projection rebuild against the local server")
	}
	fixture := newTemporalScenarioFixture(t)
	var open runsvc.Run
	waitScenario(t, 20*time.Second, func() bool {
		var err error
		open, err = fixture.cli.GetRunByTicket(context.Background(), scenarioTicket)
		return err == nil && open.State == runsvc.StateWaiting && open.CurrentNode == "implement"
	})

	// Start a second execution directly through the public SDK. It is not
	// returned by the task-system poller, so recovery must discover it only
	// through Temporal Visibility and restore its closed projection row.
	temporalClient, err := client.Dial(client.Options{HostPort: "localhost:7233", Namespace: fixture.namespace})
	if err != nil {
		t.Fatal(err)
	}
	defer temporalClient.Close()
	wf, err := workflow.Parse(scenarioWorkflowName, scenarioWorkflowYAML)
	if err != nil {
		t.Fatal(err)
	}
	closedStart := runsvc.Start{
		ID:   identity.NewRunID(scenarioRepo, scenarioWorkflowName, "CLOSED-1"),
		Repo: scenarioRepo, RepoPath: scenarioRepoPath, Workflow: *wf,
		Ticket:  task.TicketRef{ID: "CLOSED-1", Key: "CLOSED-1", Title: "Closed ticket"},
		Runtime: runsvc.RuntimePolicy{KeepTerminalsAlive: true, KeepSessionsAlive: true},
	}
	closedHandle, err := temporalClient.ExecuteWorkflow(context.Background(), client.StartWorkflowOptions{
		ID: string(closedStart.ID), TaskQueue: temporalexec.TaskQueue,
		WorkflowIDReusePolicy:                    enumspb.WORKFLOW_ID_REUSE_POLICY_ALLOW_DUPLICATE_FAILED_ONLY,
		WorkflowExecutionErrorWhenAlreadyStarted: true,
	}, temporalexec.TicketWorkflow, closedStart)
	if err != nil {
		t.Fatal(err)
	}
	completeTemporalRun(t, temporalClient, closedHandle, closedStart.ID)
	unrelatedWorker := temporalworker.New(temporalClient, "unrelated-task-queue", temporalworker.Options{})
	unrelatedWorker.RegisterWorkflow(unrelatedTemporalWorkflow)
	if err := unrelatedWorker.Start(); err != nil {
		t.Fatal(err)
	}
	unrelated, err := temporalClient.ExecuteWorkflow(context.Background(), client.StartWorkflowOptions{
		ID: "unrelated-workflow", TaskQueue: "unrelated-task-queue",
	}, unrelatedTemporalWorkflow)
	if err != nil {
		unrelatedWorker.Stop()
		t.Fatal(err)
	}
	if err := unrelated.Get(context.Background(), nil); err != nil {
		unrelatedWorker.Stop()
		t.Fatal(err)
	}
	unrelatedWorker.Stop()
	beforeExecutions := listTemporalExecutions(t, temporalClient)
	before := snapshotScenarioTaskState(fixture.tasks)
	fixture.close(t)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- serveRoot(ctx, fixture.paths, true) }()
	recoveryClient := server.NewClient(fixture.paths.Socket)
	waitForServerOrError(t, recoveryClient, done)
	defer func() {
		_ = recoveryClient.Stop(context.Background())
		<-done
	}()

	runs, err := recoveryClient.ListRuns(context.Background(), runsvc.Filter{})
	if err != nil {
		t.Fatal(err)
	}
	var foundOpen, foundClosed bool
	for _, recovered := range runs {
		switch recovered.ID {
		case open.ID:
			foundOpen = recovered.State == runsvc.StateWaiting && recovered.CurrentNode == "implement" && recovered.Ticket.Key == scenarioTicket
		case closedStart.ID:
			foundClosed = recovered.State == runsvc.StateCompleted && recovered.Ticket.Key == "CLOSED-1" && recovered.Workflow == scenarioWorkflowName
		}
	}
	if !foundOpen || !foundClosed {
		t.Fatalf("recovered runs = %#v, open=%v closed=%v", runs, foundOpen, foundClosed)
	}
	assertNoEmbeddedExecutionTables(t, fixture.paths.Database)
	afterExecutions := listTemporalExecutions(t, temporalClient)
	if !reflect.DeepEqual(beforeExecutions, afterExecutions) {
		t.Fatalf("Temporal recovery changed executions: before=%v after=%v", beforeExecutions, afterExecutions)
	}
	db, err := sql.Open("sqlite", fixture.paths.Database)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	for _, table := range []string{"relay_processed_reports", "relay_node_sessions"} {
		var count int
		if err := db.QueryRow("SELECT COUNT(*) FROM " + table).Scan(&count); err != nil {
			t.Fatal(err)
		}
		if count != 0 {
			t.Fatalf("recovery restored derived cache %s with %d rows", table, count)
		}
	}
	var runtimeCount int
	if err := db.QueryRow(`SELECT COUNT(*) FROM relay_node_runtime WHERE run_id = ?`, string(open.ID)).Scan(&runtimeCount); err != nil {
		t.Fatal(err)
	}
	if runtimeCount == 0 {
		t.Fatal("recovery did not restore the open run's runtime binding")
	}
	after := snapshotScenarioTaskState(fixture.tasks)
	if before.parentStatus != after.parentStatus || before.mailboxCreates != after.mailboxCreates || before.comments != after.comments || !reflect.DeepEqual(before.transitions, after.transitions) {
		t.Fatalf("Temporal rebuild mutated task-system state: before=%+v after=%+v", before, after)
	}
}

type temporalRunSignal struct {
	ReportID    string             `json:"reportId"`
	Node        string             `json:"node"`
	NodeVisitID runsvc.NodeVisitID `json:"nodeVisitId"`
	Report      workflow.Report    `json:"report"`
}

func TestTemporalRecoveryRejectsUnreadableActiveHistory(t *testing.T) {
	if os.Getenv("RELAY_FLOW_TEMPORAL_LIVE") != "1" {
		t.Skip("set RELAY_FLOW_TEMPORAL_LIVE=1 to run unreadable Temporal history recovery against the local server")
	}
	fixture := newTemporalScenarioFixture(t)
	temporalClient, err := client.Dial(client.Options{HostPort: "localhost:7233", Namespace: fixture.namespace})
	if err != nil {
		t.Fatal(err)
	}
	malformedID := "malformed-active-history"
	_, err = temporalClient.ExecuteWorkflow(context.Background(), client.StartWorkflowOptions{
		ID: malformedID, TaskQueue: temporalexec.TaskQueue,
	}, temporalexec.TicketWorkflowName, "not-a-run-start")
	if err != nil {
		temporalClient.Close()
		fixture.close(t)
		t.Fatal(err)
	}
	beforeTasks := snapshotScenarioTaskState(fixture.tasks)
	fixture.close(t)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- serveRoot(ctx, fixture.paths, true) }()
	deadline := time.NewTimer(20 * time.Second)
	defer deadline.Stop()
	var recoveryErr error
	select {
	case recoveryErr = <-done:
		if recoveryErr == nil || (!strings.Contains(recoveryErr.Error(), "decode") && !strings.Contains(recoveryErr.Error(), "snapshot")) {
			t.Fatalf("unreadable history recovery error = %v", recoveryErr)
		}
	case <-deadline.C:
		t.Fatal("recovery unexpectedly started after unreadable active history")
	}
	if _, err := os.Stat(fixture.paths.Socket); !os.IsNotExist(err) {
		t.Fatalf("recovery left a listening socket after unreadable history: %v", err)
	}
	afterTasks := snapshotScenarioTaskState(fixture.tasks)
	if !reflect.DeepEqual(beforeTasks, afterTasks) {
		t.Fatalf("unreadable history recovery mutated task-system state: before=%+v after=%+v", beforeTasks, afterTasks)
	}
	_ = temporalClient.CancelWorkflow(context.Background(), malformedID, "")
	temporalClient.Close()
}

func TestTemporalRecoveryMissingClaimDoesNotStartReplacement(t *testing.T) {
	if os.Getenv("RELAY_FLOW_TEMPORAL_LIVE") != "1" {
		t.Skip("set RELAY_FLOW_TEMPORAL_LIVE=1 to run Temporal missing-claim recovery against the local server")
	}
	fixture := newTemporalScenarioFixture(t)
	waitScenario(t, 20*time.Second, func() bool {
		run, err := fixture.cli.GetRunByTicket(context.Background(), scenarioTicket)
		return err == nil && run.State == runsvc.StateWaiting && run.CurrentNode == "implement"
	})
	temporalClient, err := client.Dial(client.Options{HostPort: "localhost:7233", Namespace: fixture.namespace})
	if err != nil {
		t.Fatal(err)
	}
	defer temporalClient.Close()
	beforeExecutions := listTemporalExecutions(t, temporalClient)
	beforeTasks := snapshotScenarioTaskState(fixture.tasks)
	fixture.close(t)
	fixture.tasks.setRecoveryTickets([]task.Ticket{{
		ID: "missing-claim", Key: "MISSING-CLAIM", Title: "Missing claim",
		WorkflowClaims: []string{"wf:" + scenarioWorkflowName},
	}}, 2)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- serveRoot(ctx, fixture.paths, true) }()
	recoveryClient := server.NewClient(fixture.paths.Socket)
	waitForServerOrError(t, recoveryClient, done)
	defer func() {
		_ = recoveryClient.Stop(context.Background())
		<-done
	}()
	if got, err := recoveryClient.GetRunByTicket(context.Background(), "MISSING-CLAIM"); err == nil || got.ID != "" {
		t.Fatalf("missing claimed ticket acquired a recovery run: %+v, err=%v", got, err)
	}
	afterExecutions := listTemporalExecutions(t, temporalClient)
	if !reflect.DeepEqual(beforeExecutions, afterExecutions) {
		t.Fatalf("missing-claim recovery changed Temporal executions: before=%v after=%v", beforeExecutions, afterExecutions)
	}
	afterTasks := snapshotScenarioTaskState(fixture.tasks)
	if !reflect.DeepEqual(beforeTasks, afterTasks) {
		t.Fatalf("missing-claim recovery mutated task-system state: before=%+v after=%+v", beforeTasks, afterTasks)
	}
}

func listTemporalExecutions(t *testing.T, c client.Client) map[string]string {
	t.Helper()
	out := map[string]string{}
	var token []byte
	for {
		response, err := c.ListWorkflow(context.Background(), &workflowservice.ListWorkflowExecutionsRequest{
			PageSize: 100, Query: "WorkflowType = 'TicketWorkflow' AND TaskQueue = 'relay-flow'", NextPageToken: token,
		})
		if err != nil {
			t.Fatal(err)
		}
		for _, info := range response.Executions {
			if info != nil && info.Execution != nil {
				out[info.Execution.WorkflowId] = info.Execution.RunId
			}
		}
		if len(response.NextPageToken) == 0 {
			return out
		}
		token = response.NextPageToken
	}
}

func completeTemporalRun(t *testing.T, c client.Client, handle client.WorkflowRun, id runsvc.ID) {
	t.Helper()
	steps := []struct {
		node string
		next string
	}{
		{node: "implement", next: "verify"},
		{node: "verify", next: "pr-review"},
		{node: "pr-review", next: "end"},
	}
	for i, step := range steps {
		var state temporalexec.RunStateSnapshot
		waitScenario(t, 20*time.Second, func() bool {
			encoded, err := c.QueryWorkflow(context.Background(), string(id), handle.GetRunID(), "relay-flow/run-state-v1")
			if err != nil || encoded.Get(&state) != nil {
				return false
			}
			return state.Run.State == runsvc.StateWaiting && state.Run.CurrentNode == step.node && state.Run.CurrentNodeVisitID != ""
		})
		report := scenarioReport(workflow.OutcomeSuccess, step.next)
		if err := c.SignalWorkflow(context.Background(), string(id), handle.GetRunID(), "report", temporalRunSignal{
			ReportID: fmt.Sprintf("recovery-report-%d", i), Node: step.node,
			NodeVisitID: state.Run.CurrentNodeVisitID, Report: report,
		}); err != nil {
			t.Fatal(err)
		}
	}
	if err := handle.Get(context.Background(), nil); err != nil {
		t.Fatalf("closed Temporal workflow: %v", err)
	}
}

func unrelatedTemporalWorkflow(temporalworkflow.Context) error { return nil }

type scenarioTaskSnapshot struct {
	parentStatus    string
	mailboxCreates  int
	comments        int
	transitions     []string
	mailboxes       map[string]task.Mailbox
	mailboxStatus   map[string]string
	commentBodies   map[string]scenarioComment
	createCounts    map[string]int
	completionCount map[string]int
}

func snapshotScenarioTaskState(tasks *scenarioTaskSystem) scenarioTaskSnapshot {
	tasks.mu.Lock()
	defer tasks.mu.Unlock()
	mailboxes := make(map[string]task.Mailbox, len(tasks.mailboxes))
	for node, mailbox := range tasks.mailboxes {
		mailboxes[node] = mailbox
	}
	mailboxStatus := make(map[string]string, len(tasks.mailboxStatus))
	for node, status := range tasks.mailboxStatus {
		mailboxStatus[node] = status
	}
	commentBodies := make(map[string]scenarioComment, len(tasks.comments))
	for marker, comment := range tasks.comments {
		commentBodies[marker] = comment
	}
	createCounts := make(map[string]int, len(tasks.creates))
	for node, count := range tasks.creates {
		createCounts[node] = count
	}
	completionCount := make(map[string]int, len(tasks.completions))
	for node, count := range tasks.completions {
		completionCount[node] = count
	}
	return scenarioTaskSnapshot{
		parentStatus: tasks.parentStatus, mailboxCreates: len(tasks.creates), comments: len(tasks.comments),
		transitions: append([]string(nil), tasks.transitions...), mailboxes: mailboxes, mailboxStatus: mailboxStatus,
		commentBodies: commentBodies, createCounts: createCounts, completionCount: completionCount,
	}
}

type scenarioRunnerSnapshot struct {
	environments map[string]runner.Environment
	terminals    map[string]scenarioTerminal
	launches     map[string]int
	cleanups     int
}

func snapshotScenarioRunnerState(r *scenarioRunner) scenarioRunnerSnapshot {
	r.mu.Lock()
	defer r.mu.Unlock()
	environments := make(map[string]runner.Environment, len(r.environments))
	for id, env := range r.environments {
		environments[id] = env
	}
	terminals := make(map[string]scenarioTerminal, len(r.terminals))
	for title, term := range r.terminals {
		if term != nil {
			terminals[title] = *term
		}
	}
	launches := make(map[string]int, len(r.launches))
	for title, count := range r.launches {
		launches[title] = count
	}
	return scenarioRunnerSnapshot{environments: environments, terminals: terminals, launches: launches, cleanups: r.cleanups}
}
