package main

// Composition root for `relay-flow serve` (task 5.1). Explicit ordered Go
// wiring per docs/structs-methods-interfaces.md "Startup Wiring"
// (lines 1081-1097) and design.md decision 24. The flock on server.lock is
// acquired FIRST; startup refuses to run if another relay-flow process
// holds it. No container, no DI framework.

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/rajpopat27/relay-flow/internal/config"
	"github.com/rajpopat27/relay-flow/internal/execution/goworkflows"
	"github.com/rajpopat27/relay-flow/internal/harness"
	"github.com/rajpopat27/relay-flow/internal/paths"
	recoverpkg "github.com/rajpopat27/relay-flow/internal/recover"
	"github.com/rajpopat27/relay-flow/internal/repo"
	"github.com/rajpopat27/relay-flow/internal/router"
	runsvc "github.com/rajpopat27/relay-flow/internal/run"
	"github.com/rajpopat27/relay-flow/internal/runner"
	"github.com/rajpopat27/relay-flow/internal/server"
	"github.com/rajpopat27/relay-flow/internal/task"
	"github.com/rajpopat27/relay-flow/internal/workflow"

	// Adapter registrations (factories are registered by name via init).
	_ "github.com/rajpopat27/relay-flow/internal/harness/opencode"
	_ "github.com/rajpopat27/relay-flow/internal/runner/orca"
	_ "github.com/rajpopat27/relay-flow/internal/task/jira"
)

// serveRoot is the composition root. It runs the documented startup order
// and blocks until ctx is canceled, then shuts down in reverse.
//
// Order (docs/structs-methods-interfaces.md lines 1081-1097, verbatim):
//
//	flock → load machine config → select task/runner/harness factories →
//	construct shared runner/harness → load repos + one task.System per repo →
//	load workflow files → validate each workflow against every referenced
//	repo task system → bind workflows+matchers to repos → open go-workflows
//	SQLite engine and start its workers → construct the Run Manager →
//	start the Repo Poller group → start the Unix-socket server.
func serveRoot(ctx context.Context, p paths.Paths, recover bool) error {
	// Flock FIRST (docs lines 1028/1142): refuse to start if another
	// process holds it. The only pre-flock side effect is creating the
	// Root directory so the lock file itself can exist; nothing else in
	// the layout is touched until the lock is held.
	if err := os.MkdirAll(p.Root, 0o700); err != nil {
		return fmt.Errorf("create root %s: %w", p.Root, err)
	}
	if err := os.Chmod(p.Root, 0o700); err != nil {
		return fmt.Errorf("chmod root %s: %w", p.Root, err)
	}
	lockFile, err := os.OpenFile(p.Lock, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return fmt.Errorf("open lock %s: %w", p.Lock, err)
	}
	defer lockFile.Close()
	if err := lockFile.Chmod(0o600); err != nil {
		return fmt.Errorf("chmod lock %s: %w", p.Lock, err)
	}
	if err := syscall.Flock(int(lockFile.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		return fmt.Errorf("another relay-flow server holds %s", p.Lock)
	}
	defer func() { _ = syscall.Flock(int(lockFile.Fd()), syscall.LOCK_UN) }()

	// Single-instance held; materialize the rest of the layout.
	if err := paths.Ensure(p); err != nil {
		return err
	}

	// Load machine config.
	cfg, err := config.LoadMachine(p.Config)
	if err != nil {
		return err
	}

	// Select factories; construct shared runner and harness.
	rnr, err := runner.New(cfg.RunnerPlugin, cfg.RunnerConfig)
	if err != nil {
		return fmt.Errorf("runner plugin %q: %w", cfg.RunnerPlugin, err)
	}
	hrn, err := harness.New(cfg.HarnessPlugin, cfg.HarnessConfig)
	if err != nil {
		return fmt.Errorf("harness plugin %q: %w", cfg.HarnessPlugin, err)
	}

	// Load registered repos and one task.System per repo. The registry is
	// shared with repo.Service (constructed below) so the engine, pollers,
	// and server handlers observe the same in-memory set.
	repoReg := repo.NewRegistry()
	for name, rc := range cfg.Repos {
		ts, err := task.New(ctx, cfg.TaskPlugin, task.RepoSpec{
			Name:       name,
			Path:       rc.Path,
			RootConfig: cfg.TaskConfig,
			RepoConfig: rc.TaskConfig,
		})
		if err != nil {
			return fmt.Errorf("repo %q task system: %w", name, err)
		}
		repoReg.Replace(&repo.Repo{
			Name:       name,
			Path:       rc.Path,
			TaskConfig: rc.TaskConfig,
			TaskSystem: ts,
		})
	}

	// Load workflow files.
	store := &workflow.Store{Dir: p.Workflows}
	workflowList, err := store.LoadAll()
	if err != nil {
		return fmt.Errorf("load workflows: %w", err)
	}

	// Validate each workflow structurally (lifecycle nodes, routes,
	// reachability, nudge templates) before any per-repo validation or
	// binding; an invalid stored workflow must fail startup, not bind.
	for _, wf := range workflowList {
		if err := wf.Validate(); err != nil {
			return fmt.Errorf("workflow %q: %w", wf.Name, err)
		}
	}

	// Validate each workflow against every referenced repo task system.
	for _, wf := range workflowList {
		nodeCfgs := map[string]config.RawValues{}
		for nodeName, n := range wf.Nodes {
			nodeCfgs[nodeName] = n.TaskConfig
		}
		for _, repoName := range wf.Repos {
			rp, ok := repoReg.Get(repoName)
			if !ok {
				return fmt.Errorf("workflow %q references unregistered repo %q", wf.Name, repoName)
			}
			if err := rp.TaskSystem.ValidateConfig(ctx, wf.TaskConfig, nodeCfgs); err != nil {
				return fmt.Errorf("workflow %q repo %q: %w", wf.Name, repoName, err)
			}
		}
	}

	// Bind workflows and compiled matchers to repos.
	if err := repoReg.BindWorkflows(workflowList); err != nil {
		return err
	}

	// 5.5 normal-start refusal: serve REQUIRES an initialized database
	// (init creates it); --recover is the explicit opt-out.
	if !recover {
		if _, err := os.Stat(p.Database); err != nil {
			if os.IsNotExist(err) {
				return fmt.Errorf("database %s is missing; run `relay-flow init` first, or `relay-flow serve --recover` to rebuild from the task system", p.Database)
			}
			return fmt.Errorf("stat database %s: %w", p.Database, err)
		}
	}

	// 5.6 (decision 22): --recover replaces the execution state BEFORE any
	// worker starts, with two explicit guarantees:
	//   (a) The flock acquired at the top of serveRoot proves no other
	//       relay-flow process has this database open — relay-flow is the
	//       only process that ever opens state.db, and we hold server.lock.
	//   (b) The old database is moved aside as a timestamped sibling (never
	//       destroyed in place) and the destination path is verified absent
	//       before goworkflows.New runs. Engine.New therefore opens a
	//       provably empty database, and Engine.Start has no prior history
	//       to resume.
	if recover {
		stamp := time.Now().UTC().Format("20060102T150405Z")
		for _, suffix := range []string{"", "-wal", "-shm"} {
			src := p.Database + suffix
			if _, err := os.Stat(src); os.IsNotExist(err) {
				continue
			}
			dst := fmt.Sprintf("%s.recover-%s.bak%s", p.Database, stamp, suffix)
			if err := os.Rename(src, dst); err != nil {
				return fmt.Errorf("preserve stale database %s as %s: %w", src, dst, err)
			}
		}
		// Provably-empty engine: the primary DB path MUST NOT exist before
		// engine construction. If it does, the rename-aside above failed
		// silently for an unexpected reason; refuse to start rather than
		// resume stale state.
		if _, err := os.Stat(p.Database); err == nil {
			return fmt.Errorf("recover: %s still exists after rename-aside; refusing to open possibly-stale database", p.Database)
		} else if !os.IsNotExist(err) {
			return fmt.Errorf("recover: stat %s: %w", p.Database, err)
		}
	}

	// Open the go-workflows SQLite engine (migrates relay_runs; does not
	// yet start workers).
	engine, err := goworkflows.New(p.Database, goworkflows.Dependencies{
		Repos:         repoReg,
		Runner:        rnr,
		Harness:       hrn,
		TaskSystem:    cfg.TaskPlugin,
		RetentionDays: cfg.CompletedRunRetentionDays,
		Runtime: &runsvc.RuntimePolicy{
			KeepTerminalsAlive: cfg.KeepTerminalsAlive,
			KeepSessionsAlive:  cfg.KeepSessionsAlive,
		},
	})
	if err != nil {
		return fmt.Errorf("open engine: %w", err)
	}

	// 5.2 fail-fast preflight: before workers/pollers start, validate
	// task-system, runner, and harness credentials/permissions/connectivity,
	// every registered repo, and every configured agent. Known permanent
	// errors abort startup; runtime failures of existing runs retry forever
	// (design.md decision 16).
	if err := preflight(ctx, repoReg, rnr, hrn, workflowList); err != nil {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		_ = engine.Shutdown(shutdownCtx)
		cancel()
		return fmt.Errorf("startup validation: %w", err)
	}

	// Start engine workers and the one-shot startup retention sweep
	// (3.25; runs once here, no background ticker).
	if err := engine.Start(ctx); err != nil {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		_ = engine.Shutdown(shutdownCtx)
		cancel()
		return fmt.Errorf("start engine: %w", err)
	}

	// 5.4 lifecycle gate: ONE sync.Mutex shared by workflow.Service
	// Submit/Remove and RunManager.EnsureRun (design.md decision 23). Plain
	// mutex in the wiring — no lock service, no new abstraction.
	lifecycleGate := &sync.Mutex{}

	// Construct the Run Manager.
	runManager := &runsvc.RunManager{Executor: engine, Runs: engine, Gate: lifecycleGate}

	// Services consumed by the Unix-socket server. The repo.Service takes
	// the SAME registry by pointer (via replaceInternal), so the engine,
	// pollers, and handlers observe one in-memory repo set.
	wfSvc := workflow.NewService(store, engine, repoExists{repoReg})
	wfSvc.Gate = lifecycleGate
	wfSvc.ValidateTaskConfig = workflowConfigValidator(repoReg)
	// Submit/Remove must also rebuild repo bindings under the gate (spec
	// 3.34: bindings rebuilt on submit/remove/startup). Wired as a plain
	// callback — no dispatcher.
	wfSvc.Rebind = func() error {
		return repoReg.BindWorkflows(wfSvc.Registry().List())
	}
	for _, wf := range workflowList {
		wfSvc.Registry().Replace(wf)
	}
	repoSvc := repo.NewServiceWithRegistry(repo.ServiceConfig{
		ConfigPath: p.Config,
		TaskPlugin: cfg.TaskPlugin,
		Runner:     rnr,
		Harness:    hrn,
		Active:     engine,
		Workflows:  wfSvc.Registry(),
	}, repoReg)

	// 5.6 database-loss recovery (explicit --recover only; never inferred).
	// Runs after the engine is open and BEFORE normal pollers start so the
	// recovered runs exist before any poll cycle observes them. Mailbox
	// specs come from the engine so the recover path builds the same
	// description content as normal run execution.
	if recover {
		specsFor := func(wf *workflow.Workflow, ticketKey string) []task.MailboxSpec {
			return goworkflows.MailboxSpecs(wf, ticketKey)
		}
		if err := recoverpkg.FromTaskSystem(ctx, repoReg, rnr, runManager, specsFor); err != nil {
			shutdownCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			_ = engine.Shutdown(shutdownCtx)
			cancel()
			return fmt.Errorf("recover: %w", err)
		}
	}

	// Start the Repo Poller group with the handleBatch callback (5.3,
	// docs/structs-methods-interfaces.md lines 1099-1117 verbatim). No
	// dispatcher type, no extra abstraction.
	pollers := repo.NewPollerGroup(10, handleBatch(runManager))
	pollers.Interval = time.Duration(cfg.PollIntervalSeconds) * time.Second
	pollers.ReplaceRepos(repoSvc.Registry().List())
	pollerCtx, cancelPollers := context.WithCancel(context.Background())
	pollersDone := make(chan struct{})
	go func() {
		pollers.Run(pollerCtx)
		close(pollersDone)
	}()

	// cleanup is the single bounded shutdown path (5.5). Order:
	//   1. Stop accepting new polls AND new HTTP requests immediately.
	//   2. Wait (within one 30s budget) for in-flight polls and in-flight
	//      HTTP calls to return.
	//   3. Cancel worker polling and close SQLite via engine.Shutdown.
	// Every termination path — signal, /stop, listener error, startup
	// failure after pollers launched — funnels through here so no path
	// closes the database under running handlers or skips poller join.
	cleanup := func(httpServer *http.Server, serveErr chan error, serveConsumed bool) {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		// Stop new work immediately: no new polls, no new HTTP requests.
		cancelPollers()
		if httpServer != nil {
			_ = httpServer.Shutdown(shutdownCtx)
		}
		// Wait for in-flight HTTP within the budget.
		if httpServer != nil && !serveConsumed {
			select {
			case <-serveErr:
			case <-shutdownCtx.Done():
			}
		}
		// Wait for in-flight pollers within the same 30s budget (spec:
		// "waits up to 30 seconds ... then close the socket and database").
		// PollerGroup.Run returns once every in-flight handleBatch handler
		// completes; on deadline we proceed to engine.Shutdown anyway so
		// shutdown is bounded — the spec's rule is that already-running
		// external work is not interruptible and durable state must remain
		// recoverable (engine history is authoritative; a handler that
		// survives past deadline hits a closed-DB error and logs it).
		select {
		case <-pollersDone:
		case <-shutdownCtx.Done():
		}
		// Cancel worker polling and close the database. Engine.Shutdown
		// uses the shared budget for its own bounded wait on active
		// workflow/activity tasks.
		_ = engine.Shutdown(shutdownCtx)
	}

	// Start the Unix-socket server.
	_ = os.Remove(p.Socket)
	listener, err := net.Listen("unix", p.Socket)
	if err != nil {
		cleanup(nil, nil, true)
		return fmt.Errorf("listen %s: %w", p.Socket, err)
	}
	if err := os.Chmod(p.Socket, 0o600); err != nil {
		listener.Close()
		_ = os.Remove(p.Socket) // leave no stale socket file on failure
		cleanup(nil, nil, true)
		return fmt.Errorf("chmod %s: %w", p.Socket, err)
	}

	// /stop and ctx cancellation funnel into stopCtx; both end at the
	// single cleanup path.
	stopCtx, stopServe := context.WithCancel(context.Background())
	defer stopServe()

	deps := &serveDeps{
		wf:         wfSvc,
		repos:      repoSvc,
		engine:     engine,
		runManager: runManager,
		// Repo register/remove must reach the running poller group — the
		// startup ReplaceRepos only covers repos present at boot. Re-sync
		// after every successful mutation so a newly registered repo is
		// polled on the next tick without a serve restart.
		onReposChanged: func() {
			pollers.ReplaceRepos(repoSvc.Registry().List())
		},
		shutdown: func(context.Context) error {
			stopServe()
			return nil
		},
	}
	httpServer := &http.Server{Handler: server.New(deps)}

	serveErr := make(chan error, 1)
	go func() { serveErr <- httpServer.Serve(listener) }()

	var serveResult error
	serveConsumed := false
	select {
	case <-ctx.Done():
	case <-stopCtx.Done():
	case err := <-serveErr:
		serveConsumed = true
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			serveResult = fmt.Errorf("serve %s: %w", p.Socket, err)
		}
		// Listener closed on its own — treat as normal shutdown request.
	}

	cleanup(httpServer, serveErr, serveConsumed)
	return serveResult
}

func workflowConfigValidator(repoReg *repo.Registry) func(context.Context, *workflow.Workflow) error {
	return func(ctx context.Context, wf *workflow.Workflow) error {
		nodeCfgs := map[string]config.RawValues{}
		for nodeName, n := range wf.Nodes {
			nodeCfgs[nodeName] = n.TaskConfig
		}
		for _, repoName := range wf.Repos {
			rp, ok := repoReg.Get(repoName)
			if !ok {
				return fmt.Errorf("workflow %q references unregistered repo %q", wf.Name, repoName)
			}
			if err := rp.TaskSystem.ValidateConfig(ctx, wf.TaskConfig, nodeCfgs); err != nil {
				return fmt.Errorf("workflow %q repo %q: %w", wf.Name, repoName, err)
			}
		}
		return nil
	}
}

// repoExists adapts *repo.Registry to workflow.RepoLookup (Exists).
type repoExists struct{ reg *repo.Registry }

func (r repoExists) Exists(name string) bool {
	_, ok := r.reg.Get(name)
	return ok
}

// serveDeps adapts composition-root services to server.Deps. Thin forwarder;
// no logic. Signatures match docs/structs-methods-interfaces.md Client.
type serveDeps struct {
	wf             *workflow.Service
	repos          *repo.Service
	engine         *goworkflows.Engine
	runManager     *runsvc.RunManager
	onReposChanged func()
	shutdown       func(context.Context) error
}

func (d *serveDeps) SubmitWorkflow(ctx context.Context, yaml []byte) (*workflow.Workflow, error) {
	return d.wf.Submit(ctx, yaml)
}
func (d *serveDeps) GetWorkflow(_ context.Context, name string) (*workflow.Workflow, error) {
	return d.wf.Get(name)
}
func (d *serveDeps) ListWorkflows(context.Context) ([]*workflow.Workflow, error) {
	return d.wf.List(), nil
}
func (d *serveDeps) RemoveWorkflow(ctx context.Context, name string) error {
	return d.wf.Remove(ctx, name)
}

func (d *serveDeps) ListRuns(ctx context.Context, filter runsvc.Filter) ([]runsvc.Run, error) {
	return d.engine.ListRuns(ctx, filter)
}
func (d *serveDeps) GetRunByTicket(ctx context.Context, ticket string) (runsvc.Run, error) {
	return d.engine.FindRunByTicket(ctx, ticket)
}
func (d *serveDeps) CancelRun(ctx context.Context, ticket, reason string) error {
	return d.runManager.CancelByTicket(ctx, ticket, reason)
}

func (d *serveDeps) SubmitReport(ctx context.Context, rep runsvc.ReportRequest) (runsvc.ReportAck, error) {
	return d.engine.SubmitReport(ctx, rep)
}
func (d *serveDeps) HasProcessedReport(ctx context.Context, id runsvc.ID, reportID string) (bool, error) {
	return d.engine.HasProcessedReport(ctx, id, reportID)
}
func (d *serveDeps) RegisterNodeSession(ctx context.Context, registration runsvc.NodeRuntimeRegistration) (runsvc.NodeRuntimeRegistrationAck, error) {
	return d.engine.RegisterNodeSession(ctx, registration)
}

func (d *serveDeps) DiscoverRepos(ctx context.Context) ([]runner.RepoCandidate, error) {
	return d.repos.Discover(ctx)
}
func (d *serveDeps) TaskFields(context.Context) ([]string, error) {
	return d.repos.RequiredRepoKeys(), nil
}
func (d *serveDeps) RegisterRepo(ctx context.Context, input repo.RegisterInput) (repo.Info, error) {
	info, err := d.repos.Register(ctx, input)
	if err == nil && d.onReposChanged != nil {
		d.onReposChanged()
	}
	return info, err
}
func (d *serveDeps) ListRepos(context.Context) ([]repo.Info, error) {
	return d.repos.List(), nil
}
func (d *serveDeps) GetRepo(_ context.Context, name string) (repo.Info, error) {
	return d.repos.Get(name)
}
func (d *serveDeps) RemoveRepo(ctx context.Context, name string) error {
	err := d.repos.Remove(ctx, name)
	if err == nil && d.onReposChanged != nil {
		d.onReposChanged()
	}
	return err
}

func (d *serveDeps) Shutdown(ctx context.Context) error { return d.shutdown(ctx) }

// handleBatch is the Repo Poller group callback (5.3). Implemented verbatim
// from docs/structs-methods-interfaces.md lines 1101-1117: per ticket,
// ResolveWorkflow; ErrNoMatch → continue; other routing errors → log and
// continue (mutate nothing); success → RunManager.EnsureRun, log its errors.
// No dispatcher type, no extra abstraction. A small closure captures the
// RunManager so the repo.BatchHandler signature is satisfied.
//
// 9.2 poller logging: one info line per repo poll cycle (batch size), one
// info line per ticket with the routing outcome, one debug line per ticket
// with normalized fields. EnsureRun emits its own outcome lines from
// internal/run/manager.go.
func handleBatch(runManager *runsvc.RunManager) repo.BatchHandler {
	return func(ctx context.Context, rp *repo.Repo, tickets []task.Ticket) {
		slog.Info("poll cycle", "repo", rp.Name, "batch", len(tickets))
		for _, ticket := range tickets {
			slog.Debug("poll ticket",
				"repo", rp.Name, "ticket", ticket.Key, "id", ticket.ID,
				"title", ticket.Title, "claims", strings.Join(ticket.WorkflowClaims, ","))
			wf, err := router.ResolveWorkflow(rp, ticket)
			switch {
			case errors.Is(err, router.ErrNoMatch):
				slog.Info("route outcome",
					"repo", rp.Name, "ticket", ticket.Key, "outcome", "no-match")
				continue
			case err != nil:
				var amb *router.AmbiguousError
				var inv *router.InvalidClaimError
				outcome := "error"
				switch {
				case errors.As(err, &amb):
					outcome = "ambiguous"
				case errors.As(err, &inv):
					outcome = "invalid-claim"
				}
				slog.Info("route outcome",
					"repo", rp.Name, "ticket", ticket.Key, "outcome", outcome, "error", err)
				continue
			}
			routeOutcome := "claimed"
			for _, c := range ticket.WorkflowClaims {
				if c == "wf:"+wf.Name {
					routeOutcome = "already-claimed"
					break
				}
			}
			slog.Info("route outcome",
				"repo", rp.Name, "ticket", ticket.Key, "workflow", wf.Name, "outcome", routeOutcome)
			if err := runManager.EnsureRun(ctx, rp, wf, ticket); err != nil {
				// EnsureRun already logged the outcome; nothing to add here.
				_ = err
			}
		}
	}
}

// preflight is the 5.2 fail-fast startup validation. It runs after the
// engine is open and BEFORE workers/pollers start. Permanent errors abort
// startup; runtime errors of existing runs retry forever (different rule).
//
// Probes are no-side-effect reads on each adapter boundary:
//   - runner: ValidateRepo per registered repo (Orca connectivity + repo
//     presence at the configured path).
//   - task system: Poll per repo (credentials/permissions/connectivity;
//     returns active parents only, mutates nothing).
//   - harness: ValidateAgent per (repo, workflow) agent node (configured
//     agent must exist for that repo).
//
// Repos with no workflows still validate runner + task connectivity; agent
// validation runs once per distinct (repoPath, agent) pair to avoid
// redundant CLI calls when several workflows share an agent on one repo.
func preflight(ctx context.Context, repoReg *repo.Registry, rnr runner.Runner, hrn harness.Harness, workflows []*workflow.Workflow) error {
	for _, rp := range repoReg.List() {
		if err := rnr.ValidateRepo(ctx, rp.Name, rp.Path); err != nil {
			return fmt.Errorf("repo %q runner: %w", rp.Name, err)
		}
		if _, err := rp.TaskSystem.Poll(ctx); err != nil {
			return fmt.Errorf("repo %q task system: %w", rp.Name, err)
		}
	}
	type agentProbe struct {
		repoPath string
		agent    string
	}
	seen := map[agentProbe]bool{}
	for _, wf := range workflows {
		for nodeName, n := range wf.Nodes {
			// Validate EVERY configured agent (agent and HITL work nodes
			// both declare one); skip start/end which never carry Agent.
			if (n.Type != workflow.NodeAgent && n.Type != workflow.NodeHITL) || n.Agent == "" {
				continue
			}
			for _, repoName := range wf.Repos {
				rp, ok := repoReg.Get(repoName)
				if !ok {
					return fmt.Errorf("workflow %q references unregistered repo %q", wf.Name, repoName)
				}
				key := agentProbe{repoPath: rp.Path, agent: n.Agent}
				if seen[key] {
					continue
				}
				seen[key] = true
				if err := hrn.ValidateAgent(ctx, rp.Path, n.Agent); err != nil {
					return fmt.Errorf("workflow %q node %q repo %q: %w", wf.Name, nodeName, repoName, err)
				}
			}
		}
	}
	return nil
}

// The recover composition lives in internal/recover (recoverpkg.FromTaskSystem)
// so it is importable by both serve and the recover test — see task 6.3.
