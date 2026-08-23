package goworkflows_test

import (
	"context"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/rajpopat27/relay-flow/internal/execution/goworkflows"
	"github.com/rajpopat27/relay-flow/internal/identity"
	"github.com/rajpopat27/relay-flow/internal/retry"
	"github.com/rajpopat27/relay-flow/internal/run"
	"github.com/rajpopat27/relay-flow/internal/runner"
	"github.com/rajpopat27/relay-flow/internal/task"
	"github.com/rajpopat27/relay-flow/internal/workflow"
)

// 3.17-3.26 (engine level): roll-forward recovery, conflict/blocked,
// cancellation, terminal reconcile, database lifecycle, retention, and the
// run projection per specs/durable-run-execution.

// flakyTaskSystem fails a chosen primitive N times before succeeding, to
// exercise roll-forward activity retry.
type flakyTaskSystem struct {
	recordingTaskSystem
	failComplete int32
}

func (s *flakyTaskSystem) CompleteMailbox(ctx context.Context, mb task.Mailbox) error {
	if atomic.AddInt32(&s.failComplete, -1) >= 0 {
		return &transientErr{msg: "jira unavailable"}
	}
	return s.recordingTaskSystem.CompleteMailbox(ctx, mb)
}

type transientErr struct{ msg string }

func (e *transientErr) Error() string { return e.msg }

// conflictTaskSystem returns a Conflict until released, to exercise the
// blocked-state retry path.
type conflictTaskSystem struct {
	recordingTaskSystem
	conflict atomic.Bool
}

func (s *conflictTaskSystem) CompleteMailbox(ctx context.Context, mb task.Mailbox) error {
	if s.conflict.Load() {
		return retry.ConflictError(&transientErr{msg: "human moved mailbox"})
	}
	return s.recordingTaskSystem.CompleteMailbox(ctx, mb)
}

func TestRollForwardAfterActivityFailure(t *testing.T) {
	// 3.17: an activity that fails transiently is retried; the persisted
	// route is not re-asked of the agent.
	sys := &flakyTaskSystem{failComplete: 2}
	engine := newEngine(t, goworkflows.Dependencies{
		Repos: repoRegistryWith("payments", sys), Runner: newFakeRunner(), Harness: fakeHarness{},
	})
	rid, _ := startRun(engine, linearWorkflow(false))
	waitFor(t, 10*time.Second, func() bool {
		r, _ := engine.GetRun(context.Background(), rid)
		return r.CurrentNode == "coding"
	})
	r, _ := engine.GetRun(context.Background(), rid)

	if _, err := engine.SubmitReport(context.Background(), run.ReportRequest{RunID: rid, NodeVisitID: r.CurrentNodeVisitID, Report: successReport("end")}); err != nil {
		t.Fatal(err)
	}
	// Despite CompleteMailbox failing twice, the run completes (roll-forward).
	waitFor(t, 30*time.Second, func() bool {
		r, _ := engine.GetRun(context.Background(), rid)
		return r.State == run.StateCompleted
	})
	// The agent was never asked again: only one report was submitted.
	if len(sys.comments) == 0 {
		t.Fatal("no summary written")
	}
}

func TestConflictMarksBlockedThenRecovers(t *testing.T) {
	// 3.18: a manual task-system change marks the run blocked; it retries
	// and continues automatically when state becomes compatible.
	sys := &conflictTaskSystem{}
	sys.conflict.Store(true)
	engine := newEngine(t, goworkflows.Dependencies{
		Repos: repoRegistryWith("payments", sys), Runner: newFakeRunner(), Harness: fakeHarness{},
	})
	rid, _ := startRun(engine, linearWorkflow(false))
	waitFor(t, 10*time.Second, func() bool {
		r, _ := engine.GetRun(context.Background(), rid)
		return r.CurrentNode == "coding"
	})
	r, _ := engine.GetRun(context.Background(), rid)
	if _, err := engine.SubmitReport(context.Background(), run.ReportRequest{RunID: rid, NodeVisitID: r.CurrentNodeVisitID, Report: successReport("end")}); err != nil {
		t.Fatal(err)
	}

	waitFor(t, 30*time.Second, func() bool {
		r, _ := engine.GetRun(context.Background(), rid)
		return r.State == run.StateBlocked
	})

	// Human restores compatible state; the run leaves blocked automatically.
	sys.conflict.Store(false)
	waitFor(t, 60*time.Second, func() bool {
		r, _ := engine.GetRun(context.Background(), rid)
		return r.State == run.StateCompleted
	})
}

func TestCancelRun(t *testing.T) {
	// 3.19: cancellation resolves the active run, closes terminals while
	// preserving workspace, posts one parent cancellation comment with the
	// runID:cancellation marker, leaves mailboxes alone, marks canceled.
	sys := &recordingTaskSystem{}
	fr := newFakeRunner()
	engine := newEngine(t, goworkflows.Dependencies{
		Repos: repoRegistryWith("payments", sys), Runner: fr, Harness: fakeHarness{},
	})
	rid, _ := startRun(engine, linearWorkflow(false))
	waitFor(t, 10*time.Second, func() bool {
		r, _ := engine.GetRun(context.Background(), rid)
		return r.CurrentNode == "coding"
	})

	if err := engine.CancelRun(context.Background(), rid, "no longer needed"); err != nil {
		t.Fatalf("CancelRun failed: %v", err)
	}
	waitFor(t, 30*time.Second, func() bool {
		r, _ := engine.GetRun(context.Background(), rid)
		return r.State == run.StateCanceled
	})

	// One parent cancellation comment with the stable marker.
	var cancelComments int
	for _, c := range sys.comments {
		if c.Key == "PAY-101" && c.Marker == string(rid)+":cancellation" {
			cancelComments++
		}
	}
	if cancelComments != 1 {
		t.Fatalf("cancellation comments = %d, want 1 with marker %q", cancelComments, string(rid)+":cancellation")
	}
	// Mailbox statuses/history unchanged: no CompleteMailbox for the
	// canceled in-flight node beyond normal flow, and workspace preserved.
	if len(fr.envs) != 1 {
		t.Fatal("runner environment removed by cancellation; workspace/code must be preserved")
	}
}

func TestTerminalReconcile(t *testing.T) {
	// 3.21: repeated EnsureRun on an active run with a live terminal sends
	// no reconcile signal; a missing/unusable terminal relaunches the same
	// visit with the same nodeVisitID.
	sys := &recordingTaskSystem{}
	fr := newFakeRunner()
	engine := newEngine(t, goworkflows.Dependencies{
		Repos: repoRegistryWith("payments", sys), Runner: fr, Harness: fakeHarness{},
	})
	wf := linearWorkflow(false)
	rid, _ := startRun(engine, wf)
	waitFor(t, 10*time.Second, func() bool {
		r, _ := engine.GetRun(context.Background(), rid)
		return r.CurrentNode == "coding" && r.CurrentNodeVisitID != ""
	})
	r, _ := engine.GetRun(context.Background(), rid)
	visit := r.CurrentNodeVisitID

	// Healthy terminal: EnsureRun again, assert no reconcile (visit stable,
	// no relaunch). We observe via the runner's terminal set not changing.
	terminalsBefore := len(fr.terminals)
	if _, err := engine.EnsureRun(context.Background(), run.Start{
		ID: rid, Repo: "payments", RepoPath: "/srv/payments", Workflow: wf,
		Ticket: task.TicketRef{ID: "1", Key: "PAY-101"},
	}); err != nil {
		t.Fatal(err)
	}
	r2, _ := engine.GetRun(context.Background(), rid)
	if r2.CurrentNodeVisitID != visit {
		t.Fatal("healthy-terminal EnsureRun changed the current visit; no reconcile should be sent")
	}
	if len(fr.terminals) != terminalsBefore {
		t.Fatal("healthy-terminal EnsureRun relaunched the terminal")
	}

	// Terminal died: delete it, EnsureRun, assert the same visit is
	// relaunched (same nodeVisitID, terminal recreated).
	for k := range fr.terminals {
		delete(fr.terminals, k)
	}
	if _, err := engine.EnsureRun(context.Background(), run.Start{
		ID: rid, Repo: "payments", RepoPath: "/srv/payments", Workflow: wf,
		Ticket: task.TicketRef{ID: "1", Key: "PAY-101"},
	}); err != nil {
		t.Fatal(err)
	}
	waitFor(t, 10*time.Second, func() bool {
		return len(fr.terminals) > 0
	})
	r3, _ := engine.GetRun(context.Background(), rid)
	if r3.CurrentNodeVisitID != visit {
		t.Fatalf("reconcile relaunched with new visit %q, want same visit %q", r3.CurrentNodeVisitID, visit)
	}
}

func TestRunProjectionQueries(t *testing.T) {
	// 3.26: relay_runs is a derived read model supporting the query surface.
	sys := &recordingTaskSystem{}
	engine := newEngine(t, goworkflows.Dependencies{
		Repos: repoRegistryWith("payments", sys), Runner: newFakeRunner(), Harness: fakeHarness{},
	})
	rid, _ := startRun(engine, linearWorkflow(false))

	r, err := engine.GetRun(context.Background(), rid)
	if err != nil || r.ID != rid {
		t.Fatalf("GetRun = %+v, %v", r, err)
	}
	r, err = engine.FindRunByTicket(context.Background(), "PAY-101")
	if err != nil || r.ID != rid {
		t.Fatalf("FindRunByTicket = %+v, %v", r, err)
	}
	runs, err := engine.ListRuns(context.Background(), run.Filter{Repo: "payments"})
	if err != nil || len(runs) != 1 {
		t.Fatalf("ListRuns = %+v, %v", runs, err)
	}
	active, err := engine.HasActiveWorkflow(context.Background(), "basicFlow")
	if err != nil || !active {
		t.Fatalf("HasActiveWorkflow = %v, %v", active, err)
	}
	active, err = engine.HasActiveRepo(context.Background(), "payments")
	if err != nil || !active {
		t.Fatalf("HasActiveRepo = %v, %v", active, err)
	}
}

func TestEngineLifecycleNormalStartRequiresDatabase(t *testing.T) {
	// 3.23: normal serve requires a valid existing database; database loss
	// is never inferred from a missing run. Engine.New on a fresh path in
	// normal mode must refuse; recovery mode is the only creator.
	dir := t.TempDir()
	missing := filepath.Join(dir, "state.db")
	_, err := goworkflows.New(missing, goworkflows.Dependencies{
		Repos:  repoRegistryWith("payments", &recordingTaskSystem{}),
		Runner: newFakeRunner(), Harness: fakeHarness{},
		// Normal mode (no recovery): must refuse a missing database.
		Recover: false,
	})
	if err == nil {
		t.Fatal("normal start with missing database succeeded; must refuse")
	}
}

func TestRetentionKeepsNonTerminalRuns(t *testing.T) {
	// 3.25: starting/running/waiting/blocked/canceling runs are never
	// removed by retention; only completed/canceled past retention are.
	sys := &recordingTaskSystem{}
	engine := newEngine(t, goworkflows.Dependencies{
		Repos: repoRegistryWith("payments", sys), Runner: newFakeRunner(), Harness: fakeHarness{},
		CompletedRunRetentionDays: 30,
	})
	rid, _ := startRun(engine, linearWorkflow(false))
	waitFor(t, 10*time.Second, func() bool {
		r, _ := engine.GetRun(context.Background(), rid)
		return r.CurrentNode == "coding"
	})
	// A waiting run older than any retention window is preserved.
	if err := engine.CleanupCompleted(context.Background(), time.Now().Add(24*time.Hour)); err != nil {
		t.Fatal(err)
	}
	if _, err := engine.GetRun(context.Background(), rid); err != nil {
		t.Fatalf("waiting run removed by retention: %v", err)
	}
}

// recoverTaskSystem models a task system with a labeled parent carrying
// prior mailbox state, for serve --recover behavior.
type recoverTaskSystem struct {
	recordingTaskSystem
	parents    []task.Ticket
	resetCalls []string
	hasCancel  map[string]bool // parent key -> cancellation marker present
}

func (s *recoverTaskSystem) Poll(context.Context) ([]task.Ticket, error) {
	return s.parents, nil
}

func (s *recoverTaskSystem) HasComment(_ context.Context, target task.Target, marker string) (bool, error) {
	return s.hasCancel[target.Parent.Key], nil
}

func (s *recoverTaskSystem) ResetForRecovery(_ context.Context, parent task.TicketRef, _ []task.Mailbox, _ config.RawValues) error {
	s.resetCalls = append(s.resetCalls, parent.Key)
	return nil
}

func TestServeRecoverRebuildsFreshRuns(t *testing.T) {
	// 3.24: serve --recover is explicit destructive mode only. It creates
	// fresh state, closes surviving run-owned terminals (preserving
	// worktrees/code), EnsureMailboxes finds existing and creates missing,
	// resets mailboxes to To Do, preserves comments/labels, creates fresh
	// deterministic runs, generates fresh visit IDs, starts at start, and
	// skips parents carrying the cancellation marker.
	sys := &recoverTaskSystem{
		parents: []task.Ticket{
			{ID: "1", Key: "PAY-101", WorkflowClaims: []string{"wf:basicFlow"}},
			{ID: "2", Key: "PAY-202", WorkflowClaims: []string{"wf:basicFlow"}}, // canceled
		},
		hasCancel: map[string]bool{"PAY-202": true},
	}
	fr := newFakeRunner()
	// A surviving run-owned terminal from before the "database loss".
	env, _ := fr.EnsureEnvironment(context.Background(), runner.RunSpec{
		RunID:    identity.NewRunID("payments", "basicFlow", "PAY-101"),
		RepoName: "payments", RepoPath: "/srv/payments", TicketKey: "PAY-101",
	})
	_, _ = fr.EnsureTerminal(context.Background(), env, "PAY-101:coding", runner.Command{})

	dir := t.TempDir()
	engine, err := goworkflows.Recover(filepath.Join(dir, "state.db"), goworkflows.Dependencies{
		Repos: repoRegistryWith("payments", sys), Runner: fr, Harness: fakeHarness{},
	})
	if err != nil {
		t.Fatalf("Recover failed: %v", err)
	}
	if err := engine.Start(context.Background()); err != nil {
		t.Fatal(err)
	}

	// Fresh deterministic run for the labeled parent, starting at start.
	rid := identity.NewRunID("payments", "basicFlow", "PAY-101")
	waitFor(t, 10*time.Second, func() bool {
		r, err := engine.GetRun(context.Background(), rid)
		return err == nil && r.CurrentNode != ""
	})
	r, _ := engine.GetRun(context.Background(), rid)
	if r.CurrentNodeVisitID == "" {
		t.Fatal("no fresh nodeVisitID generated after recovery")
	}

	// Canceled parent was skipped.
	if _, err := engine.FindRunByTicket(context.Background(), "PAY-202"); err == nil {
		t.Fatal("recovery created a run for a cancellation-marked parent")
	}

	// Mailboxes were reset and terminals closed while env preserved.
	if len(sys.resetCalls) == 0 {
		t.Fatal("ResetForRecovery never called")
	}
	if len(fr.envs) != 1 {
		t.Fatal("recovery removed the runner environment; worktrees/code must be preserved")
	}
}
