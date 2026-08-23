package goworkflows_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/rajpopat27/relay-flow/internal/execution/goworkflows"
	"github.com/rajpopat27/relay-flow/internal/identity"
	"github.com/rajpopat27/relay-flow/internal/run"
	"github.com/rajpopat27/relay-flow/internal/task"
	"github.com/rajpopat27/relay-flow/internal/workflow"
)

// 3.27-3.28: mailbox lifecycle at the run level per specs/node-mailboxes.
// Fakes in fakes_test.go record labels, specs, statuses, and comments.

func threeNodeWorkflow() workflow.Workflow {
	return workflow.Workflow{
		Name: "threeNode", Repos: []string{"payments"},
		Nodes: map[string]workflow.Node{
			"start": {OnSuccess: []workflow.Route{{Target: "exploration"}}},
			"exploration": {
				Type: workflow.NodeAgent, Agent: "build", Description: "explore the code",
				OnSuccess: []workflow.Route{{Target: "coding", When: "explored"}},
				OnFailure: []workflow.Route{{Target: "exploration"}},
			},
			"coding": {
				Type: workflow.NodeAgent, Agent: "build", Description: "write the code",
				OnSuccess: []workflow.Route{{Target: "review", When: "coded"}},
				OnFailure: []workflow.Route{{Target: "coding"}},
			},
			"review": {
				Type: workflow.NodeHITL, Agent: "reviewer", Description: "review the diff",
				OnSuccess: []workflow.Route{{Target: "end"}},
				OnFailure: []workflow.Route{{Target: "coding", When: "changes needed"}},
			},
			"end": {},
		},
	}
}

func TestMailboxesEnsuredForWorkNodesOnly(t *testing.T) {
	log := newEventLog()
	sys := newFakeTaskSystem(log)
	engine := newEngine(t, goworkflows.Dependencies{
		Repos: repoRegistryWith("payments", sys), Runner: newFakeRunner(log), Harness: newFakeHarness(log),
	})
	rid, _ := startRun(engine, threeNodeWorkflow())
	waitFor(t, 10*time.Second, func() bool {
		r, _ := engine.GetRun(context.Background(), rid)
		return r.CurrentNode == "exploration"
	})

	if len(sys.specs) == 0 {
		t.Fatal("EnsureMailboxes never called")
	}
	nodes := map[string]task.MailboxSpec{}
	for _, sp := range sys.specs {
		nodes[sp.Node] = sp
		if sp.Node == "start" || sp.Node == "end" {
			t.Fatalf("mailbox spec for reserved node %q; start/end get none", sp.Node)
		}
		if sp.Title != "PAY-101:"+sp.Node {
			t.Fatalf("mailbox title = %q, want <ticket>:<node>", sp.Title)
		}
	}
	// Each node's description must carry its type, agent, every legal success
	// and failure route target, and the explanation (When) for each route that
	// has one.
	wantRoutes := map[string]struct {
		agent   string
		ntype   string
		targets []string // success + failure
		whens   []string // configured route explanations
	}{
		"exploration": {agent: "build", ntype: "agent", targets: []string{"coding", "exploration"}, whens: []string{"explored"}},
		"coding":      {agent: "build", ntype: "agent", targets: []string{"review", "coding"}, whens: []string{"coded"}},
		"review":      {agent: "reviewer", ntype: "hitl", targets: []string{"end", "coding"}, whens: []string{"changes needed"}},
	}
	for node, want := range wantRoutes {
		sp, ok := nodes[node]
		if !ok {
			t.Fatalf("no mailbox spec for work node %q", node)
		}
		d := sp.Description
		if !strings.Contains(d, node) {
			t.Fatalf("%s description lacks node name: %q", node, d)
		}
		if !strings.Contains(d, "PAY-101") {
			t.Fatalf("%s description lacks parent identity: %q", node, d)
		}
		if !strings.Contains(d, want.agent) {
			t.Fatalf("%s description lacks agent %q: %q", node, want.agent, d)
		}
		if !strings.Contains(d, want.ntype) {
			t.Fatalf("%s description lacks node type %q: %q", node, want.ntype, d)
		}
		for _, target := range want.targets {
			if !strings.Contains(d, target) {
				t.Fatalf("%s description lacks legal route target %q: %q", node, target, d)
			}
		}
		for _, when := range want.whens {
			if !strings.Contains(d, when) {
				t.Fatalf("%s description lacks route explanation %q: %q", node, when, d)
			}
		}
	}
	// The exploration description carries its node work text.
	if !strings.Contains(nodes["exploration"].Description, "explore the code") {
		t.Fatalf("exploration description lacks node work: %q", nodes["exploration"].Description)
	}
}

func TestMailboxCarriesWorkflowLabel(t *testing.T) {
	log := newEventLog()
	sys := newFakeTaskSystem(log)
	engine := newEngine(t, goworkflows.Dependencies{
		Repos: repoRegistryWith("payments", sys), Runner: newFakeRunner(log), Harness: newFakeHarness(log),
	})
	rid, _ := startRun(engine, threeNodeWorkflow())
	waitFor(t, 10*time.Second, func() bool {
		r, _ := engine.GetRun(context.Background(), rid)
		return r.CurrentNode == "exploration"
	})
	for node, mb := range sys.mailboxesSnapshot() {
		labels := sys.labelsFor(mb.Key)
		found := false
		for _, l := range labels {
			if l == "wf:threeNode" {
				found = true
			}
		}
		if !found {
			t.Fatalf("mailbox %s (node %s) missing wf:threeNode label: %v", mb.Key, node, labels)
		}
	}
}

func TestRevisitReusesSameMailbox(t *testing.T) {
	log := newEventLog()
	sys := newFakeTaskSystem(log)
	engine := newEngine(t, goworkflows.Dependencies{
		Repos: repoRegistryWith("payments", sys), Runner: newFakeRunner(log), Harness: newFakeHarness(log),
	})
	rid, _ := startRun(engine, threeNodeWorkflow())
	waitFor(t, 10*time.Second, func() bool {
		r, _ := engine.GetRun(context.Background(), rid)
		return r.CurrentNode == "exploration"
	})
	r, _ := engine.GetRun(context.Background(), rid)
	firstVisit := r.CurrentNodeVisitID
	firstMailbox, _ := sys.mailboxFor("PAY-101", "exploration")

	// exploration failure -> exploration again (revisit).
	fail := successReport("exploration")
	fail.Status = workflow.OutcomeFailure
	fail.Feedback = workflow.Feedback{
		ReasonForNextStep: "more", RequiredActions: "explore more",
		RelevantContext: "None", ExpectedResult: "done",
	}
	if _, err := engine.SubmitReport(context.Background(), run.ReportRequest{RunID: rid, NodeVisitID: firstVisit, Report: fail}); err != nil {
		t.Fatal(err)
	}
	waitFor(t, 10*time.Second, func() bool {
		r, _ := engine.GetRun(context.Background(), rid)
		return r.CurrentNode == "exploration" && r.CurrentNodeVisitID != "" && r.CurrentNodeVisitID != firstVisit
	})
	// Same mailbox reused (found, not recreated); new visit ID differs.
	r2, _ := engine.GetRun(context.Background(), rid)
	if r2.CurrentNodeVisitID == firstVisit {
		t.Fatal("revisit kept the same nodeVisitID; each entry must get a fresh one")
	}
	if cur, _ := sys.mailboxFor("PAY-101", "exploration"); cur != firstMailbox {
		t.Fatal("revisit created a new exploration mailbox instead of reusing it")
	}
	if log.count("createMailbox:PAY-101:exploration") != 1 {
		t.Fatalf("exploration mailbox created %d times, want 1 (reused on revisit)", log.count("createMailbox:PAY-101:exploration"))
	}
}

func TestSummaryMarkerAndContentOnCurrentMailbox(t *testing.T) {
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
	r, _ := engine.GetRun(context.Background(), rid)
	if _, err := engine.SubmitReport(context.Background(), run.ReportRequest{RunID: rid, NodeVisitID: r.CurrentNodeVisitID, Report: successReport("end")}); err != nil {
		t.Fatal(err)
	}
	waitFor(t, 10*time.Second, func() bool {
		r, _ := engine.GetRun(context.Background(), rid)
		return r.State == run.StateCompleted
	})

	summaries := sys.commentBodies("PAY-101-coding")
	if len(summaries) != 1 {
		t.Fatalf("coding summaries = %d, want 1", len(summaries))
	}
	s := summaries[0]
	// Human-readable summary content from the report.
	if !strings.Contains(s.Body, "done") {
		t.Fatalf("summary body lacks completed content: %q", s.Body)
	}
	// Stable marker derived from nodeVisitID and comment type.
	if !strings.Contains(s.Marker, string(r.CurrentNodeVisitID)) {
		t.Fatalf("summary marker %q not derived from nodeVisitID %q", s.Marker, r.CurrentNodeVisitID)
	}
}

func TestSummaryCurrentFeedbackSelectedNextOnly(t *testing.T) {
	log := newEventLog()
	sys := newFakeTaskSystem(log)
	engine := newEngine(t, goworkflows.Dependencies{
		Repos: repoRegistryWith("payments", sys), Runner: newFakeRunner(log), Harness: newFakeHarness(log),
	})
	rid, _ := startRun(engine, threeNodeWorkflow())
	waitFor(t, 10*time.Second, func() bool {
		r, _ := engine.GetRun(context.Background(), rid)
		return r.CurrentNode == "exploration"
	})
	r, _ := engine.GetRun(context.Background(), rid)

	report := successReport("coding")
	report.Feedback = workflow.Feedback{
		ReasonForNextStep: "explored", RequiredActions: "code it",
		RelevantContext: "ctx", ExpectedResult: "working code",
	}
	if _, err := engine.SubmitReport(context.Background(), run.ReportRequest{RunID: rid, NodeVisitID: r.CurrentNodeVisitID, Report: report}); err != nil {
		t.Fatal(err)
	}
	waitFor(t, 10*time.Second, func() bool {
		r, _ := engine.GetRun(context.Background(), rid)
		return r.CurrentNode == "coding"
	})

	if len(sys.commentBodies("PAY-101-exploration")) == 0 {
		t.Fatal("summary not written to current exploration mailbox")
	}
	// Feedback to the selected next mailbox carries every feedback field.
	fbs := sys.commentBodies("PAY-101-coding")
	if len(fbs) == 0 {
		t.Fatal("feedback not written to selected coding mailbox")
	}
	for _, want := range []string{"explored", "code it", "ctx", "working code"} {
		if !strings.Contains(fbs[0].Body, want) {
			t.Fatalf("feedback body missing %q: %q", want, fbs[0].Body)
		}
	}
	// Unrelated mailbox (review) gets nothing.
	if len(sys.commentBodies("PAY-101-review")) != 0 {
		t.Fatal("feedback written to unrelated review mailbox")
	}
}

// 3.28: end/mailbox behavior, manual status not routing, HITL lifecycle.

func TestManualMailboxStatusDoesNotRouteGraph(t *testing.T) {
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

	// A human moves the coding mailbox to Done without a structured report.
	sys.setMailboxStatus("PAY-101-coding", "Done")

	// Reconcile/poll (EnsureRun) must not infer success or route the graph.
	wf := linearWorkflow(false)
	if _, err := engine.EnsureRun(context.Background(), run.Start{
		ID: rid, Repo: "payments", RepoPath: "/srv/payments", Workflow: wf,
		Ticket: task.TicketRef{ID: "1", Key: "PAY-101"},
	}); err != nil {
		t.Fatal(err)
	}
	time.Sleep(500 * time.Millisecond)
	r, _ := engine.GetRun(context.Background(), rid)
	if r.CurrentNode != "coding" {
		t.Fatalf("graph advanced to %q on a manual mailbox status change", r.CurrentNode)
	}
	if r.State == run.StateCompleted {
		t.Fatal("run completed without a structured report")
	}
	// No new node terminal scheduled.
	if log.count("ensureTerminal:PAY-101:review") != 0 {
		t.Fatal("manual mailbox status change scheduled the next node")
	}
}

func TestHITLUsesSameMailboxLifecycle(t *testing.T) {
	log := newEventLog()
	sys := newFakeTaskSystem(log)
	engine := newEngine(t, goworkflows.Dependencies{
		Repos: repoRegistryWith("payments", sys), Runner: newFakeRunner(log), Harness: newFakeHarness(log),
	})
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
	rid := identity.NewRunID("payments", "hitlFlow", "PAY-101")
	if _, err := engine.EnsureRun(context.Background(), run.Start{
		ID: rid, Repo: "payments", RepoPath: "/srv/payments", Workflow: wf,
		Ticket: task.TicketRef{ID: "1", Key: "PAY-101"},
	}); err != nil {
		t.Fatal(err)
	}
	waitFor(t, 10*time.Second, func() bool {
		r, _ := engine.GetRun(context.Background(), rid)
		return r.CurrentNode == "review"
	})

	// HITL review mailbox exists with the wf label.
	mb, ok := sys.mailboxFor("PAY-101", "review")
	if !ok {
		t.Fatal("no HITL review mailbox ensured")
	}
	found := false
	for _, l := range sys.labelsFor(mb.Key) {
		if l == "wf:hitlFlow" {
			found = true
		}
	}
	if !found {
		t.Fatalf("HITL mailbox missing wf label: %v", sys.labelsFor(mb.Key))
	}

	// A valid HITL report advances the run through the same lifecycle.
	r, _ := engine.GetRun(context.Background(), rid)
	if _, err := engine.SubmitReport(context.Background(), run.ReportRequest{RunID: rid, NodeVisitID: r.CurrentNodeVisitID, Report: successReport("end")}); err != nil {
		t.Fatal(err)
	}
	waitFor(t, 10*time.Second, func() bool {
		r, _ := engine.GetRun(context.Background(), rid)
		return r.State == run.StateCompleted
	})
	if len(sys.commentBodies(mb.Key)) == 0 {
		t.Fatal("HITL summary not recorded in its mailbox")
	}
}

func TestRecoveryReusesMailboxesCreatesOnlyMissing(t *testing.T) {
	// Recovery mailbox semantics through the documented task.System
	// primitives only: EnsureMailboxes finds existing and creates only
	// missing, then ResetForRecovery resets them to To Do while preserving
	// comments/labels. The full serve --recover composition is section 5.6.
	log := newEventLog()
	sys := newFakeTaskSystem(log)
	parent := task.TicketRef{ID: "1", Key: "PAY-101"}

	// Pre-existing exploration mailbox (with a comment and label to keep).
	sys.seedMailbox("PAY-101", task.Mailbox{ID: "mb-exploration", Key: "PAY-101-exploration", Node: "exploration"}, []string{"wf:threeNode"})
	sys.Comment(context.Background(), task.Target{Parent: parent, Mailbox: mailboxPtr(sys, "PAY-101", "exploration")}, "old summary", "old")

	specs := []task.MailboxSpec{
		{Node: "exploration", Title: "PAY-101:exploration", Description: "explore"},
		{Node: "coding", Title: "PAY-101:coding", Description: "code"},
		{Node: "review", Title: "PAY-101:review", Description: "review"},
	}
	mbs, err := sys.EnsureMailboxes(context.Background(), parent, "threeNode", specs)
	if err != nil {
		t.Fatal(err)
	}
	if len(mbs) != 3 {
		t.Fatalf("EnsureMailboxes = %d, want complete map of 3", len(mbs))
	}
	if log.count("foundMailbox:PAY-101:exploration") != 1 {
		t.Fatal("existing exploration mailbox not found/reused")
	}
	if log.count("createMailbox:PAY-101:exploration") != 0 {
		t.Fatal("existing exploration mailbox recreated")
	}
	if log.count("createMailbox:PAY-101:coding") != 1 || log.count("createMailbox:PAY-101:review") != 1 {
		t.Fatal("missing mailboxes not created exactly once")
	}

	// Reset to To Do, preserving the existing comment/label.
	var mbList []task.Mailbox
	for _, mb := range mbs {
		mbList = append(mbList, mb)
	}
	if err := sys.ResetForRecovery(context.Background(), parent, mbList, nil); err != nil {
		t.Fatal(err)
	}
	if sys.mailboxStatusOf("PAY-101-exploration") != "To Do" {
		t.Fatal("mailbox not reset to To Do")
	}
	if len(sys.commentBodies("PAY-101-exploration")) == 0 {
		t.Fatal("reset dropped existing mailbox comment")
	}
	if len(sys.labelsFor("PAY-101-exploration")) == 0 {
		t.Fatal("reset dropped mailbox wf label")
	}
}

func mailboxPtr(sys *fakeTaskSystem, parentKey, node string) *task.Mailbox {
	mb, _ := sys.mailboxFor(parentKey, node)
	return &mb
}
