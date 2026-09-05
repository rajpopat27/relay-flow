package temporal

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/rajpopat27/relay-flow/internal/config"
	"github.com/rajpopat27/relay-flow/internal/execution/projection"
	"github.com/rajpopat27/relay-flow/internal/harness"
	"github.com/rajpopat27/relay-flow/internal/identity"
	"github.com/rajpopat27/relay-flow/internal/repo"
	"github.com/rajpopat27/relay-flow/internal/retry"
	"github.com/rajpopat27/relay-flow/internal/run"
	"github.com/rajpopat27/relay-flow/internal/runner"
	"github.com/rajpopat27/relay-flow/internal/task"
	domainworkflow "github.com/rajpopat27/relay-flow/internal/workflow"
	enumspb "go.temporal.io/api/enums/v1"
	"go.temporal.io/sdk/client"
)

func TestTemporalRecoveryReconcilesActiveVisibilityLag(t *testing.T) {
	if os.Getenv("RELAY_FLOW_TEMPORAL_LIVE") != "1" {
		t.Skip("set RELAY_FLOW_TEMPORAL_LIVE=1 to run active Visibility-lag recovery against the local server")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	namespace := "relay-flow-visibility-lag-" + string(identity.NewNodeVisitID())[:12]
	if err := ensureSpikeNamespace(ctx, "localhost:7233", namespace); err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	database := filepath.Join(root, "state.db")
	if err := projection.InitDatabaseWithIdentity(database, projection.ExecutorIdentity{
		ExecutorPlugin: "temporal", TemporalAddress: "localhost:7233", TemporalNamespace: namespace,
	}); err != nil {
		t.Fatal(err)
	}
	sys := &lagTaskSystem{}
	rnr := &lagRunner{}
	hrn := &lagHarness{}
	registry := repo.NewRegistry()
	workflow := lagWorkflow()
	registry.Replace(&repo.Repo{
		Name: "repo", Path: "/repo", TaskSystem: sys,
		Workflows: []repo.WorkflowBinding{{Workflow: &workflow}},
	})
	engine, err := New(database, Dependencies{
		Repos: registry, Runner: rnr, Harness: hrn, TaskSystem: "lag-task",
		TemporalAddress: "localhost:7233", TemporalNamespace: namespace,
		Runtime: &run.RuntimePolicy{KeepTerminalsAlive: true, KeepSessionsAlive: true},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := engine.Start(ctx); err != nil {
		t.Fatal(err)
	}
	defer engine.Shutdown(context.Background())
	start := run.Start{
		ID: identity.NewRunID("repo", workflow.Name, "LAG-1"), Repo: "repo", RepoPath: "/repo",
		Workflow: workflow, Ticket: task.TicketRef{ID: "LAG-1", Key: "LAG-1", Title: "Visibility lag"},
		Runtime: run.RuntimePolicy{KeepTerminalsAlive: true, KeepSessionsAlive: true},
	}
	created, err := engine.EnsureRun(ctx, start)
	if err != nil || !created {
		t.Fatalf("EnsureRun = %v, %v", created, err)
	}
	waitForLagState(t, ctx, engine, start.ID, run.StateWaiting)
	rnr.setLive(true)
	description, err := engine.client.DescribeWorkflowExecution(ctx, string(start.ID), "")
	if err != nil {
		t.Fatal(err)
	}
	if description.WorkflowExecutionInfo == nil || description.WorkflowExecutionInfo.Execution == nil {
		t.Fatal("Temporal describe returned no active execution")
	}
	runID := description.WorkflowExecutionInfo.Execution.RunId
	beforeReconcile := countTemporalSignals(t, engine, start.ID, runID, reconcileSignalName)
	created, err = engine.EnsureRun(ctx, start)
	if err != nil || created {
		t.Fatalf("duplicate EnsureRun = %v, %v", created, err)
	}
	if got := countTemporalSignals(t, engine, start.ID, runID, reconcileSignalName); got != beforeReconcile {
		t.Fatalf("healthy terminal reconciliation emitted %d signals, want %d", got, beforeReconcile)
	}
	rnr.setLive(false)
	created, err = engine.EnsureRun(ctx, start)
	if err != nil || created {
		t.Fatalf("missing-terminal EnsureRun = %v, %v", created, err)
	}
	deadline := time.Now().Add(10 * time.Second)
	for countTemporalSignals(t, engine, start.ID, runID, reconcileSignalName) <= beforeReconcile {
		if time.Now().After(deadline) {
			t.Fatal("missing terminal reconciliation did not persist a reconcile signal")
		}
		time.Sleep(100 * time.Millisecond)
	}
	description, err = engine.client.DescribeWorkflowExecution(ctx, string(start.ID), "")
	if err != nil || description.WorkflowExecutionInfo == nil || description.WorkflowExecutionInfo.Execution == nil || description.WorkflowExecutionInfo.Execution.RunId != runID {
		t.Fatalf("duplicate EnsureRun changed execution: %#v, %v", description.WorkflowExecutionInfo, err)
	}
	workflowSnapshot, err := engine.workflowFromHistory(ctx, start.ID, runID)
	if err != nil || workflowSnapshot.Name != workflow.Name || workflowSnapshot.Nodes["work"].Description != "work" {
		t.Fatalf("Temporal workflow snapshot = %#v, %v", workflowSnapshot, err)
	}
	reportState, err := engine.queryReportState(ctx, start.ID, "not-yet-processed")
	if err != nil || reportState.State != run.StateWaiting || reportState.CurrentNode != "work" || reportState.CurrentNodeVisitID == "" || reportState.Processed {
		t.Fatalf("Temporal report-state snapshot = %#v, %v", reportState, err)
	}
	if _, err := engine.runs.DB.ExecContext(ctx, `DELETE FROM relay_runs WHERE id = ?`, string(start.ID)); err != nil {
		t.Fatal(err)
	}
	created, err = engine.EnsureRun(ctx, start)
	if err != nil || created {
		t.Fatalf("EnsureRun after projection loss = %v, %v", created, err)
	}
	if got, err := engine.runs.Get(ctx, start.ID); err != nil || got.CurrentNode != "work" || got.CurrentNodeVisitID == "" {
		t.Fatalf("reconciled missing projection = %+v, %v", got, err)
	}

	// A false visible entry models Visibility exposing only an older closed
	// history for this Workflow ID. Recovery must still use exact DescribeWorkflowExecution
	// and restore the currently running execution, never start a replacement.
	if err := engine.reconcileClaimedParents(ctx, map[string]bool{string(start.ID): false}); err != nil {
		t.Fatalf("reconcile visibility lag: %v", err)
	}
	got, err := engine.runs.Get(ctx, start.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.ID != start.ID || got.CurrentNode != "work" || got.State != run.StateWaiting || got.CurrentNodeVisitID == "" {
		t.Fatalf("reconciled projection = %+v", got)
	}
	description, err = engine.client.DescribeWorkflowExecution(ctx, string(start.ID), "")
	if err != nil {
		t.Fatal(err)
	}
	if description.WorkflowExecutionInfo == nil || description.WorkflowExecutionInfo.Execution == nil || description.WorkflowExecutionInfo.Execution.RunId != runID {
		t.Fatalf("visibility-lag reconciliation changed execution: %#v", description.WorkflowExecutionInfo)
	}
	if got := sys.pollCountValue(); got != 1 {
		t.Fatalf("task-system Poll calls = %d, want one read-only reconciliation poll", got)
	}
}

func TestTemporalRecoveryReconcilesClosedHistoryToCurrentExecution(t *testing.T) {
	if os.Getenv("RELAY_FLOW_TEMPORAL_LIVE") != "1" {
		t.Skip("set RELAY_FLOW_TEMPORAL_LIVE=1 to run closed-history Visibility lag coverage")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	namespace := "relay-flow-closed-lag-" + string(identity.NewNodeVisitID())[:12]
	if err := ensureSpikeNamespace(ctx, "localhost:7233", namespace); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "state.db")
	if err := projection.InitDatabaseWithIdentity(path, projection.ExecutorIdentity{ExecutorPlugin: "temporal", TemporalAddress: "localhost:7233", TemporalNamespace: namespace}); err != nil {
		t.Fatal(err)
	}
	sys := &lagTaskSystem{}
	registry := repo.NewRegistry()
	wf := lagWorkflow()
	registry.Replace(&repo.Repo{Name: "repo", Path: "/repo", TaskSystem: sys, Workflows: []repo.WorkflowBinding{{Workflow: &wf}}})
	engine, err := New(path, Dependencies{Repos: registry, Runner: &lagRunner{}, Harness: &lagHarness{}, TaskSystem: "lag-task", TemporalAddress: "localhost:7233", TemporalNamespace: namespace})
	if err != nil {
		t.Fatal(err)
	}
	if err := engine.Start(ctx); err != nil {
		t.Fatal(err)
	}
	defer engine.Shutdown(context.Background())
	start := run.Start{ID: identity.NewRunID("repo", wf.Name, "LAG-1"), Repo: "repo", RepoPath: "/repo", Workflow: wf, Ticket: task.TicketRef{ID: "LAG-1", Key: "LAG-1", Title: "Closed history lag"}}
	badWorkflow := wf
	badWorkflow.Nodes = make(map[string]domainworkflow.Node, len(wf.Nodes))
	for name, node := range wf.Nodes {
		badWorkflow.Nodes[name] = node
	}
	badWorkflow.Nodes["start"] = domainworkflow.Node{}
	badStart := start
	badStart.Workflow = badWorkflow
	failed, err := engine.client.ExecuteWorkflow(ctx, client.StartWorkflowOptions{ID: string(start.ID), TaskQueue: TaskQueue, WorkflowIDReusePolicy: enumspb.WORKFLOW_ID_REUSE_POLICY_ALLOW_DUPLICATE_FAILED_ONLY}, TicketWorkflow, badStart)
	if err != nil {
		t.Fatal(err)
	}
	if err := failed.Get(ctx, nil); err == nil {
		t.Fatal("malformed first execution unexpectedly succeeded")
	}
	current, err := engine.client.ExecuteWorkflow(ctx, client.StartWorkflowOptions{ID: string(start.ID), TaskQueue: TaskQueue, WorkflowIDReusePolicy: enumspb.WORKFLOW_ID_REUSE_POLICY_ALLOW_DUPLICATE_FAILED_ONLY}, TicketWorkflow, start)
	if err != nil {
		t.Fatal(err)
	}
	waitForLagState(t, ctx, engine, start.ID, run.StateWaiting)
	if err := engine.reconcileClaimedParents(ctx, map[string]bool{string(start.ID): false}); err != nil {
		t.Fatalf("reconcile closed-history lag: %v", err)
	}
	got, err := engine.runs.Get(ctx, start.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.State != run.StateWaiting || got.CurrentNode != "work" || got.CurrentNodeVisitID == "" {
		t.Fatalf("reconciled current execution projection = %+v", got)
	}
	description, err := engine.client.DescribeWorkflowExecution(ctx, string(start.ID), "")
	if err != nil || description.WorkflowExecutionInfo == nil || description.WorkflowExecutionInfo.Execution == nil || description.WorkflowExecutionInfo.Execution.RunId != current.GetRunID() {
		t.Fatalf("reconciliation changed current execution: %#v, %v", description.WorkflowExecutionInfo, err)
	}
}

func TestTemporalConflictRetryBlocksThenResumes(t *testing.T) {
	if os.Getenv("RELAY_FLOW_TEMPORAL_LIVE") != "1" {
		t.Skip("set RELAY_FLOW_TEMPORAL_LIVE=1 to run Temporal conflict retry against the local server")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	namespace := "relay-flow-conflict-" + string(identity.NewNodeVisitID())[:12]
	if err := ensureSpikeNamespace(ctx, "localhost:7233", namespace); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "state.db")
	if err := projection.InitDatabaseWithIdentity(path, projection.ExecutorIdentity{ExecutorPlugin: "temporal", TemporalAddress: "localhost:7233", TemporalNamespace: namespace}); err != nil {
		t.Fatal(err)
	}
	sys := &lagTaskSystem{applyFailures: 1}
	registry := repo.NewRegistry()
	wf := lagWorkflow()
	registry.Replace(&repo.Repo{Name: "repo", Path: "/repo", TaskSystem: sys, Workflows: []repo.WorkflowBinding{{Workflow: &wf}}})
	engine, err := New(path, Dependencies{Repos: registry, Runner: &lagRunner{}, Harness: &lagHarness{}, TaskSystem: "lag-task", TemporalAddress: "localhost:7233", TemporalNamespace: namespace})
	if err != nil {
		t.Fatal(err)
	}
	if err := engine.Start(ctx); err != nil {
		t.Fatal(err)
	}
	defer engine.Shutdown(context.Background())
	start := run.Start{ID: identity.NewRunID("repo", wf.Name, "CONFLICT-1"), Repo: "repo", RepoPath: "/repo", Workflow: wf, Ticket: task.TicketRef{ID: "CONFLICT-1", Key: "CONFLICT-1", Title: "Conflict"}}
	if created, err := engine.EnsureRun(ctx, start); err != nil || !created {
		t.Fatalf("EnsureRun = %v, %v", created, err)
	}
	deadline := time.Now().Add(20 * time.Second)
	for {
		got, err := engine.runs.Get(ctx, start.ID)
		if err == nil && got.State == run.StateBlocked {
			snapshot, queryErr := engine.queryRunState(ctx, start.ID, "")
			if queryErr == nil && snapshot.Run.Retry != nil && snapshot.Run.State == run.StateBlocked {
				break
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("run never became blocked with retry metadata: %+v, %v", got, err)
		}
		time.Sleep(100 * time.Millisecond)
	}
	waitForLagState(t, ctx, engine, start.ID, run.StateWaiting)
	got, err := engine.runs.Get(ctx, start.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Retry != nil {
		t.Fatalf("retry metadata remained after conflict recovery: %+v", got.Retry)
	}
}

func lagWorkflow() domainworkflow.Workflow {
	return domainworkflow.Workflow{
		Name: "lagFlow", Repos: []string{"repo"},
		Nodes: map[string]domainworkflow.Node{
			"start": {OnSuccess: []domainworkflow.Route{{Target: "work"}}},
			"work": {
				Type: domainworkflow.NodeAgent, Agent: "agent", Description: "work",
				OnSuccess: []domainworkflow.Route{{Target: "end"}},
				OnFailure: []domainworkflow.Route{{Target: "end"}},
			},
			"end": {},
		},
	}
}

func countTemporalSignals(t *testing.T, engine *Engine, id run.ID, temporalRunID, signalName string) int {
	t.Helper()
	iterator := engine.client.GetWorkflowHistory(context.Background(), string(id), temporalRunID, false, enumspb.HISTORY_EVENT_FILTER_TYPE_ALL_EVENT)
	count := 0
	for iterator.HasNext() {
		event, err := iterator.Next()
		if err != nil {
			t.Fatalf("read Temporal history for signal count: %v", err)
		}
		if event.GetEventType() == enumspb.EVENT_TYPE_WORKFLOW_EXECUTION_SIGNALED && event.GetWorkflowExecutionSignaledEventAttributes().GetSignalName() == signalName {
			count++
		}
	}
	return count
}

func waitForLagState(t *testing.T, ctx context.Context, engine *Engine, id run.ID, state run.State) {
	t.Helper()
	for {
		snapshot, err := engine.queryRunState(ctx, id, "")
		if err == nil && snapshot.Run.State == state && snapshot.Run.CurrentNode != "" {
			return
		}
		select {
		case <-ctx.Done():
			t.Fatalf("waiting for Temporal state %s: %v", state, ctx.Err())
		case <-time.After(100 * time.Millisecond):
		}
	}
}

type lagTaskSystem struct {
	mu            sync.Mutex
	polls         int
	applyFailures int
}

var _ task.System = (*lagTaskSystem)(nil)

func (s *lagTaskSystem) Poll(context.Context) ([]task.Ticket, error) {
	s.mu.Lock()
	s.polls++
	s.mu.Unlock()
	return []task.Ticket{{ID: "LAG-1", Key: "LAG-1", Title: "Visibility lag", WorkflowClaims: []string{"wf:lagFlow"}}}, nil
}
func (*lagTaskSystem) CompileFilter(config.RawValues) (func(task.Ticket) bool, error) {
	return func(task.Ticket) bool { return true }, nil
}
func (*lagTaskSystem) Claim(context.Context, task.TicketRef, string) error { return nil }
func (*lagTaskSystem) ValidateConfig(context.Context, config.RawValues, map[string]config.RawValues) error {
	return nil
}
func (*lagTaskSystem) RenderText(task.TextKind, task.TextData) (string, error) { return "", nil }
func (*lagTaskSystem) EnsureMailboxes(_ context.Context, _ task.TicketRef, _ string, specs []task.MailboxSpec) (map[string]task.Mailbox, error) {
	out := make(map[string]task.Mailbox, len(specs))
	for _, spec := range specs {
		out[spec.Node] = task.Mailbox{ID: "mb-" + spec.Node, Key: spec.Title, Node: spec.Node}
	}
	return out, nil
}
func (s *lagTaskSystem) ApplyTaskConfig(context.Context, task.Target, config.RawValues) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.applyFailures > 0 {
		s.applyFailures--
		return retry.ConflictError(errors.New("task status is not yet compatible"))
	}
	return nil
}
func (*lagTaskSystem) CompleteMailbox(context.Context, task.Mailbox) error { return nil }
func (*lagTaskSystem) HasComment(context.Context, task.Target, string) (bool, error) {
	return false, nil
}
func (*lagTaskSystem) Comment(context.Context, task.Target, string, string) error { return nil }
func (*lagTaskSystem) ResetForRecovery(context.Context, task.TicketRef, []task.Mailbox, config.RawValues) error {
	return nil
}
func (s *lagTaskSystem) pollCountValue() int { s.mu.Lock(); defer s.mu.Unlock(); return s.polls }

type lagRunner struct {
	mu   sync.Mutex
	live bool
}

var _ runner.Runner = (*lagRunner)(nil)

func (*lagRunner) DiscoverRepos(context.Context) ([]runner.RepoCandidate, error) { return nil, nil }
func (*lagRunner) ValidateRepo(context.Context, string, string) error            { return nil }
func (*lagRunner) EnsureEnvironment(context.Context, runner.RunSpec) (runner.Environment, error) {
	return runner.Environment{ID: "env-lag", Path: "/repo"}, nil
}
func (*lagRunner) SetEnvironmentStatus(context.Context, runner.Environment, string) error { return nil }
func (r *lagRunner) setLive(live bool) {
	r.mu.Lock()
	r.live = live
	r.mu.Unlock()
}

func (r *lagRunner) FindTerminal(_ context.Context, terminal runner.Terminal) (runner.Terminal, bool, error) {
	r.mu.Lock()
	live := r.live
	r.mu.Unlock()
	if live && terminal.ID != "" {
		return terminal, true, nil
	}
	return runner.Terminal{}, false, nil
}
func (*lagRunner) CreateTerminal(_ context.Context, _ runner.Environment, title string, _ runner.Command) (runner.Terminal, error) {
	return runner.Terminal{ID: "term-" + title, Title: title}, nil
}
func (*lagRunner) EnsureTerminal(_ context.Context, _ runner.Environment, _ runner.Terminal, title string, _ runner.Command) (runner.Terminal, error) {
	return runner.Terminal{ID: "term-" + title, Title: title}, nil
}
func (*lagRunner) SendTerminal(context.Context, runner.Terminal, string) error { return nil }
func (*lagRunner) CloseTerminal(context.Context, runner.Terminal) error        { return nil }
func (*lagRunner) CloseTerminals(context.Context, runner.RunSpec) error        { return nil }
func (*lagRunner) CleanupRun(context.Context, runner.RunSpec) error            { return nil }

type lagHarness struct{}

var _ harness.Harness = (*lagHarness)(nil)

func (*lagHarness) SetupRepo(context.Context, string) error { return nil }
func (*lagHarness) FindSession(context.Context, string, string) (harness.Session, bool, error) {
	return harness.Session{}, false, nil
}
func (*lagHarness) ValidateAgent(context.Context, string, string) error { return nil }
func (*lagHarness) RenderPrompt(harness.PromptKind, harness.PromptData, string) (string, error) {
	return "prompt", nil
}
func (*lagHarness) BuildCommand(harness.LaunchSpec) (runner.Command, error) {
	return runner.Command{Executable: "agent"}, nil
}
