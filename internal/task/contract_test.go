package task_test

import (
	"context"
	"testing"

	"github.com/rajpopat27/relay-flow/internal/config"
	"github.com/rajpopat27/relay-flow/internal/task"
)

// 3.6: task-system contract tests against a fake adapter, per
// specs/integration-contracts. The fake implements task.System and the
// tests assert the contract shape and primitive separation.

// fakeSystem is a minimal in-memory task.System used by contract tests. It
// holds a mixed backing store (active parents, mailbox subtasks, completed
// parents) and Poll returns only active parents, per the contract.
type fakeSystem struct {
	all       []storedTicket // mixed backing store
	mailboxes map[string]task.Mailbox
	completed []string
	comments  []commentCall
	applied   []applyCall
	resets    []string
}

var _ task.System = (*fakeSystem)(nil)

type storedTicket struct {
	ticket    task.Ticket
	isMailbox bool
	active    bool
}

type commentCall struct {
	Target task.Target
	Body   string
	Marker string
}

type applyCall struct {
	Target     task.Target
	TaskConfig config.RawValues
}

func newFakeSystem() *fakeSystem {
	return &fakeSystem{mailboxes: map[string]task.Mailbox{}}
}

func (f *fakeSystem) Poll(context.Context) ([]task.Ticket, error) {
	var out []task.Ticket
	for _, st := range f.all {
		if st.active && !st.isMailbox {
			out = append(out, st.ticket)
		}
	}
	return out, nil
}

func (f *fakeSystem) CompileFilter(config.RawValues) (func(task.Ticket) bool, error) {
	return func(task.Ticket) bool { return true }, nil
}

func (f *fakeSystem) Claim(context.Context, task.TicketRef, string) error { return nil }

func (f *fakeSystem) ValidateConfig(context.Context, config.RawValues, map[string]config.RawValues) error {
	return nil
}

func (f *fakeSystem) EnsureMailboxes(_ context.Context, _ task.TicketRef, _ string, specs []task.MailboxSpec) (map[string]task.Mailbox, error) {
	for _, s := range specs {
		if _, ok := f.mailboxes[s.Node]; !ok {
			f.mailboxes[s.Node] = task.Mailbox{ID: "mb-" + s.Node, Key: "PAY-" + s.Node, Node: s.Node}
		}
	}
	out := map[string]task.Mailbox{}
	for _, s := range specs {
		out[s.Node] = f.mailboxes[s.Node]
	}
	return out, nil
}

func (f *fakeSystem) ApplyTaskConfig(_ context.Context, target task.Target, cfg config.RawValues) error {
	f.applied = append(f.applied, applyCall{Target: target, TaskConfig: cfg})
	return nil
}

func (f *fakeSystem) CompleteMailbox(_ context.Context, mb task.Mailbox) error {
	f.completed = append(f.completed, mb.Key)
	return nil
}

func (f *fakeSystem) HasComment(_ context.Context, _ task.Target, marker string) (bool, error) {
	for _, c := range f.comments {
		if c.Marker == marker {
			return true, nil
		}
	}
	return false, nil
}

func (f *fakeSystem) Comment(_ context.Context, target task.Target, body, marker string) error {
	f.comments = append(f.comments, commentCall{Target: target, Body: body, Marker: marker})
	return nil
}

func (f *fakeSystem) ResetForRecovery(_ context.Context, parent task.TicketRef, _ []task.Mailbox, _ config.RawValues) error {
	f.resets = append(f.resets, parent.Key)
	return nil
}

func TestPollReturnsActiveParentsOnly(t *testing.T) {
	f := newFakeSystem()
	// Mixed backing store: an active parent, a mailbox subtask that also
	// carries the wf: label, and a completed (inactive) parent.
	f.all = []storedTicket{
		{ticket: task.Ticket{ID: "1", Key: "PAY-101", Title: "parent", WorkflowClaims: []string{"wf:basicFlow"}}, active: true},
		{ticket: task.Ticket{ID: "2", Key: "PAY-102", Title: "PAY-101:coding", WorkflowClaims: []string{"wf:basicFlow"}}, isMailbox: true, active: true},
		{ticket: task.Ticket{ID: "3", Key: "PAY-103", Title: "done parent"}, active: false},
	}
	f.mailboxes["coding"] = task.Mailbox{ID: "2", Key: "PAY-102", Node: "coding"}

	got, err := f.Poll(context.Background())
	if err != nil {
		t.Fatalf("Poll failed: %v", err)
	}
	if len(got) != 1 || got[0].Key != "PAY-101" {
		t.Fatalf("Poll = %+v, want only the active parent PAY-101", got)
	}
	for _, ticket := range got {
		if ticket.Key == "PAY-102" {
			t.Fatal("Poll returned a mailbox subtask as a run candidate")
		}
		if ticket.Key == "PAY-103" {
			t.Fatal("Poll returned an inactive/completed parent")
		}
	}
}

func TestEnsureMailboxesFindsExistingCreatesOnlyMissing(t *testing.T) {
	f := newFakeSystem()
	parent := task.TicketRef{ID: "1", Key: "PAY-101", Title: "parent"}
	specs := []task.MailboxSpec{
		{Node: "exploration", Title: "PAY-101:exploration", Description: "explore"},
		{Node: "coding", Title: "PAY-101:coding", Description: "code"},
	}

	first, err := f.EnsureMailboxes(context.Background(), parent, "basicFlow", specs)
	if err != nil {
		t.Fatalf("EnsureMailboxes failed: %v", err)
	}
	if len(first) != 2 {
		t.Fatalf("EnsureMailboxes returned %d mailboxes, want 2", len(first))
	}

	// Second call: finds existing, creates none.
	second, err := f.EnsureMailboxes(context.Background(), parent, "basicFlow", specs)
	if err != nil {
		t.Fatalf("EnsureMailboxes (repeat) failed: %v", err)
	}
	for node, mb := range first {
		if second[node] != mb {
			t.Fatalf("node %s: mailbox %v changed to %v on repeat; must be reused", node, mb, second[node])
		}
	}

	// Add a node: only the missing one is created.
	specs = append(specs, task.MailboxSpec{Node: "review", Title: "PAY-101:review", Description: "review"})
	third, err := f.EnsureMailboxes(context.Background(), parent, "basicFlow", specs)
	if err != nil {
		t.Fatalf("EnsureMailboxes (extended) failed: %v", err)
	}
	if len(third) != 3 {
		t.Fatalf("EnsureMailboxes returned %d, want complete map of 3", len(third))
	}
	if third["exploration"] != first["exploration"] || third["coding"] != first["coding"] {
		t.Fatal("existing mailboxes were recreated instead of found")
	}
	if third["review"].Node != "review" {
		t.Fatalf("missing mailbox not created: %+v", third["review"])
	}
}

func TestCompleteMailboxIsNarrow(t *testing.T) {
	f := newFakeSystem()
	mb := task.Mailbox{ID: "2", Key: "PAY-102", Node: "coding"}
	if err := f.CompleteMailbox(context.Background(), mb); err != nil {
		t.Fatalf("CompleteMailbox failed: %v", err)
	}
	if len(f.completed) != 1 || f.completed[0] != "PAY-102" {
		t.Fatalf("completed = %v", f.completed)
	}
	// CompleteMailbox performs no comment/routing/runner work.
	if len(f.comments) != 0 {
		t.Fatalf("CompleteMailbox wrote comments: %v", f.comments)
	}
	if len(f.applied) != 0 {
		t.Fatalf("CompleteMailbox applied task config: %v", f.applied)
	}
}

func TestSeparatePrimitives(t *testing.T) {
	// Compile-time: the System interface exposes each primitive separately.
	var sys task.System = newFakeSystem()
	ctx := context.Background()
	parent := task.TicketRef{ID: "1", Key: "PAY-101"}
	mb := task.Mailbox{ID: "2", Key: "PAY-102", Node: "coding"}
	target := task.Target{Parent: parent, Mailbox: &mb}

	if err := sys.ApplyTaskConfig(ctx, target, config.RawValues{}); err != nil {
		t.Fatal(err)
	}
	if err := sys.CompleteMailbox(ctx, mb); err != nil {
		t.Fatal(err)
	}
	if _, err := sys.HasComment(ctx, target, "m"); err != nil {
		t.Fatal(err)
	}
	if err := sys.Comment(ctx, target, "body", "m"); err != nil {
		t.Fatal(err)
	}
	if err := sys.ResetForRecovery(ctx, parent, []task.Mailbox{mb}, config.RawValues{}); err != nil {
		t.Fatal(err)
	}
}
