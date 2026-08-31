package jira

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/rajpopat27/relay-flow/internal/config"
	"github.com/rajpopat27/relay-flow/internal/run"
	"github.com/rajpopat27/relay-flow/internal/task"
)

func TestTaskTextTemplatesExposeValues(t *testing.T) {
	fake := &fakeJira{}
	sys, err := newSystem(context.Background(), &fakeClient{fake: fake}, task.RepoSpec{
		Name: "payments",
		RootConfig: config.RawValues{"templates": map[string]any{
			"mailboxDescription": "mailbox {{runID}}|{{ticket}}|{{workflow}}|{{repo}}|{{node}}|{{nodeType}}|{{agent}}|{{nodeDescription}}|{{nextSteps}}|{{successRoutes}}|{{failureRoutes}}|{{mailbox}}",
			"summaryComment":     "summary {{sourceNode}}|{{targetNode}}|{{mailbox}}\n{{summaryReport}}",
			"feedbackComment":    "feedback {{sourceNode}}|{{targetNode}}|{{mailbox}}\n{{feedbackReport}}",
		}},
		RepoConfig: config.RawValues{"project": "PAY", "component": "api"},
	})
	if err != nil {
		t.Fatal(err)
	}
	data := task.TextData{
		RunID: "run-1", Ticket: "PAY-1", Workflow: "flow", Repo: "payments",
		Node: "review", NodeType: "hitl", Agent: "reviewer", NodeDescription: "Review changes.",
		NextSteps: "implement; end", SuccessRoutes: "end", FailureRoutes: "implement",
		Mailbox: "PAY-3", SourceNode: "review", TargetNode: "implement",
		SummaryReport: "COMPLETED:\nreviewed", FeedbackReport: "REQUIRED ACTIONS:\nfix it",
	}
	description, err := sys.RenderText(task.TextMailboxDescription, data)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"run-1", "PAY-1", "flow", "payments", "review", "hitl", "reviewer", "Review changes.", "implement; end", "PAY-3"} {
		if !strings.Contains(description, want) {
			t.Fatalf("description missing %q: %q", want, description)
		}
	}
	summary, err := sys.RenderText(task.TextSummaryComment, data)
	if err != nil || !strings.Contains(summary, data.SummaryReport) {
		t.Fatalf("summary = %q, %v", summary, err)
	}
	feedback, err := sys.RenderText(task.TextFeedbackComment, data)
	if err != nil || !strings.Contains(feedback, "feedback review|implement|PAY-3") || !strings.Contains(feedback, data.FeedbackReport) {
		t.Fatalf("feedback = %q, %v", feedback, err)
	}
}

func TestCommentTemplatesRequireReportValues(t *testing.T) {
	for _, tc := range []struct {
		name      string
		override  map[string]any
		wantError string
	}{
		{name: "summary", override: map[string]any{"summaryComment": "missing"}, wantError: "summaryComment must contain {{summaryReport}}"},
		{name: "feedback", override: map[string]any{"feedbackComment": "missing"}, wantError: "feedbackComment must contain {{feedbackReport}}"},
		{name: "unknown", override: map[string]any{"mailboxDescription": "{{mailboxInstructions}}"}, wantError: "unknown template variable {{mailboxInstructions}}"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			raw := config.Merge(DefaultConfig(), config.RawValues{"templates": tc.override})
			var cfg Config
			if err := config.DecodeStrict(raw, &cfg); err != nil {
				t.Fatal(err)
			}
			err := validateTemplates(cfg.Templates)
			if err == nil || !strings.Contains(err.Error(), tc.wantError) {
				t.Fatalf("validateTemplates error = %v, want %q", err, tc.wantError)
			}
		})
	}
}

func TestTaskTemplatesRenderSharedReportContractValues(t *testing.T) {
	b, err := os.ReadFile("../../../testdata/report-contract.json")
	if err != nil {
		t.Fatal(err)
	}
	var fixtures map[string]struct {
		Envelope       run.ReportRequest `json:"envelope"`
		SummaryReport  string            `json:"summaryReport"`
		FeedbackReport string            `json:"feedbackReport"`
	}
	if err := json.Unmarshal(b, &fixtures); err != nil {
		t.Fatal(err)
	}
	fixture := fixtures["work"]
	sys, err := newSystem(context.Background(), &fakeClient{fake: &fakeJira{}}, task.RepoSpec{
		Name: "payments", RepoConfig: config.RawValues{"project": "PAY", "component": "api"},
	})
	if err != nil {
		t.Fatal(err)
	}
	data := task.TextData{
		Node: "coding", SourceNode: "coding", TargetNode: "coding", Mailbox: "PAY-101:coding",
		SummaryReport: fixture.SummaryReport, FeedbackReport: fixture.FeedbackReport,
	}
	summary, err := sys.RenderText(task.TextSummaryComment, data)
	if err != nil {
		t.Fatal(err)
	}
	if summary != "Summary for coding\n\n"+fixture.SummaryReport {
		t.Fatalf("summary template did not render shared summaryReport:\n%s", summary)
	}
	feedback, err := sys.RenderText(task.TextFeedbackComment, data)
	if err != nil {
		t.Fatal(err)
	}
	wantFeedback := "Feedback from coding to coding mailbox PAY-101:coding\n\n" + fixture.FeedbackReport
	if feedback != wantFeedback {
		t.Fatalf("feedback template did not render shared feedbackReport:\n%s", feedback)
	}
}
