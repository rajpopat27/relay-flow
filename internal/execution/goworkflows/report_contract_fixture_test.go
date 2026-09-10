package goworkflows

import (
	"encoding/json"
	"os"
	"testing"

	"github.com/rajpopat27/relay-flow/internal/run"
)

func TestSharedReportContractFixtureRendersCommentValues(t *testing.T) {
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
	if got := renderSummaryReport(fixture.Envelope.Report); got != fixture.SummaryReport {
		t.Fatalf("summaryReport mismatch\ngot:\n%s\nwant:\n%s", got, fixture.SummaryReport)
	}
	if got := renderFeedbackReport(fixture.Envelope.Report); got != fixture.FeedbackReport {
		t.Fatalf("feedbackReport mismatch\ngot:\n%s\nwant:\n%s", got, fixture.FeedbackReport)
	}
}
