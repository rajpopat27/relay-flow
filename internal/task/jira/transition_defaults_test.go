package jira

import (
	"context"
	"errors"
	"testing"

	"github.com/rajpopat27/relay-flow/internal/config"
	"github.com/rajpopat27/relay-flow/internal/retry"
	"github.com/rajpopat27/relay-flow/internal/task"
)

// 3.5: Jira transition defaults per specs/workflow-definition "Jira
// transition defaults are deterministic": omitted transitions default to
// parent In Progress at start, mailbox In Progress at agent/HITL work
// nodes, and parent Done at end; an omitted work-node parent transition
// leaves the parent unchanged.
//
// This test is package-local (package jira) so it can drive the adapter's
// ApplyTaskConfig against a test-local fake client without inventing an
// exported production constructor seam. The adapter resolves defaults
// internally; the fake records the transitions the adapter issues.

// fakeJira is a test-local fakeable Jira boundary recording transitions and
// serving a fixed search batch for Poll.
type fakeJira struct {
	parentTransitions []string
	taskTransitions   []string
	assignments       []string
	assignErr         error
	transitionErr     error
	events            []string
	// searchJSON is the raw Jira search response Poll serves.
	searchJSON    []byte
	comments      []string
	addedComments []string
	labelCalls    []string
}

func (f *fakeJira) transition(key, status string) {
	if key == "PAY-101" {
		f.parentTransitions = append(f.parentTransitions, status)
	} else {
		f.taskTransitions = append(f.taskTransitions, status)
	}
}

// newSystemWithFake builds the adapter around the test-local Jira client.
func newSystemWithFake(t *testing.T, fake *fakeJira) task.System {
	t.Helper()
	sys, err := newSystemForTest(fake)
	if err != nil {
		t.Fatalf("adapter construction failed: %v", err)
	}
	return sys
}

func TestStartDefaultParentInProgress(t *testing.T) {
	fake := &fakeJira{}
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
	fake := &fakeJira{}
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
	fake := &fakeJira{}
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
	if len(fake.events) != 1 || fake.events[0] != "transition" {
		t.Fatalf("mailbox events = %v, want one combined transition", fake.events)
	}
}

func TestWorkNodeAssignmentFailurePreventsTransition(t *testing.T) {
	fake := &fakeJira{assignErr: errors.New("assignment failed")}
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
	fake := &fakeJira{}
	sys := newSystemWithFake(t, fake)
	parent := task.TicketRef{ID: "1", Key: "PAY-101", Title: "parent"}

	// end: omitted transitionTo. The end effective config carries no
	// transition; the adapter must still default the parent to Done. Run
	// orchestration merges EndDefaults under the end node config before
	// applying it to the parent target.
	endCfg := config.Merge(sys.(task.LifecycleDefaults).EndDefaults(), config.RawValues{})
	if err := sys.ApplyTaskConfig(context.Background(), task.Target{Parent: parent}, endCfg); err != nil {
		t.Fatalf("ApplyTaskConfig end failed: %v", err)
	}
	if len(fake.parentTransitions) != 1 || fake.parentTransitions[0] != "Done" {
		t.Fatalf("end parent transitions = %v, want [Done]", fake.parentTransitions)
	}
}

func TestPrepareRestartReopensMailboxesWithoutChangingParent(t *testing.T) {
	fake := &fakeJira{}
	sys := newSystemWithFake(t, fake)
	preparer, ok := sys.(task.RestartPreparer)
	if !ok {
		t.Fatal("Jira system does not implement RestartPreparer")
	}
	parent := task.TicketRef{ID: "1", Key: "PAY-101"}
	mailboxes := []task.Mailbox{
		{ID: "2", Key: "PAY-102", Node: "coding"},
		{ID: "3", Key: "PAY-103", Node: "review"},
	}
	if err := preparer.PrepareRestart(context.Background(), parent, mailboxes); err != nil {
		t.Fatalf("PrepareRestart failed: %v", err)
	}
	if len(fake.taskTransitions) != 2 || fake.taskTransitions[0] != "To Do" || fake.taskTransitions[1] != "To Do" {
		t.Fatalf("mailbox transitions = %v, want two To Do transitions", fake.taskTransitions)
	}
	if len(fake.parentTransitions) != 0 {
		t.Fatalf("parent transitions = %v, want none (start owns parent status)", fake.parentTransitions)
	}
}

func TestPrepareRestartPreservesHumanJiraStateOnConflict(t *testing.T) {
	fake := &fakeJira{transitionErr: errors.New(`transition to "To Do" is not available for PAY-102`)}
	sys := newSystemWithFake(t, fake)
	preparer := sys.(task.RestartPreparer)
	err := preparer.PrepareRestart(context.Background(), task.TicketRef{Key: "PAY-101"}, []task.Mailbox{
		{ID: "2", Key: "PAY-102", Node: "coding"},
	})
	if err == nil {
		t.Fatal("incompatible Jira mailbox status was accepted")
	}
	if got := retry.Classify(err).Kind; got != retry.Conflict {
		t.Fatalf("failure kind = %q, want conflict: %v", got, err)
	}
	if len(fake.parentTransitions) != 0 {
		t.Fatalf("parent transitions = %v, want none", fake.parentTransitions)
	}
}

func TestExplicitTransitionsWin(t *testing.T) {
	fake := &fakeJira{}
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
