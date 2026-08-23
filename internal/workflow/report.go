package workflow

import (
	"fmt"
	"strings"
)

// Structured report contract. All strings are required; the literal "None"
// represents an intentionally empty section.
const None = "None"

type Summary struct {
	Completed        string `json:"completed"`
	NotCompleted     string `json:"notCompleted"`
	IssuesDiscovered string `json:"issuesDiscovered"`
	Verification     string `json:"verification"`
	Notes            string `json:"notes"`
}

type Feedback struct {
	ReasonForNextStep string `json:"reasonForNextStep"`
	RequiredActions   string `json:"requiredActions"`
	RelevantContext   string `json:"relevantContext"`
	ExpectedResult    string `json:"expectedResult"`
}

type Report struct {
	Status   Outcome  `json:"status"`
	NextStep string   `json:"nextStep"`
	Summary  Summary  `json:"summary"`
	Feedback Feedback `json:"feedback"`
}

// ValidateReport verifies every section, the outcome, and that NextStep
// names exactly one configured target for that outcome. When NextStep is
// end, every feedback field must be None. It is pure and is called both at
// the server boundary and inside durable execution.
func (w *Workflow) ValidateReport(node string, report Report) error {
	if _, ok := w.Nodes[node]; !ok {
		return fmt.Errorf("workflow %q has no node %q", w.Name, node)
	}
	if report.Status != OutcomeSuccess && report.Status != OutcomeFailure {
		return fmt.Errorf("report status %q must be %q or %q", report.Status, OutcomeSuccess, OutcomeFailure)
	}
	for _, field := range []struct{ name, value string }{
		{"summary.completed", report.Summary.Completed},
		{"summary.notCompleted", report.Summary.NotCompleted},
		{"summary.issuesDiscovered", report.Summary.IssuesDiscovered},
		{"summary.verification", report.Summary.Verification},
		{"summary.notes", report.Summary.Notes},
		{"feedback.reasonForNextStep", report.Feedback.ReasonForNextStep},
		{"feedback.requiredActions", report.Feedback.RequiredActions},
		{"feedback.relevantContext", report.Feedback.RelevantContext},
		{"feedback.expectedResult", report.Feedback.ExpectedResult},
		{"nextStep", report.NextStep},
	} {
		if field.value == "" {
			return fmt.Errorf("report section %s is required (use %q for an intentionally empty section)", field.name, None)
		}
	}
	routes, err := w.Routes(node, report.Status)
	if err != nil {
		return err
	}
	legal := false
	for _, r := range routes {
		if r.Target == report.NextStep {
			legal = true
			break
		}
	}
	if !legal {
		return fmt.Errorf("report nextStep %q is not a configured %s target for node %q; valid targets: %s",
			report.NextStep, report.Status, node, strings.Join(sortedTargets(routes), ", "))
	}
	if report.NextStep == EndNode {
		f := report.Feedback
		if f.ReasonForNextStep != None || f.RequiredActions != None || f.RelevantContext != None || f.ExpectedResult != None {
			return fmt.Errorf("report selects %q: every feedback field must be %q because end has no mailbox", EndNode, None)
		}
	}
	return nil
}
