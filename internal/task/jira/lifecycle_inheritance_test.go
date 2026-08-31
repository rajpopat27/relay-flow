package jira

import (
	"context"
	"testing"

	"github.com/rajpopat27/relay-flow/internal/config"
	"github.com/rajpopat27/relay-flow/internal/task"
)

// Inherited root/repository taskConfig must have the same effect at runtime
// that validation implies. ApplyTaskConfig receives only the configuration the
// caller assembles (lifecycle defaults merged under workflow/node values), so
// an adapter that keeps inherited values to itself accepts configuration,
// validates it against the live task system, and then silently discards it.
//
// The adapter therefore returns its inherited transitionTo/assignee values as
// part of its lifecycle defaults, producing the effective precedence
// built-in default < root < repo < workflow < node.
//
// internal/task/beads/lifecycle_inheritance_test.go is the sibling of this
// file and asserts the same invariants against the Beads adapter. Keep the two
// in sync: a change that breaks inheritance in one adapter must fail here too.

// repoScopedSystem builds the adapter with root/repository-scoped values, the
// scopes that never appear in an ApplyTaskConfig argument.
func repoScopedSystem(t *testing.T, fake *fakeJira, repoValues config.RawValues) task.System {
	t.Helper()
	repoConfig := config.Merge(config.RawValues{"project": "PAY", "component": "api"}, repoValues)
	sys, err := newSystem(context.Background(), &fakeClient{fake: fake}, task.RepoSpec{
		Name:       "payments",
		RepoConfig: repoConfig,
	})
	if err != nil {
		t.Fatalf("adapter construction failed: %v", err)
	}
	return sys
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
	parent := task.TicketRef{ID: "1", Key: "PAY-101"}
	mailbox := &task.Mailbox{ID: "2", Key: "PAY-102", Node: "implement"}

	t.Run("repo parentStatus reaches start", func(t *testing.T) {
		fake := &fakeJira{}
		sys := repoScopedSystem(t, fake, config.RawValues{
			"transitionTo": map[string]any{"parentStatus": "In Review"},
		})
		cfg := operationConfig(lifecycleDefaultsOf(t, sys).StartDefaults(), nil, nil)

		if err := sys.ApplyTaskConfig(context.Background(), task.Target{Parent: parent}, cfg); err != nil {
			t.Fatal(err)
		}
		if len(fake.parentTransitions) != 1 || fake.parentTransitions[0] != "In Review" {
			t.Fatalf("parent transitions = %v, want the inherited repo value to beat the built-in default",
				fake.parentTransitions)
		}
	})

	t.Run("repo taskStatus reaches a work node", func(t *testing.T) {
		fake := &fakeJira{}
		sys := repoScopedSystem(t, fake, config.RawValues{
			"transitionTo": map[string]any{"taskStatus": "In Review"},
		})
		cfg := operationConfig(lifecycleDefaultsOf(t, sys).WorkDefaults(), nil, nil)

		if err := sys.ApplyTaskConfig(context.Background(), task.Target{Parent: parent, Mailbox: mailbox}, cfg); err != nil {
			t.Fatal(err)
		}
		if len(fake.taskTransitions) != 1 || fake.taskTransitions[0] != "In Review" {
			t.Fatalf("mailbox transitions = %v, want the inherited repo value to beat the built-in default",
				fake.taskTransitions)
		}
	})
}

func TestLifecycleDefaultsCarryInheritedAssignee(t *testing.T) {
	fake := &fakeJira{}
	sys := repoScopedSystem(t, fake, config.RawValues{"assignee": "repo-bot@example.com"})
	parent := task.TicketRef{ID: "1", Key: "PAY-101"}
	mailbox := &task.Mailbox{ID: "2", Key: "PAY-102", Node: "implement"}
	cfg := operationConfig(lifecycleDefaultsOf(t, sys).WorkDefaults(), nil, nil)

	if err := sys.ApplyTaskConfig(context.Background(), task.Target{Parent: parent, Mailbox: mailbox}, cfg); err != nil {
		t.Fatal(err)
	}
	if len(fake.assignments) != 1 || fake.assignments[0] != "PAY-102:repo-bot@example.com" {
		t.Fatalf("assignments = %v, want the inherited repo assignee applied to the mailbox", fake.assignments)
	}
}

func TestLifecycleDefaultsPrecedenceDefaultRepoWorkflowNode(t *testing.T) {
	parent := task.TicketRef{ID: "1", Key: "PAY-101"}
	mailbox := &task.Mailbox{ID: "2", Key: "PAY-102", Node: "implement"}

	for _, tc := range []struct {
		name     string
		workflow config.RawValues
		node     config.RawValues
		want     string
	}{
		{name: "repo beats built-in default", want: "Repo Status"},
		{
			name:     "workflow beats repo",
			workflow: config.RawValues{"transitionTo": map[string]any{"taskStatus": "Workflow Status"}},
			want:     "Workflow Status",
		},
		{
			name:     "node beats workflow",
			workflow: config.RawValues{"transitionTo": map[string]any{"taskStatus": "Workflow Status"}},
			node:     config.RawValues{"transitionTo": map[string]any{"taskStatus": "Node Status"}},
			want:     "Node Status",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fake := &fakeJira{}
			sys := repoScopedSystem(t, fake, config.RawValues{
				"transitionTo": map[string]any{"taskStatus": "Repo Status"},
			})
			cfg := operationConfig(lifecycleDefaultsOf(t, sys).WorkDefaults(), tc.workflow, tc.node)

			if err := sys.ApplyTaskConfig(context.Background(), task.Target{Parent: parent, Mailbox: mailbox}, cfg); err != nil {
				t.Fatal(err)
			}
			if len(fake.taskTransitions) != 1 || fake.taskTransitions[0] != tc.want {
				t.Fatalf("mailbox transitions = %v, want [%s]", fake.taskTransitions, tc.want)
			}
		})
	}
}

// With nothing inherited, the built-in lifecycle values still apply.
func TestLifecycleDefaultsWithoutInheritedValues(t *testing.T) {
	fake := &fakeJira{}
	sys := repoScopedSystem(t, fake, nil)
	d := lifecycleDefaultsOf(t, sys)
	parent := task.TicketRef{ID: "1", Key: "PAY-101"}
	mailbox := &task.Mailbox{ID: "2", Key: "PAY-102", Node: "implement"}

	if err := sys.ApplyTaskConfig(context.Background(), task.Target{Parent: parent}, operationConfig(d.StartDefaults(), nil, nil)); err != nil {
		t.Fatal(err)
	}
	if err := sys.ApplyTaskConfig(context.Background(), task.Target{Parent: parent, Mailbox: mailbox}, operationConfig(d.WorkDefaults(), nil, nil)); err != nil {
		t.Fatal(err)
	}
	if err := sys.ApplyTaskConfig(context.Background(), task.Target{Parent: parent}, operationConfig(d.EndDefaults(), nil, nil)); err != nil {
		t.Fatal(err)
	}
	if len(fake.parentTransitions) != 2 || fake.parentTransitions[0] != defaultStartParentStatus || fake.parentTransitions[1] != defaultEndParentStatus {
		t.Fatalf("parent transitions = %v, want [%s %s]", fake.parentTransitions, defaultStartParentStatus, defaultEndParentStatus)
	}
	if len(fake.taskTransitions) != 1 || fake.taskTransitions[0] != defaultWorkTaskStatus {
		t.Fatalf("mailbox transitions = %v, want [%s]", fake.taskTransitions, defaultWorkTaskStatus)
	}
	if len(fake.assignments) != 0 {
		t.Fatalf("assignments = %v, want none when no assignee is configured", fake.assignments)
	}
}
