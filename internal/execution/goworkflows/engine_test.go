package goworkflows_test

import (
	"context"
	"database/sql"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/rajpopat27/relay-flow/internal/execution/goworkflows"
	"github.com/rajpopat27/relay-flow/internal/harness"
	"github.com/rajpopat27/relay-flow/internal/harness/opencode"
	"github.com/rajpopat27/relay-flow/internal/identity"
	"github.com/rajpopat27/relay-flow/internal/repo"
	"github.com/rajpopat27/relay-flow/internal/run"
	"github.com/rajpopat27/relay-flow/internal/task"
	"github.com/rajpopat27/relay-flow/internal/workflow"
)

// 3.14-3.16, 3.20, 3.22, 3.26: durable run execution behavior per
// specs/durable-run-execution. Fakes live in fakes_test.go and record one
// ordered event log so ordering and no-replay claims are observable.

func linearWorkflow(cleanup bool) workflow.Workflow {
	return workflow.Workflow{
		Name:               "basicFlow",
		Repos:              []string{"payments"},
		CleanupRunnerOnEnd: cleanup,
		TaskConfig: map[string]any{
			"transitionTo": map[string]any{"parentStatus": "In Progress"},
		},
		Nodes: map[string]workflow.Node{
			"start": {OnSuccess: []workflow.Route{{Target: "coding"}}},
			"coding": {
				Type: workflow.NodeAgent, Agent: "build", Description: "work",
				OnSuccess: []workflow.Route{{Target: "end"}},
				OnFailure: []workflow.Route{{Target: "coding"}},
			},
			"end": {TaskConfig: map[string]any{"transitionTo": map[string]any{"parentStatus": "Done"}}},
		},
	}
}

func TestMailboxDescriptionAndLaunchPromptAreTaskSystemNeutral(t *testing.T) {
	wf := linearWorkflow(false)
	node := wf.Nodes["coding"]
	node.Type = workflow.NodeHITL
	spec := goworkflows.MailboxSpecForNode(&wf, "PAY-101", "coding", node)
	description := spec.Description
	for _, want := range []string{
		"Required report format:",
		"Node names identify workflow stages",
		"SUMMARY is written to this current mailbox",
		"requested changes in FEEDBACK",
	} {
		if !strings.Contains(description, want) {
			t.Fatalf("HITL mailbox description missing %q:\n%s", want, description)
		}
	}
	for _, unwanted := range []string{"Jira", "OpenCode", "Question tool", "Approve and Reject", "Discuss the task with the human"} {
		if strings.Contains(description, unwanted) {
			t.Fatalf("generic mailbox description contains %q:\n%s", unwanted, description)
		}
	}

	for _, taskSystem := range []string{"jira", "linear"} {
		prompt, err := opencode.New().RenderPrompt(harness.PromptInitial, harness.PromptData{
			TaskSystem: taskSystem, Ticket: "PAY-101", Mailbox: "PAY-234", NodeType: workflow.NodeHITL,
		}, "")
		if err != nil {
			t.Fatal(err)
		}
		want := "Task system: " + taskSystem + "\nUse the " + taskSystem + " tools to read the parent ticket PAY-101.\n\nYour mailbox is PAY-234. Read its description and comments for node instructions and feedback.\n\nReturn the complete report directly. Relay-flow will show a native TUI approval dialog after the report is valid. Do not use OpenCode's Question tool for relay-flow approval."
		if prompt != want {
			t.Fatalf("RenderPrompt(%s) = %q, want %q", taskSystem, prompt, want)
		}
		if strings.Contains(prompt, "Jira") || strings.Contains(prompt, "subtask") {
			t.Fatalf("launch prompt contains task-system-specific mailbox wording: %q", prompt)
		}
		if strings.Contains(prompt, "STATUS:") || strings.Contains(prompt, node.Description) {
			t.Fatalf("launch prompt duplicates mailbox instructions: %q", prompt)
		}
	}
}

func startRun(engine *goworkflows.Engine, wf workflow.Workflow) (run.ID, error) {
	rid := identity.NewRunID("payments", wf.Name, "PAY-101")
	_, err := engine.EnsureRun(context.Background(), run.Start{
		ID:       rid,
		Repo:     "payments",
		RepoPath: "/srv/payments",
		Workflow: wf,
		Ticket:   task.TicketRef{ID: "1", Key: "PAY-101", Title: "parent"},
	})
	return rid, err
}

func newEngine(t *testing.T, deps goworkflows.Dependencies) *goworkflows.Engine {
	t.Helper()
	path := filepath.Join(t.TempDir(), "state.db")
	e, err := goworkflows.New(path, deps)
	if err != nil {
		t.Fatalf("goworkflows.New failed: %v", err)
	}
	if err := e.Start(context.Background()); err != nil {
		t.Fatalf("engine.Start failed: %v", err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		_ = e.Shutdown(ctx)
	})
	return e
}

func TestEnsureRunCreatesMissingWorkflowForExistingProjection(t *testing.T) {
	log := newEventLog()
	sys := newFakeTaskSystem(log)
	path := filepath.Join(t.TempDir(), "state.db")
	deps := goworkflows.Dependencies{
		Repos: repoRegistryWith("payments", sys), Runner: newFakeRunner(log), Harness: newFakeHarness(log),
	}
	engine, err := goworkflows.New(path, deps)
	if err != nil {
		t.Fatal(err)
	}
	wf := linearWorkflow(false)
	rid := identity.NewRunID("payments", wf.Name, "PAY-101")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO relay_runs
		(id, repo, workflow, ticket_id, ticket_key, state, started_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)`, rid, "payments", wf.Name, "1", "PAY-101", run.StateStarting, time.Now(), time.Now()); err != nil {
		t.Fatal(err)
	}
	db.Close()
	if err := engine.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = engine.Shutdown(context.Background()) })

	created, err := engine.EnsureRun(context.Background(), run.Start{
		ID: rid, Repo: "payments", RepoPath: "/srv/payments", Workflow: wf,
		Ticket: task.TicketRef{ID: "1", Key: "PAY-101"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !created {
		t.Fatal("EnsureRun did not create the missing workflow instance")
	}
	waitFor(t, 10*time.Second, func() bool {
		r, err := engine.GetRun(context.Background(), rid)
		return err == nil && r.CurrentNode == "coding"
	})
}

func successReport(next string) workflow.Report {
	none := "None"
	return workflow.Report{
		Status:   workflow.OutcomeSuccess,
		NextStep: next,
		Summary: workflow.Summary{
			Completed: "done", Commits: "abc123", NotCompleted: none, IssuesDiscovered: none,
			Verification: "tested", Notes: none,
		},
		Feedback: workflow.Feedback{
			ReasonForNextStep: none, RequiredActions: none,
			RelevantContext: none, ExpectedResult: none,
		},
	}
}

// --- 3.15: serial graph ---

func TestRunBeginsAtStartAndFollowsEntryEdge(t *testing.T) {
	log := newEventLog()
	sys := newFakeTaskSystem(log)
	fr := newFakeRunner(log)
	fh := newFakeHarness(log)
	engine := newEngine(t, goworkflows.Dependencies{
		Repos: repoRegistryWith("payments", sys), Runner: fr, Harness: fh,
	})

	wf := linearWorkflow(false)
	node := wf.Nodes["coding"]
	node.NudgePrompt = "Continue {{ticket}} at {{node}}. Valid next steps: {{nextSteps}}."
	wf.Nodes["coding"] = node
	rid, err := startRun(engine, wf)
	if err != nil {
		t.Fatalf("EnsureRun failed: %v", err)
	}

	waitFor(t, 10*time.Second, func() bool {
		r, err := engine.GetRun(context.Background(), rid)
		return err == nil && r.CurrentNode == "coding" && r.CurrentNodeVisitID != ""
	})

	r, _ := engine.GetRun(context.Background(), rid)
	if r.State != run.StateWaiting && r.State != run.StateRunning {
		t.Fatalf("state = %q, want waiting/running at coding", r.State)
	}
	if len(fr.envs) != 1 {
		t.Fatalf("runner environments = %d, want exactly 1 ticket-scoped env", len(fr.envs))
	}
	runtime, err := engine.GetNodeRuntime(context.Background(), rid, "coding")
	if err != nil {
		t.Fatalf("GetNodeRuntime: %v", err)
	}
	if runtime.TerminalID == "" || runtime.NodeVisitID != r.CurrentNodeVisitID {
		t.Fatalf("terminal was not persisted for current visit: %+v", runtime)
	}
	promptCalls := fh.promptCalls()
	if len(promptCalls) != 1 {
		t.Fatalf("RenderPrompt calls = %+v, want one initial prompt", promptCalls)
	}
	call := promptCalls[0]
	if call.NudgeTemplate != node.NudgePrompt {
		t.Fatalf("nudge passed to harness = %q, want raw template %q", call.NudgeTemplate, node.NudgePrompt)
	}
	if call.Data.Ticket != "PAY-101" || call.Data.Workflow != wf.Name || call.Data.Repo != "payments" || call.Data.Node != "coding" || call.Data.NextSteps == "" {
		t.Fatalf("nudge prompt data = %+v, want current workflow values", call.Data)
	}

	// Pre-edge gate: before following the start edge the run ensures the
	// runner environment AND validates every referenced agent, and applies
	// the start taskConfig. Assert all three happened before the coding
	// terminal was started.
	events := log.all()
	envIdx, validateIdx, applyIdx, terminalIdx := -1, -1, -1, -1
	for i, e := range events {
		switch {
		case envIdx < 0 && hasPrefix(e, "ensureEnvironment:"):
			envIdx = i
		case validateIdx < 0 && hasPrefix(e, "validateAgent:build"):
			validateIdx = i
		case applyIdx < 0 && hasPrefix(e, "applyTaskConfig:"):
			applyIdx = i
		case terminalIdx < 0 && hasPrefix(e, "ensureTerminal:PAY-101:coding"):
			terminalIdx = i
		}
	}
	if envIdx < 0 {
		t.Fatal("runner environment never ensured before start edge")
	}
	if validateIdx < 0 {
		t.Fatal("referenced agent never validated before start edge")
	}
	if applyIdx < 0 {
		t.Fatal("start taskConfig never applied")
	}
	if terminalIdx < 0 {
		t.Fatal("coding terminal never started")
	}
	if !(envIdx < terminalIdx && validateIdx < terminalIdx && applyIdx < terminalIdx) {
		t.Fatalf("pre-edge gate violated; events=%v", events)
	}
}

func TestSerialGraphOneNodeAtATime(t *testing.T) {
	log := newEventLog()
	sys := newFakeTaskSystem(log)
	fr := newFakeRunner(log)
	engine := newEngine(t, goworkflows.Dependencies{
		Repos: repoRegistryWith("payments", sys), Runner: fr, Harness: newFakeHarness(log),
	})

	rid, err := startRun(engine, linearWorkflow(false))
	if err != nil {
		t.Fatal(err)
	}
	waitFor(t, 10*time.Second, func() bool {
		r, _ := engine.GetRun(context.Background(), rid)
		return r.CurrentNode == "coding" && r.CurrentNodeVisitID != ""
	})

	ack, err := engine.SubmitReport(context.Background(), reportRequest(rid, "coding", successReport("end")))
	if err != nil {
		t.Fatalf("SubmitReport failed: %v", err)
	}
	if !ack.Accepted {
		t.Fatalf("ack = %+v, want accepted", ack)
	}

	waitFor(t, 10*time.Second, func() bool {
		r, _ := engine.GetRun(context.Background(), rid)
		return r.State == run.StateCompleted
	})
}

func TestRevisitCreatesNewVisit(t *testing.T) {
	log := newEventLog()
	sys := newFakeTaskSystem(log)
	engine := newEngine(t, goworkflows.Dependencies{
		Repos: repoRegistryWith("payments", sys), Runner: newFakeRunner(log), Harness: newFakeHarness(log),
	})
	rid, _ := startRun(engine, linearWorkflow(false))
	waitFor(t, 10*time.Second, func() bool {
		r, _ := engine.GetRun(context.Background(), rid)
		return r.CurrentNode == "coding" && r.CurrentNodeVisitID != ""
	})
	if indexOf(log.all(), "environmentStatus:in-progress") < 0 {
		t.Fatalf("agent node did not set workspace status in-progress; events=%v", log.all())
	}

	r, _ := engine.GetRun(context.Background(), rid)
	first := r.CurrentNodeVisitID

	fail := successReport("coding")
	fail.Status = workflow.OutcomeFailure
	if _, err := engine.SubmitReport(context.Background(), reportRequest(rid, "coding", fail)); err != nil {
		t.Fatal(err)
	}

	waitFor(t, 10*time.Second, func() bool {
		r, _ := engine.GetRun(context.Background(), rid)
		return r.CurrentNode == "coding" && r.CurrentNodeVisitID != "" && r.CurrentNodeVisitID != first
	})
}

func TestEndAppliesConfigAndCompletes(t *testing.T) {
	log := newEventLog()
	sys := newFakeTaskSystem(log)
	fr := newFakeRunner(log)
	wf := linearWorkflow(true) // cleanupRunnerOnEnd
	engine := newEngine(t, goworkflows.Dependencies{
		Repos: repoRegistryWith("payments", sys), Runner: fr, Harness: newFakeHarness(log),
		Runtime: &run.RuntimePolicy{KeepTerminalsAlive: true, KeepSessionsAlive: true},
	})
	rid, _ := startRun(engine, wf)
	waitFor(t, 10*time.Second, func() bool {
		r, _ := engine.GetRun(context.Background(), rid)
		return r.CurrentNode == "coding"
	})
	if _, err := engine.RegisterNodeSession(context.Background(), run.NodeRuntimeRegistration{
		RunID: rid, Node: "coding", SessionID: "session-coding",
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := engine.SubmitReport(context.Background(), reportRequest(rid, "coding", successReport("end"))); err != nil {
		t.Fatal(err)
	}
	waitFor(t, 10*time.Second, func() bool {
		r, _ := engine.GetRun(context.Background(), rid)
		return r.State == run.StateCompleted
	})

	// end taskConfig applied (parent Done) before runner cleanup.
	events := log.all()
	endApplyIdx, cleanupIdx := -1, -1
	for i, e := range events {
		if endApplyIdx < 0 && e == "applyTaskConfig:PAY-101" && i > 0 {
			// the second parent application is the end config (start applied first)
			endApplyIdx = i
		}
		if cleanupIdx < 0 && hasPrefix(e, "cleanupRun:") {
			cleanupIdx = i
		}
	}
	if endApplyIdx < 0 {
		t.Fatalf("end taskConfig never applied to the parent; events=%v", events)
	}
	if len(fr.cleaned) != 1 {
		t.Fatalf("CleanupRun calls = %v, want 1 with cleanupRunnerOnEnd despite terminal retention", fr.cleaned)
	}
	if cleanupIdx < endApplyIdx {
		t.Fatalf("runner cleanup ran before end taskConfig; events=%v", events)
	}
	r2, _ := engine.GetRun(context.Background(), rid)
	if r2.State == run.StateCompleted && r2.FinishedAt == nil {
		t.Fatal("completed run has no FinishedAt")
	}
	rt, err := engine.GetNodeRuntime(context.Background(), rid, "coding")
	if err != nil {
		t.Fatal(err)
	}
	if rt.TerminalID != "" || rt.SessionID != "session-coding" {
		t.Fatalf("runtime after end cleanup = %+v, want terminal cleared and session retained", rt)
	}
}

func TestEndCleanupDisabledKeepsRetainedRunner(t *testing.T) {
	log := newEventLog()
	fr := newFakeRunner(log)
	engine := newEngine(t, goworkflows.Dependencies{
		Repos: repoRegistryWith("payments", newFakeTaskSystem(log)), Runner: fr, Harness: newFakeHarness(log),
		Runtime: &run.RuntimePolicy{KeepTerminalsAlive: true, KeepSessionsAlive: true},
	})
	rid, _ := startRun(engine, linearWorkflow(false))
	waitFor(t, 10*time.Second, func() bool {
		r, _ := engine.GetRun(context.Background(), rid)
		return r.CurrentNode == "coding"
	})
	if _, err := engine.SubmitReport(context.Background(), reportRequest(rid, "coding", successReport("end"))); err != nil {
		t.Fatal(err)
	}
	waitFor(t, 10*time.Second, func() bool {
		r, _ := engine.GetRun(context.Background(), rid)
		return r.State == run.StateCompleted
	})
	if indexOf(log.all(), "environmentStatus:completed") < 0 {
		t.Fatalf("end did not set workspace status completed; events=%v", log.all())
	}
	if len(fr.cleaned) != 0 || fr.liveTerminals() != 1 {
		t.Fatalf("cleanup disabled: CleanupRun calls=%v live terminals=%d, want 0 and 1", fr.cleaned, fr.liveTerminals())
	}
}

// --- 3.16: transition ordering ---

func TestTransitionOrdering(t *testing.T) {
	log := newEventLog()
	sys := newFakeTaskSystem(log)
	fr := newFakeRunner(log)
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
	engine := newEngine(t, goworkflows.Dependencies{
		Repos: repoRegistryWith("payments", sys), Runner: fr, Harness: newFakeHarness(log),
	})
	rid, _ := startRun(engine, wf)
	waitFor(t, 10*time.Second, func() bool {
		r, _ := engine.GetRun(context.Background(), rid)
		return r.CurrentNode == "coding" && r.CurrentNodeVisitID != ""
	})
	report := successReport("review")
	report.Feedback = workflow.Feedback{
		ReasonForNextStep: "ready", RequiredActions: "review it",
		RelevantContext: "diff", ExpectedResult: "approval",
	}
	if _, err := engine.SubmitReport(context.Background(), reportRequest(rid, "coding", report)); err != nil {
		t.Fatal(err)
	}

	waitFor(t, 10*time.Second, func() bool {
		r, _ := engine.GetRun(context.Background(), rid)
		return r.CurrentNode == "review"
	})
	events := log.all()
	if statusIdx, terminalIdx := indexOf(events, "environmentStatus:in-review"), indexOf(events, "ensureTerminal:PAY-101:review"); statusIdx < 0 || terminalIdx < 0 || statusIdx >= terminalIdx {
		t.Fatalf("HITL status was not set before terminal start; events=%v", events)
	}

	// Exact cross-primitive order, observed through the fake-adapter and fake-
	// runner call logs (the settled observation seam):
	// summary(current) -> feedback(selected next) -> CompleteMailbox(current)
	// -> ApplyTaskConfig(next) -> next terminal.
	// That the report+selected route are persisted BEFORE any of these effects
	// is proven at the outcome level by TestCrashImmediatelyAfterReportPersistence
	// (recovery_test.go): a crash after acceptance, with comment injection
	// failing, restarts on the same db and resumes the PERSISTED selected route
	// without re-asking the agent or re-running effects.
	idx := map[string]int{}
	for _, want := range []string{
		"comment:PAY-101-coding",         // summary to current mailbox
		"comment:PAY-101-review",         // feedback to selected next mailbox
		"completeMailbox:PAY-101-coding", // complete current
		"applyTaskConfig:PAY-101-review", // apply next node config
		"ensureTerminal:PAY-101:review",  // start next terminal
	} {
		idx[want] = indexOf(events, want)
		if idx[want] < 0 {
			t.Fatalf("missing event %q; events=%v", want, events)
		}
	}
	order := []string{
		"comment:PAY-101-coding", "comment:PAY-101-review", "completeMailbox:PAY-101-coding",
		"applyTaskConfig:PAY-101-review", "ensureTerminal:PAY-101:review",
	}
	for i := 0; i+1 < len(order); i++ {
		if idx[order[i]] >= idx[order[i+1]] {
			t.Fatalf("order violated: %q(%d) !< %q(%d); events=%v",
				order[i], idx[order[i]], order[i+1], idx[order[i+1]], events)
		}
	}
}

func TestEndSkipsFeedbackComment(t *testing.T) {
	log := newEventLog()
	sys := newFakeTaskSystem(log)
	engine := newEngine(t, goworkflows.Dependencies{
		Repos: repoRegistryWith("payments", sys), Runner: newFakeRunner(log), Harness: newFakeHarness(log),
	})
	rid, _ := startRun(engine, linearWorkflow(false))
	waitFor(t, 10*time.Second, func() bool {
		r, _ := engine.GetRun(context.Background(), rid)
		return r.CurrentNode == "coding"
	})
	if _, err := engine.SubmitReport(context.Background(), reportRequest(rid, "coding", successReport("end"))); err != nil {
		t.Fatal(err)
	}
	waitFor(t, 10*time.Second, func() bool {
		r, _ := engine.GetRun(context.Background(), rid)
		return r.State == run.StateCompleted
	})
	for _, e := range log.all() {
		if e == "comment:PAY-end" {
			t.Fatal("feedback comment written to an end mailbox; end has none")
		}
	}
}

// --- 3.20/3.22: report delivery, dedup, ack semantics ---

func TestReportAckOnlyAfterDurablePersistence(t *testing.T) {
	log := newEventLog()
	sys := newFakeTaskSystem(log)
	engine := newEngine(t, goworkflows.Dependencies{
		Repos: repoRegistryWith("payments", sys), Runner: newFakeRunner(log), Harness: newFakeHarness(log),
	})
	rid, _ := startRun(engine, linearWorkflow(false))
	waitFor(t, 10*time.Second, func() bool {
		r, _ := engine.GetRun(context.Background(), rid)
		return r.CurrentNode == "coding"
	})
	ack, err := engine.SubmitReport(context.Background(), reportRequest(rid, "coding", successReport("end")))
	if err != nil {
		t.Fatal(err)
	}
	if !ack.Accepted || ack.Duplicate {
		t.Fatalf("first report ack = %+v, want {accepted:true, duplicate:false}", ack)
	}
	// After the ack, the report is durably persisted: a crash/restart at
	// this exact point must resume the persisted route without re-asking
	// the agent. Covered by the crash-boundary test in recovery_test.go.
}

func TestNonCurrentVisitAckedAsOldDuplicate(t *testing.T) {
	log := newEventLog()
	sys := newFakeTaskSystem(log)
	engine := newEngine(t, goworkflows.Dependencies{
		Repos: repoRegistryWith("payments", sys), Runner: newFakeRunner(log), Harness: newFakeHarness(log),
	})
	rid, _ := startRun(engine, linearWorkflow(false))
	waitFor(t, 10*time.Second, func() bool {
		r, _ := engine.GetRun(context.Background(), rid)
		return r.CurrentNode == "coding"
	})
	req := reportRequest(rid, "coding", successReport("end"))
	if _, err := engine.SubmitReport(context.Background(), req); err != nil {
		t.Fatal(err)
	}
	waitFor(t, 10*time.Second, func() bool {
		r, _ := engine.GetRun(context.Background(), rid)
		return r.State == run.StateCompleted
	})

	commentsBefore := log.count("comment:")
	// Once reportId is processed, its body is irrelevant. Even a changed,
	// invalid payload is dropped before validation.
	req.Report.NextStep = "not-a-route"
	ack, err := engine.SubmitReport(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	if !ack.Accepted || !ack.Duplicate {
		t.Fatalf("stale report ack = %+v, want {accepted:true, duplicate:true}", ack)
	}
	if log.count("comment:") != commentsBefore {
		t.Fatal("duplicate report caused repeated mailbox comments")
	}
	if log.count("completeMailbox:") != 1 {
		t.Fatal("duplicate report caused repeated mailbox completion")
	}
}

func TestFirstReportOnlyConsumed(t *testing.T) {
	log := newEventLog()
	sys := newFakeTaskSystem(log)
	engine := newEngine(t, goworkflows.Dependencies{
		Repos: repoRegistryWith("payments", sys), Runner: newFakeRunner(log), Harness: newFakeHarness(log),
	})
	rid, _ := startRun(engine, linearWorkflow(false))
	waitFor(t, 10*time.Second, func() bool {
		r, _ := engine.GetRun(context.Background(), rid)
		return r.CurrentNode == "coding"
	})
	req := reportRequest(rid, "coding", successReport("end"))
	ack1, err := engine.SubmitReport(context.Background(), req)
	if err != nil || !ack1.Accepted || ack1.Duplicate {
		t.Fatalf("first ack = %+v err=%v", ack1, err)
	}
	ack2, err := engine.SubmitReport(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	if !ack2.Accepted {
		t.Fatalf("second ack = %+v, want accepted (harmless)", ack2)
	}
	waitFor(t, 10*time.Second, func() bool {
		r, _ := engine.GetRun(context.Background(), rid)
		return r.State == run.StateCompleted
	})
	if n := log.count("comment:PAY-101-coding"); n != 1 {
		t.Fatalf("coding summaries = %d, want exactly 1 (no repeated graph effects)", n)
	}
}

// --- 3.14: run identity (engine-level) ---

func TestEnsureRunIdempotentAndVisitStableAcrossReplay(t *testing.T) {
	log := newEventLog()
	sys := newFakeTaskSystem(log)
	engine := newEngine(t, goworkflows.Dependencies{
		Repos: repoRegistryWith("payments", sys), Runner: newFakeRunner(log), Harness: newFakeHarness(log),
	})
	wf := linearWorkflow(false)
	rid, _ := startRun(engine, wf)
	waitFor(t, 10*time.Second, func() bool {
		r, _ := engine.GetRun(context.Background(), rid)
		return r.CurrentNode == "coding" && r.CurrentNodeVisitID != ""
	})
	r, _ := engine.GetRun(context.Background(), rid)
	visit := r.CurrentNodeVisitID

	// Repeated EnsureRun with the same deterministic ID returns the existing
	// run without restarting and without changing the current visit.
	created, err := engine.EnsureRun(context.Background(), run.Start{
		ID: rid, Repo: "payments", RepoPath: "/srv/payments", Workflow: wf,
		Ticket: task.TicketRef{ID: "1", Key: "PAY-101"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if created {
		t.Fatal("repeated EnsureRun reported created=true; want existing run")
	}
	r2, _ := engine.GetRun(context.Background(), rid)
	if r2.CurrentNodeVisitID != visit {
		t.Fatalf("visit changed on repeated EnsureRun: %q -> %q", visit, r2.CurrentNodeVisitID)
	}
}

// --- helpers ---

func repoRegistryWith(name string, sys task.System) *repo.Registry {
	reg := &repo.Registry{}
	reg.Replace(&repo.Repo{Name: name, Path: "/srv/" + name, TaskSystem: sys})
	return reg
}

func waitFor(t *testing.T, d time.Duration, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("condition not met within " + d.String())
}

func indexOf(events []string, want string) int {
	for i, e := range events {
		if e == want {
			return i
		}
	}
	return -1
}
