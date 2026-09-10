package temporal

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/rajpopat27/relay-flow/internal/retry"
	"github.com/rajpopat27/relay-flow/internal/run"
	"github.com/rajpopat27/relay-flow/internal/task"
	domainworkflow "github.com/rajpopat27/relay-flow/internal/workflow"
	enumspb "go.temporal.io/api/enums/v1"
	historypb "go.temporal.io/api/history/v1"
	"go.temporal.io/api/serviceerror"
	workflowservice "go.temporal.io/api/workflowservice/v1"
	"go.temporal.io/sdk/activity"
	"go.temporal.io/sdk/client"
	"go.temporal.io/sdk/converter"
	temporalSDK "go.temporal.io/sdk/temporal"
	"go.temporal.io/sdk/testsuite"
	temporalworker "go.temporal.io/sdk/worker"
	temporalworkflow "go.temporal.io/sdk/workflow"
	"google.golang.org/protobuf/types/known/durationpb"
)

const (
	spikeTaskQueue            = "relay-flow"
	spikeWorkflowType         = "TemporalCompatibilityWorkflow"
	spikeReportSignalName     = "report"
	spikeReconcileSignalName  = "reconcile"
	spikeFinishSignalName     = "finish"
	spikeRunStateQuery        = "relay-flow/run-state-v1"
	spikeReportStateQueryName = "relay-flow/report-state-v1"
	spikeProgressQueryName    = "compatibility/progress-v1"
	spikeRetention            = 30 * 24 * time.Hour
)

// spikeInput deliberately contains only the immutable run snapshot and scalar
// control values. It must remain safe to serialize into Temporal history.
type spikeInput struct {
	Snapshot run.Start `json:"snapshot"`
	Mode     string    `json:"mode"`
}

type spikeReportSignal struct {
	ReportID    string `json:"reportId"`
	Node        string `json:"node"`
	NodeVisitID string `json:"nodeVisitId"`
}

type spikeReconcileSignal struct {
	Reason string `json:"reason"`
}

type spikeNodeRuntimeBinding struct {
	Node        string `json:"node"`
	TerminalID  string `json:"terminalId"`
	SessionID   string `json:"sessionId"`
	NodeVisitID string `json:"nodeVisitId"`
}

type spikeRunStateSnapshot struct {
	Run             run.Run                   `json:"run"`
	RuntimeBindings []spikeNodeRuntimeBinding `json:"runtimeBindings"`
}

type spikeReportStateQuery struct {
	ReportID string `json:"reportId"`
}

type spikeReportStateSnapshot struct {
	CurrentNode        string    `json:"currentNode"`
	CurrentNodeVisitID string    `json:"currentNodeVisitId"`
	State              run.State `json:"state"`
	Processed          bool      `json:"processed"`
}

// spikeProgressSnapshot is test-only observability. The two fixed query
// contracts above intentionally do not expose unbounded report history or
// implementation counters.
type spikeProgressSnapshot struct {
	ReconcileCount int `json:"reconcileCount"`
	DuplicateCount int `json:"duplicateCount"`
	IgnoredCount   int `json:"ignoredCount"`
}

type spikeResult struct {
	WorkflowID      string    `json:"workflowId"`
	RunID           string    `json:"runId"`
	VisitID         string    `json:"visitId"`
	ReportID        string    `json:"reportId"`
	State           run.State `json:"state"`
	ReconcileCount  int       `json:"reconcileCount"`
	DuplicateCount  int       `json:"duplicateCount"`
	IgnoredCount    int       `json:"ignoredCount"`
	ActivityResult  string    `json:"activityResult"`
	ActivityErrType string    `json:"activityErrType"`
}

type spikeActivityResult struct {
	Value string `json:"value"`
}

type spikeActivities struct {
	mu       sync.Mutex
	attempts map[string]int
	cleanup  atomic.Int32
}

func (a *spikeActivities) Record(ctx context.Context, input spikeInput) (spikeActivityResult, error) {
	a.mu.Lock()
	if a.attempts == nil {
		a.attempts = make(map[string]int)
	}
	a.attempts[input.Snapshot.Ticket.Key]++
	a.mu.Unlock()

	switch input.Mode {
	case "failure":
		failure := retry.Classify(errors.New("compatibility transient failure"))
		return spikeActivityResult{}, temporalSDK.NewApplicationError("compatibility failure", string(failure.Kind))
	case "conflict":
		failure := retry.Classify(retry.ConflictError(errors.New("compatibility conflict")))
		return spikeActivityResult{}, temporalSDK.NewApplicationError("compatibility conflict", string(failure.Kind))
	case "cancel-activity":
		ticker := time.NewTicker(50 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return spikeActivityResult{}, ctx.Err()
			case <-ticker.C:
				activity.RecordHeartbeat(ctx, "compatibility activity is still running")
				if err := ctx.Err(); err != nil {
					return spikeActivityResult{}, err
				}
			}
		}
	default:
		return spikeActivityResult{Value: "activity-ok"}, nil
	}
}

func (a *spikeActivities) Cleanup(context.Context, spikeInput) error {
	a.cleanup.Add(1)
	return nil
}

func (a *spikeActivities) attemptsFor(ticket string) int {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.attempts[ticket]
}

func spikeActivityOptions() temporalworkflow.ActivityOptions {
	return temporalworkflow.ActivityOptions{
		StartToCloseTimeout: 5 * time.Minute,
		WaitForCancellation: true,
		RetryPolicy:         &temporalSDK.RetryPolicy{MaximumAttempts: 1},
	}
}

// spikeWorkflow intentionally exercises the SDK primitives that the
// production interpreter will use: package-level workflow registration,
// replay-safe side effects, durable timers, selectors, signals, queries,
// activity futures, disconnected cleanup, and Temporal time.
func spikeWorkflow(ctx temporalworkflow.Context, input spikeInput) (spikeResult, error) {
	info := temporalworkflow.GetInfo(ctx)
	consumed := make(map[string]bool)
	state := spikeRunStateSnapshot{
		Run: run.Run{
			ID:        input.Snapshot.ID,
			LogicalID: input.Snapshot.LogicalID,
			AttemptID: input.Snapshot.AttemptID,
			Repo:      input.Snapshot.Repo,
			Workflow:  input.Snapshot.Workflow.Name,
			Ticket:    input.Snapshot.Ticket,
			State:     run.StateStarting,
			StartedAt: info.WorkflowStartTime,
			UpdatedAt: temporalworkflow.Now(ctx),
		},
	}
	state.Run.CurrentNode = "compatibility"
	state.Run.CurrentNodeVisitID = ""
	state.RuntimeBindings = []spikeNodeRuntimeBinding{{
		Node:        "compatibility",
		TerminalID:  "terminal-1",
		SessionID:   "session-1",
		NodeVisitID: "",
	}}

	if err := temporalworkflow.SetQueryHandler(ctx, spikeRunStateQuery, func() (spikeRunStateSnapshot, error) {
		return state, nil
	}); err != nil {
		return spikeResult{}, err
	}
	reconcileCount := 0
	duplicateCount := 0
	ignoredCount := 0
	if err := temporalworkflow.SetQueryHandler(ctx, spikeReportStateQueryName, func(query spikeReportStateQuery) (spikeReportStateSnapshot, error) {
		return spikeReportStateSnapshot{
			CurrentNode:        state.Run.CurrentNode,
			CurrentNodeVisitID: string(state.Run.CurrentNodeVisitID),
			State:              state.Run.State,
			Processed:          consumed[query.ReportID],
		}, nil
	}); err != nil {
		return spikeResult{}, err
	}
	if err := temporalworkflow.SetQueryHandler(ctx, spikeProgressQueryName, func() (spikeProgressSnapshot, error) {
		return spikeProgressSnapshot{
			ReconcileCount: reconcileCount,
			DuplicateCount: duplicateCount,
			IgnoredCount:   ignoredCount,
		}, nil
	}); err != nil {
		return spikeResult{}, err
	}

	var visitID string
	if err := temporalworkflow.SideEffect(ctx, func(temporalworkflow.Context) interface{} {
		return "visit-" + uuid.NewString()
	}).Get(&visitID); err != nil {
		return spikeResult{}, err
	}
	state.Run.CurrentNodeVisitID = run.NodeVisitID(visitID)
	state.RuntimeBindings[0].NodeVisitID = visitID
	state.Run.State = run.StateRunning
	state.Run.UpdatedAt = temporalworkflow.Now(ctx)

	activityCtx := temporalworkflow.WithActivityOptions(ctx, spikeActivityOptions())
	state.Run.State = run.StateRunning
	var activityResult spikeActivityResult
	if err := temporalworkflow.ExecuteActivity(activityCtx, "Record", input).Get(activityCtx, &activityResult); err != nil {
		state.Run.LastError = err.Error()
		var applicationErr *temporalSDK.ApplicationError
		if errors.As(err, &applicationErr) {
			stateResult := spikeResult{
				WorkflowID:      info.WorkflowExecution.ID,
				RunID:           info.WorkflowExecution.RunID,
				VisitID:         visitID,
				State:           run.State("failed"),
				ActivityErrType: applicationErr.Type(),
			}
			state.Run.State = run.State("failed")
			return stateResult, err
		}
		if temporalSDK.IsCanceledError(err) || temporalSDK.IsCanceledError(ctx.Err()) {
			state.Run.State = run.StateCanceling
			cleanupCtx, cancel := temporalworkflow.NewDisconnectedContext(ctx)
			defer cancel()
			state.Run.State = run.StateCanceling
			if cleanupErr := temporalworkflow.ExecuteActivity(
				temporalworkflow.WithActivityOptions(cleanupCtx, spikeActivityOptions()),
				"Cleanup", input,
			).Get(cleanupCtx, nil); cleanupErr != nil {
				state.Run.LastError = cleanupErr.Error()
				return spikeResult{}, cleanupErr
			}
			state.Run.State = run.StateCanceled
			return spikeResult{
				WorkflowID: info.WorkflowExecution.ID,
				RunID:      info.WorkflowExecution.RunID,
				VisitID:    visitID,
				State:      run.StateCanceled,
			}, temporalSDK.NewCanceledError()
		}
		state.Run.State = run.State("failed")
		return spikeResult{
			WorkflowID: info.WorkflowExecution.ID,
			RunID:      info.WorkflowExecution.RunID,
			VisitID:    visitID,
			State:      run.State("failed"),
		}, err
	}
	state.Run.UpdatedAt = temporalworkflow.Now(ctx)

	if input.Mode == "complete" {
		state.Run.State = run.StateCompleted
		return spikeResult{
			WorkflowID:     info.WorkflowExecution.ID,
			RunID:          info.WorkflowExecution.RunID,
			VisitID:        visitID,
			State:          run.StateCompleted,
			ActivityResult: activityResult.Value,
		}, nil
	}

	state.Run.State = run.StateWaiting
	timer := temporalworkflow.NewTimer(ctx, time.Hour)
	reportCh := temporalworkflow.GetSignalChannel(ctx, spikeReportSignalName)
	reconcileCh := temporalworkflow.GetSignalChannel(ctx, spikeReconcileSignalName)
	finishCh := temporalworkflow.GetSignalChannel(ctx, spikeFinishSignalName)
	var latestReport string

	for {
		selector := temporalworkflow.NewSelector(ctx)
		selector.AddReceive(reportCh, func(channel temporalworkflow.ReceiveChannel, more bool) {
			if !more {
				return
			}
			var report spikeReportSignal
			channel.Receive(ctx, &report)
			if report.Node != state.Run.CurrentNode || report.NodeVisitID != visitID {
				ignoredCount++
				return
			}
			if consumed[report.ReportID] {
				duplicateCount++
				return
			}
			consumed[report.ReportID] = true
			latestReport = report.ReportID
			state.Run.State = run.State("reported")
		})
		selector.AddReceive(reconcileCh, func(channel temporalworkflow.ReceiveChannel, more bool) {
			if !more {
				return
			}
			var signal spikeReconcileSignal
			channel.Receive(ctx, &signal)
			reconcileCount++
		})
		selector.AddReceive(finishCh, func(channel temporalworkflow.ReceiveChannel, more bool) {
			if !more {
				return
			}
			channel.Receive(ctx, nil)
			state.Run.State = run.StateCompleted
		})
		selector.AddReceive(ctx.Done(), func(channel temporalworkflow.ReceiveChannel, more bool) {
			if more {
				channel.Receive(ctx, nil)
			}
		})
		selector.AddFuture(timer, func(temporalworkflow.Future) {
			state.Run.State = run.State("timer-fired")
		})
		selector.Select(ctx)

		if temporalSDK.IsCanceledError(ctx.Err()) {
			cleanupCtx, cancel := temporalworkflow.NewDisconnectedContext(ctx)
			defer cancel()
			state.Run.State = run.StateCanceling
			if cleanupErr := temporalworkflow.ExecuteActivity(
				temporalworkflow.WithActivityOptions(cleanupCtx, spikeActivityOptions()),
				"Cleanup", input,
			).Get(cleanupCtx, nil); cleanupErr != nil {
				return spikeResult{}, cleanupErr
			}
			state.Run.State = run.StateCanceled
			return spikeResult{
				WorkflowID:     info.WorkflowExecution.ID,
				RunID:          info.WorkflowExecution.RunID,
				VisitID:        visitID,
				ReportID:       latestReport,
				State:          run.StateCanceled,
				ReconcileCount: reconcileCount,
				DuplicateCount: duplicateCount,
				IgnoredCount:   ignoredCount,
			}, temporalSDK.NewCanceledError()
		}
		if state.Run.State == run.StateCompleted {
			return spikeResult{
				WorkflowID:     info.WorkflowExecution.ID,
				RunID:          info.WorkflowExecution.RunID,
				VisitID:        visitID,
				ReportID:       latestReport,
				State:          run.StateCompleted,
				ReconcileCount: reconcileCount,
				DuplicateCount: duplicateCount,
				IgnoredCount:   ignoredCount,
				ActivityResult: activityResult.Value,
			}, nil
		}
		if state.Run.State == run.State("timer-fired") {
			return spikeResult{
				WorkflowID:     info.WorkflowExecution.ID,
				RunID:          info.WorkflowExecution.RunID,
				VisitID:        visitID,
				State:          run.State("timer-fired"),
				ReconcileCount: reconcileCount,
				DuplicateCount: duplicateCount,
				IgnoredCount:   ignoredCount,
			}, nil
		}
	}
}

func newSpikeStart(ticket string) run.Start {
	return run.Start{
		ID:       run.ID("repo/compatibility/" + ticket),
		Repo:     "compatibility-repo",
		RepoPath: "/tmp/compatibility-repo",
		Workflow: domainworkflow.Workflow{
			Name:  "compatibility",
			Repos: []string{"compatibility-repo"},
			Nodes: map[string]domainworkflow.Node{
				"start":         {OnSuccess: []domainworkflow.Route{{Target: "compatibility"}}},
				"compatibility": {Type: domainworkflow.NodeAgent, Agent: "build", Description: "compatibility"},
				"end":           {},
			},
		},
		Ticket:  task.TicketRef{ID: ticket, Key: ticket, Title: "Compatibility ticket"},
		Runtime: run.RuntimePolicy{KeepTerminalsAlive: true, KeepSessionsAlive: true},
	}
}

func spikeWorkerOptions() temporalworker.Options {
	return temporalworker.Options{
		MaxConcurrentWorkflowTaskExecutionSize: 10,
		MaxConcurrentActivityExecutionSize:     20,
		MaxConcurrentWorkflowTaskPollers:       2,
		MaxConcurrentActivityTaskPollers:       2,
		WorkerStopTimeout:                      30 * time.Second,
	}
}

func startSpikeWorker(t *testing.T, temporalClient client.Client, activities *spikeActivities) temporalworker.Worker {
	t.Helper()
	w := temporalworker.New(temporalClient, spikeTaskQueue, spikeWorkerOptions())
	w.RegisterWorkflowWithOptions(spikeWorkflow, temporalworkflow.RegisterOptions{Name: spikeWorkflowType})
	w.RegisterActivity(activities)
	if err := w.Start(); err != nil {
		t.Fatalf("start Temporal worker: %v", err)
	}
	return w
}

func TestTemporalCompatibilitySerializableContracts(t *testing.T) {
	start := newSpikeStart("COMPAT-1")
	state := spikeRunStateSnapshot{
		Run: run.Run{
			ID: start.ID, LogicalID: start.ID, AttemptID: 1, Repo: start.Repo,
			Workflow: start.Workflow.Name, Ticket: start.Ticket, State: run.StateWaiting,
			StartedAt: time.Unix(100, 0).UTC(), UpdatedAt: time.Unix(200, 0).UTC(),
		},
		RuntimeBindings: []spikeNodeRuntimeBinding{{
			Node: "coding", TerminalID: "term-1", SessionID: "session-1", NodeVisitID: "visit-1",
		}},
	}
	query := spikeReportStateQuery{ReportID: "report-1"}
	values, err := converter.GetDefaultDataConverter().ToPayloads(start, state, query)
	if err != nil {
		t.Fatalf("encode serializable Temporal values: %v", err)
	}
	var gotStart run.Start
	var gotState spikeRunStateSnapshot
	var gotQuery spikeReportStateQuery
	if err := converter.GetDefaultDataConverter().FromPayloads(values, &gotStart, &gotState, &gotQuery); err != nil {
		t.Fatalf("decode serializable Temporal values: %v", err)
	}
	if gotStart.ID != start.ID || gotStart.Workflow.Name != start.Workflow.Name || gotStart.Ticket.Key != start.Ticket.Key {
		t.Fatalf("run.Start snapshot changed across serialization: %#v", gotStart)
	}
	if len(gotState.RuntimeBindings) != 1 || gotState.RuntimeBindings[0].NodeVisitID != "visit-1" {
		t.Fatalf("runtime binding changed across serialization: %#v", gotState.RuntimeBindings)
	}
	if gotQuery != query {
		t.Fatalf("report-state query changed across serialization: %#v", gotQuery)
	}
}

func TestTemporalCompatibilityWorkflowTestSuite(t *testing.T) {
	var suite testsuite.WorkflowTestSuite
	env := suite.NewTestWorkflowEnvironment()
	activities := &spikeActivities{}
	env.RegisterWorkflowWithOptions(spikeWorkflow, temporalworkflow.RegisterOptions{Name: spikeWorkflowType})
	env.RegisterActivity(activities)
	env.SetStartWorkflowOptions(client.StartWorkflowOptions{ID: "unit-compatibility"})
	env.ExecuteWorkflow(spikeWorkflow, spikeInput{Snapshot: newSpikeStart("UNIT-1"), Mode: "complete"})
	if err := env.GetWorkflowError(); err != nil {
		t.Fatalf("workflow test environment: %v", err)
	}
	var result spikeResult
	if err := env.GetWorkflowResult(&result); err != nil {
		t.Fatalf("workflow result: %v", err)
	}
	if result.State != run.StateCompleted || result.ActivityResult != "activity-ok" {
		t.Fatalf("workflow result = %#v", result)
	}
	if result.VisitID == "" || result.WorkflowID != "unit-compatibility" {
		t.Fatalf("workflow identity/result = %#v", result)
	}
	if got := activities.attemptsFor("UNIT-1"); got != 1 {
		t.Fatalf("activity calls = %d, want one", got)
	}
}

func TestTemporalCompatibilityWorkerOptions(t *testing.T) {
	opts := spikeWorkerOptions()
	if opts.MaxConcurrentWorkflowTaskExecutionSize != 10 || opts.MaxConcurrentActivityExecutionSize != 20 {
		t.Fatalf("execution limits = workflow %d/activity %d, want 10/20", opts.MaxConcurrentWorkflowTaskExecutionSize, opts.MaxConcurrentActivityExecutionSize)
	}
	if opts.MaxConcurrentWorkflowTaskPollers != 2 || opts.MaxConcurrentActivityTaskPollers != 2 {
		t.Fatalf("poller limits = workflow %d/activity %d, want 2/2", opts.MaxConcurrentWorkflowTaskPollers, opts.MaxConcurrentActivityTaskPollers)
	}
	if opts.WorkerStopTimeout != 30*time.Second {
		t.Fatalf("worker stop timeout = %s, want 30s", opts.WorkerStopTimeout)
	}
	activityOpts := spikeActivityOptions()
	if activityOpts.StartToCloseTimeout != 5*time.Minute || !activityOpts.WaitForCancellation || activityOpts.RetryPolicy == nil || activityOpts.RetryPolicy.MaximumAttempts != 1 {
		t.Fatalf("activity options = %#v, want 5m, wait-for-cancellation, max attempts 1", activityOpts)
	}
}

func TestTemporalCompatibilityLive(t *testing.T) {
	if os.Getenv("RELAY_FLOW_TEMPORAL_LIVE") != "1" {
		t.Skip("set RELAY_FLOW_TEMPORAL_LIVE=1 to run against the local Temporal Server")
	}
	address := os.Getenv("RELAY_FLOW_TEMPORAL_ADDRESS")
	if address == "" {
		address = "localhost:7233"
	}
	namespace := os.Getenv("RELAY_FLOW_TEMPORAL_NAMESPACE")
	if namespace == "" {
		namespace = fmt.Sprintf("relay-flow-compat-%d", os.Getpid())
	}
	if namespace == client.DefaultNamespace || strings.TrimSpace(namespace) == "" {
		t.Fatalf("live compatibility test requires a dedicated named namespace, got %q", namespace)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	if err := ensureSpikeNamespace(ctx, address, namespace); err != nil {
		t.Fatalf("ensure dedicated Temporal namespace: %v", err)
	}
	temporalClient, err := client.Dial(client.Options{HostPort: address, Namespace: namespace})
	if err != nil {
		t.Fatalf("dial Temporal at %s namespace %s: %v", address, namespace, err)
	}
	defer temporalClient.Close()
	activities := &spikeActivities{}
	w := startSpikeWorker(t, temporalClient, activities)

	// Explicit ID and running duplicate handling.
	waitID := fmt.Sprintf("compat-wait-%d", time.Now().UnixNano())
	waitRun, err := startSpikeWorkflow(ctx, temporalClient, waitID, spikeInput{Snapshot: newSpikeStart("WAIT-1"), Mode: "wait"})
	if err != nil {
		t.Fatalf("start waiting workflow: %v", err)
	}
	if waitRun.GetID() != waitID || waitRun.GetRunID() == "" {
		t.Fatalf("workflow identity = id %q/run %q, want explicit ID and server run ID", waitRun.GetID(), waitRun.GetRunID())
	}
	if _, err := startSpikeWorkflow(ctx, temporalClient, waitID, spikeInput{Snapshot: newSpikeStart("WAIT-DUP"), Mode: "wait"}); err == nil {
		t.Fatal("starting a running workflow ID unexpectedly succeeded")
	} else {
		var alreadyStarted *serviceerror.WorkflowExecutionAlreadyStarted
		if !errors.As(err, &alreadyStarted) {
			t.Fatalf("running duplicate error = %T %v, want WorkflowExecutionAlreadyStarted", err, err)
		}
	}
	state := waitForSpikeState(t, ctx, temporalClient, waitID, waitRun.GetRunID(), string(run.StateWaiting))
	if state.Run.ID != run.ID(newSpikeStart("WAIT-1").ID) || state.Run.CurrentNodeVisitID == "" || state.Run.StartedAt.IsZero() {
		t.Fatalf("run-state snapshot = %#v", state)
	}
	visitID := string(state.Run.CurrentNodeVisitID)
	assertSpikeActivityHistory(t, ctx, temporalClient, waitID, waitRun.GetRunID(), waitID)

	// Query the exact report-state shape before and after stale/current signals.
	reportState := querySpikeReportState(t, ctx, temporalClient, waitID, waitRun.GetRunID(), "report-1")
	if reportState.Processed || reportState.CurrentNodeVisitID != visitID || reportState.State != run.StateWaiting {
		t.Fatalf("initial report-state snapshot = %#v", reportState)
	}
	if err := temporalClient.SignalWorkflow(ctx, waitID, waitRun.GetRunID(), spikeReconcileSignalName, spikeReconcileSignal{Reason: "terminal missing"}); err != nil {
		t.Fatalf("reconcile signal: %v", err)
	}
	waitForReconcileCount(t, ctx, temporalClient, waitID, waitRun.GetRunID(), 1)
	if err := temporalClient.SignalWorkflow(ctx, waitID, waitRun.GetRunID(), spikeReportSignalName, spikeReportSignal{ReportID: "stale", Node: "other", NodeVisitID: visitID}); err != nil {
		t.Fatalf("stale report signal: %v", err)
	}
	waitForProgress(t, ctx, temporalClient, waitID, waitRun.GetRunID(), func(progress spikeProgressSnapshot) bool {
		return progress.IgnoredCount >= 1
	}, "stale report")
	if err := temporalClient.SignalWorkflow(ctx, waitID, waitRun.GetRunID(), spikeReportSignalName, spikeReportSignal{ReportID: "report-1", Node: "compatibility", NodeVisitID: visitID}); err != nil {
		t.Fatalf("current report signal: %v", err)
	}
	waitForSpikeState(t, ctx, temporalClient, waitID, waitRun.GetRunID(), "reported")
	if err := temporalClient.SignalWorkflow(ctx, waitID, waitRun.GetRunID(), spikeReportSignalName, spikeReportSignal{ReportID: "report-1", Node: "compatibility", NodeVisitID: visitID}); err != nil {
		t.Fatalf("duplicate report signal: %v", err)
	}
	reportState = querySpikeReportState(t, ctx, temporalClient, waitID, waitRun.GetRunID(), "report-1")
	if !reportState.Processed || reportState.CurrentNodeVisitID != visitID {
		t.Fatalf("consumed report-state snapshot = %#v", reportState)
	}
	waitForDuplicateCount(t, ctx, temporalClient, waitID, waitRun.GetRunID(), 1)

	// Stop and reconnect the worker while the workflow is still waiting.
	w.Stop()
	w = startSpikeWorker(t, temporalClient, activities)
	waitForSpikeState(t, ctx, temporalClient, waitID, waitRun.GetRunID(), "reported")
	if err := temporalClient.SignalWorkflow(ctx, waitID, waitRun.GetRunID(), spikeFinishSignalName, struct{}{}); err != nil {
		t.Fatalf("finish signal after worker restart: %v", err)
	}
	var completed spikeResult
	if err := waitRun.Get(ctx, &completed); err != nil {
		t.Fatalf("wait workflow result after worker restart: %v", err)
	}
	if completed.State != run.StateCompleted || completed.ReportID != "report-1" || completed.ReconcileCount != 1 || completed.DuplicateCount != 1 || completed.IgnoredCount != 1 {
		t.Fatalf("completed workflow result = %#v", completed)
	}
	assertSpikeHistorySnapshot(t, ctx, temporalClient, waitID, waitRun.GetRunID(), newSpikeStart("WAIT-1"))

	// Failed executions may be reused with ALLOW_DUPLICATE_FAILED_ONLY.
	failedID := fmt.Sprintf("compat-failed-%d", time.Now().UnixNano())
	failedRun, err := startSpikeWorkflow(ctx, temporalClient, failedID, spikeInput{Snapshot: newSpikeStart("FAIL-1"), Mode: "failure"})
	if err != nil {
		t.Fatalf("start failed workflow: %v", err)
	}
	failedErr := failedRun.Get(ctx, nil)
	if failedErr == nil {
		t.Fatal("failed workflow unexpectedly completed")
	}
	var transientErr *temporalSDK.ApplicationError
	if !errors.As(failedErr, &transientErr) || transientErr.Type() != string(retry.Transient) {
		t.Fatalf("failed workflow error = %T %v, want Temporal ApplicationError type %q", failedErr, failedErr, retry.Transient)
	}
	if activities.attemptsFor("FAIL-1") != 1 {
		t.Fatalf("failed activity attempts = %d, want native retry maximum one", activities.attemptsFor("FAIL-1"))
	}
	restarted, err := startSpikeWorkflow(ctx, temporalClient, failedID, spikeInput{Snapshot: newSpikeStart("FAIL-2"), Mode: "complete"})
	if err != nil {
		t.Fatalf("restart failed workflow with same ID: %v", err)
	}
	var restartResult spikeResult
	if err := restarted.Get(ctx, &restartResult); err != nil || restartResult.State != run.StateCompleted {
		t.Fatalf("restarted failed workflow = %#v, err %v", restartResult, err)
	}

	conflictID := fmt.Sprintf("compat-conflict-%d", time.Now().UnixNano())
	conflictRun, err := startSpikeWorkflow(ctx, temporalClient, conflictID, spikeInput{Snapshot: newSpikeStart("CONFLICT-1"), Mode: "conflict"})
	if err != nil {
		t.Fatalf("start conflict workflow: %v", err)
	}
	conflictErr := conflictRun.Get(ctx, nil)
	if conflictErr == nil {
		t.Fatal("conflict workflow unexpectedly completed")
	}
	var conflictAppErr *temporalSDK.ApplicationError
	if !errors.As(conflictErr, &conflictAppErr) || conflictAppErr.Type() != string(retry.Conflict) {
		t.Fatalf("conflict workflow error = %T %v, want Temporal ApplicationError type %q", conflictErr, conflictErr, retry.Conflict)
	}
	if activities.attemptsFor("CONFLICT-1") != 1 {
		t.Fatalf("conflict activity attempts = %d, want native retry maximum one", activities.attemptsFor("CONFLICT-1"))
	}

	// Cancellation during an activity runs disconnected cleanup and ends with
	// Temporal's canceled status rather than completed.
	cancelID := fmt.Sprintf("compat-cancel-%d", time.Now().UnixNano())
	cancelRunHandle, err := startSpikeWorkflow(ctx, temporalClient, cancelID, spikeInput{Snapshot: newSpikeStart("CANCEL-1"), Mode: "cancel-activity"})
	if err != nil {
		t.Fatalf("start cancel workflow: %v", err)
	}
	waitForSpikeState(t, ctx, temporalClient, cancelID, cancelRunHandle.GetRunID(), string(run.StateRunning))
	if err := temporalClient.CancelWorkflowWithOptions(ctx, client.CancelWorkflowOptions{WorkflowID: cancelID, RunID: cancelRunHandle.GetRunID(), Reason: "compatibility cancellation"}); err != nil {
		t.Fatalf("cancel workflow: %v", err)
	}
	if err := cancelRunHandle.Get(ctx, nil); err == nil {
		t.Fatal("canceled workflow unexpectedly completed")
	}
	waitForCleanup(t, activities)
	cancelDescription, err := temporalClient.DescribeWorkflowExecution(ctx, cancelID, cancelRunHandle.GetRunID())
	if err != nil {
		t.Fatalf("describe canceled workflow: %v", err)
	}
	if cancelDescription.WorkflowExecutionInfo.GetStatus() != enumspb.WORKFLOW_EXECUTION_STATUS_CANCELED {
		t.Fatalf("canceled workflow status = %s", cancelDescription.WorkflowExecutionInfo.GetStatus())
	}
	canceledRestart, err := startSpikeWorkflow(ctx, temporalClient, cancelID, spikeInput{Snapshot: newSpikeStart("CANCEL-2"), Mode: "complete"})
	if err != nil {
		t.Fatalf("restart canceled workflow with same ID: %v", err)
	}
	var canceledRestartResult spikeResult
	if err := canceledRestart.Get(ctx, &canceledRestartResult); err != nil || canceledRestartResult.State != run.StateCompleted {
		t.Fatalf("restarted canceled workflow = %#v, err %v", canceledRestartResult, err)
	}

	// A completed execution cannot be reused under ALLOW_DUPLICATE_FAILED_ONLY.
	completedID := fmt.Sprintf("compat-completed-%d", time.Now().UnixNano())
	firstCompleted, err := startSpikeWorkflow(ctx, temporalClient, completedID, spikeInput{Snapshot: newSpikeStart("DONE-1"), Mode: "complete"})
	if err != nil {
		t.Fatalf("start completed workflow: %v", err)
	}
	if err := firstCompleted.Get(ctx, nil); err != nil {
		t.Fatalf("completed workflow result: %v", err)
	}
	if _, err := startSpikeWorkflow(ctx, temporalClient, completedID, spikeInput{Snapshot: newSpikeStart("DONE-2"), Mode: "complete"}); err == nil {
		t.Fatal("reusing a completed workflow ID unexpectedly succeeded")
	} else {
		var alreadyStarted *serviceerror.WorkflowExecutionAlreadyStarted
		if !errors.As(err, &alreadyStarted) {
			t.Fatalf("completed duplicate error = %T %v, want WorkflowExecutionAlreadyStarted", err, err)
		}
	}

	w.Stop()
}

func startSpikeWorkflow(ctx context.Context, c client.Client, id string, input spikeInput) (client.WorkflowRun, error) {
	return c.ExecuteWorkflow(ctx, client.StartWorkflowOptions{
		ID:                                       id,
		TaskQueue:                                spikeTaskQueue,
		WorkflowIDReusePolicy:                    enumspb.WORKFLOW_ID_REUSE_POLICY_ALLOW_DUPLICATE_FAILED_ONLY,
		WorkflowExecutionErrorWhenAlreadyStarted: true,
	}, spikeWorkflowType, input)
}

func ensureSpikeNamespace(ctx context.Context, address, namespace string) error {
	ns, err := client.NewNamespaceClient(client.Options{HostPort: address})
	if err != nil {
		return err
	}
	defer ns.Close()
	description, err := ns.Describe(ctx, namespace)
	if err != nil {
		var notFound *serviceerror.NamespaceNotFound
		if !errors.As(err, &notFound) {
			return err
		}
		if err := ns.Register(ctx, &workflowservice.RegisterNamespaceRequest{
			Namespace:                        namespace,
			Description:                      "relay-flow compatibility spike",
			WorkflowExecutionRetentionPeriod: durationpb.New(spikeRetention),
		}); err != nil {
			var alreadyExists *serviceerror.NamespaceAlreadyExists
			if !errors.As(err, &alreadyExists) {
				return fmt.Errorf("register namespace: %w", err)
			}
		}
		description, err = ns.Describe(ctx, namespace)
		if err != nil {
			return err
		}
		// Namespace registration is acknowledged before every frontend worker
		// cache necessarily observes it. Leave a small propagation window so a
		// first live spike run does not race namespace visibility.
		time.Sleep(5 * time.Second)
	}
	if description.Config == nil || description.Config.WorkflowExecutionRetentionTtl == nil {
		return fmt.Errorf("namespace %q has no workflow retention configuration", namespace)
	}
	if description.Config.WorkflowExecutionRetentionTtl.AsDuration() < spikeRetention {
		return fmt.Errorf("namespace %q retention is %s, need at least %s", namespace, description.Config.WorkflowExecutionRetentionTtl.AsDuration(), spikeRetention)
	}
	return nil
}

func querySpikeState(ctx context.Context, c client.Client, workflowID, runID string) (spikeRunStateSnapshot, error) {
	encoded, err := c.QueryWorkflow(ctx, workflowID, runID, spikeRunStateQuery)
	if err != nil {
		return spikeRunStateSnapshot{}, err
	}
	var state spikeRunStateSnapshot
	if err := encoded.Get(&state); err != nil {
		return spikeRunStateSnapshot{}, err
	}
	return state, nil
}

func querySpikeReportState(t *testing.T, ctx context.Context, c client.Client, workflowID, runID, reportID string) spikeReportStateSnapshot {
	t.Helper()
	encoded, err := c.QueryWorkflow(ctx, workflowID, runID, spikeReportStateQueryName, spikeReportStateQuery{ReportID: reportID})
	if err != nil {
		t.Fatalf("query report state %s: %v", reportID, err)
	}
	var state spikeReportStateSnapshot
	if err := encoded.Get(&state); err != nil {
		t.Fatalf("decode report state %s: %v", reportID, err)
	}
	return state
}

func waitForSpikeState(t *testing.T, ctx context.Context, c client.Client, workflowID, runID, want string) spikeRunStateSnapshot {
	t.Helper()
	for {
		state, err := querySpikeState(ctx, c, workflowID, runID)
		if err == nil && string(state.Run.State) == want {
			return state
		}
		select {
		case <-ctx.Done():
			t.Fatalf("waiting for workflow %s state %q: %v (last state %#v)", workflowID, want, err, state)
		case <-time.After(100 * time.Millisecond):
		}
	}
}

func waitForReconcileCount(t *testing.T, ctx context.Context, c client.Client, workflowID, runID string, want int) {
	t.Helper()
	waitForProgress(t, ctx, c, workflowID, runID, func(progress spikeProgressSnapshot) bool {
		return progress.ReconcileCount >= want
	}, "reconcile signal")
}

func waitForDuplicateCount(t *testing.T, ctx context.Context, c client.Client, workflowID, runID string, want int) {
	t.Helper()
	waitForProgress(t, ctx, c, workflowID, runID, func(progress spikeProgressSnapshot) bool {
		return progress.DuplicateCount >= want
	}, "duplicate report")
}

func waitForProgress(t *testing.T, ctx context.Context, c client.Client, workflowID, runID string, done func(spikeProgressSnapshot) bool, label string) {
	t.Helper()
	deadline := time.NewTimer(5 * time.Second)
	defer deadline.Stop()
	for {
		progress, err := querySpikeProgressNoFatal(ctx, c, workflowID, runID)
		if err == nil && done(progress) {
			return
		}
		select {
		case <-deadline.C:
			t.Fatalf("waiting for %s (progress %#v, err %v)", label, progress, err)
		case <-ctx.Done():
			t.Fatalf("waiting for %s: %v", label, ctx.Err())
		case <-time.After(100 * time.Millisecond):
		}
	}
}

func querySpikeReportStateNoFatal(ctx context.Context, c client.Client, workflowID, runID, reportID string) (spikeReportStateSnapshot, error) {
	encoded, err := c.QueryWorkflow(ctx, workflowID, runID, spikeReportStateQueryName, spikeReportStateQuery{ReportID: reportID})
	if err != nil {
		return spikeReportStateSnapshot{}, err
	}
	var state spikeReportStateSnapshot
	if err := encoded.Get(&state); err != nil {
		return spikeReportStateSnapshot{}, err
	}
	return state, nil
}

func querySpikeProgressNoFatal(ctx context.Context, c client.Client, workflowID, runID string) (spikeProgressSnapshot, error) {
	encoded, err := c.QueryWorkflow(ctx, workflowID, runID, spikeProgressQueryName)
	if err != nil {
		return spikeProgressSnapshot{}, err
	}
	var progress spikeProgressSnapshot
	if err := encoded.Get(&progress); err != nil {
		return spikeProgressSnapshot{}, err
	}
	return progress, nil
}

func waitForCleanup(t *testing.T, activities *spikeActivities) {
	t.Helper()
	deadline := time.NewTimer(30 * time.Second)
	defer deadline.Stop()
	for activities.cleanup.Load() == 0 {
		select {
		case <-deadline.C:
			t.Fatal("cancellation cleanup activity did not execute")
		case <-time.After(100 * time.Millisecond):
		}
	}
}

func historyForSpike(ctx context.Context, c client.Client, workflowID, runID string) ([]*historypb.HistoryEvent, error) {
	iterator := c.GetWorkflowHistory(ctx, workflowID, runID, false, enumspb.HISTORY_EVENT_FILTER_TYPE_ALL_EVENT)
	var events []*historypb.HistoryEvent
	for iterator.HasNext() {
		event, err := iterator.Next()
		if err != nil {
			return nil, err
		}
		events = append(events, event)
	}
	return events, nil
}

func assertSpikeActivityHistory(t *testing.T, ctx context.Context, c client.Client, workflowID, runID, expectedWorkflowID string) {
	t.Helper()
	events, err := historyForSpike(ctx, c, workflowID, runID)
	if err != nil {
		t.Fatalf("get workflow history: %v", err)
	}
	var started, scheduled, timerStarted, marker bool
	for _, event := range events {
		switch event.GetEventType() {
		case enumspb.EVENT_TYPE_WORKFLOW_EXECUTION_STARTED:
			started = true
			attrs := event.GetWorkflowExecutionStartedEventAttributes()
			if attrs == nil || attrs.GetWorkflowId() != expectedWorkflowID || attrs.GetTaskQueue().GetName() != spikeTaskQueue || attrs.GetRetryPolicy() != nil {
				t.Fatalf("workflow-start attributes = %#v", attrs)
			}
		case enumspb.EVENT_TYPE_ACTIVITY_TASK_SCHEDULED:
			scheduled = true
			attrs := event.GetActivityTaskScheduledEventAttributes()
			if attrs.GetStartToCloseTimeout().AsDuration() != 5*time.Minute || attrs.GetRetryPolicy().GetMaximumAttempts() != 1 {
				t.Fatalf("activity schedule options = %#v", attrs)
			}
		case enumspb.EVENT_TYPE_TIMER_STARTED:
			timerStarted = true
		case enumspb.EVENT_TYPE_MARKER_RECORDED:
			marker = true
		}
	}
	if !started || !scheduled || !timerStarted || !marker {
		t.Fatalf("history markers started=%v activity=%v timer=%v sideEffect=%v", started, scheduled, timerStarted, marker)
	}
}

func assertSpikeHistorySnapshot(t *testing.T, ctx context.Context, c client.Client, workflowID, runID string, expected run.Start) {
	t.Helper()
	events, err := historyForSpike(ctx, c, workflowID, runID)
	if err != nil {
		t.Fatalf("get snapshot history: %v", err)
	}
	for _, event := range events {
		if event.GetEventType() != enumspb.EVENT_TYPE_WORKFLOW_EXECUTION_STARTED {
			continue
		}
		attrs := event.GetWorkflowExecutionStartedEventAttributes()
		if attrs == nil || attrs.Input == nil {
			t.Fatal("workflow-start event has no input snapshot")
		}
		var got spikeInput
		if err := converter.GetDefaultDataConverter().FromPayloads(attrs.Input, &got); err != nil {
			t.Fatalf("decode workflow-start snapshot: %v", err)
		}
		if got.Snapshot.ID != expected.ID || got.Snapshot.Workflow.Name != expected.Workflow.Name || got.Snapshot.Ticket.Key != expected.Ticket.Key {
			t.Fatalf("history snapshot = %#v, want %#v", got.Snapshot, expected)
		}
		return
	}
	t.Fatal("workflow-start event not found")
}
