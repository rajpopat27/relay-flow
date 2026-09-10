package task_test

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/rajpopat27/relay-flow/internal/config"
	"github.com/rajpopat27/relay-flow/internal/task"
)

const alternateAuthPlugin = "alternate-auth-format-test"

var registerAlternateAuth sync.Once

func TestAlternativeTaskPluginOwnsCredentialsFormat(t *testing.T) {
	registerAlternateAuth.Do(func() {
		task.Register(alternateAuthPlugin, task.Factory{
			RequiredRepoKeys: func() []string { return nil },
			TaskScopeKey:     func(_, _ config.RawValues) (string, error) { return "alternate", nil },
			Auth: func(_ context.Context, _ []string, stdin io.Reader) error {
				key, err := io.ReadAll(stdin)
				if err != nil {
					return err
				}
				return os.WriteFile(filepath.Join(os.Getenv("RELAY_FLOW_HOME"), "credentials.yaml"),
					[]byte("alternateApiKey: "+strings.TrimSpace(string(key))+"\n"), 0o600)
			},
			New: func(context.Context, task.RepoSpec) (task.System, error) { return nil, nil },
		})
	})
	root := t.TempDir()
	t.Setenv("RELAY_FLOW_HOME", root)
	if err := task.Auth(context.Background(), alternateAuthPlugin, nil, strings.NewReader("different-secret")); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(root, "credentials.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if string(raw) != "alternateApiKey: different-secret\n" {
		t.Fatalf("alternate credentials = %q", raw)
	}
}
