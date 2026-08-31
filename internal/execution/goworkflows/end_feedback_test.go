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

func TestEndSelectionSkipsFeedbackCommentActivity(t *testing.T) {
	log := newEventLog()
	sys := newFakeTaskSystem(log)
	engine := newEndFeedbackTestEngine(t, sys)
	wf := workflow.Workflow{
		Name:  "endFeedback",
		Repos: []string{"payments"},
		Nodes: map[string]workflow.Node{
			"start": {OnSuccess: []workflow.Route{{Target: "implement"}}},
			"implement": {
				Type: workflow.NodeAgent, Agent: "build", Description: "implement",
				OnSuccess: []workflow.Route{{Target: "end"}},
				OnFailure: []workflow.Route{{Target: "implement"}},
			},
			"end": {},
		},
	}
	rid := identity.NewRunID("payments", wf.Name, "PAY-101")
	if _, err := engine.EnsureRun(context.Background(), run.Start{
		ID: rid, Repo: "payments", RepoPath: "/srv/payments", Workflow: wf,
		Ticket: task.TicketRef{ID: "demo-parent", Key: "demo-parent", Title: "parent"},
	}); err != nil {
		t.Fatal(err)
	}
	waitForEndFeedbackTest(t, engine, rid, func(r run.Run) bool {
		return r.CurrentNode == "implement"
	})

	if _, err := engine.SubmitReport(context.Background(), run.ReportRequest{
		RunID: rid, Node: "implement", ReportID: "end-feedback-test",
		Report: endFeedbackSuccessReport(),
	}); err != nil {
		t.Fatal(err)
	}
	waitForEndFeedbackTest(t, engine, rid, func(r run.Run) bool {
		return r.State == run.StateCompleted
	})

	events := log.all()
	if !hasEvent(events, "comment:demo-parent-implement") {
		t.Fatalf("current summary comment missing: %v", events)
	}
	if countEvent(events, "comment:demo-parent-implement") != 1 {
		t.Fatalf("summary comment count = %d; events=%v", countEvent(events, "comment:demo-parent-implement"), events)
	}
	if hasEventPrefixExcept(events, "comment:", "comment:demo-parent-implement") {
		t.Fatalf("end selection wrote feedback comment; events=%v", events)
	}
}

func endFeedbackSuccessReport() workflow.Report {
	none := "None"
	return workflow.Report{
		Status: workflow.OutcomeSuccess, NextStep: "end",
		Summary: workflow.Summary{
			Completed: "done", Commits: "abc123", NotCompleted: none,
			IssuesDiscovered: none, Verification: "tested", Notes: none,
		},
		Feedback: workflow.Feedback{
			ReasonForNextStep: none, RequiredActions: none,
			RelevantContext: none, ExpectedResult: none,
		},
	}
}

func newEndFeedbackTestEngine(t *testing.T, sys task.System) *goworkflows.Engine {
	t.Helper()
	return newEngine(t, goworkflows.Dependencies{
		Repos:   repoRegistryWith("payments", sys),
		Runner:  newFakeRunner(newEventLog()),
		Harness: newFakeHarness(newEventLog()),
	})
}

func waitForEndFeedbackTest(t *testing.T, engine *goworkflows.Engine, id run.ID, predicate func(run.Run) bool) {
	t.Helper()
	waitFor(t, 10*time.Second, func() bool {
		r, err := engine.GetRun(context.Background(), id)
		return err == nil && predicate(r)
	})
}

func hasEvent(events []string, want string) bool {
	for _, event := range events {
		if event == want {
			return true
		}
	}
	return false
}

func countEvent(events []string, want string) int {
	count := 0
	for _, event := range events {
		if event == want {
			count++
		}
	}
	return count
}

func hasEventPrefixExcept(events []string, prefix, allowed string) bool {
	for _, event := range events {
		if strings.HasPrefix(event, prefix) && event != allowed {
			return true
		}
	}
	return false
}
