package run

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/rajpopat27/relay-flow/internal/workflow"
)

func TestRunDetailDerivationIsRepeatable(t *testing.T) {
	started := time.Now().UTC().Add(-time.Minute)
	finished := started.Add(30 * time.Second)
	detail := NewRunDetail(Run{State: StateWaiting})
	detail.Steps = []StepEntry{{Status: StepSucceeded, StartedAt: &started, FinishedAt: &finished, Resource: "harness"}}
	detail.DeriveInspectionFields(time.Now().UTC())
	first := detail.ResourcesDuration.Harness
	detail.DeriveInspectionFields(time.Now().UTC())
	if first <= 0 || detail.ResourcesDuration.Harness != first {
		t.Fatalf("resource duration changed on repeat: first=%s second=%s", first, detail.ResourcesDuration.Harness)
	}
}

func TestCompletedRunDoesNotInventUnvisitedBranchRows(t *testing.T) {
	detail := NewRunDetail(Run{State: StateCompleted, CurrentNode: workflow.EndNode})
	detail.Steps = []StepEntry{
		{Sequence: 0, Node: workflow.StartNode, Status: StepSucceeded},
		{Sequence: 1, Node: "coding", Status: StepSucceeded},
		{Sequence: 2, Node: workflow.EndNode, Status: StepSucceeded},
	}
	detail.AddPendingNodes(workflow.Workflow{Nodes: map[string]workflow.Node{
		workflow.StartNode: {}, "coding": {}, "review": {}, workflow.EndNode: {},
	}})
	if len(detail.Steps) != 3 {
		t.Fatalf("completed branch added pending rows: %#v", detail.Steps)
	}
}

func TestRunDetailAddsPendingStaticNodesWithoutChangingActualVisits(t *testing.T) {
	detail := NewRunDetail(Run{State: StateWaiting, CurrentNode: "coding"})
	detail.Steps = []StepEntry{{Sequence: 0, Node: workflow.StartNode, Status: StepSucceeded, Depth: 1}, {Sequence: 1, Node: "coding", Status: StepWaiting, Depth: 1}}
	detail.AddPendingNodes(workflow.Workflow{Nodes: map[string]workflow.Node{
		workflow.StartNode: {}, "coding": {OnSuccess: []workflow.Route{{Target: workflow.EndNode}}}, workflow.EndNode: {}, "review": {},
	}})
	detail.DeriveInspectionFields(time.Now().UTC())
	if len(detail.Steps) != 3 || detail.Steps[2].Status != StepPending || detail.Progress.Pending != 1 || detail.Progress.Total != 3 {
		t.Fatalf("pending detail = %#v", detail)
	}
	if detail.Steps[0].Status != StepSucceeded || detail.Steps[1].Status != StepWaiting {
		t.Fatalf("actual visits changed: %#v", detail.Steps)
	}
}

func TestRunDetailJSONKeepsBaseRunFieldsAndStepIdentity(t *testing.T) {
	detail := NewRunDetail(Run{ID: "repo/workflow/T-1", State: StateWaiting})
	detail.Steps = []StepEntry{{RunID: detail.ID, Sequence: 1, Node: "coding", Status: StepWaiting}}
	raw, err := json.Marshal(detail)
	if err != nil {
		t.Fatal(err)
	}
	text := string(raw)
	for _, want := range []string{`"id":"repo/workflow/T-1"`, `"steps"`, `"node":"coding"`} {
		if !strings.Contains(text, want) {
			t.Fatalf("JSON %q missing %s", text, want)
		}
	}
	if !strings.Contains(text, `"runId":"repo/workflow/T-1"`) {
		t.Fatalf("step identity missing from JSON: %s", text)
	}
}
