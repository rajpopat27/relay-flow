package task_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/rajpopat27/relay-flow/internal/config"
	"github.com/rajpopat27/relay-flow/internal/task"
)

func TestStartupDispatchCallsOnlySelectedPluginWithoutConstructingSystems(t *testing.T) {
	selected := t.Name() + t.TempDir()
	other := selected + "-other"
	calls := 0
	failure := errors.New("connection unavailable")
	task.Register(other, task.Factory{ProbeStartup: func(context.Context, config.RawValues, map[string]config.Repo) error {
		t.Fatal("called unselected plugin")
		return nil
	}})
	task.Register(selected, task.Factory{
		DefaultConfig: func() config.RawValues { return config.RawValues{"default": "value"} },
		New: func(context.Context, task.RepoSpec) (task.System, error) {
			t.Fatal("startup constructed a repo system")
			return nil, nil
		},
		ProbeStartup: func(ctx context.Context, root config.RawValues, repos map[string]config.Repo) error {
			calls++
			if root["default"] != "value" || root["explicit"] != "override" || repos["app"].Path != "/work/app" {
				t.Fatalf("startup inputs = %v, %v", root, repos)
			}
			return failure
		},
	})
	err := task.ProbeStartup(context.Background(), selected, config.RawValues{"explicit": "override"}, map[string]config.Repo{"app": {Path: "/work/app"}})
	if !errors.Is(err, failure) || calls != 1 {
		t.Fatalf("dispatch = %v, calls=%d", err, calls)
	}
}

func TestStartupDispatchRequiresProbeSupportEvenWithoutRepos(t *testing.T) {
	name := t.Name() + t.TempDir()
	task.Register(name, task.Factory{})
	if err := task.ProbeStartup(context.Background(), name, nil, nil); err == nil || !strings.Contains(err.Error(), "startup probe") {
		t.Fatalf("missing startup callback = %v", err)
	}
	if err := task.ProbeStartup(context.Background(), "unknown-startup-plugin", nil, nil); err == nil || !strings.Contains(err.Error(), "registered:") {
		t.Fatalf("unknown plugin = %v", err)
	}
}
