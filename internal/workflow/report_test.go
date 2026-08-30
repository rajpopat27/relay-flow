package workflow_test

import (
	"strings"
	"testing"

	"github.com/rajpopat27/relay-flow/internal/workflow"
)

// 3.2 + 3.3: structured report contract and ValidateReport behavior per
// specs/structured-node-reporting.

func fullSummary() workflow.Summary {
	return workflow.Summary{
		Completed:        "did the work",
		Commits:          "abc123",
		NotCompleted:     "None",
		IssuesDiscovered: "None",
		Verification:     "ran tests",
		Notes:            "None",
	}
}

func fullFeedback() workflow.Feedback {
	return workflow.Feedback{
		ReasonForNextStep: "work done",
		RequiredActions:   "review it",
		RelevantContext:   "None",
		ExpectedResult:    "approval",
	}
}

func noneFeedback() workflow.Feedback {
	return workflow.Feedback{
		ReasonForNextStep: "None",
		RequiredActions:   "None",
		RelevantContext:   "None",
		ExpectedResult:    "None",
	}
}

func TestValidateReportAcceptsCompleteSuccess(t *testing.T) {
	wf := parse(t, "basicFlow", minimalValid)
	r := workflow.Report{
		Status:   workflow.OutcomeSuccess,
		NextStep: "end",
		Summary:  fullSummary(),
		Feedback: noneFeedback(),
	}
	if err := wf.ValidateReport("coding", r); err != nil {
		t.Fatalf("valid end report rejected: %v", err)
	}
}

func TestValidateReportRequiresEverySection(t *testing.T) {
	wf := parse(t, "basicFlow", minimalValid)

	base := workflow.Report{
		Status:   workflow.OutcomeSuccess,
		NextStep: "end",
		Summary:  fullSummary(),
		Feedback: noneFeedback(),
	}

	// Empty (non-None) strings are invalid: a required section is either
	// real content or the literal None.
	cases := map[string]func(workflow.Report) workflow.Report{
		"missing completed": func(r workflow.Report) workflow.Report {
			r.Summary.Completed = ""
			return r
		},
		"missing commits": func(r workflow.Report) workflow.Report {
			r.Summary.Commits = ""
			return r
		},
		"missing notCompleted": func(r workflow.Report) workflow.Report {
			r.Summary.NotCompleted = ""
			return r
		},
		"missing issuesDiscovered": func(r workflow.Report) workflow.Report {
			r.Summary.IssuesDiscovered = ""
			return r
		},
		"missing verification": func(r workflow.Report) workflow.Report {
			r.Summary.Verification = ""
			return r
		},
		"missing notes": func(r workflow.Report) workflow.Report {
			r.Summary.Notes = ""
			return r
		},
		"missing reasonForNextStep": func(r workflow.Report) workflow.Report {
			r.Feedback.ReasonForNextStep = ""
			return r
		},
		"missing requiredActions": func(r workflow.Report) workflow.Report {
			r.Feedback.RequiredActions = ""
			return r
		},
		"missing relevantContext": func(r workflow.Report) workflow.Report {
			r.Feedback.RelevantContext = ""
			return r
		},
		"missing expectedResult": func(r workflow.Report) workflow.Report {
			r.Feedback.ExpectedResult = ""
			return r
		},
		"missing nextStep": func(r workflow.Report) workflow.Report {
			r.NextStep = ""
			return r
		},
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			if err := wf.ValidateReport("coding", mutate(base)); err == nil {
				t.Fatal("incomplete report accepted")
			}
		})
	}
}

func TestValidateReportStatusValues(t *testing.T) {
	wf := parse(t, "basicFlow", minimalValid)
	r := workflow.Report{NextStep: "end", Summary: fullSummary(), Feedback: noneFeedback()}

	r.Status = "done"
	if err := wf.ValidateReport("coding", r); err == nil {
		t.Fatal("status other than success|failure accepted")
	}
	r.Status = workflow.OutcomeFailure
	// failure cannot select end (only coding is a failure route); use coding.
	r.NextStep = "coding"
	r.Feedback = fullFeedback()
	if err := wf.ValidateReport("coding", r); err != nil {
		t.Fatalf("valid failure report rejected: %v", err)
	}
}

func TestValidateReportNextStepMustMatchStatusRoute(t *testing.T) {
	wf := parse(t, "basicFlow", minimalValid)

	t.Run("target legal only for other outcome", func(t *testing.T) {
		// end is only configured for success on coding.
		r := workflow.Report{
			Status:   workflow.OutcomeFailure,
			NextStep: "end",
			Summary:  fullSummary(),
			Feedback: fullFeedback(),
		}
		if err := wf.ValidateReport("coding", r); err == nil {
			t.Fatal("failure report selecting a success-only target accepted")
		}
	})

	t.Run("unknown target", func(t *testing.T) {
		r := workflow.Report{
			Status:   workflow.OutcomeSuccess,
			NextStep: "nowhere",
			Summary:  fullSummary(),
			Feedback: fullFeedback(),
		}
		err := wf.ValidateReport("coding", r)
		if err == nil {
			t.Fatal("next step naming an unconfigured target accepted")
		}
		// The error should surface the legal choices.
		if !strings.Contains(err.Error(), "end") {
			t.Fatalf("error %q does not list the legal choice end", err)
		}
	})

	t.Run("revisit loop route accepted", func(t *testing.T) {
		r := workflow.Report{
			Status:   workflow.OutcomeFailure,
			NextStep: "coding",
			Summary:  fullSummary(),
			Feedback: fullFeedback(),
		}
		if err := wf.ValidateReport("coding", r); err != nil {
			t.Fatalf("failure self-loop rejected: %v", err)
		}
	})
}

func TestValidateReportEndRequiresNoneFeedback(t *testing.T) {
	wf := parse(t, "basicFlow", minimalValid)

	t.Run("end with all-None feedback accepted", func(t *testing.T) {
		r := workflow.Report{
			Status:   workflow.OutcomeSuccess,
			NextStep: "end",
			Summary:  fullSummary(),
			Feedback: noneFeedback(),
		}
		if err := wf.ValidateReport("coding", r); err != nil {
			t.Fatalf("end report with None feedback rejected: %v", err)
		}
	})

	t.Run("end with feedback content rejected", func(t *testing.T) {
		r := workflow.Report{
			Status:   workflow.OutcomeSuccess,
			NextStep: "end",
			Summary:  fullSummary(),
			Feedback: fullFeedback(),
		}
		if err := wf.ValidateReport("coding", r); err == nil {
			t.Fatal("end report carrying feedback accepted; end has no mailbox")
		}
	})

	t.Run("end report still requires summary", func(t *testing.T) {
		r := workflow.Report{
			Status:   workflow.OutcomeSuccess,
			NextStep: "end",
			Summary:  workflow.Summary{},
			Feedback: noneFeedback(),
		}
		if err := wf.ValidateReport("coding", r); err == nil {
			t.Fatal("end report without summary accepted")
		}
	})
}

func TestValidateReportCalledForBothNodeTypes(t *testing.T) {
	hitlYaml := strings.Replace(minimalValid, "    type: agent", "    type: hitl", 1)
	wf := parse(t, "basicFlow", hitlYaml)

	valid := workflow.Report{
		Status:   workflow.OutcomeSuccess,
		NextStep: "end",
		Summary:  fullSummary(),
		Feedback: noneFeedback(),
	}
	if err := wf.ValidateReport("coding", valid); err != nil {
		t.Fatalf("HITL node report rejected: %v", err)
	}
	invalid := valid
	invalid.NextStep = "nowhere"
	if err := wf.ValidateReport("coding", invalid); err == nil {
		t.Fatal("invalid HITL report accepted; ValidateReport is shared by agent and HITL nodes")
	}
}

func TestValidateReportNoneMeansIntentionallyEmpty(t *testing.T) {
	wf := parse(t, "basicFlow", minimalValid)
	s := fullSummary()
	s.IssuesDiscovered = "None"
	s.NotCompleted = "None"
	r := workflow.Report{
		Status:   workflow.OutcomeSuccess,
		NextStep: "end",
		Summary:  s,
		Feedback: noneFeedback(),
	}
	if err := wf.ValidateReport("coding", r); err != nil {
		t.Fatalf("report with literal None sections rejected: %v", err)
	}
}
