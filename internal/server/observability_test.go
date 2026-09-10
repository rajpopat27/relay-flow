package server

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/rajpopat27/relay-flow/internal/run"
	"github.com/rajpopat27/relay-flow/internal/task"
	"github.com/rajpopat27/relay-flow/internal/workflow"
)

func TestWorkflowSummaryKeepsDefinitionFieldsOnJSONRoundTrip(t *testing.T) {
	definition := &workflow.Workflow{Name: "basicFlow", Repos: []string{"payments"}, Nodes: map[string]workflow.Node{"start": {}, "end": {}}}
	summary := WorkflowSummary{Workflow: definition, Repositories: []string{"payments"}, NodeCount: 2, ActiveRuns: 1}
	raw, err := json.Marshal(summary)
	if err != nil {
		t.Fatal(err)
	}
	var decoded WorkflowSummary
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded.Workflow == nil || decoded.Name != definition.Name || decoded.NodeCount != 2 || decoded.ActiveRuns != 1 {
		t.Fatalf("decoded summary = %#v", decoded)
	}
	arrayRaw, err := json.Marshal([]WorkflowSummary{summary})
	if err != nil {
		t.Fatal(err)
	}
	var legacy []*workflow.Workflow
	if err := json.Unmarshal(arrayRaw, &legacy); err != nil || len(legacy) != 1 || legacy[0].Name != definition.Name {
		t.Fatalf("legacy workflow decode = %#v, err=%v", legacy, err)
	}
	baseRaw, err := json.Marshal([]*workflow.Workflow{definition})
	if err != nil {
		t.Fatal(err)
	}
	var fallback []WorkflowSummary
	if err := json.Unmarshal(baseRaw, &fallback); err != nil || len(fallback) != 1 || fallback[0].Workflow == nil || fallback[0].Name != definition.Name {
		t.Fatalf("fallback summary decode = %#v, err=%v", fallback, err)
	}
}

func TestBuildWorkflowSummariesUsesLatestRunAndActiveCount(t *testing.T) {
	start := time.Now().UTC().Add(-time.Hour)
	workflows := []*workflow.Workflow{
		{Name: "basicFlow", Repos: []string{"payments"}, Nodes: map[string]workflow.Node{
			"start": {}, "end": {},
		}},
	}
	runs := []run.Run{
		{Workflow: "basicFlow", State: run.StateCompleted, StartedAt: start, UpdatedAt: start.Add(10 * time.Minute), Ticket: task.TicketRef{Key: "PAY-1"}},
		{Workflow: "basicFlow", State: run.StateWaiting, StartedAt: start.Add(time.Minute), UpdatedAt: start.Add(20 * time.Minute), Ticket: task.TicketRef{Key: "PAY-2"}},
	}
	got := BuildWorkflowSummaries(workflows, runs)
	if len(got) != 1 || got[0].ActiveRuns != 1 || got[0].LatestRun == nil || got[0].LatestRun.Ticket.Key != "PAY-2" {
		t.Fatalf("summaries = %#v", got)
	}
}
