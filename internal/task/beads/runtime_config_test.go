package beads

import (
	"context"
	"strings"
	"testing"

	"github.com/rajpopat27/relay-flow/internal/config"
)

func TestApplyTaskConfigUsesEffectiveInheritedTransition(t *testing.T) {
	client := newStatusClient(map[string]string{"demo-parent.1": "open"})
	root := config.RawValues{"transitionTo": map[string]any{"taskStatus": "closed"}}
	repo := config.RawValues{"transitionTo": map[string]any{"taskStatus": "in_progress"}}
	sys := &system{cli: client, base: config.Merge(DefaultConfig(), root, repo)}

	if err := sys.ApplyTaskConfig(context.Background(), statusTarget("demo-parent.1"), nil); err != nil {
		t.Fatal(err)
	}
	if len(client.updates) != 1 || client.updates[0].input.Status != "in_progress" {
		t.Fatalf("updates = %+v, want inherited repo transition taskStatus in_progress", client.updates)
	}
	if client.issues["demo-parent.1"].Status != "in_progress" {
		t.Fatalf("mailbox status = %q, want in_progress", client.issues["demo-parent.1"].Status)
	}
}

func TestApplyTaskConfigWorkflowAndNodeTransitionsOverrideInheritedValues(t *testing.T) {
	t.Run("workflow overrides repo", func(t *testing.T) {
		client := newStatusClient(map[string]string{"demo-parent.1": "in_progress"})
		sys := &system{cli: client, base: config.Merge(DefaultConfig(), config.RawValues{
			"transitionTo": map[string]any{"taskStatus": "in_progress"},
		})}
		workflow := config.RawValues{"transitionTo": map[string]any{"taskStatus": "closed"}}

		if err := sys.ApplyTaskConfig(context.Background(), statusTarget("demo-parent.1"), workflow); err != nil {
			t.Fatal(err)
		}
		if len(client.updates) != 1 || client.updates[0].input.Status != "closed" {
			t.Fatalf("updates = %+v, want workflow taskStatus closed", client.updates)
		}
	})

	t.Run("node overrides workflow", func(t *testing.T) {
		client := newStatusClient(map[string]string{"demo-parent.1": "open"})
		sys := &system{cli: client, base: config.Merge(DefaultConfig(), config.RawValues{
			"transitionTo": map[string]any{"taskStatus": "closed"},
		})}
		workflow := config.RawValues{"transitionTo": map[string]any{"taskStatus": "closed"}}
		node := config.RawValues{"transitionTo": map[string]any{"taskStatus": "in_progress"}}
		operation := config.Merge(workflow, node)

		if err := sys.ApplyTaskConfig(context.Background(), statusTarget("demo-parent.1"), operation); err != nil {
			t.Fatal(err)
		}
		if len(client.updates) != 1 || client.updates[0].input.Status != "in_progress" {
			t.Fatalf("updates = %+v, want node taskStatus in_progress", client.updates)
		}
	})
}

func TestApplyTaskConfigExplicitNodeTransitionOverridesLifecycleDefault(t *testing.T) {
	client := newStatusClient(map[string]string{"demo-parent.1": "in_progress"})
	sys := &system{cli: client}

	defaults := sys.WorkDefaults()
	node := config.RawValues{"transitionTo": map[string]any{"taskStatus": "closed"}}
	operation := config.Merge(defaults, node)
	if err := sys.ApplyTaskConfig(context.Background(), statusTarget("demo-parent.1"), operation); err != nil {
		t.Fatal(err)
	}
	if len(client.updates) != 1 || client.updates[0].input.Status != "closed" {
		t.Fatalf("updates = %+v, want explicit node taskStatus closed", client.updates)
	}
}

// RenderText has a fixed task.System signature and receives no workflow/node
// config. Beads therefore chooses the explicit limitation: lower-scope
// template overrides are rejected rather than accepted without runtime effect.
func TestBeadsLowerScopeTemplateOverridesAreRejected(t *testing.T) {
	sys := &system{base: config.Merge(DefaultConfig())}
	workflow := config.RawValues{"templates": map[string]any{
		"summaryComment": "workflow {{summaryReport}}",
	}}
	node := config.RawValues{"templates": map[string]any{
		"feedbackComment": "node {{feedbackReport}}",
	}}

	err := sys.ValidateConfig(context.Background(), workflow, map[string]config.RawValues{"implement": node})
	lower := strings.ToLower(errString(err))
	if err == nil || !strings.Contains(lower, "template") || !strings.Contains(lower, "scope") {
		t.Fatalf("lower-scope template override validation error = %v, want explicit unsupported-scope error", err)
	}
}

func errString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}
