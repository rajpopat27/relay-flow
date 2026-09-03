package beads

import (
	"context"
	"testing"

	"github.com/rajpopat27/relay-flow/internal/config"
	"github.com/rajpopat27/relay-flow/internal/task"
	"github.com/rajpopat27/relay-flow/internal/task/beads/bdcli"
)

// Inherited root/repository taskConfig must have the same effect at runtime
// that validation implies. ApplyTaskConfig receives only the configuration the
// caller assembles (lifecycle defaults merged under workflow/node values), so
// an adapter that keeps inherited values to itself accepts configuration,
// validates it, and then silently discards it.
//
// The adapter therefore returns its inherited transitionTo/assignee values as
// part of its lifecycle defaults, producing the effective precedence
// built-in default < root < repo < workflow < node.
//
// internal/task/jira/lifecycle_inheritance_test.go is the sibling of this file
// and asserts the same invariants against the Jira adapter. Keep the two in
// sync: a change that breaks inheritance in one adapter must fail here too.

// repoScopedSystem builds the adapter with root/repository-scoped values, the
// scopes that never appear in an ApplyTaskConfig argument.
func repoScopedSystem(client bdcli.Client, repoValues config.RawValues) *system {
	return &system{cli: client, base: config.Merge(DefaultConfig(), repoValues)}
}

// operationConfig reproduces what the caller assembles before ApplyTaskConfig.
func operationConfig(defaults, workflow, node config.RawValues) config.RawValues {
	return config.Merge(defaults, config.Merge(workflow, node))
}

func lifecycleDefaultsOf(t *testing.T, sys task.System) task.LifecycleDefaults {
	t.Helper()
	d, ok := sys.(task.LifecycleDefaults)
	if !ok {
		t.Fatal("adapter does not expose task.LifecycleDefaults; inherited values cannot reach runtime")
	}
	return d
}

func TestLifecycleDefaultsCarryInheritedTransitionTo(t *testing.T) {
	t.Run("repo parentStatus reaches start", func(t *testing.T) {
		client := newStatusClient(map[string]string{"demo-parent": "open"})
		sys := repoScopedSystem(client, config.RawValues{
			"transitionTo": map[string]any{"parentStatus": "blocked"},
		})
		cfg := operationConfig(lifecycleDefaultsOf(t, sys).StartDefaults(), nil, nil)

		if err := sys.ApplyTaskConfig(context.Background(), endTarget(), cfg); err != nil {
			t.Fatal(err)
		}
		if len(client.updates) != 1 || client.updates[0].input.Status != "blocked" {
			t.Fatalf("updates = %+v, want the inherited repo value to beat the built-in default", client.updates)
		}
	})

	t.Run("repo taskStatus reaches a work node", func(t *testing.T) {
		client := newStatusClient(map[string]string{"demo-parent.1": "open"})
		sys := repoScopedSystem(client, config.RawValues{
			"transitionTo": map[string]any{"taskStatus": "blocked"},
		})
		cfg := operationConfig(lifecycleDefaultsOf(t, sys).WorkDefaults(), nil, nil)

		if err := sys.ApplyTaskConfig(context.Background(), statusTarget("demo-parent.1"), cfg); err != nil {
			t.Fatal(err)
		}
		if len(client.updates) != 1 || client.updates[0].input.Status != "blocked" {
			t.Fatalf("updates = %+v, want the inherited repo value to beat the built-in default", client.updates)
		}
	})
}

func TestLifecycleDefaultsCarryInheritedAssignee(t *testing.T) {
	client := newStatusClient(map[string]string{"demo-parent.1": "open"})
	sys := repoScopedSystem(client, config.RawValues{"assignee": "repo-bot@example.com"})
	cfg := operationConfig(lifecycleDefaultsOf(t, sys).WorkDefaults(), nil, nil)

	if err := sys.ApplyTaskConfig(context.Background(), statusTarget("demo-parent.1"), cfg); err != nil {
		t.Fatal(err)
	}
	if len(client.updates) != 1 {
		t.Fatalf("updates = %+v, want one combined update", client.updates)
	}
	update := client.updates[0].input
	if update.Status != "in_progress" || update.Assignee != "repo-bot@example.com" {
		t.Fatalf("update = %+v, want the built-in status plus the inherited repo assignee", update)
	}
}

func TestLifecycleDefaultsPrecedenceDefaultRepoWorkflowNode(t *testing.T) {
	for _, tc := range []struct {
		name     string
		workflow config.RawValues
		node     config.RawValues
		want     string
	}{
		{name: "repo beats built-in default", want: "blocked"},
		{
			name:     "workflow beats repo",
			workflow: config.RawValues{"transitionTo": map[string]any{"taskStatus": "hooked"}},
			want:     "hooked",
		},
		{
			name:     "node beats workflow",
			workflow: config.RawValues{"transitionTo": map[string]any{"taskStatus": "hooked"}},
			node:     config.RawValues{"transitionTo": map[string]any{"taskStatus": "deferred"}},
			want:     "deferred",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			client := newStatusClient(map[string]string{"demo-parent.1": "open"})
			sys := repoScopedSystem(client, config.RawValues{
				"transitionTo": map[string]any{"taskStatus": "blocked"},
			})
			cfg := operationConfig(lifecycleDefaultsOf(t, sys).WorkDefaults(), tc.workflow, tc.node)

			if err := sys.ApplyTaskConfig(context.Background(), statusTarget("demo-parent.1"), cfg); err != nil {
				t.Fatal(err)
			}
			if len(client.updates) != 1 || client.updates[0].input.Status != tc.want {
				t.Fatalf("updates = %+v, want [%s]", client.updates, tc.want)
			}
		})
	}
}

// With nothing inherited, the built-in lifecycle values still apply: the
// parent opens the run in_progress, a work mailbox moves to in_progress, and
// the parent closes at end.
func TestLifecycleDefaultsWithoutInheritedValues(t *testing.T) {
	client := newStatusClient(map[string]string{"demo-parent": "open", "demo-parent.1": "open"})
	sys := repoScopedSystem(client, nil)
	d := lifecycleDefaultsOf(t, sys)
	ctx := context.Background()

	if err := sys.ApplyTaskConfig(ctx, endTarget(), operationConfig(d.StartDefaults(), nil, nil)); err != nil {
		t.Fatal(err)
	}
	if err := sys.ApplyTaskConfig(ctx, statusTarget("demo-parent.1"), operationConfig(d.WorkDefaults(), nil, nil)); err != nil {
		t.Fatal(err)
	}
	if err := sys.ApplyTaskConfig(ctx, endTarget(), operationConfig(d.EndDefaults(), nil, nil)); err != nil {
		t.Fatal(err)
	}

	want := []struct{ issueID, status string }{
		{"demo-parent", "in_progress"},
		{"demo-parent.1", "in_progress"},
		{"demo-parent", "closed"},
	}
	if len(client.updates) != len(want) {
		t.Fatalf("updates = %+v, want %d lifecycle updates", client.updates, len(want))
	}
	for i, expected := range want {
		got := client.updates[i]
		if got.issueID != expected.issueID || got.input.Status != expected.status {
			t.Fatalf("update %d = %+v, want %s -> %s", i, got, expected.issueID, expected.status)
		}
		if got.input.Assignee != "" {
			t.Fatalf("update %d assigned %q with no assignee configured", i, got.input.Assignee)
		}
	}
}
