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

	"github.com/rajpopat27/relay-flow/internal/harness"
	"github.com/rajpopat27/relay-flow/internal/identity"
	"github.com/rajpopat27/relay-flow/internal/repo"
	"github.com/rajpopat27/relay-flow/internal/run"
	"github.com/rajpopat27/relay-flow/internal/runner"
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
	snapshots map[run.ID]*workflow.Workflow // in-memory cache; history is authoritative

	workerCtx    context.Context
	workerCancel context.CancelFunc
	shutdownOnce sync.Once
	workerName   string
}

// InitDatabase creates the SQLite database at path (mode 0600) with the
// relay_runs projection schema and closes it. Used by `relay-flow init`;
// serve uses New to open the full engine.
func InitDatabase(path string) error {
	db, err := sql.Open("sqlite", fmt.Sprintf("file:%s?_txlock=immediate", path))
	if err != nil {
		return fmt.Errorf("open %s: %w", path, err)
	}
	defer db.Close()
	if _, err := db.Exec(`PRAGMA schema_version`); err != nil {
		return fmt.Errorf("open %s: %w", path, err)
	}
	if _, err := db.Exec("PRAGMA journal_mode=WAL;"); err != nil {
		return fmt.Errorf("open %s: %w", path, err)
	}
	if err := os.Chmod(path, 0o600); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("chmod %s: %w", path, err)
	}
	proj := &RunProjection{DB: db}
	if err := proj.migrate(); err != nil {
		return fmt.Errorf("migrate relay_runs: %w", err)
	}
	return nil
}

// HasNonterminalRuns inspects an existing database without migrating or
// otherwise modifying it. It is used by init --force before config changes.
func HasNonterminalRuns(path string) (bool, error) {
	db, err := sql.Open("sqlite", fmt.Sprintf("file:%s?mode=ro", path))
	if err != nil {
		return false, fmt.Errorf("open %s: %w", path, err)
	}
	defer db.Close()
	var active bool
	if err := db.QueryRow(`SELECT EXISTS(
		SELECT 1 FROM relay_runs WHERE state NOT IN ('completed', 'canceled')
	)`).Scan(&active); err != nil {
		return false, fmt.Errorf("inspect %s: %w", path, err)
	}
	return active, nil
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
		a.EnsureNodeRuntime,
		a.CloseTerminals,
		a.CleanupRun,
		a.CheckpointNodeRuntime,
		a.FinalizeNodeRuntimes,
		a.Comment,
		a.CompleteMailbox,
		a.ProjectionUpdateNodeRuntimeVisit,
		a.ProjectionRecordProcessedReport,
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
		return run.ReportAck{Accepted: false}, err
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

// CancelRun cancels the workflow instance; cleanup runs on a disconnected
// workflow context and cannot interrupt an already-running activity.
func (e *Engine) CancelRun(ctx context.Context, id run.ID, reason string) error {
	if _, err := e.runs.get(ctx, id); err != nil {
		return fmt.Errorf("resolve run %s: %w", id, err)
	}
	if err := e.runs.updateState(ctx, id, run.StateCanceling, reason, nil); err != nil {
		return err
	}
	inst, err := e.instance(ctx, id)
	if err != nil {
		return fmt.Errorf("cancel %s: %w", id, err)
	}
	if err := e.client.CancelWorkflowInstance(ctx, inst); err != nil {
		return fmt.Errorf("cancel %s: %w", id, err)
	}
	return nil
}

// instance resolves the current execution for the durable run ID.
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

func (e *Engine) ListRuns(ctx context.Context, filter run.Filter) ([]run.Run, error) {
	return e.runs.list(ctx, filter)
}

func (e *Engine) HasActiveWorkflow(ctx context.Context, wf string) (bool, error) {
	return e.runs.hasActive(ctx, "workflow", wf)
}

func (e *Engine) HasActiveRepo(ctx context.Context, repo string) (bool, error) {
	return e.runs.hasActive(ctx, "repo", repo)
}
