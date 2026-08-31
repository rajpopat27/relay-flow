package beads

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/rajpopat27/relay-flow/internal/config"
	"github.com/rajpopat27/relay-flow/internal/retry"
	"github.com/rajpopat27/relay-flow/internal/task"
	"github.com/rajpopat27/relay-flow/internal/task/beads/bdcli"
)

func TestApplyTaskConfigExpectedMailboxStateUsesUnconditionalUpdate(t *testing.T) {
	client := newStatusClient(map[string]string{"demo-parent.1": "in_progress"})
	sys := &system{cli: client}
	target := task.Target{
		Parent:  task.TicketRef{ID: "demo-parent", Key: "demo-parent"},
		Mailbox: &task.Mailbox{ID: "demo-parent.1", Key: "demo-parent.1", Node: "implement"},
	}

	err := sys.ApplyTaskConfig(context.Background(), target, config.RawValues{
		"status": map[string]any{"mailbox": "closed"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(client.shown, []string{"demo-parent.1"}) {
		t.Fatalf("show calls = %v, want one mailbox read", client.shown)
	}
	if len(client.updates) != 1 {
		t.Fatalf("updates = %+v, want one unconditional status update", client.updates)
	}
	update := client.updates[0]
	if update.issueID != "demo-parent.1" || update.input.Status != "closed" {
		t.Fatalf("status update = %+v", update)
	}
	if update.input.Description != nil || len(update.input.AddLabels) != 0 || update.input.ClearDefer || update.input.Force {
		t.Fatalf("status update carried unrelated flags: %+v", update.input)
	}
}

func TestApplyTaskConfigAlreadyAtTargetIsIdempotent(t *testing.T) {
	client := newStatusClient(map[string]string{"demo-parent.1": "closed"})
	sys := &system{cli: client}
	target := task.Target{
		Parent:  task.TicketRef{ID: "demo-parent", Key: "demo-parent"},
		Mailbox: &task.Mailbox{ID: "demo-parent.1", Key: "demo-parent.1", Node: "implement"},
	}

	if err := sys.ApplyTaskConfig(context.Background(), target, config.RawValues{
		"status": map[string]any{"mailbox": "closed"},
	}); err != nil {
		t.Fatal(err)
	}
	if len(client.shown) != 1 || client.shown[0] != "demo-parent.1" {
		t.Fatalf("show calls = %v, want one target-state read", client.shown)
	}
	if len(client.updates) != 0 {
		t.Fatalf("already-target status issued updates: %+v", client.updates)
	}
}

func TestApplyTaskConfigWithoutStatusIsNoOp(t *testing.T) {
	client := newStatusClient(map[string]string{"demo-parent": "open"})
	sys := &system{cli: client}
	target := task.Target{Parent: task.TicketRef{ID: "demo-parent", Key: "demo-parent"}}

	if err := sys.ApplyTaskConfig(context.Background(), target, nil); err != nil {
		t.Fatal(err)
	}
	if len(client.shown) != 0 || len(client.updates) != 0 {
		t.Fatalf("no-status config caused calls: show=%v updates=%+v", client.shown, client.updates)
	}
}

func TestApplyTaskConfigIncompatibleMailboxStateReturnsConflict(t *testing.T) {
	client := newStatusClient(map[string]string{"demo-parent.1": "in_review"})
	sys := &system{cli: client}
	target := task.Target{
		Parent:  task.TicketRef{ID: "demo-parent", Key: "demo-parent"},
		Mailbox: &task.Mailbox{ID: "demo-parent.1", Key: "demo-parent.1", Node: "implement"},
	}

	err := sys.ApplyTaskConfig(context.Background(), target, config.RawValues{
		"status": map[string]any{"mailbox": "closed"},
	})
	if err == nil {
		t.Fatal("incompatible mailbox state was accepted")
	}
	if got := retry.Classify(err).Kind; got != retry.Conflict {
		t.Fatalf("failure kind = %q, want conflict: %v", got, err)
	}
	if len(client.updates) != 0 {
		t.Fatalf("incompatible state issued a status update: %+v", client.updates)
	}
}

func TestApplyTaskConfigParentStatusUsesReadBeforeWrite(t *testing.T) {
	client := newStatusClient(map[string]string{"demo-parent": "open"})
	sys := &system{cli: client}
	target := task.Target{Parent: task.TicketRef{ID: "demo-parent", Key: "demo-parent"}}

	if err := sys.ApplyTaskConfig(context.Background(), target, config.RawValues{
		"status": map[string]any{"parent": "closed"},
	}); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(client.shown, []string{"demo-parent"}) {
		t.Fatalf("show calls = %v, want one parent read", client.shown)
	}
	if len(client.updates) != 1 || client.updates[0].issueID != "demo-parent" || client.updates[0].input.Status != "closed" {
		t.Fatalf("parent status updates = %+v", client.updates)
	}
}

func TestCompleteMailboxUsesReadBeforeWriteAndIsIdempotent(t *testing.T) {
	client := newStatusClient(map[string]string{"demo-parent.1": "in_progress"})
	sys := &system{cli: client}
	mailbox := task.Mailbox{ID: "demo-parent.1", Key: "demo-parent.1", Node: "implement"}

	if err := sys.CompleteMailbox(context.Background(), mailbox); err != nil {
		t.Fatal(err)
	}
	if len(client.shown) != 1 || client.shown[0] != mailbox.Key || len(client.updates) != 1 || client.updates[0].input.Status != "closed" {
		t.Fatalf("completion calls = show:%v updates:%+v", client.shown, client.updates)
	}

	client.shown = nil
	client.updates = nil
	client.issues[mailbox.Key] = bdcli.Issue{ID: mailbox.Key, Status: "closed"}
	if err := sys.CompleteMailbox(context.Background(), mailbox); err != nil {
		t.Fatal(err)
	}
	if len(client.shown) != 1 || len(client.updates) != 0 {
		t.Fatalf("already-closed completion calls = show:%v updates:%+v", client.shown, client.updates)
	}
}

func TestCompleteMailboxIncompatibleStateReturnsConflictWithoutUpdate(t *testing.T) {
	client := newStatusClient(map[string]string{"demo-parent.1": "in_review"})
	sys := &system{cli: client}
	mailbox := task.Mailbox{ID: "demo-parent.1", Key: "demo-parent.1", Node: "implement"}

	err := sys.CompleteMailbox(context.Background(), mailbox)
	if err == nil {
		t.Fatal("incompatible mailbox completion was accepted")
	}
	if got := retry.Classify(err).Kind; got != retry.Conflict {
		t.Fatalf("failure kind = %q, want conflict: %v", got, err)
	}
	if len(client.updates) != 0 {
		t.Fatalf("incompatible completion issued update: %+v", client.updates)
	}
}

func TestStatusRaceAfterReadUsesOneUnconditionalUpdate(t *testing.T) {
	client := newStatusClient(map[string]string{"demo-parent.1": "in_progress"})
	client.beforeUpdate = func(issueID string) {
		client.issues[issueID] = bdcli.Issue{ID: issueID, Status: "in_review"}
	}
	sys := &system{cli: client}
	target := task.Target{
		Parent:  task.TicketRef{ID: "demo-parent", Key: "demo-parent"},
		Mailbox: &task.Mailbox{ID: "demo-parent.1", Key: "demo-parent.1", Node: "implement"},
	}

	if err := sys.ApplyTaskConfig(context.Background(), target, config.RawValues{
		"status": map[string]any{"mailbox": "closed"},
	}); err != nil {
		t.Fatalf("accepted read/write race returned error: %v", err)
	}
	if len(client.shown) != 1 || len(client.updates) != 1 {
		t.Fatalf("race calls = show:%v updates:%+v, want one read and one write", client.shown, client.updates)
	}
	if client.updates[0].input.Status != "closed" {
		t.Fatalf("race update = %+v", client.updates[0])
	}
}

func TestResetForRecoveryReopensParentAndMailboxesAndClearsDefer(t *testing.T) {
	client := newStatusClient(map[string]string{
		"demo-parent":   "blocked",
		"demo-parent.1": "closed",
		"demo-parent.2": "deferred",
	})
	sys := &system{cli: client}
	parent := task.TicketRef{ID: "demo-parent", Key: "demo-parent", Title: "parent"}
	mailboxes := []task.Mailbox{
		{ID: "demo-parent.1", Key: "demo-parent.1", Node: "implement"},
		{ID: "demo-parent.2", Key: "demo-parent.2", Node: "review"},
	}

	if err := sys.ResetForRecovery(context.Background(), parent, mailboxes, nil); err != nil {
		t.Fatal(err)
	}
	if len(client.updates) != 3 {
		t.Fatalf("recovery updates = %+v, want parent plus two mailboxes", client.updates)
	}
	seen := make(map[string]bdcli.UpdateInput, len(client.updates))
	for _, update := range client.updates {
		seen[update.issueID] = update.input
	}
	for _, issueID := range []string{"demo-parent", "demo-parent.1", "demo-parent.2"} {
		input, ok := seen[issueID]
		if !ok {
			t.Fatalf("recovery did not reset %s: %+v", issueID, seen)
		}
		if input.Status != "open" || !input.ClearDefer {
			t.Fatalf("recovery update for %s = %+v, want open and clear defer", issueID, input)
		}
		if input.Description != nil || len(input.AddLabels) != 0 {
			t.Fatalf("recovery update for %s changed preserved text/labels: %+v", issueID, input)
		}
	}
}

type statusClient struct {
	mailboxClient
	issues       map[string]bdcli.Issue
	shown        []string
	updates      []updateCall
	beforeUpdate func(string)
}

func newStatusClient(statuses map[string]string) *statusClient {
	issues := make(map[string]bdcli.Issue, len(statuses))
	for issueID, status := range statuses {
		issues[issueID] = bdcli.Issue{ID: issueID, Status: status}
	}
	return &statusClient{issues: issues}
}

func (f *statusClient) Show(_ context.Context, issueID string) (bdcli.Issue, error) {
	f.shown = append(f.shown, issueID)
	issue, ok := f.issues[issueID]
	if !ok {
		return bdcli.Issue{}, errors.New("unknown issue " + issueID)
	}
	return issue, nil
}

func (f *statusClient) Update(_ context.Context, issueID string, input bdcli.UpdateInput) error {
	f.updates = append(f.updates, updateCall{issueID: issueID, input: input})
	if f.beforeUpdate != nil {
		f.beforeUpdate(issueID)
	}
	issue := f.issues[issueID]
	if input.Status != "" {
		issue.Status = input.Status
	}
	f.issues[issueID] = issue
	return nil
}

var _ bdcli.Client = (*statusClient)(nil)
