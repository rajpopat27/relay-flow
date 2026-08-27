package jira

import (
	"context"
	"errors"
	"testing"

	"github.com/rajpopat27/relay-flow/internal/config"
	"github.com/rajpopat27/relay-flow/internal/task"
)

// 3.5: Jira transition defaults per specs/workflow-definition "Jira
// transition defaults are deterministic": omitted transitions default to
// parent In Progress at start, mailbox In Progress at agent/HITL work
// nodes, and parent Done at end; an omitted work-node parent transition
// leaves the parent unchanged.
//
// This test is package-local (package jira) so it can drive the adapter's
// ApplyTaskConfig against a test-local fake ACLI without inventing an
// exported production constructor seam. The adapter resolves defaults
// internally; the fake records the transitions the adapter issues.

// fakeACLI is a test-local fakeable ACLI boundary recording transitions and
// serving a fixed search batch for Poll.
type fakeACLI struct {
	parentTransitions []string
	taskTransitions   []string
	assignments       []string
	assignErr         error
	events            []string
	// searchJSON is the raw Jira search response Poll serves.
	searchJSON []byte
}

func (f *fakeACLI) transition(key, status string) {
	if key == "PAY-101" {
		f.parentTransitions = append(f.parentTransitions, status)
	} else {
		f.taskTransitions = append(f.taskTransitions, status)
	}
}

// newSystemWithFake builds the adapter around the test-local fake ACLI. The
// adapter owns its ACLI wiring; the test injects only the CLI seam.
func newSystemWithFake(t *testing.T, fake *fakeACLI) task.System {
	t.Helper()
	sys, err := newSystemForTest(fake)
	if err != nil {
		t.Fatalf("adapter construction failed: %v", err)
	}
	return sys
}

func TestStartDefaultParentInProgress(t *testing.T) {
	fake := &fakeACLI{}
	sys := newSystemWithFake(t, fake)
	parent := task.TicketRef{ID: "1", Key: "PAY-101", Title: "parent"}

	// start: omitted transitionTo, parent-only target.
	if err := sys.ApplyTaskConfig(context.Background(), task.Target{Parent: parent}, config.RawValues{}); err != nil {
		t.Fatalf("ApplyTaskConfig start failed: %v", err)
	}
	if len(fake.parentTransitions) != 1 || fake.parentTransitions[0] != "In Progress" {
		t.Fatalf("start parent transitions = %v, want [In Progress]", fake.parentTransitions)
	}
}

func TestWorkNodeDefaultMailboxInProgressParentUnchanged(t *testing.T) {
	fake := &fakeACLI{}
	sys := newSystemWithFake(t, fake)
	parent := task.TicketRef{ID: "1", Key: "PAY-101", Title: "parent"}
	mb := task.Mailbox{ID: "2", Key: "PAY-102", Node: "coding"}

	if err := sys.ApplyTaskConfig(context.Background(), task.Target{Parent: parent, Mailbox: &mb}, config.RawValues{}); err != nil {
		t.Fatalf("ApplyTaskConfig failed: %v", err)
	}
	if len(fake.taskTransitions) != 1 || fake.taskTransitions[0] != "In Progress" {
		t.Fatalf("mailbox transitions = %v, want [In Progress]", fake.taskTransitions)
	}
	if len(fake.parentTransitions) != 0 {
		t.Fatalf("parent transitions = %v, want none (parent unchanged when omitted)", fake.parentTransitions)
	}
	if len(fake.assignments) != 0 {
		t.Fatalf("mailbox assignments = %v, want none when assignee omitted", fake.assignments)
	}
}

func TestWorkNodeAssignsMailboxBeforeTransition(t *testing.T) {
	fake := &fakeACLI{}
	sys := newSystemWithFake(t, fake)
	parent := task.TicketRef{ID: "1", Key: "PAY-101", Title: "parent"}
	mb := task.Mailbox{ID: "2", Key: "PAY-102", Node: "coding"}

	err := sys.ApplyTaskConfig(context.Background(), task.Target{Parent: parent, Mailbox: &mb}, config.RawValues{
		"assignee": "reviewer@example.com",
	})
	if err != nil {
		t.Fatalf("ApplyTaskConfig failed: %v", err)
	}
	if len(fake.assignments) != 1 || fake.assignments[0] != "PAY-102:reviewer@example.com" {
		t.Fatalf("mailbox assignments = %v, want [PAY-102:reviewer@example.com]", fake.assignments)
	}
	if len(fake.events) != 2 || fake.events[0] != "assign" || fake.events[1] != "transition" {
		t.Fatalf("mailbox events = %v, want [assign transition]", fake.events)
	}
}

func TestWorkNodeAssignmentFailurePreventsTransition(t *testing.T) {
	fake := &fakeACLI{assignErr: errors.New("assignment failed")}
	sys := newSystemWithFake(t, fake)
	parent := task.TicketRef{ID: "1", Key: "PAY-101", Title: "parent"}
	mb := task.Mailbox{ID: "2", Key: "PAY-102", Node: "coding"}

	err := sys.ApplyTaskConfig(context.Background(), task.Target{Parent: parent, Mailbox: &mb}, config.RawValues{
		"assignee": "reviewer@example.com",
	})
	if err == nil || len(fake.taskTransitions) != 0 {
		t.Fatalf("ApplyTaskConfig error = %v, transitions = %v", err, fake.taskTransitions)
	}
}

func TestEndDefaultParentDone(t *testing.T) {
	fake := &fakeACLI{}
	sys := newSystemWithFake(t, fake)
	parent := task.TicketRef{ID: "1", Key: "PAY-101", Title: "parent"}

	// end: omitted transitionTo. The end effective config carries no
	// transition; the adapter must still default the parent to Done. Run
	// orchestration applies the end node config to the parent target.
	if err := sys.ApplyTaskConfig(context.Background(), task.Target{Parent: parent}, endConfig(config.RawValues{})); err != nil {
		t.Fatalf("ApplyTaskConfig end failed: %v", err)
	}
	if len(fake.parentTransitions) != 1 || fake.parentTransitions[0] != "Done" {
		t.Fatalf("end parent transitions = %v, want [Done]", fake.parentTransitions)
	}
}

func TestExplicitTransitionsWin(t *testing.T) {
	fake := &fakeACLI{}
	sys := newSystemWithFake(t, fake)
	parent := task.TicketRef{ID: "1", Key: "PAY-101", Title: "parent"}
	mb := task.Mailbox{ID: "2", Key: "PAY-102", Node: "review"}

	err := sys.ApplyTaskConfig(context.Background(), task.Target{Parent: parent, Mailbox: &mb}, config.RawValues{
		"transitionTo": map[string]any{"parentStatus": "In Review", "taskStatus": "In Review"},
	})
	if err != nil {
		t.Fatalf("ApplyTaskConfig failed: %v", err)
	}
	if len(fake.parentTransitions) != 1 || fake.parentTransitions[0] != "In Review" {
		t.Fatalf("parent transitions = %v, want [In Review]", fake.parentTransitions)
	}
	if len(fake.taskTransitions) != 1 || fake.taskTransitions[0] != "In Review" {
		t.Fatalf("mailbox transitions = %v, want [In Review]", fake.taskTransitions)
	}
}
