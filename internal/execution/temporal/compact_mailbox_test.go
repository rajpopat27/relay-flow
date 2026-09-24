package temporal

import (
	"strings"
	"testing"

	"github.com/rajpopat27/relay-flow/internal/run"
	"github.com/rajpopat27/relay-flow/internal/task"
	"github.com/rajpopat27/relay-flow/internal/workflow"
)

type compactMailboxRenderer struct{ *lagTaskSystem }

func (*compactMailboxRenderer) RenderText(_ task.TextKind, data task.TextData) (string, error) {
	return data.Ticket + " / " + data.Node + " — " + data.NodeType + " — " + data.Agent +
		"\n" + data.NodeDescription + "\nSuccess routes:\n" + data.SuccessRoutes +
		"\nFailure routes:\n" + data.FailureRoutes + "\n" + data.Report, nil
}

func TestCompactMailboxTemplateDoesNotAppendGenericInstructions(t *testing.T) {
	wf := workflow.Workflow{Name: "test", Nodes: map[string]workflow.Node{
		"coder": {Type: workflow.NodeAgent, Agent: "coder", Description: "Implement", OnSuccess: []workflow.Route{{Target: "end"}}},
	}}
	specs, err := RenderMailboxSpecs(&compactMailboxRenderer{&lagTaskSystem{}}, run.Work{
		Parent: task.TicketRef{Key: "PAY-101"}, Repo: "payments", Workflow: wf.Name,
	}, &wf)
	if err != nil {
		t.Fatal(err)
	}
	if len(specs) != 1 {
		t.Fatalf("compact mailbox descriptions = %+v", specs)
	}
	for _, required := range []string{"PAY-101 / coder — agent — coder", "Implement", "Success routes:\nend", "Failure routes:", workflow.ReportFormat} {
		if !strings.Contains(specs[0].Description, required) {
			t.Fatalf("mailbox description lacks %q: %q", required, specs[0].Description)
		}
	}
	if strings.Contains(specs[0].Description, "Required report format:") {
		t.Fatalf("generic instructions appended to Jira template: %q", specs[0].Description)
	}
}

func TestConciseReportRendersOnlySelectedSummaryAndFeedback(t *testing.T) {
	r := workflow.Report{
		Summary: workflow.Summary{Completed: "done", Commits: workflow.None, NotCompleted: workflow.None,
			IssuesDiscovered: workflow.None, Verification: workflow.None, Notes: workflow.None},
		Feedback: workflow.Feedback{ReasonForNextStep: workflow.None, RequiredActions: "review it",
			RelevantContext: workflow.None, ExpectedResult: workflow.None},
	}
	if got := renderSummaryReport(r); got != "done" {
		t.Fatalf("summary comment = %q", got)
	}
	if got := renderFeedbackReport(r); got != "review it" {
		t.Fatalf("feedback comment = %q", got)
	}
}
