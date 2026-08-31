package beads

import (
	"context"
	"testing"

	"github.com/rajpopat27/relay-flow/internal/config"
	"github.com/rajpopat27/relay-flow/internal/retry"
	"github.com/rajpopat27/relay-flow/internal/task"
)

func transitionConfig(parentStatus, taskStatus string) config.RawValues {
	transition := map[string]any{}
	if parentStatus != "" {
		transition["parentStatus"] = parentStatus
	}
	if taskStatus != "" {
		transition["taskStatus"] = taskStatus
	}
	return config.RawValues{"transitionTo": transition}
}

func statusTarget(mailboxID string) task.Target {
	return task.Target{
		Parent:  task.TicketRef{ID: "demo-parent", Key: "demo-parent"},
		Mailbox: &task.Mailbox{ID: mailboxID, Key: mailboxID, Node: "implement"},
	}
}

func TestApplyTaskConfigOpensMailboxForFirstWorkEntry(t *testing.T) {
	client := newStatusClient(map[string]string{"demo-parent.1": "open"})
	sys := &system{cli: client}

	if err := sys.ApplyTaskConfig(context.Background(), statusTarget("demo-parent.1"), transitionConfig("", "in_progress")); err != nil {
		t.Fatal(err)
	}
	if len(client.shown) != 1 || client.shown[0] != "demo-parent.1" {
		t.Fatalf("show calls = %v, want one mailbox read", client.shown)
	}
	if len(client.updates) != 1 || client.updates[0].issueID != "demo-parent.1" || client.updates[0].input.Status != "in_progress" {
		t.Fatalf("updates = %+v, want open → in_progress", client.updates)
	}
}

func TestCompleteMailboxClosesInProgressMailbox(t *testing.T) {
	client := newStatusClient(map[string]string{"demo-parent.1": "in_progress"})
	sys := &system{cli: client}

	if err := sys.CompleteMailbox(context.Background(), task.Mailbox{ID: "demo-parent.1", Key: "demo-parent.1", Node: "implement"}); err != nil {
		t.Fatal(err)
	}
	if len(client.shown) != 1 || client.shown[0] != "demo-parent.1" {
		t.Fatalf("show calls = %v, want one mailbox read", client.shown)
	}
	if len(client.updates) != 1 || client.updates[0].issueID != "demo-parent.1" || client.updates[0].input.Status != "closed" {
		t.Fatalf("updates = %+v, want in_progress → closed", client.updates)
	}
}

func TestApplyTaskConfigTargetStateRetryDoesNotWrite(t *testing.T) {
	client := newStatusClient(map[string]string{"demo-parent.1": "in_progress"})
	sys := &system{cli: client}

	if err := sys.ApplyTaskConfig(context.Background(), statusTarget("demo-parent.1"), transitionConfig("", "in_progress")); err != nil {
		t.Fatal(err)
	}
	if len(client.shown) != 1 || client.shown[0] != "demo-parent.1" {
		t.Fatalf("show calls = %v, want one target-state read", client.shown)
	}
	if len(client.updates) != 0 {
		t.Fatalf("target-state retry issued an update: %+v", client.updates)
	}
}

func TestApplyTaskConfigReopensClosedMailboxOnWorkflowRevisit(t *testing.T) {
	client := newStatusClient(map[string]string{"demo-parent.1": "closed"})
	sys := &system{cli: client}

	if err := sys.ApplyTaskConfig(context.Background(), statusTarget("demo-parent.1"), transitionConfig("", "in_progress")); err != nil {
		t.Fatal(err)
	}
	if len(client.shown) != 1 || client.shown[0] != "demo-parent.1" {
		t.Fatalf("show calls = %v, want one mailbox read", client.shown)
	}
	if len(client.updates) != 1 || client.updates[0].issueID != "demo-parent.1" || client.updates[0].input.Status != "in_progress" {
		t.Fatalf("updates = %+v, want closed → in_progress", client.updates)
	}
}

func TestApplyTaskConfigRejectsIncompatibleManualMailboxState(t *testing.T) {
	client := newStatusClient(map[string]string{"demo-parent.1": "blocked"})
	sys := &system{cli: client}

	err := sys.ApplyTaskConfig(context.Background(), statusTarget("demo-parent.1"), transitionConfig("", "in_progress"))
	if err == nil {
		t.Fatal("incompatible manual mailbox state was accepted")
	}
	if got := retry.Classify(err).Kind; got != retry.Conflict {
		t.Fatalf("failure kind = %q, want conflict: %v", got, err)
	}
	if len(client.updates) != 0 {
		t.Fatalf("incompatible manual state issued an update: %+v", client.updates)
	}
}

func TestApplyTaskConfigClosesParentFromRelayFlowStates(t *testing.T) {
	for _, tc := range []struct {
		name       string
		status     string
		wantUpdate bool
	}{
		{name: "open", status: "open", wantUpdate: true},
		{name: "in progress", status: "in_progress", wantUpdate: true},
		{name: "blocked", status: "blocked", wantUpdate: true},
		{name: "deferred", status: "deferred", wantUpdate: true},
		{name: "hooked", status: "hooked", wantUpdate: true},
		{name: "already closed", status: "closed", wantUpdate: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			client := newStatusClient(map[string]string{"demo-parent": tc.status})
			sys := &system{cli: client}
			target := task.Target{Parent: task.TicketRef{ID: "demo-parent", Key: "demo-parent"}}

			if err := sys.ApplyTaskConfig(context.Background(), target, transitionConfig("closed", "")); err != nil {
				t.Fatal(err)
			}
			if len(client.shown) != 1 || client.shown[0] != "demo-parent" {
				t.Fatalf("show calls = %v, want one parent read", client.shown)
			}
			if tc.wantUpdate {
				if len(client.updates) != 1 || client.updates[0].issueID != "demo-parent" || client.updates[0].input.Status != "closed" {
					t.Fatalf("updates = %+v, want parent → closed", client.updates)
				}
				return
			}
			if len(client.updates) != 0 {
				t.Fatalf("already-target parent issued an update: %+v", client.updates)
			}
		})
	}
}
