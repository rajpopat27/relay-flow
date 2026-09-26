// Package goworkflows is the durable execution engine: one generic
// TicketWorkflow interpreter over go-workflows with a SQLite backend, the
// relay_runs projection, typed activity retry loops, and signal-based report
// handling. All go-workflows types stay inside this package.
package goworkflows

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"sync"
	"time"

	"github.com/cschleiden/go-workflows/backend"
	"github.com/cschleiden/go-workflows/backend/converter"
	"github.com/cschleiden/go-workflows/backend/history"
	"github.com/cschleiden/go-workflows/backend/sqlite"
	"github.com/cschleiden/go-workflows/client"
	"github.com/cschleiden/go-workflows/worker"
	goworkflow "github.com/cschleiden/go-workflows/workflow"
	"github.com/google/uuid"

	"github.com/rajpopat27/relay-flow/internal/execution/projection"
	"github.com/rajpopat27/relay-flow/internal/harness"
	"github.com/rajpopat27/relay-flow/internal/identity"
	"github.com/rajpopat27/relay-flow/internal/repo"
	"github.com/rajpopat27/relay-flow/internal/retry"
	"github.com/rajpopat27/relay-flow/internal/run"
	"github.com/rajpopat27/relay-flow/internal/runner"
	"github.com/rajpopat27/relay-flow/internal/task"
	"github.com/rajpopat27/relay-flow/internal/workflow"
)

// Dependencies carries the replaceable boundaries used by activities.
type Dependencies struct {
	Repos      *repo.Registry
	Runner     runner.Runner
	Harness    harness.Harness
	TaskSystem string

	// RetentionDays bounds completed/canceled run retention; zero uses the
	// machine default of 30 days.
	RetentionDays int
	// Runtime is copied into every new run's immutable durable snapshot. Nil
	// applies machine defaults (terminals true, sessions true).
	Runtime *run.RuntimePolicy
}

// Engine is the durable executor. It implements run.Executor and
// run.RunQueries.
type Engine struct {
	backend    backend.Backend
	db         *sql.DB
	client     *client.Client
	wfWorker   *worker.Worker
	actWorker  *worker.Worker
	activities *Activities
	runs       *RunProjection
	retention  time.Duration
	runtime    run.RuntimePolicy

	mu        sync.RWMutex
	cancelMu  sync.Mutex                    // serializes cancellation history check + request
	snapshots map[run.ID]*workflow.Workflow // in-memory cache; history is authoritative

	workerCtx    context.Context
	workerCancel context.CancelFunc
	shutdownOnce sync.Once
	workerName   string
}

// InitDatabase preserves the embedded-engine public helper while delegating
// relay-owned schema lifecycle to the shared projection package.
func InitDatabase(path string) error { return projection.InitDatabase(path) }

// HasNonterminalRuns inspects the shared projection without migrating it.
func HasNonterminalRuns(path string) (bool, error) {
	return projection.HasNonterminalRuns(path)
}

// New opens the SQLite database at path (created with mode 0600 when
// missing), migrates the relay_runs projection, and constructs the engine.
// A corrupt database file fails here.
func New(path string, deps Dependencies) (*Engine, error) {
	if deps.Repos == nil || deps.Runner == nil || deps.Harness == nil {
		return nil, fmt.Errorf("goworkflows: Repos, Runner, and Harness dependencies are required")
	}
	if deps.Runtime != nil && deps.Runtime.KeepTerminalsAlive && !deps.Runtime.KeepSessionsAlive {
		return nil, fmt.Errorf("goworkflows: keepTerminalsAlive requires keepSessionsAlive")
	}
	db, err := sql.Open("sqlite", fmt.Sprintf("file:%s?_txlock=immediate", path))
	if err != nil {
		return nil, fmt.Errorf("open %s: %w", path, err)
	}
	// Fail fast on an unusable/corrupt database file.
	if _, err := db.Exec(`PRAGMA schema_version`); err != nil {
		db.Close()
		return nil, fmt.Errorf("open %s: %w", path, err)
	}
	// SQLite allows one writer; a single connection acts as the mutex.
	if _, err := db.Exec("PRAGMA journal_mode=WAL;"); err != nil {
		db.Close()
		return nil, fmt.Errorf("open %s: %w", path, err)
	}
	if _, err := db.Exec("PRAGMA busy_timeout = 5000;"); err != nil {
		db.Close()
		return nil, fmt.Errorf("open %s: %w", path, err)
	}
	db.SetMaxOpenConns(1)
	if err := os.Chmod(path, 0o600); err != nil && !os.IsNotExist(err) {
		db.Close()
		return nil, fmt.Errorf("chmod %s: %w", path, err)
	}
	proj := &RunProjection{DB: db}
	if err := proj.migrate(); err != nil {
		db.Close()
		return nil, fmt.Errorf("migrate relay_runs: %w", err)
	}
	// A marker-less legacy database is adopted only by the embedded executor;
	// a persisted Temporal identity fails closed rather than being combined
	// with go-workflows state.
	if err := (&projection.RunProjection{DB: db}).VerifyIdentity(context.Background(), projection.ExecutorIdentity{ExecutorPlugin: "goworkflows"}); err != nil {
		db.Close()
		return nil, fmt.Errorf("verify executor identity: %w", err)
	}
	activities := &Activities{
		Repos:      deps.Repos,
		Runner:     deps.Runner,
		Harness:    deps.Harness,
		TaskSystem: deps.TaskSystem,
		Runs:       proj,
	}
	retention := 30 * 24 * time.Hour
	if deps.RetentionDays > 0 {
		retention = time.Duration(deps.RetentionDays) * 24 * time.Hour
	}
	runtimePolicy := run.RuntimePolicy{KeepTerminalsAlive: true, KeepSessionsAlive: true}
	if deps.Runtime != nil {
		runtimePolicy = *deps.Runtime
	}
	return &Engine{
		db:         db,
		activities: activities,
		runs:       proj,
		retention:  retention,
		runtime:    runtimePolicy,
		snapshots:  map[run.ID]*workflow.Workflow{},
	}, nil
}

// Start opens the go-workflows SQLite backend on the same database, starts
// one workflow worker (max 10 parallel workflow tasks) and one activity
// worker (max 20 parallel activities), runs the startup retention sweep, and
// resumes pending engine tasks automatically.
func (e *Engine) Start(ctx context.Context) error {
	e.workerName = "relay-flow-" + uuid.NewString()
	e.backend = sqlite.NewSqliteBackendWithDB(e.db, sqlite.WithApplyMigrations(true),
		sqlite.WithBackendOptions(backend.WithWorkerName(e.workerName)))
	e.client = client.New(e.backend)

	wfOpts := worker.DefaultOptions.WorkflowWorkerOptions
	wfOpts.MaxParallelWorkflowTasks = 10
	e.wfWorker = worker.NewWorkflowWorker(e.backend, &wfOpts)
	actOpts := worker.DefaultOptions.ActivityWorkerOptions
	actOpts.MaxParallelActivityTasks = 20
	e.actWorker = worker.NewActivityWorker(e.backend, &actOpts)
	if err := e.wfWorker.RegisterWorkflow(e.activities.TicketWorkflow); err != nil {
		return fmt.Errorf("register TicketWorkflow: %w", err)
	}
	if err := e.registerActivities(); err != nil {
		return err
	}
	e.workerCtx, e.workerCancel = context.WithCancel(context.Background())
	if err := e.wfWorker.Start(e.workerCtx); err != nil {
		return fmt.Errorf("start workflow worker: %w", err)
	}
	if err := e.actWorker.Start(e.workerCtx); err != nil {
		return fmt.Errorf("start activity worker: %w", err)
	}
	// A cancellation request can outlive the engine instance that accepted it.
	// Reconcile those projections before normal pollers start; a missing
	// workflow instance is a confirmed terminal execution boundary, while
	// other lookup failures remain retryable and are logged below.
	e.reconcileCancelingRuns(ctx)
	// Startup retention sweep (pre-poller window): remove old terminal
	// projection rows and their engine histories; nonterminal runs stay.
	cutoff := time.Now().Add(-e.retention)
	ids, err := e.runs.sweepRetention(ctx, cutoff)
	if err != nil {
		return fmt.Errorf("retention sweep: %w", err)
	}
	if len(ids) > 0 {
		if err := e.client.RemoveWorkflowInstances(ctx, backend.RemoveFinishedBefore(cutoff)); err != nil {
			return fmt.Errorf("remove finished engine histories: %w", err)
		}
	}
	return nil
}

func (e *Engine) registerActivities() error {
	a := e.activities
	for _, act := range []goworkflow.Activity{
		a.EnsureMailboxes,
		a.PrepareRestart,
		a.ValidateAgents,
		a.ApplyTaskConfig,
		a.EnsureEnvironment,
		a.SetEnvironmentStatus,
		a.LoadNodeRuntime,
		a.LoadCancellationReason,
		a.EnsureNodeRuntime,
		a.CloseTerminals,
		a.CleanupRun,
		a.CheckpointNodeRuntime,
		a.FinalizeNodeRuntimes,
		a.Comment,
		a.CompleteMailbox,
		a.ProjectionUpdateNodeRuntimeVisit,
		a.ProjectionRecordProcessedReport,
		a.ProjectionUpsertStep,
		a.ProjectionUpdateNode,
		a.ProjectionUpdateState,
		a.ProjectionUpdateRetry,
	} {
		if err := e.actWorker.RegisterActivity(act); err != nil {
			return fmt.Errorf("register activity: %w", err)
		}
	}
	return nil
}

// Shutdown cancels worker polling, waits a bounded time for active tasks,
// and closes SQLite. It is safe to call more than once.
func (e *Engine) Shutdown(ctx context.Context) error {
	var err error
	e.shutdownOnce.Do(func() {
		if e.workerCancel != nil {
			e.workerCancel()
		}
		// wfWorker/actWorker are nil until Start; tolerate Shutdown before
		// Start so fail-fast startup validation can release the database.
		if e.wfWorker != nil && e.actWorker != nil {
			done := make(chan struct{}, 2)
			go func() { _ = e.wfWorker.WaitForCompletion(); done <- struct{}{} }()
			go func() { _ = e.actWorker.WaitForCompletion(); done <- struct{}{} }()
			for i := 0; i < 2; i++ {
				select {
				case <-ctx.Done():
					i = 2
				case <-done:
				}
			}
		}
		// Release this worker's workflow-task leases: a stopped worker must
		// not hold instances hostage until the lock timeout. Leases are
		// crash-recovery primitives; the next engine re-locks on pickup.
		if e.workerName != "" {
			_, _ = e.db.Exec(`UPDATE instances SET locked_until = NULL, sticky_until = NULL, worker = NULL WHERE worker = ?`, e.workerName)
		}
		err = e.db.Close()
	})
	return err
}

// EnsureRun creates the durable run when missing (created=true). For an
// existing active run it reconciles the current node terminal by stable
// title and sends the reconcile signal only when that terminal is missing
// or unusable. Repeated polls are harmless.
func (e *Engine) EnsureRun(ctx context.Context, start run.Start) (bool, error) {
	if start.LogicalID == "" {
		start.LogicalID = run.ID(identity.LogicalRunID(start.ID))
	}
	if start.AttemptID == 0 {
		start.AttemptID = 1
	}
	r, err := e.runs.get(ctx, start.ID)
	if errors.Is(err, errRunNotFound) {
		start.Runtime = e.runtime
		if err := e.runs.insertStart(ctx, start, time.Now().UTC()); err != nil {
			return false, fmt.Errorf("insert run %s: %w", start.ID, err)
		}
		_, err = e.client.CreateWorkflowInstance(ctx,
			client.WorkflowInstanceOptions{InstanceID: string(start.ID)},
			e.activities.TicketWorkflow, start)
		if err != nil {
			if errors.Is(err, backend.ErrInstanceAlreadyExists) {
				return false, nil
			}
			return false, fmt.Errorf("create workflow instance %s: %w", start.ID, err)
		}
		e.mu.Lock()
		wf := start.Workflow
		e.snapshots[start.ID] = &wf
		e.mu.Unlock()
		// 9.3 run-lifecycle logging: one info line on run creation.
		slog.Info("run created",
			"ticket", start.Ticket.Key, "runID", string(start.ID),
			"repo", start.Repo, "workflow", wf.Name)
		return true, nil
	}
	if err != nil {
		return false, err
	}
	if r.State != run.StateCompleted && r.State != run.StateCanceled && r.State != run.StateCanceling {
		if _, err := e.instance(ctx, start.ID); errors.Is(err, sql.ErrNoRows) {
			start.Runtime = e.runtime
			if _, err := e.client.CreateWorkflowInstance(ctx,
				client.WorkflowInstanceOptions{InstanceID: string(start.ID)},
				e.activities.TicketWorkflow, start); err != nil && !errors.Is(err, backend.ErrInstanceAlreadyExists) {
				return false, fmt.Errorf("create missing workflow instance %s: %w", start.ID, err)
			}
			return true, nil
		} else if err != nil {
			return false, err
		}
	}
	// Existing run: reconcile only an active run at a work node.
	if r.State == run.StateCompleted || r.State == run.StateCanceled || r.State == run.StateCanceling {
		return false, nil
	}
	if r.CurrentNode == "" || r.CurrentNodeVisitID == "" {
		return false, nil
	}
	runtime, err := e.runs.getNodeRuntime(ctx, r.ID, r.CurrentNode)
	if err != nil && !errors.Is(err, errNodeRuntimeNotFound) {
		return false, fmt.Errorf("load runtime for %s/%s: %w", r.ID, r.CurrentNode, err)
	}
	ok := false
	if runtime.TerminalID != "" {
		_, ok, _ = e.activities.Runner.FindTerminal(ctx, runner.Terminal{
			ID: runtime.TerminalID, Title: r.Ticket.Key + ":" + r.CurrentNode,
		})
	}
	if ok {
		return false, nil // live usable terminal: no reconcile, no relaunch
	}
	if err := e.client.SignalWorkflow(ctx, string(r.ID), reconcileSignal, struct{}{}); err != nil {
		return false, fmt.Errorf("signal reconcile for %s: %w", r.ID, err)
	}
	return false, nil
}

// SubmitReport drops processed report IDs immediately. New reports are
// validated and acknowledged only after their workflow signal is durable.
//
// 9.4 report-path logging: one info line per event on the report path —
// received, duplicate ack, validation failure, signal persisted, ack sent.
// Attrs always carry ticket/runID/node/nodeVisitID when known.
func (e *Engine) SubmitReport(ctx context.Context, req run.ReportRequest) (run.ReportAck, error) {
	r, err := e.runs.get(ctx, req.RunID)
	if errors.Is(err, errRunNotFound) {
		// A retained newer attempt can outlive an old attempt row. Resolve the
		// stable logical ID and acknowledge the old attempt as a stale
		// duplicate; it must never be validated or signaled into the new run.
		logicalID := run.ID(identity.LogicalRunID(req.RunID))
		if latest, lookupErr := e.runs.findByLogicalID(ctx, logicalID); lookupErr == nil && latest.ID != req.RunID {
			slog.Info("report duplicate ack", "ticket", latest.Ticket.Key,
				"runID", string(req.RunID), "logicalRunID", string(logicalID),
				"node", req.Node, "reportID", req.ReportID, "state", string(latest.State))
			return run.ReportAck{Accepted: true, Duplicate: true}, nil
		}
	}
	if err != nil {
		return run.ReportAck{}, fmt.Errorf("resolve run %s: %w", req.RunID, err)
	}
	attrs := []any{
		"ticket", r.Ticket.Key, "runID", string(req.RunID),
		"repo", r.Repo, "workflow", r.Workflow,
		"node", req.Node, "reportID", req.ReportID,
	}
	processed, err := e.runs.hasProcessedReport(ctx, req.RunID, req.ReportID)
	if err != nil {
		return run.ReportAck{}, fmt.Errorf("check report %s: %w", req.ReportID, err)
	}
	if processed {
		slog.Info("report duplicate ack", append(attrs, "state", string(r.State))...)
		return run.ReportAck{Accepted: true, Duplicate: true}, nil
	}
	slog.Info("report received", append(attrs,
		"status", string(req.Report.Status), "nextStep", req.Report.NextStep)...)

	current := r.CurrentNodeVisitID != "" && req.Node == r.CurrentNode &&
		r.State != run.StateCompleted && r.State != run.StateCanceled
	if !current {
		slog.Info("report duplicate ack", append(attrs, "state", string(r.State))...)
		return run.ReportAck{Accepted: true, Duplicate: true}, nil
	}
	wf, err := e.workflowOf(ctx, req.RunID)
	if err != nil {
		return run.ReportAck{}, err
	}
	if err := wf.ValidateReport(req.Node, req.Report); err != nil {
		slog.Info("report validation failed", append(attrs, "reason", err.Error())...)
		return run.ReportAck{}, &run.InvalidReportError{Reason: err.Error()}
	}
	signal := reportSignal{
		ReportID: req.ReportID, Node: req.Node,
		NodeVisitID: r.CurrentNodeVisitID, Report: req.Report,
	}
	if err := e.client.SignalWorkflow(ctx, string(req.RunID), reportSignalName, signal); err != nil {
		return run.ReportAck{}, fmt.Errorf("signal report %s for %s: %w", req.ReportID, req.RunID, err)
	}
	attrs = append(attrs, "nodeVisitID", string(r.CurrentNodeVisitID))
	// 9.3 transition effect + 9.4 report path: durable signal persisted
	// (ack only after persistence per the report contract). One info line
	// on the first accepted signal; duplicate/stale acks above skip this.
	slog.Info("report persisted", append(attrs, "node", r.CurrentNode)...)
	slog.Info("report ack sent", append(attrs, "node", r.CurrentNode)...)
	return run.ReportAck{Accepted: true}, nil
}

// HasProcessedReport supports the server's payload-independent duplicate
// short circuit.
func (e *Engine) HasProcessedReport(ctx context.Context, id run.ID, reportID string) (bool, error) {
	return e.runs.hasProcessedReport(ctx, id, reportID)
}

// RegisterNodeSession persists the OpenCode session for its stable run/node.
func (e *Engine) RegisterNodeSession(ctx context.Context, registration run.NodeRuntimeRegistration) (run.NodeRuntimeRegistrationAck, error) {
	accepted, err := e.runs.registerNodeSession(ctx, registration)
	if err != nil {
		return run.NodeRuntimeRegistrationAck{}, err
	}
	return run.NodeRuntimeRegistrationAck{Accepted: accepted}, nil
}

// GetNodeRuntime returns one persisted per-node runtime binding.
func (e *Engine) GetNodeRuntime(ctx context.Context, id run.ID, node string) (NodeRuntime, error) {
	return e.runs.getNodeRuntime(ctx, id, node)
}

// workflowOf returns the run's immutable workflow snapshot: the in-memory
// cache when present, otherwise decoded from the instance's started-event
// inputs in durable history.
func (e *Engine) workflowOf(ctx context.Context, id run.ID) (*workflow.Workflow, error) {
	e.mu.RLock()
	wf, ok := e.snapshots[id]
	e.mu.RUnlock()
	if ok {
		return wf, nil
	}
	inst, err := e.instance(ctx, id)
	if err != nil {
		return nil, err
	}
	events, err := e.backend.GetWorkflowInstanceHistory(ctx, inst, nil)
	if err != nil {
		return nil, fmt.Errorf("load history for %s: %w", id, err)
	}
	for _, ev := range events {
		if ev.Type != history.EventType_WorkflowExecutionStarted {
			continue
		}
		attr, ok := ev.Attributes.(*history.ExecutionStartedAttributes)
		if !ok || len(attr.Inputs) == 0 {
			continue
		}
		var start run.Start
		if err := converter.DefaultConverter.From(attr.Inputs[0], &start); err != nil {
			return nil, fmt.Errorf("decode snapshot for %s: %w", id, err)
		}
		w := start.Workflow
		e.mu.Lock()
		e.snapshots[id] = &w
		e.mu.Unlock()
		return &w, nil
	}
	return nil, fmt.Errorf("no workflow snapshot in history for run %s", id)
}

// CancelRun requests cancellation of the durable workflow. The projection
// transition is compare-and-set: a concurrent completion wins if it reaches a
// terminal state first, while a persisted canceling state is the durable
// request that startup reconciliation retries.
func (e *Engine) CancelRun(ctx context.Context, id run.ID, reason string) error {
	r, err := e.runs.beginCancellation(ctx, id, reason)
	if err != nil {
		return fmt.Errorf("resolve run %s: %w", id, err)
	}
	if r.State == run.StateCompleted || r.State == run.StateCanceled {
		return nil
	}
	if r.State != run.StateCanceling {
		return fmt.Errorf("cancel %s: state changed to %s", id, r.State)
	}
	// The reason persisted by the first successful CAS is authoritative for
	// every later cancellation request.
	return e.reconcileCancellation(ctx, r, r.LastError)
}

// isMissingWorkflowInstance identifies only confirmed absence of the active
// go-workflows execution. Database and context failures remain retryable.
func isMissingWorkflowInstance(err error) bool {
	return errors.Is(err, sql.ErrNoRows) || errors.Is(err, backend.ErrInstanceNotFound)
}

// detachedContext preserves a caller deadline while detaching cancellation
// from the caller. Startup cleanup must not be able to outlive its bounded
// reconciliation window.
func detachedContext(ctx context.Context) (context.Context, context.CancelFunc) {
	base := context.WithoutCancel(ctx)
	if deadline, ok := ctx.Deadline(); ok {
		return context.WithDeadline(base, deadline)
	}
	return base, func() {}
}

// lookupInstance returns the active execution when present. If only a finished
// row remains, finished is true; a missing row is the only absence outcome.
func (e *Engine) lookupInstance(ctx context.Context, id run.ID) (*goworkflow.Instance, bool, error) {
	var execID string
	var state int
	err := e.db.QueryRowContext(ctx, `
		SELECT execution_id, state FROM instances WHERE id = ?
		ORDER BY CASE WHEN state = 0 THEN 0 ELSE 1 END, rowid DESC LIMIT 1`, string(id)).Scan(&execID, &state)
	if err != nil {
		return nil, false, fmt.Errorf("workflow instance %s not found: %w", id, err)
	}
	return &goworkflow.Instance{InstanceID: string(id), ExecutionID: execID}, state != 0, nil
}

// reconcileCancellation retries the durable cancellation request while an
// execution is active and reconciles finished/missing executions separately.
func (e *Engine) reconcileCancellation(ctx context.Context, r run.Run, reason string) error {
	inst, finished, err := e.lookupInstance(ctx, r.ID)
	if err != nil {
		if isMissingWorkflowInstance(err) {
			return e.finalizeMissingCancellation(ctx, r, reason)
		}
		return fmt.Errorf("resolve workflow instance: %w", err)
	}
	if finished {
		return e.reconcileFinishedCancellation(ctx, r, inst, reason)
	}
	if e.client == nil {
		return errors.New("workflow engine is not started")
	}
	if err := e.requestCancellation(ctx, inst); err != nil {
		if isMissingWorkflowInstance(err) {
			return e.finalizeMissingCancellation(ctx, r, reason)
		}
		return err
	}
	return nil
}

// requestCancellation checks durable history and appends at most one
// cancellation event for an execution. The mutex closes the check/request
// race between concurrent operator calls in this process; the relay lock
// prevents another relay-flow server from owning the same database.
func (e *Engine) requestCancellation(ctx context.Context, inst *goworkflow.Instance) error {
	e.cancelMu.Lock()
	defer e.cancelMu.Unlock()
	var requested int
	if err := e.db.QueryRowContext(ctx, `
		SELECT EXISTS(
			SELECT 1 FROM history
			WHERE instance_id = ? AND execution_id = ? AND event_type = ?
			UNION ALL
			SELECT 1 FROM pending_events
			WHERE instance_id = ? AND execution_id = ? AND event_type = ?
		)`,
		inst.InstanceID, inst.ExecutionID, history.EventType_WorkflowExecutionCanceled,
		inst.InstanceID, inst.ExecutionID, history.EventType_WorkflowExecutionCanceled,
	).Scan(&requested); err != nil {
		return fmt.Errorf("inspect cancellation history: %w", err)
	}
	if requested != 0 {
		return nil
	}
	return e.client.CancelWorkflowInstance(ctx, inst)
}

// finalizeMissingCancellation performs the same roll-forward cleanup as the
// workflow's disconnected cancellation path when the workflow instance has
// genuinely disappeared. It intentionally does not recreate an execution.
func (e *Engine) finalizeMissingCancellation(ctx context.Context, r run.Run, reason string) error {
	cleanupCtx, cleanupCancel := detachedContext(ctx)
	defer cleanupCancel()
	current, err := e.runs.get(cleanupCtx, r.ID)
	if err != nil {
		return fmt.Errorf("reconcile canceled run %s projection: %w", r.ID, err)
	}
	if current.State == run.StateCompleted || current.State == run.StateCanceled {
		return nil
	}
	if current.State != run.StateCanceling {
		return fmt.Errorf("reconcile canceled run %s: state changed to %s", r.ID, current.State)
	}
	if inst, finished, lookupErr := e.lookupInstance(cleanupCtx, r.ID); lookupErr == nil {
		if finished {
			return e.reconcileFinishedCancellation(cleanupCtx, current, inst, reason)
		}
		return fmt.Errorf("reconcile canceled run %s: workflow instance reappeared", r.ID)
	} else if !isMissingWorkflowInstance(lookupErr) {
		return fmt.Errorf("reconcile canceled run %s instance lookup: %w", r.ID, lookupErr)
	}
	return e.finalizeCancellationEffects(cleanupCtx, current, reason)
}

// finalizeCancellationEffects is shared by missing and already-canceled
// engine executions. The final projection transition is conditional so a
// concurrent terminal projection cannot be overwritten.
func (e *Engine) finalizeCancellationEffects(ctx context.Context, r run.Run, reason string) error {
	if r.State == run.StateCompleted || r.State == run.StateCanceled {
		return nil
	}
	if r.State != run.StateCanceling {
		return fmt.Errorf("finalize canceled run %s: state changed to %s", r.ID, r.State)
	}
	repoInfo, ok := e.activities.Repos.Get(r.Repo)
	if !ok {
		return fmt.Errorf("reconcile canceled run %s: repo %q is no longer registered", r.ID, r.Repo)
	}
	work := run.Work{
		RunID:     r.ID,
		LogicalID: r.LogicalID,
		AttemptID: r.AttemptID,
		Repo:      r.Repo,
		Workflow:  r.Workflow,
		Parent:    r.Ticket,
		Runtime:   e.runtime,
	}
	if reason == "" {
		reason = "operator requested cancellation"
	}
	if err := e.activities.FinalizeNodeRuntimes(ctx, work, repoInfo.Path, e.runtime); err != nil {
		return fmt.Errorf("finalize canceled run %s terminals: %w", r.ID, err)
	}
	markerID := r.LogicalID
	if markerID == "" {
		markerID = r.ID
	}
	if err := e.activities.Comment(ctx, r.Repo, run.CommentWork{
		RunID:  r.ID,
		Item:   task.Target{Parent: r.Ticket},
		Body:   "Run canceled: " + reason,
		Marker: run.CancellationMarker(markerID),
	}); err != nil {
		return fmt.Errorf("finalize canceled run %s comment: %w", r.ID, err)
	}
	finished := time.Now().UTC()
	updated, err := e.runs.updateStateIf(ctx, r.ID, run.StateCanceling, run.StateCanceled, "", &finished)
	if err != nil {
		return fmt.Errorf("finalize canceled run %s projection: %w", r.ID, err)
	}
	if !updated {
		latest, getErr := e.runs.get(ctx, r.ID)
		if getErr == nil && (latest.State == run.StateCanceled || latest.State == run.StateCompleted) {
			return nil
		}
		if getErr != nil {
			return fmt.Errorf("finalize canceled run %s projection after race: %w", r.ID, getErr)
		}
		return fmt.Errorf("finalize canceled run %s: state changed to %s", r.ID, latest.State)
	}
	slog.Info("run canceled", "ticket", r.Ticket.Key, "runID", string(r.ID),
		"repo", r.Repo, "workflow", r.Workflow, "state", run.StateCanceled)
	return nil
}

// reconcileFinishedCancellation reads the terminal engine event before
// deciding whether a canceling projection should become completed or canceled.
func (e *Engine) reconcileFinishedCancellation(ctx context.Context, r run.Run, inst *goworkflow.Instance, reason string) error {
	if e.backend == nil {
		return errors.New("workflow backend is not started")
	}
	events, err := e.backend.GetWorkflowInstanceHistory(ctx, inst, nil)
	if err != nil {
		return fmt.Errorf("read finished workflow history: %w", err)
	}
	var finishedEvent *history.Event
	canceledBeforeFinish := false
	for _, event := range events {
		switch event.Type {
		case history.EventType_WorkflowExecutionCanceled:
			// TicketWorkflow records this cancellation request first; after
			// cancelCleanup returns, go-workflows may append a normal
			// WorkflowExecutionFinished event. The earlier cancellation event
			// remains authoritative for relay-flow semantics.
			if finishedEvent == nil {
				canceledBeforeFinish = true
			}
		case history.EventType_WorkflowExecutionFinished:
			if finishedEvent == nil {
				finishedEvent = event
			}
		case history.EventType_WorkflowExecutionTerminated:
			return fmt.Errorf("workflow %s terminated without a cancellation or completion result", r.ID)
		}
	}
	if canceledBeforeFinish {
		return e.finalizeCancellationEffects(ctx, r, reason)
	}
	if finishedEvent == nil {
		return fmt.Errorf("finished workflow %s has no terminal history event", r.ID)
	}
	{
		finished := finishedEvent.Timestamp
		updated, err := e.runs.updateStateIf(ctx, r.ID, run.StateCanceling, run.StateCompleted, "", &finished)
		if err != nil {
			return fmt.Errorf("reconcile completed run %s projection: %w", r.ID, err)
		}
		if !updated {
			latest, getErr := e.runs.get(ctx, r.ID)
			if getErr == nil && (latest.State == run.StateCompleted || latest.State == run.StateCanceled) {
				return nil
			}
			if getErr != nil {
				return fmt.Errorf("reconcile completed run %s projection after race: %w", r.ID, getErr)
			}
			return fmt.Errorf("reconcile completed run %s: state changed to %s", r.ID, latest.State)
		}
		return nil
	}
}

// reconcileCancelingRuns retries the durable cancellation request for every
// stale canceling projection. An active instance is not skipped: its cancel
// event is re-submitted until the workflow accepts it.
func (e *Engine) reconcileCancelingRuns(ctx context.Context) {
	active := true
	runs, err := e.runs.list(ctx, run.Filter{Active: &active})
	if err != nil {
		slog.Warn("reconcile canceling runs unavailable", "error", err)
		return
	}
	for _, r := range runs {
		if r.State != run.StateCanceling {
			continue
		}
		if err := e.retryStartupCancellation(ctx, r); err != nil {
			slog.Warn("reconcile canceling run failed", "runID", r.ID, "error", err)
		}
	}
}

// retryStartupCancellation gives a durable canceling projection a bounded
// startup retry window. Normal polling deliberately does not recreate or
// advance canceling runs, so an active execution must receive its cancel
// event here or remain visibly retryable for the next restart.
func (e *Engine) retryStartupCancellation(ctx context.Context, r run.Run) error {
	retryCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	return retry.Do(retryCtx, retry.DefaultBackoffPolicy, func() error {
		return e.reconcileCancellation(retryCtx, r, r.LastError)
	})
}

// instance resolves the current active execution for the durable run ID.
func (e *Engine) instance(ctx context.Context, id run.ID) (*goworkflow.Instance, error) {
	var execID string
	err := e.db.QueryRowContext(ctx,
		`SELECT execution_id FROM instances WHERE id = ? AND state = ? ORDER BY rowid DESC LIMIT 1`,
		string(id), 0).Scan(&execID)
	if err != nil {
		return nil, fmt.Errorf("workflow instance %s not found: %w", id, err)
	}
	return &goworkflow.Instance{InstanceID: string(id), ExecutionID: execID}, nil
}

// --- run.RunQueries ---

func (e *Engine) GetRun(ctx context.Context, id run.ID) (run.Run, error) {
	return e.runs.get(ctx, id)
}

func (e *Engine) FindRunByTicket(ctx context.Context, ticket string) (run.Run, error) {
	return e.runs.findByTicket(ctx, ticket)
}

func (e *Engine) GetRunDetail(ctx context.Context, id run.ID) (run.RunDetail, error) {
	detail, err := e.runs.getDetail(ctx, id)
	if err != nil {
		return run.RunDetail{}, err
	}
	// Static definitions are used only to add explicit pending display rows;
	// the projection remains a cache and never influences execution.
	if wf, wfErr := e.workflowOf(ctx, id); wfErr == nil {
		detail.AddPendingNodes(*wf)
		detail.DeriveInspectionFields(time.Now().UTC())
	}
	return detail, nil
}

func (e *Engine) ListRuns(ctx context.Context, filter run.Filter) ([]run.Run, error) {
	return e.runs.list(ctx, filter)
}

func (e *Engine) HasActiveWorkflow(ctx context.Context, wf string) (bool, error) {
	return e.runs.hasActive(ctx, "workflow", wf)
}

func (e *Engine) HasActiveRepo(ctx context.Context, repo string) (bool, error) {
	return e.runs.hasActive(ctx, "repo", repo)
}
