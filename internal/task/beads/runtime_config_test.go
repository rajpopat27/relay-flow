package beads

import (
	"context"
	"strings"
	"testing"

	"github.com/rajpopat27/relay-flow/internal/config"
)

// Beads-specific runtime configuration behavior. The shared inheritance and
// precedence invariants live in lifecycle_inheritance_test.go, which is
// mirrored by the Jira adapter.

func TestNodeAssigneeOverridesInheritedRepoAssignee(t *testing.T) {
	client := newStatusClient(map[string]string{"demo-parent.1": "open"})
	sys := repoScopedSystem(client, config.RawValues{"assignee": "repo-bot@example.com"})
	node := config.RawValues{"assignee": "dev@example.com"}

	cfg := operationConfig(sys.WorkDefaults(), nil, node)
	if err := sys.ApplyTaskConfig(context.Background(), statusTarget("demo-parent.1"), cfg); err != nil {
		t.Fatal(err)
	}
	if len(client.updates) != 1 || client.updates[0].input.Assignee != "dev@example.com" {
		t.Fatalf("updates = %+v, want the node assignee to win", client.updates)
	}
}

// transitionTo is a lifecycle-point setting: a value configured above node
// scope applies to every lifecycle point that reads it, including end. This
// test pins that uniform precedence so the behavior is deliberate rather than
// discovered, and is why the documentation configures transitionTo on nodes.
func TestInheritedParentStatusAppliesToEveryLifecyclePoint(t *testing.T) {
	client := newStatusClient(map[string]string{"demo-parent": "in_progress"})
	sys := repoScopedSystem(client, config.RawValues{
		"transitionTo": map[string]any{"parentStatus": "in_progress"},
	})

	cfg := operationConfig(sys.EndDefaults(), nil, nil)
	if err := sys.ApplyTaskConfig(context.Background(), endTarget(), cfg); err != nil {
		t.Fatal(err)
	}
	if len(client.updates) != 0 {
		t.Fatalf("updates = %+v, want the inherited parentStatus to win over the end default", client.updates)
	}

	// The end node's own value restores closing behavior.
	node := config.RawValues{"transitionTo": map[string]any{"parentStatus": "closed"}}
	if err := sys.ApplyTaskConfig(context.Background(), endTarget(), operationConfig(sys.EndDefaults(), nil, node)); err != nil {
		t.Fatal(err)
	}
	if len(client.updates) != 1 || client.updates[0].input.Status != "closed" {
		t.Fatalf("updates = %+v, want end node parentStatus closed", client.updates)
	}
}

// RenderText has a fixed task.System signature and receives no workflow/node
// config. Beads therefore chooses the explicit limitation: lower-scope
// template overrides are rejected rather than accepted without runtime effect.
func TestBeadsLowerScopeTemplateOverridesAreRejected(t *testing.T) {
	sys := repoScopedSystem(nil, nil)
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
