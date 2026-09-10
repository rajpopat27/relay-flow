package beads

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/rajpopat27/relay-flow/internal/config"
	"github.com/rajpopat27/relay-flow/internal/task"
)

// TestAgentEnvSelectsRegisteredWorkspace asserts that a repo-bound Beads
// system exposes the configured workspace to agent processes and neutralizes
// ambient Beads selectors, mirroring the adapter's own child-command
// isolation.
func TestAgentEnvSelectsRegisteredWorkspace(t *testing.T) {
	installRepoCompositionFakeBD(t)
	root := t.TempDir()
	codePath := filepath.Join(root, "code", "payments")
	beadsDir := filepath.Join(root, "beads", "payments", ".beads")
	for _, path := range []string{codePath, beadsDir} {
		if err := os.MkdirAll(path, 0o755); err != nil {
			t.Fatal(err)
		}
	}

	sys, err := newSystem(context.Background(), task.RepoSpec{
		Name: "payments", Path: codePath,
		RepoConfig: config.RawValues{"beadsDir": beadsDir},
	})
	if err != nil {
		t.Fatal(err)
	}
	provider, ok := sys.(task.AgentEnvironment)
	if !ok {
		t.Fatal("Beads system does not expose task.AgentEnvironment")
	}
	canonical, err := canonicalBeadsDir(beadsDir)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{
		"BEADS_DIR": canonical,
		"BEADS_DB":  "",
		"BD_DB":     "",
	}
	if got := provider.AgentEnv(); !reflect.DeepEqual(got, want) {
		t.Fatalf("AgentEnv() = %#v, want %#v", got, want)
	}
}
