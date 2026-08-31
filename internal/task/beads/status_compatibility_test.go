package beads

import (
	"context"
	"testing"

	"github.com/rajpopat27/relay-flow/internal/config"
	"github.com/rajpopat27/relay-flow/internal/retry"
	"github.com/rajpopat27/relay-flow/internal/task"
	"github.com/rajpopat27/relay-flow/internal/task/beads/bdcli"
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

// endTarget is the parent-only target used for the start and end lifecycle
// points.
func endTarget() task.Target {
	return task.Target{Parent: task.TicketRef{ID: "demo-parent", Key: "demo-parent"}}
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

// relay-flow only ever drives a parent through open and in_progress, so those
// are the only states a close may follow. Any other state is human-owned and
// blocks the transition, exactly like the mailbox rule.
func TestApplyTaskConfigClosesParentOnlyFromRelayFlowStates(t *testing.T) {
	for _, tc := range []struct {
		name       string
		status     string
		wantUpdate bool
		wantConfli bool
	}{
		{name: "open", status: "open", wantUpdate: true},
		{name: "in progress", status: "in_progress", wantUpdate: true},
		{name: "already closed", status: "closed"},
		{name: "human blocked", status: "blocked", wantConfli: true},
		{name: "human deferred", status: "deferred", wantConfli: true},
		{name: "human hooked", status: "hooked", wantConfli: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			client := newStatusClient(map[string]string{"demo-parent": tc.status})
			sys := &system{cli: client}
			target := task.Target{Parent: task.TicketRef{ID: "demo-parent", Key: "demo-parent"}}

			err := sys.ApplyTaskConfig(context.Background(), target, transitionConfig("closed", ""))
			if tc.wantConfli {
				if err == nil {
					t.Fatalf("human-owned parent status %q was overwritten", tc.status)
				}
				if got := retry.Classify(err).Kind; got != retry.Conflict {
					t.Fatalf("failure kind = %q, want conflict: %v", got, err)
				}
				if len(client.updates) != 0 {
					t.Fatalf("human-owned parent status issued an update: %+v", client.updates)
				}
				return
			}
			if err != nil {
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

// Jira parity: the run start moves the parent to in_progress, and the parent
// stays visible to the claimed-parent poll because that query includes
// in_progress.
func TestStartDefaultsMoveParentInProgress(t *testing.T) {
	client := newStatusClient(map[string]string{"demo-parent": "open"})
	sys := &system{cli: client}
	target := task.Target{Parent: task.TicketRef{ID: "demo-parent", Key: "demo-parent"}}

	if err := sys.ApplyTaskConfig(context.Background(), target, sys.StartDefaults()); err != nil {
		t.Fatal(err)
	}
	if len(client.updates) != 1 || client.updates[0].input.Status != "in_progress" {
		t.Fatalf("updates = %+v, want parent open → in_progress at start", client.updates)
	}
	if got := client.issues["demo-parent"].Status; got != "in_progress" {
		t.Fatalf("parent status = %q, want in_progress", got)
	}
}

// Jira parity: entering a node assigns that node's mailbox to the configured
// assignee in the same update that moves its status.
func TestApplyTaskConfigAssignsMailboxToConfiguredAssignee(t *testing.T) {
	client := newStatusClient(map[string]string{"demo-parent.1": "open"})
	sys := &system{cli: client}
	cfg := config.Merge(transitionConfig("", "in_progress"), config.RawValues{"assignee": "dev@example.com"})

	if err := sys.ApplyTaskConfig(context.Background(), statusTarget("demo-parent.1"), cfg); err != nil {
		t.Fatal(err)
	}
	if len(client.shown) != 1 || len(client.updates) != 1 {
		t.Fatalf("calls = show:%v updates:%+v, want one read and one write", client.shown, client.updates)
	}
	update := client.updates[0].input
	if update.Status != "in_progress" || update.Assignee != "dev@example.com" {
		t.Fatalf("update = %+v, want status and assignee in one call", update)
	}
}

func TestApplyTaskConfigAssigneeIsIdempotentAndStandalone(t *testing.T) {
	t.Run("already assigned at target status", func(t *testing.T) {
		client := newStatusClient(map[string]string{"demo-parent.1": "in_progress"})
		client.issues["demo-parent.1"] = bdcli.Issue{ID: "demo-parent.1", Status: "in_progress", Assignee: "dev@example.com"}
		sys := &system{cli: client}
		cfg := config.Merge(transitionConfig("", "in_progress"), config.RawValues{"assignee": "dev@example.com"})

		if err := sys.ApplyTaskConfig(context.Background(), statusTarget("demo-parent.1"), cfg); err != nil {
			t.Fatal(err)
		}
		if len(client.updates) != 0 {
			t.Fatalf("retried assignment issued an update: %+v", client.updates)
		}
	})

	t.Run("assignee without a transition", func(t *testing.T) {
		client := newStatusClient(map[string]string{"demo-parent.1": "in_progress"})
		sys := &system{cli: client}

		if err := sys.ApplyTaskConfig(context.Background(), statusTarget("demo-parent.1"),
			config.RawValues{"assignee": "dev@example.com"}); err != nil {
			t.Fatal(err)
		}
		if len(client.updates) != 1 || client.updates[0].input.Status != "" ||
			client.updates[0].input.Assignee != "dev@example.com" {
			t.Fatalf("updates = %+v, want an assignee-only update", client.updates)
		}
	})
}
