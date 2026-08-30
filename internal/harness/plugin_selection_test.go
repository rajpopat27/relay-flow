package harness_test

// This file intentionally lives in one package but exercises all three
// plugin registries (task, runner, harness); they share one behavior.

import (
	"context"
	"strings"
	"testing"

	"github.com/rajpopat27/relay-flow/internal/config"
	"github.com/rajpopat27/relay-flow/internal/harness"
	"github.com/rajpopat27/relay-flow/internal/runner"
	"github.com/rajpopat27/relay-flow/internal/task"
)

// 3.9: plugin selection per specs/integration-contracts "Plugin types are
// selected at machine scope": one plugin of each kind selected at root,
// unknown names error listing registered names, duplicate registration
// panics.

func TestHarnessRegistryUnknownNameListsRegistered(t *testing.T) {
	harness.Register("fakeharness-unknown", func(config.RawValues) (harness.Harness, error) {
		return newFakeHarness(), nil
	})
	_, err := harness.New("does-not-exist", config.RawValues{})
	if err == nil {
		t.Fatal("unknown harness name accepted")
	}
	if !strings.Contains(err.Error(), "fakeharness-unknown") {
		t.Fatalf("error %q does not list registered names", err)
	}
}

func TestRunnerRegistryUnknownNameListsRegistered(t *testing.T) {
	runner.Register("fakerunner-unknown", func(config.RawValues) (runner.Runner, error) {
		return nil, nil
	})
	_, err := runner.New("does-not-exist", config.RawValues{})
	if err == nil {
		t.Fatal("unknown runner name accepted")
	}
	if !strings.Contains(err.Error(), "fakerunner-unknown") {
		t.Fatalf("error %q does not list registered names", err)
	}
}

func TestTaskRegistryUnknownNameListsRegistered(t *testing.T) {
	task.Register("faketask-unknown", task.Factory{
		RequiredRepoKeys: func() []string { return nil },
		TaskScopeKey: func(_, _ config.RawValues) (string, error) {
			return "scope", nil
		},
		New: func(context.Context, task.RepoSpec) (task.System, error) {
			return nil, nil
		},
	})
	_, err := task.New(context.Background(), "does-not-exist", task.RepoSpec{})
	if err == nil {
		t.Fatal("unknown task name accepted")
	}
	if !strings.Contains(err.Error(), "faketask-unknown") {
		t.Fatalf("error %q does not list registered names", err)
	}
}

func TestDuplicateRegistrationPanics(t *testing.T) {
	assertPanic := func(name string, fn func()) {
		t.Helper()
		defer func() {
			if r := recover(); r == nil {
				t.Fatalf("%s duplicate registration did not panic", name)
			}
		}()
		fn()
	}

	harness.Register("dup-harness-x", func(config.RawValues) (harness.Harness, error) { return nil, nil })
	assertPanic("harness", func() {
		harness.Register("dup-harness-x", func(config.RawValues) (harness.Harness, error) { return nil, nil })
	})

	runner.Register("dup-runner-x", func(config.RawValues) (runner.Runner, error) { return nil, nil })
	assertPanic("runner", func() {
		runner.Register("dup-runner-x", func(config.RawValues) (runner.Runner, error) { return nil, nil })
	})

	factory := task.Factory{
		RequiredRepoKeys: func() []string { return nil },
		TaskScopeKey:     func(_, _ config.RawValues) (string, error) { return "s", nil },
		New:              func(context.Context, task.RepoSpec) (task.System, error) { return nil, nil },
	}
	task.Register("dup-task-x", factory)
	assertPanic("task", func() { task.Register("dup-task-x", factory) })
}

func TestNamesListsRegistered(t *testing.T) {
	// Self-contained: register fixtures within this test rather than relying
	// on earlier tests in the binary.
	harness.Register("names-harness-x", func(config.RawValues) (harness.Harness, error) { return nil, nil })
	runner.Register("names-runner-x", func(config.RawValues) (runner.Runner, error) { return nil, nil })
	task.Register("names-task-x", task.Factory{
		RequiredRepoKeys: func() []string { return nil },
		TaskScopeKey:     func(_, _ config.RawValues) (string, error) { return "s", nil },
		New:              func(context.Context, task.RepoSpec) (task.System, error) { return nil, nil },
	})

	if !contains(harness.Names(), "names-harness-x") {
		t.Fatalf("harness.Names() = %v", harness.Names())
	}
	if !contains(runner.Names(), "names-runner-x") {
		t.Fatalf("runner.Names() = %v", runner.Names())
	}
	if !contains(task.Names(), "names-task-x") {
		t.Fatalf("task.Names() = %v", task.Names())
	}
}

func contains(xs []string, want string) bool {
	for _, x := range xs {
		if x == want {
			return true
		}
	}
	return false
}
