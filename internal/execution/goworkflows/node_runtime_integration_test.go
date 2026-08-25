package goworkflows_test

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/rajpopat27/relay-flow/internal/execution/goworkflows"
	"github.com/rajpopat27/relay-flow/internal/identity"
	"github.com/rajpopat27/relay-flow/internal/run"
	"github.com/rajpopat27/relay-flow/internal/task"
	"github.com/rajpopat27/relay-flow/internal/workflow"
)

func TestPersistedRuntimeLoopAndRestart(t *testing.T) {
	log := newEventLog()
	sys := newFakeTaskSystem(log)
	fr := newFakeRunner(log)
	fh := newFakeHarness(log)
	wf := workflow.Workflow{
		Name: "loopFlow", Repos: []string{"payments"},
		Nodes: map[string]workflow.Node{
			"start": {OnSuccess: []workflow.Route{{Target: "implement"}}},
			"implement": {Type: workflow.NodeAgent, Agent: "build", Description: "implement",
				OnSuccess: []workflow.Route{{Target: "verify"}}, OnFailure: []workflow.Route{{Target: "verify"}}},
			"verify": {Type: workflow.NodeAgent, Agent: "verify", Description: "verify",
				OnSuccess: []workflow.Route{{Target: "end"}}, OnFailure: []workflow.Route{{Target: "implement"}}},
			"end": {},
		},
	}
	db := filepath.Join(t.TempDir(), "state.db")
	policy := run.RuntimePolicy{KeepTerminalsAlive: true, KeepSessionsAlive: true}
	deps := goworkflows.Dependencies{
		Repos: repoRegistryWith("payments", sys), Runner: fr, Harness: fh, Runtime: &policy,
	}
	e1, err := goworkflows.New(db, deps)
	if err != nil {
		t.Fatal(err)
	}
	if err := e1.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	rid := identity.NewRunID("payments", "loopFlow", "PAY-101")
	start := run.Start{ID: rid, Repo: "payments", RepoPath: "/srv/payments", Workflow: wf, Ticket: task.TicketRef{ID: "1", Key: "PAY-101"}, Runtime: policy}
	if _, err := e1.EnsureRun(context.Background(), start); err != nil {
		t.Fatal(err)
	}
	waitFor(t, 10*time.Second, func() bool {
		r, _ := e1.GetRun(context.Background(), rid)
		return r.CurrentNode == "implement" && r.CurrentNodeVisitID != ""
	})
	first, _ := e1.GetRun(context.Background(), rid)
	firstRT, _ := e1.GetNodeRuntime(context.Background(), rid, "implement")
	if _, err := e1.RegisterNodeSession(context.Background(), run.NodeRuntimeRegistration{RunID: rid, Node: "implement", NodeVisitID: first.CurrentNodeVisitID, SessionID: "session-implement"}); err != nil {
		t.Fatal(err)
	}
	if _, err := e1.SubmitReport(context.Background(), run.ReportRequest{RunID: rid, NodeVisitID: first.CurrentNodeVisitID, Report: loopReport(workflow.OutcomeSuccess, "verify")}); err != nil {
		t.Fatal(err)
	}
	waitFor(t, 10*time.Second, func() bool { r, _ := e1.GetRun(context.Background(), rid); return r.CurrentNode == "verify" })
	verify, _ := e1.GetRun(context.Background(), rid)
	if _, err := e1.RegisterNodeSession(context.Background(), run.NodeRuntimeRegistration{RunID: rid, Node: "verify", NodeVisitID: verify.CurrentNodeVisitID, SessionID: "session-verify"}); err != nil {
		t.Fatal(err)
	}
	verifyRT, err := e1.GetNodeRuntime(context.Background(), rid, "verify")
	if err != nil || verifyRT.SessionID != "session-verify" || verifyRT.NodeVisitID != verify.CurrentNodeVisitID {
		t.Fatalf("verify runtime = %+v, %v", verifyRT, err)
	}
	if _, err := e1.SubmitReport(context.Background(), run.ReportRequest{RunID: rid, NodeVisitID: verify.CurrentNodeVisitID, Report: loopReport(workflow.OutcomeFailure, "implement")}); err != nil {
		t.Fatal(err)
	}
	waitFor(t, 10*time.Second, func() bool {
		r, _ := e1.GetRun(context.Background(), rid)
		return r.CurrentNode == "implement" && r.CurrentNodeVisitID != first.CurrentNodeVisitID
	})
	second, _ := e1.GetRun(context.Background(), rid)
	if _, err := e1.RegisterNodeSession(context.Background(), run.NodeRuntimeRegistration{RunID: rid, Node: "implement", NodeVisitID: second.CurrentNodeVisitID, SessionID: "session-implement"}); err != nil {
		t.Fatal(err)
	}
	secondRT, _ := e1.GetNodeRuntime(context.Background(), rid, "implement")
	if secondRT.TerminalID != firstRT.TerminalID || secondRT.SessionID != "session-implement" || secondRT.NodeVisitID != second.CurrentNodeVisitID {
		t.Fatalf("loop runtime = %+v; first=%+v", secondRT, firstRT)
	}
	staleAck, err := e1.SubmitReport(context.Background(), run.ReportRequest{RunID: rid, NodeVisitID: first.CurrentNodeVisitID, Report: loopReport(workflow.OutcomeSuccess, "verify")})
	if err != nil || !staleAck.Duplicate {
		t.Fatalf("stale ack=%+v err=%v", staleAck, err)
	}
	if log.count("findTerminal:") != 0 {
		t.Fatalf("normal path used title discovery: %v", log.all())
	}
	if log.count("findSession:") != 0 {
		t.Fatalf("normal path used session discovery: %v", log.all())
	}

	oldTerminal := secondRT.TerminalID
	fr.killTerminals()
	findTerminalBefore := log.count("findTerminal:")
	findSessionBefore := log.count("findSession:")
	e2 := restartEngine(t, db, deps, e1)
	waitFor(t, 10*time.Second, func() bool {
		r, _ := e2.GetRun(context.Background(), rid)
		return r.CurrentNodeVisitID == second.CurrentNodeVisitID
	})
	if _, err := e2.EnsureRun(context.Background(), start); err != nil {
		t.Fatal(err)
	}
	resumeEvent := "buildCommand:implement:" + string(second.CurrentNodeVisitID) + ":resume=session-implement"
	waitFor(t, 10*time.Second, func() bool { return log.count(resumeEvent) == 1 })
	after, _ := e2.GetNodeRuntime(context.Background(), rid, "implement")
	if after.TerminalID == oldTerminal || after.SessionID != "session-implement" || after.NodeVisitID != second.CurrentNodeVisitID {
		t.Fatalf("restart did not resume/replace direct runtime: old=%+v after=%+v", secondRT, after)
	}
	if log.count("findTerminal:") != findTerminalBefore || log.count("findSession:") != findSessionBefore {
		t.Fatalf("restart used discovery: %v", log.all())
	}
}

func loopReport(status workflow.Outcome, next string) workflow.Report {
	r := successReport(next)
	r.Status = status
	if next != "end" {
		r.Feedback.ReasonForNextStep = "route"
		r.Feedback.RequiredActions = "act"
		r.Feedback.RelevantContext = "context"
		r.Feedback.ExpectedResult = "result"
	}
	return r
}
