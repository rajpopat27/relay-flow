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
	"net"
	"net/http"
	"os"
	"syscall"
	"time"

	"github.com/rajpopat27/relay-flow/internal/config"
	"github.com/rajpopat27/relay-flow/internal/execution/goworkflows"
	"github.com/rajpopat27/relay-flow/internal/harness"
	"github.com/rajpopat27/relay-flow/internal/paths"
	"github.com/rajpopat27/relay-flow/internal/repo"
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

	// Open the go-workflows SQLite engine and start its workers.
	engine, err := goworkflows.New(p.Database, goworkflows.Dependencies{
		Repos:         repoReg,
		Runner:        rnr,
		Harness:       hrn,
		RetentionDays: cfg.CompletedRunRetentionDays,
	})
	if err != nil {
		return fmt.Errorf("open engine: %w", err)
	}
	if err := engine.Start(ctx); err != nil {
		_ = engine.Shutdown(context.Background())
		return fmt.Errorf("start engine: %w", err)
	}

	// Construct the Run Manager.
	runManager := &runsvc.RunManager{Executor: engine, Runs: engine}

	// Services consumed by the Unix-socket server. The repo.Service takes
	// the SAME registry by pointer (via replaceInternal), so the engine,
	// pollers, and handlers observe one in-memory repo set.
	wfSvc := workflow.NewService(store, engine, repoExists{repoReg})
	for _, wf := range workflowList {
		wfSvc.Registry().Replace(wf)
	}
	repoSvc := repo.NewServiceWithRegistry(repo.ServiceConfig{
		ConfigPath: p.Config,
		TaskPlugin: cfg.TaskPlugin,
		Runner:     rnr,
		Active:     engine,
		Workflows:  wfSvc.Registry(),
	}, repoReg)

	// Start the Repo Poller group. The handleBatch callback is wired in 5.3;
	// for now the handler is a no-op so the group cadence/concurrency still
	// runs and tests can observe the socket.
	pollers := repo.NewPollerGroup(10, func(context.Context, *repo.Repo, []task.Ticket) {})
	pollers.Interval = time.Duration(cfg.PollIntervalSeconds) * time.Second
	pollers.ReplaceRepos(repoSvc.Registry().List())
	pollerCtx, cancelPollers := context.WithCancel(context.Background())
	defer cancelPollers()
	go pollers.Run(pollerCtx)

	// Start the Unix-socket server.
	_ = os.Remove(p.Socket)
	listener, err := net.Listen("unix", p.Socket)
	if err != nil {
		_ = engine.Shutdown(context.Background())
		return fmt.Errorf("listen %s: %w", p.Socket, err)
	}
	if err := os.Chmod(p.Socket, 0o600); err != nil {
		listener.Close()
		_ = engine.Shutdown(context.Background())
		return fmt.Errorf("chmod %s: %w", p.Socket, err)
	}

	deps := &serveDeps{
		wf:         wfSvc,
		repos:      repoSvc,
		engine:     engine,
		runManager: runManager,
		shutdown:   func(context.Context) error { return nil }, // graceful shutdown wired in 5.5
	}
	httpServer := &http.Server{Handler: server.New(deps)}

	serveErr := make(chan error, 1)
	go func() { serveErr <- httpServer.Serve(listener) }()

	select {
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		_ = httpServer.Shutdown(shutdownCtx)
		cancel()
		<-serveErr
	case err := <-serveErr:
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			_ = engine.Shutdown(context.Background())
			return fmt.Errorf("serve %s: %w", p.Socket, err)
		}
	}
	if err := engine.Shutdown(context.Background()); err != nil {
		return err
	}
	_ = recover // --recover wiring lands in 5.6
	return nil
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
	wf         *workflow.Service
	repos      *repo.Service
	engine     *goworkflows.Engine
	runManager *runsvc.RunManager
	shutdown   func(context.Context) error
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

func (d *serveDeps) DiscoverRepos(ctx context.Context) ([]runner.RepoCandidate, error) {
	return d.repos.Discover(ctx)
}
func (d *serveDeps) TaskFields(context.Context) ([]string, error) {
	return d.repos.RequiredRepoKeys(), nil
}
func (d *serveDeps) RegisterRepo(ctx context.Context, input repo.RegisterInput) (repo.Info, error) {
	return d.repos.Register(ctx, input)
}
func (d *serveDeps) ListRepos(context.Context) ([]repo.Info, error) {
	return d.repos.List(), nil
}
func (d *serveDeps) GetRepo(_ context.Context, name string) (repo.Info, error) {
	return d.repos.Get(name)
}
func (d *serveDeps) RemoveRepo(ctx context.Context, name string) error {
	return d.repos.Remove(ctx, name)
}

func (d *serveDeps) Shutdown(ctx context.Context) error { return d.shutdown(ctx) }
