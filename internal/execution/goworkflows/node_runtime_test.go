package goworkflows

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/rajpopat27/relay-flow/internal/harness"
	"github.com/rajpopat27/relay-flow/internal/repo"
	"github.com/rajpopat27/relay-flow/internal/run"
	"github.com/rajpopat27/relay-flow/internal/runner"
	"github.com/rajpopat27/relay-flow/internal/task"
	"github.com/rajpopat27/relay-flow/internal/workflow"
)

func TestNodeRuntimeMigrationAndRestart(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "state.db")
	db := openProjectionDB(t, path)

	// Simulate an existing database that predates relay_node_runtime.
	if _, err := db.Exec(`CREATE TABLE relay_runs (
		id TEXT PRIMARY KEY, repo TEXT NOT NULL, workflow TEXT NOT NULL,
		ticket_id TEXT NOT NULL, ticket_key TEXT NOT NULL, state TEXT NOT NULL,
		current_node TEXT, current_node_visit_id TEXT, last_error TEXT,
		retry_error TEXT, retry_attempt INTEGER, next_retry_at DATETIME,
		started_at DATETIME NOT NULL, updated_at DATETIME NOT NULL, finished_at DATETIME
	)`); err != nil {
		t.Fatal(err)
	}
	p := &RunProjection{DB: db}
	if err := p.migrate(); err != nil {
		t.Fatalf("migrate existing database: %v", err)
	}

	id := run.ID("payments/basic/PAY-101")
	if err := p.insertStart(ctx, run.Start{
		ID: id, Repo: "payments", Workflow: workflow.Workflow{Name: "basic"},
		Ticket: task.TicketRef{ID: "1", Key: "PAY-101"},
	}, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	firstVisit := run.NodeVisitID("visit-1")
	if err := p.updateNode(ctx, id, run.StateRunning, "implement", firstVisit); err != nil {
		t.Fatal(err)
	}
	if err := p.updateNodeRuntime(ctx, NodeRuntime{
		RunID: id, Node: "implement", TerminalID: "term-1", SessionID: "session-1", NodeVisitID: firstVisit,
	}); err != nil {
		t.Fatal(err)
	}

	// Moving to another node retains implement's runtime row.
	if err := p.updateNode(ctx, id, run.StateRunning, "verify", "visit-2"); err != nil {
		t.Fatal(err)
	}
	implement, err := p.getNodeRuntime(ctx, id, "implement")
	if err != nil {
		t.Fatal(err)
	}
	if implement.TerminalID != "term-1" || implement.SessionID != "session-1" || implement.NodeVisitID != firstVisit {
		t.Fatalf("runtime changed across transition: %+v", implement)
	}

	// Revisiting updates only the visit ID, preserving reusable identities.
	secondVisit := run.NodeVisitID("visit-3")
	if err := p.updateNode(ctx, id, run.StateRunning, "implement", secondVisit); err != nil {
		t.Fatal(err)
	}
	implement, err = p.getNodeRuntime(ctx, id, "implement")
	if err != nil {
		t.Fatal(err)
	}
	if implement.TerminalID != "term-1" || implement.SessionID != "session-1" || implement.NodeVisitID != secondVisit {
		t.Fatalf("runtime not preserved on revisit: %+v", implement)
	}
	if implement.UpdatedAt.IsZero() {
		t.Fatal("runtime updated_at is zero")
	}

	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	db = openProjectionDB(t, path)
	defer db.Close()
	p = &RunProjection{DB: db}
	if err := p.migrate(); err != nil {
		t.Fatalf("migrate after restart: %v", err)
	}
	implement, err = p.getNodeRuntime(ctx, id, "implement")
	if err != nil {
		t.Fatalf("query runtime after restart: %v", err)
	}
	if implement.TerminalID != "term-1" || implement.SessionID != "session-1" || implement.NodeVisitID != secondVisit {
		t.Fatalf("runtime changed across restart: %+v", implement)
	}
}

func TestNodeRuntimeRemovedOnlyByRetention(t *testing.T) {
	ctx := context.Background()
	db := openProjectionDB(t, filepath.Join(t.TempDir(), "state.db"))
	defer db.Close()
	p := &RunProjection{DB: db}
	if err := p.migrate(); err != nil {
		t.Fatal(err)
	}

	id := run.ID("payments/basic/PAY-OLD")
	started := time.Now().UTC().Add(-32 * 24 * time.Hour)
	if err := p.insertStart(ctx, run.Start{
		ID: id, Repo: "payments", Workflow: workflow.Workflow{Name: "basic"},
		Ticket: task.TicketRef{ID: "old", Key: "PAY-OLD"},
	}, started); err != nil {
		t.Fatal(err)
	}
	if err := p.updateNode(ctx, id, run.StateRunning, "implement", "visit-old"); err != nil {
		t.Fatal(err)
	}
	if err := p.updateNodeRuntime(ctx, NodeRuntime{
		RunID: id, Node: "implement", TerminalID: "term-old", SessionID: "session-old", NodeVisitID: "visit-old",
	}); err != nil {
		t.Fatal(err)
	}
	finished := time.Now().UTC().Add(-31 * 24 * time.Hour)
	if err := p.updateState(ctx, id, run.StateCompleted, "", &finished); err != nil {
		t.Fatal(err)
	}
	if _, err := p.getNodeRuntime(ctx, id, "implement"); err != nil {
		t.Fatalf("completion removed runtime before retention: %v", err)
	}

	ids, err := p.sweepRetention(ctx, time.Now().UTC().Add(-30*24*time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if len(ids) != 1 || ids[0] != string(id) {
		t.Fatalf("retained ids = %v, want [%s]", ids, id)
	}
	if _, err := p.getNodeRuntime(ctx, id, "implement"); !errors.Is(err, errNodeRuntimeNotFound) {
		t.Fatalf("runtime after retention error = %v, want not found", err)
	}
}

func TestNodeRuntimeSessionRegistrationKeepsOldSessionBoundToOldVisit(t *testing.T) {
	ctx := context.Background()
	db := openProjectionDB(t, filepath.Join(t.TempDir(), "state.db"))
	defer db.Close()
	p := &RunProjection{DB: db}
	if err := p.migrate(); err != nil {
		t.Fatal(err)
	}
	id := run.ID("payments/basic/PAY-101")
	if err := p.insertStart(ctx, run.Start{
		ID: id, Repo: "payments", Workflow: workflow.Workflow{Name: "basic"},
		Ticket: task.TicketRef{ID: "1", Key: "PAY-101"},
	}, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	if err := p.updateNodeRuntimeVisit(ctx, id, "implement", "visit-current"); err != nil {
		t.Fatal(err)
	}

	accepted, err := p.registerNodeSession(ctx, run.NodeRuntimeRegistration{
		RunID: id, Node: "implement", SessionID: "session-current",
	})
	if err != nil || !accepted {
		t.Fatalf("current registration = %v, %v; want accepted", accepted, err)
	}
	if err := p.updateNodeRuntimeVisit(ctx, id, "implement", "visit-next"); err != nil {
		t.Fatal(err)
	}
	accepted, err = p.registerNodeSession(ctx, run.NodeRuntimeRegistration{
		RunID: id, Node: "implement", SessionID: "session-current",
	})
	if err != nil || accepted {
		t.Fatalf("old session registration = %v, %v; want rejected", accepted, err)
	}
	rt, err := p.getNodeRuntime(ctx, id, "implement")
	if err != nil {
		t.Fatal(err)
	}
	if rt.SessionID != "session-current" || rt.NodeVisitID != "visit-next" {
		t.Fatalf("old session changed current visit binding: %+v", rt)
	}
}

func TestEnsureNodeRuntimeUsesDirectIDsAndFallsBackFresh(t *testing.T) {
	ctx := context.Background()
	fr := &runtimeTestRunner{}
	fh := &runtimeTestHarness{}
	db := openProjectionDB(t, filepath.Join(t.TempDir(), "state.db"))
	defer db.Close()
	p := &RunProjection{DB: db}
	if err := p.migrate(); err != nil {
		t.Fatal(err)
	}
	id := run.ID("payments/basic/PAY-101")
	if err := p.insertStart(ctx, run.Start{
		ID: id, Repo: "payments", Workflow: workflow.Workflow{Name: "basic"},
		Ticket: task.TicketRef{ID: "1", Key: "PAY-101"},
	}, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	if err := p.updateNodeRuntime(ctx, NodeRuntime{
		RunID: id, Node: "implement", TerminalID: "dead-term", SessionID: "dead-session", NodeVisitID: "visit-2",
	}); err != nil {
		t.Fatal(err)
	}
	a := &Activities{Runner: fr, Harness: fh, Runs: p}
	nw := run.NodeWork{Work: run.Work{RunID: id, Repo: "payments", Workflow: "basic", Parent: task.TicketRef{Key: "PAY-101"}}, Node: "implement", NodeVisitID: "visit-2"}
	spec := harness.LaunchSpec{RunID: id, NodeVisitID: "visit-2", RepoName: "payments", Workflow: "basic", Ticket: "PAY-101", Node: "implement", Agent: "build", Title: "PAY-101:implement", Prompt: "work", NudgePrompt: "custom instructions"}
	if err := a.EnsureNodeRuntime(ctx, nw, "/srv/payments", spec, NodeRuntime{NodeVisitID: "visit-2"}); err != nil {
		t.Fatal(err)
	}
	rt, err := p.getNodeRuntime(ctx, id, "implement")
	if err != nil {
		t.Fatal(err)
	}
	if rt.TerminalID == "" || rt.TerminalID == "dead-term" || rt.SessionID != "dead-session" {
		t.Fatalf("failed direct IDs not replaced atomically: %+v", rt)
	}
	if fr.findCalls != 2 || fh.buildCalls != 1 || fr.createCalls != 1 {
		t.Fatalf("stored-ID replacement calls: find=%d build=%d create=%d", fr.findCalls, fh.buildCalls, fr.createCalls)
	}
	if len(fr.findIDs) != 2 || fr.findIDs[0] != "dead-term" || fr.findIDs[1] != "dead-term" {
		t.Fatalf("FindTerminal IDs = %v, want [dead-term dead-term]", fr.findIDs)
	}
	if len(fh.resumeIDs) != 1 || fh.resumeIDs[0] != "dead-session" {
		t.Fatalf("BuildCommand ResumeIDs = %v, want [dead-session]", fh.resumeIDs)
	}
	if !reflect.DeepEqual(fh.rendered, []harness.PromptKind{harness.PromptInitial}) {
		t.Fatalf("dead-terminal rendered prompts = %v, want initial", fh.rendered)
	}
	for _, prompt := range fh.prompts {
		if prompt != "work" {
			t.Fatalf("same-visit relaunch prompt = %q, want standard prompt only", prompt)
		}
	}
}

func TestEnsureNodeRuntimeInitialLaunchAppendsCustomInstructions(t *testing.T) {
	ctx := context.Background()
	db := openProjectionDB(t, filepath.Join(t.TempDir(), "state.db"))
	defer db.Close()
	p := &RunProjection{DB: db}
	if err := p.migrate(); err != nil {
		t.Fatal(err)
	}
	id := run.ID("payments/basic/PAY-100")
	if err := p.insertStart(ctx, run.Start{ID: id, Repo: "payments", Workflow: workflow.Workflow{Name: "basic"}, Ticket: task.TicketRef{ID: "0", Key: "PAY-100"}}, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	if err := p.updateNodeRuntimeVisit(ctx, id, "implement", "visit-first"); err != nil {
		t.Fatal(err)
	}
	fh := &runtimeTestHarness{}
	fr := &runtimeTestRunner{}
	a := &Activities{Runner: fr, Harness: fh, Runs: p}
	nw := run.NodeWork{Work: run.Work{RunID: id}, Node: "implement", NodeVisitID: "visit-first"}
	spec := harness.LaunchSpec{RunID: id, NodeVisitID: "visit-first", Node: "implement", NodeType: workflow.NodeHITL, Agent: "build", Prompt: "standard prompt", NudgePrompt: "custom instructions"}
	if err := a.EnsureNodeRuntime(ctx, nw, "", spec, NodeRuntime{}); err != nil {
		t.Fatal(err)
	}
	if len(fh.prompts) != 1 || fh.prompts[0] != "work\n\ncustom instructions" {
		t.Fatalf("initial prompt = %q", fh.prompts)
	}
	if len(fr.statuses) != 1 || fr.statuses[0] != runner.WorkspaceStatusInReview {
		t.Fatalf("HITL workspace statuses = %v, want in-review", fr.statuses)
	}
}

func TestEnsureNodeRuntimeSendFailureClosesLiveTerminal(t *testing.T) {
	ctx := context.Background()
	db := openProjectionDB(t, filepath.Join(t.TempDir(), "state.db"))
	defer db.Close()
	p := &RunProjection{DB: db}
	if err := p.migrate(); err != nil {
		t.Fatal(err)
	}
	id := run.ID("payments/basic/PAY-102")
	if err := p.insertStart(ctx, run.Start{ID: id, Repo: "payments", Workflow: workflow.Workflow{Name: "basic"}, Ticket: task.TicketRef{ID: "2", Key: "PAY-102"}}, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	if err := p.updateNodeRuntime(ctx, NodeRuntime{RunID: id, Node: "implement", TerminalID: "live-old", SessionID: "session-old", NodeVisitID: "visit-old"}); err != nil {
		t.Fatal(err)
	}
	if err := p.updateNodeRuntimeVisit(ctx, id, "implement", "visit-new"); err != nil {
		t.Fatal(err)
	}
	fr := &runtimeTestRunner{live: true, sendErr: errors.New("send failed")}
	fh := &runtimeTestHarness{}
	a := &Activities{Runner: fr, Harness: fh, Runs: p}
	nw := run.NodeWork{Work: run.Work{RunID: id, Repo: "payments", Workflow: "basic", Parent: task.TicketRef{Key: "PAY-102"}}, Node: "implement", NodeVisitID: "visit-new", Mailbox: task.Mailbox{Key: "PAY-234", Node: "implement"}}
	spec := harness.LaunchSpec{RunID: id, NodeVisitID: "visit-new", Node: "implement", Agent: "build", Title: "PAY-102:implement", Prompt: "work", NudgePrompt: "Read the latest review feedback."}
	if err := a.EnsureNodeRuntime(ctx, nw, "/srv/payments", spec, NodeRuntime{RunID: id, Node: "implement", TerminalID: "live-old", SessionID: "session-old", NodeVisitID: "visit-old"}); err != nil {
		t.Fatal(err)
	}
	if fr.closeCalls != 1 || fr.closedIDs[0] != "live-old" {
		t.Fatalf("old live terminal not closed before replacement: %+v", fr.closedIDs)
	}
	if len(fr.sentTexts) != 1 || fr.sentTexts[0] != "feedback\n\nRead the latest review feedback." {
		t.Fatalf("live revisit prompt = %q", fr.sentTexts)
	}
	if len(fh.prompts) != 1 || fh.prompts[0] != "work\n\nRead the latest review feedback." {
		t.Fatalf("revisit replacement prompt = %q", fh.prompts)
	}
	rt, _ := p.getNodeRuntime(ctx, id, "implement")
	if rt.TerminalID == "live-old" || rt.SessionID != "session-old" {
		t.Fatalf("send failure did not replace IDs: %+v", rt)
	}
	if len(fh.resumeIDs) != 1 || fh.resumeIDs[0] != "session-old" {
		t.Fatalf("replacement ResumeIDs = %v, want [session-old]", fh.resumeIDs)
	}
	if !reflect.DeepEqual(fh.rendered, []harness.PromptKind{harness.PromptFeedback, harness.PromptInitial}) {
		t.Fatalf("rendered prompts = %v, want feedback then replacement initial", fh.rendered)
	}
}

func TestEnsureNodeRuntimeSameVisitSendsNothing(t *testing.T) {
	ctx := context.Background()
	db := openProjectionDB(t, filepath.Join(t.TempDir(), "state.db"))
	defer db.Close()
	p := &RunProjection{DB: db}
	if err := p.migrate(); err != nil {
		t.Fatal(err)
	}
	id := run.ID("payments/basic/PAY-103")
	if err := p.insertStart(ctx, run.Start{ID: id, Repo: "payments", Workflow: workflow.Workflow{Name: "basic"}, Ticket: task.TicketRef{ID: "3", Key: "PAY-103"}}, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	if err := p.updateNodeRuntime(ctx, NodeRuntime{RunID: id, Node: "implement", TerminalID: "live", SessionID: "session", NodeVisitID: "visit"}); err != nil {
		t.Fatal(err)
	}
	fr := &runtimeTestRunner{live: true}
	a := &Activities{Runner: fr, Harness: &runtimeTestHarness{}, Runs: p}
	nw := run.NodeWork{Work: run.Work{RunID: id}, Node: "implement", NodeVisitID: "visit"}
	spec := harness.LaunchSpec{RunID: id, NodeVisitID: "visit", Node: "implement", Agent: "build", NudgePrompt: "custom instructions"}
	if err := a.EnsureNodeRuntime(ctx, nw, "", spec, NodeRuntime{RunID: id, Node: "implement", TerminalID: "live", SessionID: "session", NodeVisitID: "visit"}); err != nil {
		t.Fatal(err)
	}
	if len(fr.sentTexts) != 0 || fr.createCalls != 0 {
		t.Fatalf("same visit sent=%q creates=%d", fr.sentTexts, fr.createCalls)
	}
	fh := a.Harness.(*runtimeTestHarness)
	if len(fh.rendered) != 0 || fh.buildCalls != 0 {
		t.Fatalf("same visit rendered=%v buildCalls=%d, want silence", fh.rendered, fh.buildCalls)
	}
}

func TestEnsureNodeRuntimeRejectsStaleVisitWithoutLaunch(t *testing.T) {
	ctx := context.Background()
	db := openProjectionDB(t, filepath.Join(t.TempDir(), "state.db"))
	defer db.Close()
	p := &RunProjection{DB: db}
	if err := p.migrate(); err != nil {
		t.Fatal(err)
	}
	id := run.ID("payments/basic/PAY-103")
	if err := p.insertStart(ctx, run.Start{ID: id, Repo: "payments", Workflow: workflow.Workflow{Name: "basic"}, Ticket: task.TicketRef{ID: "3", Key: "PAY-103"}}, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	if err := p.updateNodeRuntimeVisit(ctx, id, "implement", "visit-current"); err != nil {
		t.Fatal(err)
	}
	fr := &runtimeTestRunner{}
	a := &Activities{Runner: fr, Harness: &runtimeTestHarness{}, Runs: p}
	nw := run.NodeWork{Work: run.Work{RunID: id}, Node: "implement", NodeVisitID: "visit-stale"}
	err := a.EnsureNodeRuntime(ctx, nw, "", harness.LaunchSpec{NodeVisitID: "visit-stale", Node: "implement", Agent: "build"}, NodeRuntime{})
	if err == nil || fr.createCalls != 0 {
		t.Fatalf("stale visit err=%v createCalls=%d", err, fr.createCalls)
	}
}

func TestRuntimeKeepPolicies(t *testing.T) {
	ctx := context.Background()
	for _, tc := range []struct {
		name           string
		policy         run.RuntimePolicy
		wantTerminal   string
		wantSession    string
		wantCloseCalls int
	}{
		{name: "explicitly close terminal keep session", policy: run.RuntimePolicy{KeepSessionsAlive: true}, wantSession: "session-1", wantCloseCalls: 1},
		{name: "explicitly discard both", policy: run.RuntimePolicy{}, wantCloseCalls: 1},
		{name: "keep both", policy: run.RuntimePolicy{KeepTerminalsAlive: true, KeepSessionsAlive: true}, wantTerminal: "term-1", wantSession: "session-1"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db := openProjectionDB(t, filepath.Join(t.TempDir(), "state.db"))
			defer db.Close()
			p := &RunProjection{DB: db}
			if err := p.migrate(); err != nil {
				t.Fatal(err)
			}
			id := run.ID("payments/basic/PAY-104")
			if err := p.insertStart(ctx, run.Start{ID: id, Repo: "payments", Workflow: workflow.Workflow{Name: "basic"}, Ticket: task.TicketRef{ID: "4", Key: "PAY-104"}}, time.Now().UTC()); err != nil {
				t.Fatal(err)
			}
			if err := p.updateNodeRuntime(ctx, NodeRuntime{RunID: id, Node: "implement", TerminalID: "term-1", SessionID: "session-1", NodeVisitID: "visit-1"}); err != nil {
				t.Fatal(err)
			}
			fr := &runtimeTestRunner{}
			a := &Activities{Runner: fr, Runs: p}
			nw := run.NodeWork{Work: run.Work{RunID: id, Parent: task.TicketRef{Key: "PAY-104"}}, Node: "implement"}
			if err := a.CheckpointNodeRuntime(ctx, nw, "", tc.policy); err != nil {
				t.Fatal(err)
			}
			rt, err := p.getNodeRuntime(ctx, id, "implement")
			if err != nil {
				t.Fatal(err)
			}
			if rt.TerminalID != tc.wantTerminal || rt.SessionID != tc.wantSession || fr.closeCalls != tc.wantCloseCalls {
				t.Fatalf("runtime=%+v closeCalls=%d", rt, fr.closeCalls)
			}
		})
	}
}

func TestEngineRuntimePolicyDefaultsKeepBoth(t *testing.T) {
	deps := Dependencies{
		Repos:   repo.NewRegistry(),
		Runner:  &runtimeTestRunner{},
		Harness: &runtimeTestHarness{},
	}
	engine, err := New(filepath.Join(t.TempDir(), "state.db"), deps)
	if err != nil {
		t.Fatal(err)
	}
	defer engine.db.Close()
	if !engine.runtime.KeepTerminalsAlive || !engine.runtime.KeepSessionsAlive {
		t.Fatalf("default runtime policy = %+v, want both true", engine.runtime)
	}
}

type runtimeTestRunner struct {
	findCalls   int
	findIDs     []string
	createCalls int
	live        bool
	liveID      string
	sendErr     error
	sentTexts   []string
	closeCalls  int
	closedIDs   []string
	statuses    []string
}

func (*runtimeTestRunner) DiscoverRepos(context.Context) ([]runner.RepoCandidate, error) {
	return nil, nil
}
func (*runtimeTestRunner) ValidateRepo(context.Context, string, string) error { return nil }
func (*runtimeTestRunner) EnsureEnvironment(context.Context, runner.RunSpec) (runner.Environment, error) {
	return runner.Environment{ID: "env"}, nil
}
func (r *runtimeTestRunner) SetEnvironmentStatus(_ context.Context, _ runner.Environment, status string) error {
	r.statuses = append(r.statuses, status)
	return nil
}
func (r *runtimeTestRunner) FindTerminal(_ context.Context, terminal runner.Terminal) (runner.Terminal, bool, error) {
	r.findCalls++
	r.findIDs = append(r.findIDs, terminal.ID)
	if !r.live || (r.liveID != "" && terminal.ID != r.liveID) {
		return runner.Terminal{}, false, nil
	}
	return terminal, true, nil
}
func (r *runtimeTestRunner) SendTerminal(_ context.Context, _ runner.Terminal, text string) error {
	r.sentTexts = append(r.sentTexts, text)
	return r.sendErr
}
func (r *runtimeTestRunner) CreateTerminal(context.Context, runner.Environment, string, runner.Command) (runner.Terminal, error) {
	r.createCalls++
	r.live = true
	r.liveID = "fresh-term"
	return runner.Terminal{ID: r.liveID}, nil
}
func (r *runtimeTestRunner) CloseTerminal(_ context.Context, terminal runner.Terminal) error {
	r.closeCalls++
	r.closedIDs = append(r.closedIDs, terminal.ID)
	r.live = false
	return nil
}
func (r *runtimeTestRunner) EnsureTerminal(ctx context.Context, env runner.Environment, stored runner.Terminal, title string, command runner.Command) (runner.Terminal, error) {
	if terminal, ok, err := r.FindTerminal(ctx, stored); err != nil {
		return runner.Terminal{}, err
	} else if ok {
		return terminal, nil
	}
	return r.CreateTerminal(ctx, env, title, command)
}
func (*runtimeTestRunner) CloseTerminals(context.Context, runner.RunSpec) error { return nil }
func (*runtimeTestRunner) CleanupRun(context.Context, runner.RunSpec) error     { return nil }

type runtimeTestHarness struct {
	buildCalls int
	prompts    []string
	resumeIDs  []string
	rendered   []harness.PromptKind
}

func (h *runtimeTestHarness) RenderPrompt(kind harness.PromptKind, _ harness.PromptData, nudge string) (string, error) {
	h.rendered = append(h.rendered, kind)
	prompt := "work"
	if kind == harness.PromptFeedback {
		prompt = "feedback"
	}
	if nudge != "" {
		prompt += "\n\n" + nudge
	}
	return prompt, nil
}

func (*runtimeTestHarness) SetupRepo(context.Context, string) error             { return nil }
func (*runtimeTestHarness) ValidateAgent(context.Context, string, string) error { return nil }
func (*runtimeTestHarness) FindSession(context.Context, string, string) (harness.Session, bool, error) {
	return harness.Session{}, false, nil
}
func (h *runtimeTestHarness) BuildCommand(spec harness.LaunchSpec) (runner.Command, error) {
	h.buildCalls++
	h.prompts = append(h.prompts, spec.Prompt)
	h.resumeIDs = append(h.resumeIDs, spec.ResumeID)
	return runner.Command{Executable: "opencode", Args: []string{spec.ResumeID}}, nil
}

func openProjectionDB(t *testing.T, path string) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	return db
}
