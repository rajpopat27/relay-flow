// Package temporal implements the Temporal durable executor. Temporal owns
// workflow history, signals, timers, and activity checkpoints; SQLite stores
// only the relay-owned derived projection.
package temporal

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"sync"
	"time"

	"github.com/rajpopat27/relay-flow/internal/execution/projection"
	"github.com/rajpopat27/relay-flow/internal/harness"
	"github.com/rajpopat27/relay-flow/internal/repo"
	"github.com/rajpopat27/relay-flow/internal/run"
	"github.com/rajpopat27/relay-flow/internal/runner"
	"go.temporal.io/api/serviceerror"
	workflowservice "go.temporal.io/api/workflowservice/v1"
	"go.temporal.io/sdk/client"
	temporalworker "go.temporal.io/sdk/worker"
	"go.temporal.io/sdk/workflow"
	_ "modernc.org/sqlite"
)

const (
	TaskQueue          = "relay-flow"
	TicketWorkflowName = "TicketWorkflow"
	minRetention       = 30 * 24 * time.Hour
)

// Dependencies carries the replaceable boundaries used by Temporal
// activities. Recover is consumed only by Start and is never part of the
// workflow input snapshot.
type Dependencies struct {
	Repos             *repo.Registry
	Runner            runner.Runner
	Harness           harness.Harness
	TaskSystem        string
	RetentionDays     int
	Runtime           *run.RuntimePolicy
	TemporalAddress   string
	TemporalNamespace string
	Recover           bool
}

// Engine implements both engine-neutral run boundaries.
type Engine struct {
	db                 *sql.DB
	runs               *projection.RunProjection
	deps               Dependencies
	runtime            run.RuntimePolicy
	retention          time.Duration
	namespaceRetention time.Duration

	client     client.Client
	worker     temporalworker.Worker
	activities *Activities

	mu           sync.Mutex
	lifecycleMu  sync.Mutex
	started      bool
	shutdownOnce sync.Once
	fatal        chan error
}

// InitDatabase initializes only the shared relay projection. It does not
// contact Temporal or create an engine-owned history schema.
func InitDatabase(path string) error {
	db, err := openDatabase(path)
	if err != nil {
		return err
	}
	return db.Close()
}

// HasNonterminalRuns inspects the shared projection without migrating it.
func HasNonterminalRuns(path string) (bool, error) { return projection.HasNonterminalRuns(path) }

func openDatabase(path string) (*sql.DB, error) {
	db, err := sql.Open("sqlite", fmt.Sprintf("file:%s?_txlock=immediate", path))
	if err != nil {
		return nil, fmt.Errorf("open %s: %w", path, err)
	}
	db.SetMaxOpenConns(1)
	if _, err := db.Exec(`PRAGMA schema_version`); err != nil {
		db.Close()
		return nil, fmt.Errorf("open %s: %w", path, err)
	}
	if _, err := db.Exec("PRAGMA journal_mode=WAL;"); err != nil {
		db.Close()
		return nil, fmt.Errorf("open %s: %w", path, err)
	}
	if _, err := db.Exec("PRAGMA busy_timeout = 5000;"); err != nil {
		db.Close()
		return nil, fmt.Errorf("open %s: %w", path, err)
	}
	if err := os.Chmod(path, 0o600); err != nil && !os.IsNotExist(err) {
		db.Close()
		return nil, fmt.Errorf("chmod %s: %w", path, err)
	}
	proj := &projection.RunProjection{DB: db}
	if err := proj.Migrate(); err != nil {
		db.Close()
		return nil, fmt.Errorf("migrate relay projection: %w", err)
	}
	return db, nil
}

func validateDependencies(deps Dependencies) error {
	if deps.Repos == nil || deps.Runner == nil || deps.Harness == nil {
		return errors.New("temporal: Repos, Runner, and Harness dependencies are required")
	}
	if deps.Runtime != nil && deps.Runtime.KeepTerminalsAlive && !deps.Runtime.KeepSessionsAlive {
		return errors.New("temporal: keepTerminalsAlive requires keepSessionsAlive")
	}
	if deps.TemporalAddress == "" {
		return errors.New("temporal: TemporalAddress is required")
	}
	if deps.TemporalNamespace == "" {
		return errors.New("temporal: TemporalNamespace is required")
	}
	if deps.TemporalNamespace == client.DefaultNamespace {
		return fmt.Errorf("temporal: TemporalNamespace must be a dedicated named namespace, not %q", client.DefaultNamespace)
	}
	return nil
}

// New opens and migrates the relay projection, then verifies the immutable
// installation identity. A recovery construction creates the fresh marker
// after serve has moved the old projection aside; normal construction fails
// closed when the marker is missing or mismatched.
func New(path string, deps Dependencies) (*Engine, error) {
	if err := validateDependencies(deps); err != nil {
		return nil, err
	}
	db, err := openDatabase(path)
	if err != nil {
		return nil, err
	}
	proj := &projection.RunProjection{DB: db}
	identity := projection.ExecutorIdentity{
		ExecutorPlugin: "temporal", TemporalAddress: deps.TemporalAddress, TemporalNamespace: deps.TemporalNamespace,
	}
	identityErr := proj.VerifyIdentity(context.Background(), identity)
	if deps.Recover {
		identityErr = proj.InitializeIdentity(context.Background(), identity)
	}
	if identityErr != nil {
		db.Close()
		return nil, identityErr
	}
	retention := minRetention
	if deps.RetentionDays > 0 {
		retention = time.Duration(deps.RetentionDays) * 24 * time.Hour
	}
	namespaceRetention := minRetention
	if retention > namespaceRetention {
		namespaceRetention = retention
	}
	runtimePolicy := run.RuntimePolicy{KeepTerminalsAlive: true, KeepSessionsAlive: true}
	if deps.Runtime != nil {
		runtimePolicy = *deps.Runtime
	}
	return &Engine{
		db: db, runs: proj, deps: deps, runtime: runtimePolicy, retention: retention,
		namespaceRetention: namespaceRetention, fatal: make(chan error, 1),
	}, nil
}

func temporalClientOptions(deps Dependencies) client.Options {
	return client.Options{HostPort: deps.TemporalAddress, Namespace: deps.TemporalNamespace}
}

func (e *Engine) dial(ctx context.Context) error {
	if e.client != nil {
		return nil
	}
	c, err := client.Dial(temporalClientOptions(e.deps))
	if err != nil {
		return fmt.Errorf("dial Temporal %s/%s: %w", e.deps.TemporalAddress, e.deps.TemporalNamespace, err)
	}
	e.client = c
	return nil
}

func (e *Engine) verifyNamespace(ctx context.Context) error {
	description, err := e.client.WorkflowService().DescribeNamespace(ctx, &workflowservice.DescribeNamespaceRequest{Namespace: e.deps.TemporalNamespace})
	if err != nil {
		return fmt.Errorf("describe Temporal namespace %q: %w", e.deps.TemporalNamespace, err)
	}
	if description == nil || description.Config == nil || description.Config.WorkflowExecutionRetentionTtl == nil {
		return fmt.Errorf("Temporal namespace %q has no workflow retention policy", e.deps.TemporalNamespace)
	}
	if description.Config.WorkflowExecutionRetentionTtl.AsDuration() < e.namespaceRetention {
		return fmt.Errorf("Temporal namespace %q retention %s is below required %s", e.deps.TemporalNamespace, description.Config.WorkflowExecutionRetentionTtl.AsDuration(), e.namespaceRetention)
	}
	return nil
}

// waitNamespaceReady handles the local server's short frontend-cache window
// after namespace registration. It probes only public Visibility APIs and
// never creates or mutates Temporal state.
func (e *Engine) waitNamespaceReady(ctx context.Context) error {
	for {
		_, err := e.client.ListWorkflow(ctx, &workflowservice.ListWorkflowExecutionsRequest{})
		if err == nil {
			return nil
		}
		var notFound *serviceerror.NamespaceNotFound
		if !errors.As(err, &notFound) {
			return fmt.Errorf("probe Temporal namespace %q: %w", e.deps.TemporalNamespace, err)
		}
		timer := time.NewTimer(200 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
	}
}

func stopWorker(w temporalworker.Worker) {
	if w == nil {
		return
	}
	defer func() { _ = recover() }()
	w.Stop()
}

func workerOptions(onFatal func(error), localOnly bool) temporalworker.Options {
	return temporalworker.Options{
		MaxConcurrentWorkflowTaskExecutionSize: 10,
		MaxConcurrentActivityExecutionSize:     20,
		MaxConcurrentWorkflowTaskPollers:       2,
		MaxConcurrentActivityTaskPollers:       2,
		WorkerStopTimeout:                      30 * time.Second,
		OnFatalError:                           onFatal,
		LocalActivityWorkerOnly:                localOnly,
	}
}

func (e *Engine) newWorker(localOnly bool) temporalworker.Worker {
	w := temporalworker.New(e.client, TaskQueue, workerOptions(func(err error) {
		select {
		case e.fatal <- err:
		default:
		}
	}, localOnly))
	w.RegisterWorkflowWithOptions(TicketWorkflow, workflow.RegisterOptions{Name: TicketWorkflowName})
	w.RegisterActivity(e.activities)
	return w
}

// Start connects to the configured namespace, verifies retention, rebuilds a
// projection when explicitly requested, and starts one aggregate worker.
func (e *Engine) Start(ctx context.Context) error {
	e.lifecycleMu.Lock()
	defer e.lifecycleMu.Unlock()
	e.mu.Lock()
	if e.started {
		e.mu.Unlock()
		return nil
	}
	e.mu.Unlock()
	if err := e.dial(ctx); err != nil {
		return err
	}
	if err := e.verifyNamespace(ctx); err != nil {
		e.client.Close()
		e.client = nil
		return err
	}
	if err := e.waitNamespaceReady(ctx); err != nil {
		e.client.Close()
		e.client = nil
		return err
	}
	e.activities = &Activities{
		Repos: e.deps.Repos, Runner: e.deps.Runner, Harness: e.deps.Harness,
		TaskSystem: e.deps.TaskSystem, Runs: e.runs,
	}
	if e.deps.Recover {
		if err := e.rebuildProjection(ctx); err != nil {
			e.client.Close()
			e.client = nil
			return err
		}
		// Recovery retention is projection-only and must finish before the
		// normal worker is allowed to resume pending workflow tasks.
		if _, err := e.runs.SweepRetention(ctx, time.Now().UTC().Add(-e.retention)); err != nil {
			e.client.Close()
			e.client = nil
			return fmt.Errorf("recovery retention sweep: %w", err)
		}
	}
	w := e.newWorker(false)
	if err := w.Start(); err != nil {
		stopWorker(w)
		e.client.Close()
		e.client = nil
		return fmt.Errorf("start Temporal worker: %w", err)
	}
	e.worker = w
	if !e.deps.Recover {
		if _, err := e.runs.SweepRetention(ctx, time.Now().UTC().Add(-e.retention)); err != nil {
			stopWorker(w)
			e.client.Close()
			e.client = nil
			return fmt.Errorf("retention sweep: %w", err)
		}
	}
	e.mu.Lock()
	e.started = true
	e.mu.Unlock()
	return nil
}

// Shutdown is idempotent and closes worker, Temporal client, and projection
// database in that order.
func (e *Engine) Shutdown(context.Context) error {
	e.lifecycleMu.Lock()
	defer e.lifecycleMu.Unlock()
	var err error
	e.shutdownOnce.Do(func() {
		e.mu.Lock()
		w := e.worker
		c := e.client
		e.worker = nil
		e.client = nil
		e.started = false
		e.mu.Unlock()
		if w != nil {
			stopWorker(w)
		}
		if c != nil {
			c.Close()
		}
		if e.db != nil {
			err = e.db.Close()
		}
	})
	return err
}

// FatalErrors exposes worker-fatal errors to the composition root. It is
// intentionally advisory; the selected server lifecycle remains responsible
// for deciding when to stop pollers and the socket.
func (e *Engine) FatalErrors() <-chan error { return e.fatal }

// GetNodeRuntime returns one persisted runtime binding.
func (e *Engine) GetNodeRuntime(ctx context.Context, id run.ID, node string) (projection.NodeRuntime, error) {
	return e.runs.GetNodeRuntime(ctx, id, node)
}

func (e *Engine) GetRun(ctx context.Context, id run.ID) (run.Run, error) {
	return e.runs.Get(ctx, id)
}
func (e *Engine) FindRunByTicket(ctx context.Context, ticket string) (run.Run, error) {
	return e.runs.FindByTicket(ctx, ticket)
}
func (e *Engine) ListRuns(ctx context.Context, filter run.Filter) ([]run.Run, error) {
	return e.runs.List(ctx, filter)
}
func (e *Engine) HasActiveWorkflow(ctx context.Context, name string) (bool, error) {
	return e.runs.HasActiveWorkflow(ctx, name)
}
func (e *Engine) HasActiveRepo(ctx context.Context, name string) (bool, error) {
	return e.runs.HasActiveRepo(ctx, name)
}
func (e *Engine) HasProcessedReport(ctx context.Context, id run.ID, reportID string) (bool, error) {
	return e.runs.HasProcessedReport(ctx, id, reportID)
}
func (e *Engine) RegisterNodeSession(ctx context.Context, r run.NodeRuntimeRegistration) (run.NodeRuntimeRegistrationAck, error) {
	accepted, err := e.runs.RegisterNodeSession(ctx, r)
	return run.NodeRuntimeRegistrationAck{Accepted: accepted}, err
}

// Compile-time contract checks.
var _ run.Executor = (*Engine)(nil)
var _ run.RunQueries = (*Engine)(nil)
