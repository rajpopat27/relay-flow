package goworkflows_test

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/rajpopat27/relay-flow/internal/execution/goworkflows"
	"github.com/rajpopat27/relay-flow/internal/identity"
	recoverpkg "github.com/rajpopat27/relay-flow/internal/recover"
	"github.com/rajpopat27/relay-flow/internal/repo"
	"github.com/rajpopat27/relay-flow/internal/run"
	"github.com/rajpopat27/relay-flow/internal/runner"
	"github.com/rajpopat27/relay-flow/internal/task"
	"github.com/rajpopat27/relay-flow/internal/workflow"
)

// 3.14, 3.17-3.21, 3.23-3.26 (engine level): identity/recovery, roll-forward,
// conflict/blocked, cancellation, terminal reconcile, database lifecycle,
// recover, retention, projection. Fakes in fakes_test.go.

// --- 3.14: fresh visit identity on a fresh run ---

func TestFreshRunGeneratesFreshVisitIDs(t *testing.T) {
	// Two independent runs (separate execution state) for the same
	// deterministic run ID generate non-colliding nodeVisitIDs; a fresh run
	// after explicit recovery must not reuse a stale visit ID.
	log := newEventLog()
	sys := newFakeTaskSystem(log)
	fr := newFakeRunner(log)
	wf := linearWorkflow(false)

	db1 := filepath.Join(t.TempDir(), "state.db")
	deps := goworkflows.Dependencies{Repos: repoRegistryWith("payments", sys), Runner: fr, Harness: newFakeHarness(log)}
	e1, err := goworkflows.New(db1, deps)
	if err != nil {
		t.Fatal(err)
	}
	if err := e1.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	rid, _ := startRun(e1, wf)
	waitFor(t, 10*time.Second, func() bool {
		r, _ := e1.GetRun(context.Background(), rid)
		return r.CurrentNode == "coding" && r.CurrentNodeVisitID != ""
	})
	r, _ := e1.GetRun(context.Background(), rid)
	firstVisit := r.CurrentNodeVisitID
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	_ = e1.Shutdown(ctx)
	cancel()

	db2 := filepath.Join(t.TempDir(), "state.db")
	e2, err := goworkflows.New(db2, deps)
	if err != nil {
		t.Fatal(err)
	}
	if err := e2.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer func() {
		c, cc := context.WithTimeout(context.Background(), 30*time.Second)
		defer cc()
		_ = e2.Shutdown(c)
	}()
	if _, err := e2.EnsureRun(context.Background(), run.Start{
		ID: rid, Repo: "payments", RepoPath: "/srv/payments", Workflow: wf,
		Ticket: task.TicketRef{ID: "1", Key: "PAY-101"},
	}); err != nil {
		t.Fatal(err)
	}
	waitFor(t, 10*time.Second, func() bool {
		r, err := e2.GetRun(context.Background(), rid)
		return err == nil && r.CurrentNodeVisitID != ""
	})
	r2, _ := e2.GetRun(context.Background(), rid)
	if r2.CurrentNodeVisitID == firstVisit {
		t.Fatal("fresh run collided on a prior nodeVisitID; visit IDs must be fresh/random")
	}
}

func TestVisitIDStableAcrossNormalRestart(t *testing.T) {
	// The nodeVisitID is generated once per node entry through a durable
	// replay-safe side effect: a normal Shutdown + New + Start on the SAME
	// database must return the SAME visit ID for the in-progress visit.
	log := newEventLog()
	sys := newFakeTaskSystem(log)
	fr := newFakeRunner(log)
	wf := linearWorkflow(false)
	db := filepath.Join(t.TempDir(), "state.db")
	deps := goworkflows.Dependencies{Repos: repoRegistryWith("payments", sys), Runner: fr, Harness: newFakeHarness(log)}

	e1, err := goworkflows.New(db, deps)
	if err != nil {
		t.Fatal(err)
	}
	if err := e1.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	rid, _ := startRun(e1, wf)
	waitFor(t, 10*time.Second, func() bool {
		r, _ := e1.GetRun(context.Background(), rid)
		return r.CurrentNode == "coding" && r.CurrentNodeVisitID != ""
	})
	r, _ := e1.GetRun(context.Background(), rid)
	visit := r.CurrentNodeVisitID

	e2 := restartEngine(t, db, deps, e1)
	waitFor(t, 10*time.Second, func() bool {
		r, err := e2.GetRun(context.Background(), rid)
		return err == nil && r.CurrentNodeVisitID != ""
	})
	r2, _ := e2.GetRun(context.Background(), rid)
	if r2.CurrentNodeVisitID != visit {
		t.Fatalf("visit changed across normal restart: %q -> %q; must be replay-stable", visit, r2.CurrentNodeVisitID)
	}
	// The unfinished run RESUMES work on the same db after restart: the
	// engine re-drives the in-progress node (re-ensures its terminal and
	// resumes the harness), it does not abandon the run.
	if r2.State == run.StateCanceled || r2.State == run.StateCompleted {
		t.Fatalf("unfinished run %s in terminal state %q after restart; it must resume", rid, r2.State)
	}
	waitFor(t, 10*time.Second, func() bool {
		return log.count("buildCommand:") > 0 && fr.liveTerminals() > 0
	})
	// The resumed visit can still complete normally: submit the report for
	// the configured end route and the run completes.
	if _, err := e2.SubmitReport(context.Background(), run.ReportRequest{RunID: rid, NodeVisitID: r2.CurrentNodeVisitID, Report: successReport("end")}); err != nil {
		t.Fatalf("resumed run rejected its report: %v", err)
	}
	waitFor(t, 10*time.Second, func() bool {
		r, _ := e2.GetRun(context.Background(), rid)
		return r.State == run.StateCompleted
	})
}

// --- 3.17: roll-forward recovery across crash boundaries ---

// restart reopens the engine on the same DB (a crash/restart boundary).
func restartEngine(t *testing.T, db string, deps goworkflows.Dependencies, old *goworkflows.Engine) *goworkflows.Engine {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	_ = old.Shutdown(ctx) // crash boundary: durable state persists
	cancel()
	e, err := goworkflows.New(db, deps)
	if err != nil {
		t.Fatal(err)
	}
	if err := e.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		c, cc := context.WithTimeout(context.Background(), 30*time.Second)
		defer cc()
		_ = e.Shutdown(c)
	})
	return e
}

func TestCancelRun(t *testing.T) {
	log := newEventLog()
	sys := newFakeTaskSystem(log)
	fr := newFakeRunner(log)
	engine := newEngine(t, goworkflows.Dependencies{
		Repos: repoRegistryWith("payments", sys), Runner: fr, Harness: newFakeHarness(log),
	})
	rid, _ := startRun(engine, linearWorkflow(false))
	waitFor(t, 10*time.Second, func() bool {
		r, _ := engine.GetRun(context.Background(), rid)
		return r.CurrentNode == "coding" && r.CurrentNodeVisitID != ""
	})
	if fr.liveTerminals() == 0 {
		t.Fatal("no live terminal before cancellation")
	}

	if err := engine.CancelRun(context.Background(), rid, "no longer needed"); err != nil {
		t.Fatalf("CancelRun failed: %v", err)
	}
	waitFor(t, 30*time.Second, func() bool {
		r, _ := engine.GetRun(context.Background(), rid)
		return r.State == run.StateCanceled
	})

	// Run-owned terminals closed, environment/workspace preserved.
	if fr.liveTerminals() != 0 {
		t.Fatal("cancellation left live terminals")
	}
	if log.count("closeTerminal:PAY-101:coding") != 1 {
		t.Fatalf("direct terminal closes = %v, want coding terminal closed once", log.all())
	}
	if len(fr.envs) != 1 {
		t.Fatal("cancellation removed the environment; workspace/code must be preserved")
	}

	// Exactly one parent cancellation comment with the stable marker.
	wantMarker := string(rid) + ":cancellation"
	var cancelComments int
	for _, c := range sys.commentBodies("PAY-101") {
		if c.Marker == wantMarker {
			cancelComments++
		}
	}
	if cancelComments != 1 {
		t.Fatalf("cancellation comments = %d, want 1 with marker %q", cancelComments, wantMarker)
	}

	// Mailbox statuses/history unchanged: the in-flight coding mailbox was
	// not completed by cancellation.
	if sys.mailboxStatusOf("PAY-101-coding") == "Done" {
		t.Fatal("cancellation completed the in-flight mailbox; mailbox state must be unchanged")
	}
	// No further node activity scheduled after cancellation.
	if log.count("ensureTerminal:PAY-101:review") != 0 {
		t.Fatal("activity scheduled after cancellation")
	}
}

func TestCancelDuringRunningActivity(t *testing.T) {
	// Cancellation cannot interrupt an already-running activity; it waits
	// for it to return, then runs cancellation cleanup.
	log := newEventLog()
	sys := newFakeTaskSystem(log)
	fr := newFakeRunner(log)
	engine := newEngine(t, goworkflows.Dependencies{
		Repos: repoRegistryWith("payments", sys), Runner: fr, Harness: newFakeHarness(log),
	})
	rid, _ := startRun(engine, linearWorkflow(false))
	waitFor(t, 10*time.Second, func() bool {
		r, _ := engine.GetRun(context.Background(), rid)
		return r.CurrentNode == "coding"
	})
	r, _ := engine.GetRun(context.Background(), rid)

	sys.completeSlow = 500 * time.Millisecond
	if _, err := engine.SubmitReport(context.Background(), run.ReportRequest{RunID: rid, NodeVisitID: r.CurrentNodeVisitID, Report: successReport("end")}); err != nil {
		t.Fatal(err)
	}
	waitFor(t, 10*time.Second, func() bool { return log.count("completeMailboxStart:") > 0 })

	if err := engine.CancelRun(context.Background(), rid, "stop"); err != nil {
		t.Fatal(err)
	}
	waitFor(t, 30*time.Second, func() bool {
		r, _ := engine.GetRun(context.Background(), rid)
		return r.State == run.StateCanceled
	})

	// Cancellation performs no mailbox mutation of its own. Snapshot the
	// mailbox state after the run is canceled (the in-flight activity has
	// already returned by then — cancel waits for it) and assert it stays
	// stable: no further status change or comment is written afterward.
	statusAfterCancel := sys.mailboxStatusOf("PAY-101-coding")
	commentsAfterCancel := len(sys.commentBodies("PAY-101-coding"))
	time.Sleep(300 * time.Millisecond)
	if got := sys.mailboxStatusOf("PAY-101-coding"); got != statusAfterCancel {
		t.Fatalf("mailbox status changed after cancellation: %q -> %q", statusAfterCancel, got)
	}
	if got := len(sys.commentBodies("PAY-101-coding")); got != commentsAfterCancel {
		t.Fatalf("mailbox comments grew after cancellation: %d -> %d", commentsAfterCancel, got)
	}
	// Deterministic ordering via the fake-adapter/runner call log: the
	// in-flight activity returned (completeMailbox) BEFORE terminal cleanup
	// (closeTerminals). Cancellation waited for the running activity.
	events := log.all()
	completeIdx := indexOf(events, "completeMailbox:PAY-101-coding")
	closeIdx := -1
	for i, e := range events {
		if hasPrefix(e, "closeTerminal:") {
			closeIdx = i
			break
		}
	}
	if completeIdx < 0 {
		t.Fatal("running activity never completed before cancellation cleanup")
	}
	if closeIdx < 0 || closeIdx < completeIdx {
		t.Fatalf("terminal cleanup ran before the in-flight activity returned; events=%v", events)
	}
	if log.count("closeTerminal:PAY-101:coding") != 1 {
		t.Fatalf("direct terminal closes = %v, want 1", log.all())
	}
	// Cancellation itself performs no mailbox mutation beyond the single
	// in-flight activity's own completion, and schedules no further work.
	if n := log.count("completeMailbox:PAY-101-coding"); n != 1 {
		t.Fatalf("completeMailbox ran %d times; want exactly 1 (the in-flight activity only, no extra mutation)", n)
	}
	if log.count("ensureTerminal:PAY-101:review") != 0 {
		t.Fatal("activity scheduled after cancellation")
	}
}

// --- 3.17: roll-forward recovery across crash boundaries ---

func TestCrashImmediatelyAfterReportPersistence(t *testing.T) {
	// Crash after the report+route are persisted but before any comment is
	// written: replay resumes the persisted route without re-asking the
	// agent and runs the first unfinished activity.
	log := newEventLog()
	sys := newFakeTaskSystem(log)
	fr := newFakeRunner(log)
	db := filepath.Join(t.TempDir(), "state.db")
	wf := linearWorkflow(false)
	deps := goworkflows.Dependencies{Repos: repoRegistryWith("payments", sys), Runner: fr, Harness: newFakeHarness(log)}

	e1, err := goworkflows.New(db, deps)
	if err != nil {
		t.Fatal(err)
	}
	if err := e1.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	rid, _ := startRun(e1, wf)
	waitFor(t, 10*time.Second, func() bool {
		r, _ := e1.GetRun(context.Background(), rid)
		return r.CurrentNode == "coding" && r.CurrentNodeVisitID != ""
	})
	r, _ := e1.GetRun(context.Background(), rid)
	visit := r.CurrentNodeVisitID

	// Fail comments so the report+route persist but no summary lands.
	sys.failComments = true
	if _, err := e1.SubmitReport(context.Background(), run.ReportRequest{RunID: rid, NodeVisitID: visit, Report: successReport("end")}); err != nil {
		t.Fatal(err)
	}
	// Let the report be consumed and the first comment attempt fail.
	waitFor(t, 10*time.Second, func() bool { return log.count("commentFail:") > 0 })
	buildBefore := log.count("buildCommand:")

	// Crash (Shutdown) then New+Start on the same db.
	e2 := restartEngine(t, db, deps, e1)
	sys.failComments = false

	waitFor(t, 30*time.Second, func() bool {
		r, _ := e2.GetRun(context.Background(), rid)
		return r.State == run.StateCompleted
	})
	if log.count("buildCommand:") != buildBefore {
		t.Fatal("agent re-asked after crash; persisted route must resume without a new LLM turn")
	}
	if n := log.count("comment:PAY-101-coding"); n != 1 {
		t.Fatalf("summaries = %d, want exactly 1 after roll-forward", n)
	}
}

func TestCrashAfterSummaryFeedbackBeforeCompletion(t *testing.T) {
	// Crash after summary+feedback are written but before CompleteMailbox
	// succeeds: replay retains the route and retries only completion. Uses a
	// coding->review route so a FEEDBACK activity exists.
	log := newEventLog()
	sys := newFakeTaskSystem(log)
	fr := newFakeRunner(log)
	db := filepath.Join(t.TempDir(), "state.db")
	wf := workflow.Workflow{
		Name: "reviewFlow", Repos: []string{"payments"},
		Nodes: map[string]workflow.Node{
			"start": {OnSuccess: []workflow.Route{{Target: "coding"}}},
			"coding": {
				Type: workflow.NodeAgent, Agent: "build", Description: "code",
				OnSuccess: []workflow.Route{{Target: "review"}},
				OnFailure: []workflow.Route{{Target: "coding"}},
			},
			"review": {
				Type: workflow.NodeHITL, Agent: "reviewer", Description: "review",
				OnSuccess: []workflow.Route{{Target: "end"}},
				OnFailure: []workflow.Route{{Target: "coding"}},
			},
			"end": {},
		},
	}
	deps := goworkflows.Dependencies{Repos: repoRegistryWith("payments", sys), Runner: fr, Harness: newFakeHarness(log)}

	e1, err := goworkflows.New(db, deps)
	if err != nil {
		t.Fatal(err)
	}
	if err := e1.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	rid, _ := startRun(e1, wf)
	waitFor(t, 10*time.Second, func() bool {
		r, _ := e1.GetRun(context.Background(), rid)
		return r.CurrentNode == "coding"
	})
	r, _ := e1.GetRun(context.Background(), rid)

	report := successReport("review")
	report.Feedback = workflow.Feedback{
		ReasonForNextStep: "ready", RequiredActions: "review it",
		RelevantContext: "diff", ExpectedResult: "approval",
	}
	sys.completeFail = 100
	if _, err := e1.SubmitReport(context.Background(), run.ReportRequest{RunID: rid, NodeVisitID: r.CurrentNodeVisitID, Report: report}); err != nil {
		t.Fatal(err)
	}
	// Summary to coding AND feedback to review both written; completion fails.
	waitFor(t, 10*time.Second, func() bool {
		return log.count("comment:PAY-101-coding") > 0 &&
			log.count("comment:PAY-101-review") > 0 &&
			log.count("completeMailboxFail:") > 0
	})
	summaryBefore := log.count("comment:PAY-101-coding")
	feedbackBefore := log.count("comment:PAY-101-review")

	sys.completeFail = 0
	e2 := restartEngine(t, db, deps, e1)
	// Run advances to review after the retried completion.
	waitFor(t, 30*time.Second, func() bool {
		r, _ := e2.GetRun(context.Background(), rid)
		return r.CurrentNode == "review"
	})
	// No duplicated summary or feedback; only the completion was retried.
	if log.count("comment:PAY-101-coding") != summaryBefore {
		t.Fatal("summary rewritten after crash; roll-forward must not repeat it")
	}
	if log.count("comment:PAY-101-review") != feedbackBefore {
		t.Fatal("feedback rewritten after crash; roll-forward must not repeat it")
	}
	if log.count("completeMailbox:PAY-101-coding") < 1 {
		t.Fatal("unfinished CompleteMailbox not retried after crash")
	}
}

// --- 3.18: conflict/blocked ---

func TestConflictMarksBlockedThenRecovers(t *testing.T) {
	log := newEventLog()
	sys := newFakeTaskSystem(log)
	sys.completeConflict = true
	engine := newEngine(t, goworkflows.Dependencies{
		Repos: repoRegistryWith("payments", sys), Runner: newFakeRunner(log), Harness: newFakeHarness(log),
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
	r, _ = engine.GetRun(context.Background(), rid)
	if r.LastError == "" {
		t.Fatal("blocked run exposes no conflict error in LastError")
	}
	if sys.mailboxStatusOf("PAY-101-coding") == "Done" {
		t.Fatal("mailbox completed while state was incompatible; no blind overwrite allowed")
	}
	if log.count("completeMailboxConflict:") < 1 {
		t.Fatal("no conflict retry observed")
	}

	// The run stays blocked and keeps retrying with backoff while the
	// conflict persists; it does not complete or overwrite.
	conflictsAfterBlocked := log.count("completeMailboxConflict:")
	waitFor(t, 5*time.Second, func() bool {
		return log.count("completeMailboxConflict:") > conflictsAfterBlocked
	})
	r, _ = engine.GetRun(context.Background(), rid)
	if r.State != run.StateBlocked {
		t.Fatalf("run left blocked while conflict persists: %q", r.State)
	}

	sys.completeConflict = false
	waitFor(t, 60*time.Second, func() bool {
		r, _ := engine.GetRun(context.Background(), rid)
		return r.State == run.StateCompleted
	})
}

// --- 3.21: terminal reconcile ---

func TestTerminalReconcile(t *testing.T) {
	log := newEventLog()
	sys := newFakeTaskSystem(log)
	fr := newFakeRunner(log)
	fh := newFakeHarness(log)
	engine := newEngine(t, goworkflows.Dependencies{
		Repos: repoRegistryWith("payments", sys), Runner: fr, Harness: fh,
	})
	wf := linearWorkflow(false)
	rid, _ := startRun(engine, wf)
	waitFor(t, 10*time.Second, func() bool {
		r, _ := engine.GetRun(context.Background(), rid)
		return r.CurrentNode == "coding" && r.CurrentNodeVisitID != ""
	})
	r, _ := engine.GetRun(context.Background(), rid)
	visit := r.CurrentNodeVisitID
	terminalsBefore := fr.liveTerminals()
	relaunchBefore := log.count("ensureTerminal:PAY-101:coding")
	buildBefore := log.count("buildCommand:")
	inspectBefore := log.count("inspectTerminal:")

	// Healthy terminal: EnsureRun checks the persisted direct handle and,
	// finding it live, sends no reconcile and relaunches
	// nothing.
	if _, err := engine.EnsureRun(context.Background(), run.Start{
		ID: rid, Repo: "payments", RepoPath: "/srv/payments", Workflow: wf,
		Ticket: task.TicketRef{ID: "1", Key: "PAY-101"},
	}); err != nil {
		t.Fatal(err)
	}
	time.Sleep(300 * time.Millisecond)
	if log.count("inspectTerminal:") == inspectBefore {
		t.Fatal("repeated EnsureRun never checked the persisted terminal handle")
	}
	if got := log.count("buildCommand:") - buildBefore; got != 0 {
		t.Fatalf("healthy-terminal EnsureRun relaunched the harness %d times; want 0", got)
	}
	if got := log.count("ensureTerminal:PAY-101:coding") - relaunchBefore; got != 0 {
		t.Fatalf("healthy-terminal EnsureRun recreated the terminal %d times; want 0", got)
	}
	r2, _ := engine.GetRun(context.Background(), rid)
	if r2.CurrentNodeVisitID != visit {
		t.Fatal("healthy-terminal EnsureRun changed the current visit")
	}
	if fr.liveTerminals() != terminalsBefore {
		t.Fatal("healthy-terminal EnsureRun relaunched/closed a terminal")
	}

	// Terminal died: reconcile relaunches the same visit (same nodeVisitID).
	fr.killTerminals()
	if _, err := engine.EnsureRun(context.Background(), run.Start{
		ID: rid, Repo: "payments", RepoPath: "/srv/payments", Workflow: wf,
		Ticket: task.TicketRef{ID: "1", Key: "PAY-101"},
	}); err != nil {
		t.Fatal(err)
	}
	waitFor(t, 10*time.Second, func() bool {
		return log.count("ensureTerminal:PAY-101:coding") > relaunchBefore
	})
	r3, _ := engine.GetRun(context.Background(), rid)
	if r3.CurrentNodeVisitID != visit {
		t.Fatalf("reconcile relaunched with new visit %q, want same visit %q", r3.CurrentNodeVisitID, visit)
	}
	if got := log.count("buildCommand:") - buildBefore; got != 1 {
		t.Fatalf("reconcile relaunched harness %d times, want exactly 1 for the same visit", got)
	}
}

func TestHITLReconcileRestoresMissingSessionNeverNudgesIdle(t *testing.T) {
	log := newEventLog()
	sys := newFakeTaskSystem(log)
	fr := newFakeRunner(log)
	fh := newFakeHarness(log)
	wf := workflow.Workflow{
		Name: "hitlFlow", Repos: []string{"payments"},
		Nodes: map[string]workflow.Node{
			"start": {OnSuccess: []workflow.Route{{Target: "review"}}},
			"review": {
				Type: workflow.NodeHITL, Agent: "reviewer", Description: "review",
				OnSuccess: []workflow.Route{{Target: "end"}},
				OnFailure: []workflow.Route{{Target: "review"}},
			},
			"end": {},
		},
	}
	engine := newEngine(t, goworkflows.Dependencies{
		Repos: repoRegistryWith("payments", sys), Runner: fr, Harness: fh,
	})
	rid := identity.NewRunID("payments", "hitlFlow", "PAY-101")
	start := run.Start{
		ID: rid, Repo: "payments", RepoPath: "/srv/payments", Workflow: wf,
		Ticket: task.TicketRef{ID: "1", Key: "PAY-101"},
	}
	if _, err := engine.EnsureRun(context.Background(), start); err != nil {
		t.Fatal(err)
	}
	waitFor(t, 10*time.Second, func() bool {
		r, _ := engine.GetRun(context.Background(), rid)
		return r.CurrentNode == "review"
	})
	r, _ := engine.GetRun(context.Background(), rid)
	visit := r.CurrentNodeVisitID

	// Idle live HITL terminal: reconcile leaves it untouched, no nudge.
	buildBefore := log.count("buildCommand:")
	if _, err := engine.EnsureRun(context.Background(), start); err != nil {
		t.Fatal(err)
	}
	time.Sleep(300 * time.Millisecond)
	if fh.reconcileNudge != 0 {
		t.Fatal("idle live HITL session was nudged; HITL reconcile never nudges")
	}
	if log.count("buildCommand:") != buildBefore {
		t.Fatal("idle live HITL session was relaunched")
	}

	// Missing session/terminal: reconcile restores it for the same visit,
	// without nudging.
	fr.killTerminals()
	if _, err := engine.EnsureRun(context.Background(), start); err != nil {
		t.Fatal(err)
	}
	waitFor(t, 10*time.Second, func() bool {
		return log.count("buildCommand:") > buildBefore
	})
	r2, _ := engine.GetRun(context.Background(), rid)
	if r2.CurrentNodeVisitID != visit {
		t.Fatal("HITL restore changed the visit")
	}
	if fh.reconcileNudge != 0 {
		t.Fatal("HITL restore nudged the session; restore must not nudge")
	}
}

// --- 3.20: report persistence / ack boundary ---

func TestAckImpliesDurablePersistenceAcrossRestart(t *testing.T) {
	// A report is acknowledged only after its signal is durably persisted.
	// Proof: submit+ack, then crash, then restart — the run resumes the
	// persisted route without the report being resubmitted, and a post-
	// restart retry of the same visit is a safe duplicate.
	log := newEventLog()
	sys := newFakeTaskSystem(log)
	fr := newFakeRunner(log)
	dir := t.TempDir()
	db := filepath.Join(dir, "state.db")
	wf := linearWorkflow(false)
	deps := goworkflows.Dependencies{Repos: repoRegistryWith("payments", sys), Runner: fr, Harness: newFakeHarness(log)}

	e1, err := goworkflows.New(db, deps)
	if err != nil {
		t.Fatal(err)
	}
	if err := e1.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	rid, _ := startRun(e1, wf)
	waitFor(t, 10*time.Second, func() bool {
		r, _ := e1.GetRun(context.Background(), rid)
		return r.CurrentNode == "coding"
	})
	r, _ := e1.GetRun(context.Background(), rid)
	visit := r.CurrentNodeVisitID

	ack, err := e1.SubmitReport(context.Background(), run.ReportRequest{RunID: rid, NodeVisitID: visit, Report: successReport("end")})
	if err != nil || !ack.Accepted || ack.Duplicate {
		t.Fatalf("first ack = %+v err=%v", ack, err)
	}

	// Crash after persistence, restart, resume the persisted route.
	e2 := restartEngine(t, db, deps, e1)
	waitFor(t, 30*time.Second, func() bool {
		r, _ := e2.GetRun(context.Background(), rid)
		return r.State == run.StateCompleted
	})
	// The run advanced using the persisted report; no agent re-ask, no
	// repeated summary.
	if n := log.count("comment:PAY-101-coding"); n != 1 {
		t.Fatalf("summaries after restart = %d, want 1 (persisted route resumed)", n)
	}

	// A post-restart retry of the same visit is a safe duplicate.
	commentsBefore := log.count("comment:")
	ack, err = e2.SubmitReport(context.Background(), run.ReportRequest{RunID: rid, NodeVisitID: visit, Report: successReport("end")})
	if err != nil || !ack.Accepted {
		t.Fatalf("post-restart retry: ack=%+v err=%v", ack, err)
	}
	if log.count("comment:") != commentsBefore {
		t.Fatal("post-restart duplicate caused repeated effects")
	}
}

// --- 3.21: terminal reconcile ---

func TestHealthyDatabaseMissingRunIsClaimBeforeRun(t *testing.T) {
	// With a healthy DB, a labeled ticket whose deterministic run is missing
	// is a claim-before-run crash; EnsureRun creates it normally. Database
	// loss is never inferred from this.
	log := newEventLog()
	sys := newFakeTaskSystem(log)
	engine := newEngine(t, goworkflows.Dependencies{
		Repos: repoRegistryWith("payments", sys), Runner: newFakeRunner(log), Harness: newFakeHarness(log),
	})
	// The DB is valid (engine started). The labeled ticket has no run yet.
	rid := identity.NewRunID("payments", "basicFlow", "PAY-101")
	if _, err := engine.GetRun(context.Background(), rid); err == nil {
		t.Fatal("run unexpectedly exists before EnsureRun")
	}
	created, err := engine.EnsureRun(context.Background(), run.Start{
		ID: rid, Repo: "payments", RepoPath: "/srv/payments", Workflow: linearWorkflow(false),
		Ticket: task.TicketRef{ID: "1", Key: "PAY-101"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !created {
		t.Fatal("missing claimed run not created in healthy database")
	}
}

// Normal serve refusing a missing/corrupt database is a STARTUP-WIRING
// behavior (section 5.5), not an engine-New behavior: the recovery and
// retention tests legitimately create a fresh database via New on a new path.
// The refusal is covered at the serve layer by 3.23's serve-fixture test
// (cmd/relay-flow/commands_test.go); see the recovered-vs-normal distinction
// in TestRecoverResetsJiraStateFreshRuns.
func TestNormalServeRequiresExistingDatabase(t *testing.T) {
	// An unusable/corrupt database file (wrong magic) makes New fail because
	// SQLite cannot open it — this is genuine engine-level behavior, distinct
	// from the "missing file" case which New tolerates by creating the db.
	deps := goworkflows.Dependencies{
		Repos:  repoRegistryWith("payments", newFakeTaskSystem(newEventLog())),
		Runner: newFakeRunner(newEventLog()), Harness: newFakeHarness(newEventLog()),
	}
	path := filepath.Join(t.TempDir(), "state.db")
	if err := os.WriteFile(path, []byte("not a sqlite database"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := goworkflows.New(path, deps); err == nil {
		t.Fatal("engine start on a corrupt database succeeded; must fail")
	}
}

func TestDatabaseFileIsOwnerOnly(t *testing.T) {
	// 3.29: the engine-created state.db is mode 0600.
	dir := t.TempDir()
	db := filepath.Join(dir, "state.db")
	engine, err := goworkflows.New(db, goworkflows.Dependencies{
		Repos:  repoRegistryWith("payments", newFakeTaskSystem(newEventLog())),
		Runner: newFakeRunner(newEventLog()), Harness: newFakeHarness(newEventLog()),
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := engine.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer func() {
		c, cc := context.WithTimeout(context.Background(), 30*time.Second)
		defer cc()
		_ = engine.Shutdown(c)
	}()
	fi, err := os.Stat(db)
	if err != nil {
		t.Fatalf("state.db not created: %v", err)
	}
	if fi.Mode().Perm() != 0600 {
		t.Fatalf("state.db mode = %o, want 0600", fi.Mode().Perm())
	}
}

// --- 3.24: serve --recover ---

// recoverTickets drives the REAL section-5.6 recover composition
// (internal/recover.FromTaskSystem) against the settled seams: fakes behind
// task.System/runner.Runner plus the real engine and the real RunManager.
// Rewritten in 6.3 from a test-local copy so the production wiring is
// actually covered.
func recoverTickets(ctx context.Context, engine *goworkflows.Engine, sys *fakeTaskSystem, fr *fakeRunner, wf workflow.Workflow) error {
	reg := repo.NewRegistry()
	reg.Replace(&repo.Repo{
		Name:       "payments",
		Path:       "/srv/payments",
		TaskSystem: sys,
	})
	// The router needs the workflow bound to the repo to resolve wf: claims.
	if err := reg.BindWorkflows([]*workflow.Workflow{&wf}); err != nil {
		return err
	}
	rm := &run.RunManager{Executor: engine, Runs: engine}
	specsFor := func(w *workflow.Workflow, key string) []task.MailboxSpec {
		return goworkflows.MailboxSpecs(w, key)
	}
	return recoverpkg.FromTaskSystem(ctx, reg, fr, rm, specsFor)
}

func TestServeRecoverRebuildsFreshRuns(t *testing.T) {
	log := newEventLog()
	sys := newFakeTaskSystem(log)
	fr := newFakeRunner(log)
	fh := newFakeHarness(log)
	db := filepath.Join(t.TempDir(), "state.db")

	sys.parentsToRecover = []task.Ticket{
		{ID: "1", Key: "PAY-101", WorkflowClaims: []string{"wf:basicFlow"}},
		{ID: "2", Key: "PAY-102", WorkflowClaims: []string{"wf:basicFlow"}},
		{ID: "3", Key: "PAY-103", WorkflowClaims: []string{"wf:basicFlow"}}, // canceled
	}
	sys.canceledParents = map[string]bool{"PAY-103": true}
	sys.seedMailbox("PAY-101", task.Mailbox{ID: "mb-coding", Key: "PAY-101-coding", Node: "coding"}, []string{"wf:basicFlow"})
	if err := sys.Comment(context.Background(), task.Target{
		Parent:  task.TicketRef{ID: "1", Key: "PAY-101"},
		Mailbox: mailboxPtr(sys, "PAY-101", "coding"),
	}, "old summary", "old"); err != nil {
		t.Fatal(err)
	}

	// A surviving run-owned terminal from before the loss.
	survSpec := runner.RunSpec{
		RunID:    identity.NewRunID("payments", "basicFlow", "PAY-101"),
		RepoName: "payments", RepoPath: "/srv/payments", TicketKey: "PAY-101",
	}
	env, _ := fr.EnsureEnvironment(context.Background(), survSpec)
	_, _ = fr.EnsureTerminal(context.Background(), env, "PAY-101:coding", runner.Command{})

	deps := goworkflows.Dependencies{Repos: repoRegistryWith("payments", sys), Runner: fr, Harness: fh}

	// Pre-loss: a real run produces a visit ID that must NOT be reused.
	preLossDB := filepath.Join(t.TempDir(), "state.db")
	preEngine, err := goworkflows.New(preLossDB, deps)
	if err != nil {
		t.Fatal(err)
	}
	if err := preEngine.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	preRid, _ := startRun(preEngine, linearWorkflow(false))
	waitFor(t, 10*time.Second, func() bool {
		r, _ := preEngine.GetRun(context.Background(), preRid)
		return r.CurrentNodeVisitID != ""
	})
	preR, _ := preEngine.GetRun(context.Background(), preRid)
	preLossVisit := preR.CurrentNodeVisitID
	if _, err := preEngine.RegisterNodeSession(context.Background(), run.NodeRuntimeRegistration{
		RunID: preRid, Node: "coding", NodeVisitID: preLossVisit, SessionID: "pre-loss-session",
	}); err != nil {
		t.Fatal(err)
	}
	preRuntime, err := preEngine.GetNodeRuntime(context.Background(), preRid, "coding")
	if err != nil || preRuntime.TerminalID == "" {
		t.Fatalf("pre-loss runtime = %+v, %v", preRuntime, err)
	}
	inspectBeforeRecover := log.count("inspectTerminal:")
	pc, pcancel := context.WithTimeout(context.Background(), 30*time.Second)
	_ = preEngine.Shutdown(pc)
	pcancel()

	// Recovery runs on fresh execution state (the old database is gone).
	engine, err := goworkflows.New(db, deps)
	if err != nil {
		t.Fatalf("engine open for recovery failed: %v", err)
	}
	if err := engine.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer func() {
		c, cc := context.WithTimeout(context.Background(), 30*time.Second)
		defer cc()
		_ = engine.Shutdown(c)
	}()

	if err := recoverTickets(context.Background(), engine, sys, fr, linearWorkflow(false)); err != nil {
		t.Fatalf("recoverTickets failed: %v", err)
	}

	// Fresh deterministic runs starting at the start edge target (coding),
	// with fresh visit IDs that differ from the pre-loss visit.
	for _, key := range []string{"PAY-101", "PAY-102"} {
		rid := identity.NewRunID("payments", "basicFlow", key)
		var rr run.Run
		waitFor(t, 10*time.Second, func() bool {
			var err error
			rr, err = engine.GetRun(context.Background(), rid)
			return err == nil && rr.CurrentNodeVisitID != ""
		})
		if rr.CurrentNode != "coding" {
			t.Fatalf("%s recovered to node %q, want start-edge target coding", key, rr.CurrentNode)
		}
		if rr.CurrentNodeVisitID == preLossVisit {
			t.Fatalf("%s reused the pre-loss nodeVisitID; recovery must generate fresh visit IDs", key)
		}
		rt, err := engine.GetNodeRuntime(context.Background(), rid, "coding")
		if err != nil {
			t.Fatalf("%s runtime: %v", key, err)
		}
		if rt.SessionID == "pre-loss-session" {
			t.Fatalf("%s recovery reused pre-loss session ID", key)
		}
		if rt.TerminalID == preRuntime.TerminalID {
			t.Fatalf("%s recovery reused pre-loss terminal ID %q", key, rt.TerminalID)
		}
	}
	if log.count("inspectTerminal:") != inspectBeforeRecover {
		t.Fatalf("recover used pre-loss direct terminal IDs: %v", log.all())
	}
	if log.count("closeTerminals:") == 0 {
		t.Fatalf("recover did not use documented external terminal cleanup: %v", log.all())
	}
	// Canceled parent skipped.
	if _, err := engine.FindRunByTicket(context.Background(), "PAY-103"); err == nil {
		t.Fatal("recovery created a run for a cancellation-marked parent")
	}
	// Exactly the two active labeled non-canceled parents recovered.
	runs, err := engine.ListRuns(context.Background(), run.Filter{Repo: "payments"})
	if err != nil {
		t.Fatal(err)
	}
	if len(runs) != 2 {
		t.Fatalf("recovery created %d runs, want exactly 2", len(runs))
	}

	// Surviving terminals closed, environments/workspaces preserved.
	if log.count("closeTerminals:") == 0 {
		t.Fatal("recovery never closed surviving run-owned terminals")
	}
	if len(fr.envs) == 0 {
		t.Fatal("recovery removed runner environments; worktrees/code must be preserved")
	}

	// Existing mailbox found (PAY-101), missing one created (PAY-102). The
	// pre-loss run and the recovery pass both legitimately find the seeded
	// mailbox; what must hold is that it is found (>=1) and never recreated.
	if log.count("foundMailbox:PAY-101:coding") < 1 {
		t.Fatal("existing PAY-101 coding mailbox not found/reused")
	}
	if log.count("createMailbox:PAY-101:coding") != 0 {
		t.Fatal("existing PAY-101 coding mailbox recreated")
	}
	if log.count("createMailbox:PAY-102:coding") != 1 {
		t.Fatal("missing PAY-102 coding mailbox not created")
	}

	// Mailboxes reset to To Do; comments/labels preserved. The fresh durable
	// run then legitimately re-applies the node status (In Progress), so the
	// observable post-recovery status is either the reset To Do or the fresh
	// run's In Progress — never a stale pre-loss value.
	if len(sys.resets) != 2 {
		t.Fatalf("ResetForRecovery calls = %v, want 2 (PAY-101, PAY-102)", sys.resets)
	}
	for _, key := range []string{"PAY-101-coding", "PAY-102-coding"} {
		if got := sys.mailboxStatusOf(key); got != "To Do" && got != "In Progress" {
			t.Fatalf("mailbox %s status = %q after recovery, want reset To Do or fresh-run In Progress", key, got)
		}
	}
	// Parent workflow labels preserved (claim identity survives recovery).
	for _, key := range []string{"PAY-101", "PAY-102"} {
		p, ok := sys.parentByKey(key)
		if !ok || len(p.WorkflowClaims) == 0 {
			t.Fatalf("parent %s lost its wf label during recovery", key)
		}
	}
	foundOld := false
	for _, c := range sys.commentBodies("PAY-101-coding") {
		if c.Body == "old summary" {
			foundOld = true
		}
	}
	if !foundOld {
		t.Fatal("recovery dropped existing mailbox comments; must preserve them")
	}
	if len(sys.labelsFor("PAY-101-coding")) == 0 {
		t.Fatal("recovery dropped mailbox wf label; must preserve it")
	}
}

// --- 3.25: retention ---

func TestRetentionRemovesOldTerminalRunsKeepsOthers(t *testing.T) {
	// The retention clock is driven by finished_at values inserted directly
	// into relay_runs via SQL (the documented projection table), not by an
	// engine-internal clock.
	log := newEventLog()
	sys := newFakeTaskSystem(log)
	db := filepath.Join(t.TempDir(), "state.db")
	deps := goworkflows.Dependencies{Repos: repoRegistryWith("payments", sys), Runner: newFakeRunner(log), Harness: newFakeHarness(log)}
	engine, err := goworkflows.New(db, deps)
	if err != nil {
		t.Fatal(err)
	}
	if err := engine.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer func() {
		c, cc := context.WithTimeout(context.Background(), 30*time.Second)
		defer cc()
		_ = engine.Shutdown(c)
	}()

	// Stop the engine, then INSERT already-aged terminal fixtures (finished_at
	// 31 days back, past the default 30-day retention) directly into
	// relay_runs — the settled retention clock seam (tasks.md 3.25).
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	_ = engine.Shutdown(ctx)
	cancel()
	oldCompleted := run.ID("payments/basicFlow/PAY-OLD1")
	oldCanceled := run.ID("payments/basicFlow/PAY-OLD2")
	insertOldTerminalRun(t, db, oldCompleted, run.StateCompleted, "PAY-OLD1", 31*24*time.Hour)
	insertOldTerminalRun(t, db, oldCanceled, run.StateCanceled, "PAY-OLD2", 31*24*time.Hour)
	// Nonterminal fixtures (finished_at NULL) in every other state must be
	// retained regardless of age.
	insertRelayRun(t, db, run.ID("payments/basicFlow/PAY-S1"), run.StateStarting, "PAY-S1")
	insertRelayRun(t, db, run.ID("payments/basicFlow/PAY-R1"), run.StateRunning, "PAY-R1")
	insertRelayRun(t, db, run.ID("payments/basicFlow/PAY-B1"), run.StateBlocked, "PAY-B1")
	insertRelayRun(t, db, run.ID("payments/basicFlow/PAY-C1"), run.StateCanceling, "PAY-C1")

	// Restart: the retention sweep runs once at startup (pre-poller window).
	// Old terminal fixtures are removed; nonterminal fixtures are preserved.
	engine2, err := goworkflows.New(db, deps)
	if err != nil {
		t.Fatal(err)
	}
	if err := engine2.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer func() {
		c, cc := context.WithTimeout(context.Background(), 30*time.Second)
		defer cc()
		_ = engine2.Shutdown(c)
	}()

	if _, err := engine2.GetRun(context.Background(), oldCompleted); err == nil {
		t.Fatal("old completed run not removed by the startup retention sweep")
	}
	if _, err := engine2.GetRun(context.Background(), oldCanceled); err == nil {
		t.Fatal("old canceled run not removed by the startup retention sweep")
	}
	for _, kept := range []run.ID{
		"payments/basicFlow/PAY-S1", "payments/basicFlow/PAY-R1",
		"payments/basicFlow/PAY-B1", "payments/basicFlow/PAY-C1",
	} {
		if _, err := engine2.GetRun(context.Background(), kept); err != nil {
			t.Fatalf("nonterminal run %s removed by retention: %v", kept, err)
		}
	}
}

// --- 3.26: projection (kept passing) ---
func TestRunProjectionQueries(t *testing.T) {
	log := newEventLog()
	sys := newFakeTaskSystem(log)
	engine := newEngine(t, goworkflows.Dependencies{
		Repos: repoRegistryWith("payments", sys), Runner: newFakeRunner(log), Harness: newFakeHarness(log),
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

// insertOldTerminalRun inserts an already-aged terminal fixture row directly
// into the relay_runs projection — the settled retention clock seam (tasks.md
// 3.25: "by inserting old finished_at fixtures into relay_runs").
func insertOldTerminalRun(t *testing.T, dbPath string, id run.ID, state run.State, ticketKey string, ago time.Duration) {
	t.Helper()
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	finished := time.Now().Add(-ago)
	res, err := db.Exec(
		`INSERT INTO relay_runs (id, repo, workflow, ticket_id, ticket_key, state, started_at, updated_at, finished_at)
		 VALUES (?, 'payments', 'basicFlow', ?, ?, ?, ?, ?, ?)`,
		string(id), ticketKey, ticketKey, string(state), finished.Add(-time.Hour), finished, finished)
	if err != nil {
		t.Fatalf("insert aged terminal fixture: %v", err)
	}
	if n, _ := res.RowsAffected(); n != 1 {
		t.Fatalf("insert affected %d rows, want 1", n)
	}
}

// insertRelayRun inserts a NON-terminal fixture row (finished_at NULL) so the
// retention sweep must preserve it regardless of age.
func insertRelayRun(t *testing.T, dbPath string, id run.ID, state run.State, ticketKey string) {
	t.Helper()
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	now := time.Now()
	res, err := db.Exec(
		`INSERT INTO relay_runs (id, repo, workflow, ticket_id, ticket_key, state, started_at, updated_at, finished_at)
		 VALUES (?, 'payments', 'basicFlow', ?, ?, ?, ?, ?, NULL)`,
		string(id), ticketKey, ticketKey, string(state), now, now)
	if err != nil {
		t.Fatalf("insert nonterminal fixture: %v", err)
	}
	if n, _ := res.RowsAffected(); n != 1 {
		t.Fatalf("insert affected %d rows, want 1", n)
	}
}
