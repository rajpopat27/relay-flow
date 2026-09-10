package run_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/rajpopat27/relay-flow/internal/identity"
	"github.com/rajpopat27/relay-flow/internal/repo"
	"github.com/rajpopat27/relay-flow/internal/run"
	"github.com/rajpopat27/relay-flow/internal/task"
	"github.com/rajpopat27/relay-flow/internal/workflow"
)

// 3.12: claim ordering per specs/repo-workflow-routing "Claim is persisted
// before run creation" and docs RunManager.EnsureRun ordering.

// eventLog is a shared ordered log across the fake task system and fake
// executor, proving claim-before-run-creation ordering.
type eventLog struct {
	mu     chan struct{}
	events []string
}

func newEventLog() *eventLog { return &eventLog{mu: make(chan struct{}, 1)} }

func (l *eventLog) add(e string) {
	l.mu <- struct{}{}
	l.events = append(l.events, e)
	<-l.mu
}

func (l *eventLog) all() []string {
	l.mu <- struct{}{}
	defer func() { <-l.mu }()
	return append([]string{}, l.events...)
}

// recordingSystem records Claim/HasComment calls into the shared log.
type recordingSystem struct {
	task.System
	log         *eventLog
	claimErr    error
	hasCancel   bool
	hasCommentN int
	markers     []string // markers passed to HasComment, in order
}

func (s *recordingSystem) Claim(_ context.Context, ref task.TicketRef, wf string) error {
	s.log.add("claim:" + ref.Key + ":" + wf)
	return s.claimErr
}

func (s *recordingSystem) HasComment(_ context.Context, _ task.Target, marker string) (bool, error) {
	s.hasCommentN++
	s.markers = append(s.markers, marker)
	return s.hasCancel, nil
}

// fakeExecutor records EnsureRun calls into the shared log.
type fakeExecutor struct {
	log     *eventLog
	ensures []run.Start
	created bool
	err     error
}

func (e *fakeExecutor) EnsureRun(_ context.Context, start run.Start) (bool, error) {
	e.log.add("ensureRun:" + string(start.ID))
	e.ensures = append(e.ensures, start)
	return e.created, e.err
}

func (e *fakeExecutor) SubmitReport(context.Context, run.ReportRequest) (run.ReportAck, error) {
	return run.ReportAck{}, nil
}

func (e *fakeExecutor) CancelRun(context.Context, run.ID, string) error { return nil }

// fakeQueries answers run lookups.
type fakeQueries struct {
	byTicket map[string]run.Run
	list     []run.Run
	listErr  error
}

func (q *fakeQueries) GetRun(_ context.Context, id run.ID) (run.Run, error) {
	for _, r := range q.byTicket {
		if r.ID == id {
			return r, nil
		}
	}
	return run.Run{}, errors.New("not found")
}

func (q *fakeQueries) FindRunByTicket(_ context.Context, ticket string) (run.Run, error) {
	if r, ok := q.byTicket[ticket]; ok {
		return r, nil
	}
	return run.Run{}, errors.New("not found")
}

func (q *fakeQueries) ListRuns(context.Context, run.Filter) ([]run.Run, error) {
	return append([]run.Run(nil), q.list...), q.listErr
}
func (q *fakeQueries) HasActiveWorkflow(context.Context, string) (bool, error) { return false, nil }
func (q *fakeQueries) HasActiveRepo(context.Context, string) (bool, error)     { return false, nil }

func testWorkflow(name string) *workflow.Workflow {
	return &workflow.Workflow{
		Name:  name,
		Repos: []string{"payments"},
		Nodes: map[string]workflow.Node{
			"start": {OnSuccess: []workflow.Route{{Target: "coding"}}},
			"coding": {
				Type: workflow.NodeAgent, Agent: "build", Description: "work",
				OnSuccess: []workflow.Route{{Target: "end"}},
				OnFailure: []workflow.Route{{Target: "coding"}},
			},
			"end": {},
		},
	}
}

func testRepo(sys task.System) *repo.Repo {
	return &repo.Repo{Name: "payments", Path: "/srv/payments", TaskSystem: sys}
}

func TestUnclaimedTicketClaimedBeforeEnsureRun(t *testing.T) {
	log := newEventLog()
	sys := &recordingSystem{log: log}
	exec := &fakeExecutor{log: log, created: true}
	m := &run.RunManager{Executor: exec, Runs: &fakeQueries{}}
	ticket := task.Ticket{ID: "1", Key: "PAY-101", Title: "parent"}

	err := m.EnsureRun(context.Background(), testRepo(sys), testWorkflow("basicFlow"), ticket)
	if err != nil {
		t.Fatalf("EnsureRun failed: %v", err)
	}
	events := log.all()
	if len(events) != 2 {
		t.Fatalf("events = %v, want [claim ensureRun]", events)
	}
	// Claim is persisted before the durable run is created.
	if !strings.HasPrefix(events[0], "claim:") || !strings.Contains(events[0], "basicFlow") {
		t.Fatalf("first event = %q, want the wf:basicFlow claim", events[0])
	}
	if !strings.HasPrefix(events[1], "ensureRun:") {
		t.Fatalf("second event = %q, want EnsureRun after the claim", events[1])
	}
	if !strings.Contains(events[0], "PAY-101") {
		t.Fatalf("claim event = %q, want ticket PAY-101", events[0])
	}
}

func TestClaimedTicketSkipsClaiming(t *testing.T) {
	log := newEventLog()
	sys := &recordingSystem{log: log}
	exec := &fakeExecutor{log: log}
	m := &run.RunManager{Executor: exec, Runs: &fakeQueries{}}
	ticket := task.Ticket{ID: "1", Key: "PAY-101", WorkflowClaims: []string{"wf:basicFlow"}}

	err := m.EnsureRun(context.Background(), testRepo(sys), testWorkflow("basicFlow"), ticket)
	if err != nil {
		t.Fatalf("EnsureRun failed: %v", err)
	}
	for _, e := range log.all() {
		if strings.HasPrefix(e, "claim:") {
			t.Fatalf("already-claimed ticket re-claimed: %v", log.all())
		}
	}
	if len(exec.ensures) != 1 {
		t.Fatalf("executor ensures = %d, want 1 (run still ensured)", len(exec.ensures))
	}
}

func TestExistingRunSkipsJiraCalls(t *testing.T) {
	log := newEventLog()
	sys := &recordingSystem{log: log, hasCancel: true}
	id := identity.NewRunID("payments", "basicFlow", "PAY-101")
	exec := &fakeExecutor{log: log}
	m := &run.RunManager{Executor: exec, Runs: &fakeQueries{list: []run.Run{{ID: id}}}}
	ticket := task.Ticket{ID: "1", Key: "PAY-101", WorkflowClaims: []string{"wf:basicFlow"}}

	if err := m.EnsureRun(context.Background(), testRepo(sys), testWorkflow("basicFlow"), ticket); err != nil {
		t.Fatal(err)
	}
	if sys.hasCommentN != 0 || len(log.all()) != 1 || len(exec.ensures) != 1 {
		t.Fatalf("existing run caused external work: comments=%d events=%v ensures=%d", sys.hasCommentN, log.all(), len(exec.ensures))
	}
}

func TestClaimFailurePreventsRunCreation(t *testing.T) {
	log := newEventLog()
	sys := &recordingSystem{log: log, claimErr: errors.New("jira down")}
	exec := &fakeExecutor{log: log}
	m := &run.RunManager{Executor: exec, Runs: &fakeQueries{}}
	ticket := task.Ticket{ID: "1", Key: "PAY-101"}

	err := m.EnsureRun(context.Background(), testRepo(sys), testWorkflow("basicFlow"), ticket)
	if err == nil {
		t.Fatal("EnsureRun succeeded despite claim failure")
	}
	for _, e := range log.all() {
		if strings.HasPrefix(e, "ensureRun:") {
			t.Fatal("run created after claim failure; claim must succeed first")
		}
	}
}

func TestCancellationMarkerSkipsRunCreation(t *testing.T) {
	// A labeled ticket with a missing run but carrying the
	// <runID>:cancellation marker must not be recreated.
	log := newEventLog()
	sys := &recordingSystem{log: log, hasCancel: true}
	exec := &fakeExecutor{log: log}
	m := &run.RunManager{Executor: exec, Runs: &fakeQueries{byTicket: map[string]run.Run{}}}
	ticket := task.Ticket{ID: "1", Key: "PAY-101", WorkflowClaims: []string{"wf:basicFlow"}}

	err := m.EnsureRun(context.Background(), testRepo(sys), testWorkflow("basicFlow"), ticket)
	if err != nil {
		t.Fatalf("EnsureRun failed: %v", err)
	}
	for _, e := range log.all() {
		if strings.HasPrefix(e, "ensureRun:") {
			t.Fatal("run recreated for cancellation-marked parent; marker must skip creation")
		}
	}
	if sys.hasCommentN == 0 {
		t.Fatal("Run Manager never checked the cancellation marker before creating a missing claimed run")
	}
	// The marker checked is exactly the deterministic <runID>:cancellation.
	wantMarker := string(identity.NewRunID("payments", "basicFlow", "PAY-101")) + ":cancellation"
	found := false
	for _, mk := range sys.markers {
		if mk == wantMarker {
			found = true
		}
	}
	if !found {
		t.Fatalf("cancellation marker checked = %v, want %q", sys.markers, wantMarker)
	}
}

func TestDeterministicRunID(t *testing.T) {
	log := newEventLog()
	sys := &recordingSystem{log: log}
	exec := &fakeExecutor{log: log, created: true}
	m := &run.RunManager{Executor: exec, Runs: &fakeQueries{}}
	ticket := task.Ticket{ID: "1", Key: "PAY-101"}

	if err := m.EnsureRun(context.Background(), testRepo(sys), testWorkflow("basicFlow"), ticket); err != nil {
		t.Fatal(err)
	}
	want := identity.NewRunID("payments", "basicFlow", "PAY-101")
	if exec.ensures[0].ID != want {
		t.Fatalf("run ID = %q, want deterministic %q", exec.ensures[0].ID, want)
	}
}

func TestRestartByTicketCreatesNumericFreshAttempt(t *testing.T) {
	log := newEventLog()
	sys := &recordingSystem{log: log}
	wf := testWorkflow("basicFlow")
	latestWorkflow := testWorkflow("basicFlow")
	latestNode := latestWorkflow.Nodes["coding"]
	latestNode.Description = "latest workflow definition"
	latestWorkflow.Nodes["coding"] = latestNode
	logical := identity.NewRunID("payments", wf.Name, "PAY-101")
	previous := run.Run{
		ID: logical, LogicalID: logical, AttemptID: 1,
		Repo: "payments", Workflow: wf.Name,
		Ticket: task.TicketRef{ID: "1", Key: "PAY-101"}, State: run.StateCanceled,
	}
	queries := &fakeQueries{
		byTicket: map[string]run.Run{"PAY-101": previous},
		list:     []run.Run{previous},
	}
	exec := &fakeExecutor{log: log, created: true}
	repos := repo.NewRegistry()
	repos.Replace(testRepo(sys))
	workflows := &workflow.Registry{}
	workflows.Replace(latestWorkflow)
	m := &run.RunManager{Executor: exec, Runs: queries, Repos: repos, Workflows: workflows}

	got, err := m.RestartByTicket(context.Background(), "PAY-101")
	if err != nil {
		t.Fatalf("RestartByTicket failed: %v", err)
	}
	wantID := identity.NewAttemptRunID(logical, 2)
	if got.ID != wantID || got.LogicalID != logical || got.AttemptID != 2 {
		t.Fatalf("restart run = %+v, want ID=%q logical=%q attempt=2", got, wantID, logical)
	}
	if len(exec.ensures) != 1 {
		t.Fatalf("EnsureRun calls = %d, want 1", len(exec.ensures))
	}
	start := exec.ensures[0]
	if start.ID != wantID || start.AttemptID != 2 || start.LogicalID != logical {
		t.Fatalf("restart start = %+v, want numeric attempt 2 and fenced ID", start)
	}
	if start.Workflow.Name != wf.Name || start.Workflow.Nodes["coding"].Description != "latest workflow definition" {
		t.Fatalf("restart did not use latest workflow snapshot: %+v", start.Workflow)
	}
}

func TestRestartByTicketIsIdempotentForActiveFreshAttempt(t *testing.T) {
	attempt := run.Run{
		ID: "payments/basicFlow/PAY-101~attempt~2", LogicalID: "payments/basicFlow/PAY-101", AttemptID: 2,
		Repo: "payments", Workflow: "basicFlow", Ticket: task.TicketRef{Key: "PAY-101"}, State: run.StateBlocked,
	}
	queries := &fakeQueries{byTicket: map[string]run.Run{"PAY-101": attempt}}
	exec := &fakeExecutor{log: newEventLog()}
	m := &run.RunManager{Executor: exec, Runs: queries}
	got, err := m.RestartByTicket(context.Background(), "PAY-101")
	if err != nil {
		t.Fatalf("idempotent restart failed: %v", err)
	}
	if got.ID != attempt.ID || len(exec.ensures) != 0 {
		t.Fatalf("idempotent restart = %+v, EnsureRun calls=%d", got, len(exec.ensures))
	}
}

func TestRestartByTicketWaitsForCancelingAttempt(t *testing.T) {
	attempt := run.Run{
		ID: "payments/basicFlow/PAY-101", LogicalID: "payments/basicFlow/PAY-101", AttemptID: 1,
		Repo: "payments", Workflow: "basicFlow", Ticket: task.TicketRef{Key: "PAY-101"}, State: run.StateCanceling,
	}
	queries := &fakeQueries{byTicket: map[string]run.Run{"PAY-101": attempt}}
	m := &run.RunManager{Executor: &fakeExecutor{log: newEventLog()}, Runs: queries}
	_, err := m.RestartByTicket(context.Background(), "PAY-101")
	if !errors.Is(err, run.ErrRestartConflict) {
		t.Fatalf("restart error = %v, want ErrRestartConflict", err)
	}
	if !strings.Contains(err.Error(), "wait for cancellation") {
		t.Fatalf("restart error = %v, want actionable cancellation guidance", err)
	}
}

func TestEnsureRunReusesActiveRestartAttempt(t *testing.T) {
	log := newEventLog()
	sys := &recordingSystem{log: log, hasCancel: true}
	logical := identity.NewRunID("payments", "basicFlow", "PAY-101")
	attempt := run.Run{
		ID: identity.NewAttemptRunID(logical, 2), LogicalID: logical, AttemptID: 2,
		Repo: "payments", Workflow: "basicFlow", Ticket: task.TicketRef{Key: "PAY-101"}, State: run.StateBlocked,
	}
	exec := &fakeExecutor{log: log}
	m := &run.RunManager{Executor: exec, Runs: &fakeQueries{list: []run.Run{attempt}}}
	ticket := task.Ticket{ID: "1", Key: "PAY-101", WorkflowClaims: []string{"wf:basicFlow"}}
	if err := m.EnsureRun(context.Background(), testRepo(sys), testWorkflow("basicFlow"), ticket); err != nil {
		t.Fatal(err)
	}
	if len(exec.ensures) != 1 || exec.ensures[0].ID != attempt.ID {
		t.Fatalf("ensures = %+v, want active restart ID %q", exec.ensures, attempt.ID)
	}
	if sys.hasCommentN != 0 {
		t.Fatal("active restart attempt checked cancellation marker")
	}
}

func TestCancelByTicketResolvesActiveRun(t *testing.T) {
	rid := identity.NewRunID("payments", "basicFlow", "PAY-101")
	queries := &fakeQueries{byTicket: map[string]run.Run{
		"PAY-101": {ID: rid, Ticket: task.TicketRef{Key: "PAY-101"}, State: run.StateWaiting},
	}}
	exec := &cancelRecorder{}
	m := &run.RunManager{Executor: exec, Runs: queries}

	if err := m.CancelByTicket(context.Background(), "PAY-101", "no longer needed"); err != nil {
		t.Fatalf("CancelByTicket failed: %v", err)
	}
	if exec.canceled != rid {
		t.Fatalf("canceled run = %q, want %q", exec.canceled, rid)
	}
}

type cancelRecorder struct {
	fakeExecutor
	canceled run.ID
}

func (e *cancelRecorder) CancelRun(_ context.Context, id run.ID, _ string) error {
	e.canceled = id
	return nil
}
