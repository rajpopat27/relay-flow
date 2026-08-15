package config

import (
	"os"
	"path/filepath"
	"testing"
)

const validYAML = `
pollIntervalSeconds: 30
workflows:
  taskDevelopment:
    jql: project = FOO
    closeOn: Done
    agents:
      dev:
        handles: [To Do]
        outcomes:
          done: In Review
`

func TestParse_Valid(t *testing.T) {
	c, err := Parse("test", []byte(validYAML))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if c.PollIntervalSeconds != 30 {
		t.Fatalf("pollIntervalSeconds=%d, want 30", c.PollIntervalSeconds)
	}
	if _, ok := c.Workflows["taskDevelopment"]; !ok {
		t.Fatal("workflow taskDevelopment missing")
	}
}

func TestParse_DefaultPollInterval(t *testing.T) {
	c, err := Parse("test", []byte("workflows:\n  taskDevelopment:\n    jql: project = FOO\n    closeOn: Done\n    agents:\n      dev:\n        handles: [To Do]\n        outcomes:\n          done: In Review\n"))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if c.PollIntervalSeconds != 15 {
		t.Fatalf("pollIntervalSeconds=%d, want default 15", c.PollIntervalSeconds)
	}
}

func TestParse_InvalidYAML(t *testing.T) {
	if _, err := Parse("test", []byte("{{not yaml")); err == nil {
		t.Fatal("expected error for invalid YAML")
	}
}

func TestParse_ValidationError(t *testing.T) {
	if _, err := Parse("test", []byte("pollIntervalSeconds: 5\n")); err == nil {
		t.Fatal("expected validation error for empty workflows")
	}
}

func TestParse_UnknownFieldRejected(t *testing.T) {
	if _, err := Parse("test", []byte(validYAML+"bogusKey: 1\n")); err == nil {
		t.Fatal("expected error for unknown field")
	}
}

func TestSavedPath(t *testing.T) {
	p, err := SavedPath("workflow")
	if err != nil {
		t.Fatalf("SavedPath: %v", err)
	}
	home, _ := os.UserHomeDir()
	want := filepath.Join(home, ".orca-jira-loop", "configs", "workflow.yaml")
	if p != want {
		t.Fatalf("SavedPath=%q, want %q", p, want)
	}
}

func TestLoadWithFallback_CwdFirst(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)

	// Server copy exists but cwd copy must win.
	serverDir := filepath.Join(tmp, ".orca-jira-loop", "configs")
	if err := os.MkdirAll(serverDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(serverDir, "workflow.yaml"), []byte("pollIntervalSeconds: 99\nworkflows:\n  taskDevelopment:\n    jql: project = SRV\n    closeOn: Done\n    agents:\n      dev:\n        handles: [To Do]\n        outcomes:\n          done: In Review\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	repo := filepath.Join(tmp, "repo")
	if err := os.MkdirAll(filepath.Join(repo, ".workflow"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, ".workflow", "workflow.yaml"), []byte(validYAML), 0o644); err != nil {
		t.Fatal(err)
	}
	chdir(t, repo)

	c, err := LoadWithFallback("workflow")
	if err != nil {
		t.Fatalf("LoadWithFallback: %v", err)
	}
	if c.PollIntervalSeconds != 30 {
		t.Fatalf("cwd copy should win: pollIntervalSeconds=%d, want 30", c.PollIntervalSeconds)
	}
}

func TestLoadWithFallback_ServerCopy(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)

	serverDir := filepath.Join(tmp, ".orca-jira-loop", "configs")
	if err := os.MkdirAll(serverDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(serverDir, "workflow.yaml"), []byte(validYAML), 0o644); err != nil {
		t.Fatal(err)
	}

	// cwd has no .workflow dir.
	repo := filepath.Join(tmp, "repo")
	if err := os.MkdirAll(repo, 0o755); err != nil {
		t.Fatal(err)
	}
	chdir(t, repo)

	c, err := LoadWithFallback("workflow")
	if err != nil {
		t.Fatalf("LoadWithFallback: %v", err)
	}
	if c.PollIntervalSeconds != 30 {
		t.Fatalf("pollIntervalSeconds=%d, want 30", c.PollIntervalSeconds)
	}
}

func TestLoadWithFallback_Missing(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)
	chdir(t, tmp)
	if _, err := LoadWithFallback("nope"); err == nil {
		t.Fatal("expected error when neither copy exists")
	}
}

func chdir(t *testing.T, dir string) {
	t.Helper()
	old, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chdir(old) })
}
