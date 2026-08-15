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
    issueTypes: [Task]
    closeOn: Done
    agents:
      dev:
        handles:
          - status: To Do
            outcomes:
              done: In Review
`

const minimalYAML = `workflows:
  taskDevelopment:
    jql: project = FOO
    issueTypes: Task
    closeOn: Done
    agents:
      dev:
        handles:
          - status: To Do
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
	if !c.Workflows["taskDevelopment"].IssueTypes.Has("Task") {
		t.Fatal("issueTypes [Task] not parsed")
	}
}

func TestParse_IssueTypesRequired(t *testing.T) {
	_, err := Parse("test", []byte(`
workflows:
  taskDevelopment:
    jql: project = FOO
    closeOn: Done
    agents:
      dev:
        handles:
          - status: To Do
            outcomes:
              done: In Review
`))
	if err == nil {
		t.Fatal("expected error when issueTypes missing")
	}
}

func TestParse_JQLMustNotContainIssueType(t *testing.T) {
	_, err := Parse("test", []byte(`
workflows:
  taskDevelopment:
    jql: project = FOO AND issuetype = Task
    issueTypes: [Task]
    closeOn: Done
    agents:
      dev:
        handles:
          - status: To Do
            outcomes:
              done: In Review
`))
	if err == nil {
		t.Fatal("expected error when jql already contains issuetype (belongs in issueTypes)")
	}
}

func TestParse_DefaultPollInterval(t *testing.T) {
	c, err := Parse("test", []byte(minimalYAML))
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
	if err := os.WriteFile(filepath.Join(serverDir, "workflow.yaml"), []byte("pollIntervalSeconds: 99\n"+minimalYAML), 0o644); err != nil {
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

const perStatusYAML = `
workflows:
  taskDevelopment:
    jql: project = FOO
    issueTypes: [Task]
    closeOn: Done
    agents:
      plan:
        handles:
          - status: To Do
            outcomes:
              done: In Progress
              blocked: To Do
          - status: In Review
            outcomes:
              done: Done
              blocked: To Do
      build:
        handles:
          - status: In Progress
            outcomes:
              done: Testing
              blocked: In Progress
          - status: Testing
            outcomes:
              done: In Review
              blocked: In Progress
`

func TestParse_PerStatusOutcomes(t *testing.T) {
	c, err := Parse("test", []byte(perStatusYAML))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	plan, ok := c.AgentConfigFor("taskDevelopment", "plan")
	if !ok {
		t.Fatal("agent plan missing")
	}
	if got := plan.OutcomesFor("To Do")["done"]; got != "In Progress" {
		t.Fatalf("plan To Do done=%q, want In Progress", got)
	}
	if got := plan.OutcomesFor("In Review")["done"]; got != "Done" {
		t.Fatalf("plan In Review done=%q, want Done", got)
	}
	// Case-insensitive status lookup.
	if got := plan.OutcomesFor("to do")["blocked"]; got != "To Do" {
		t.Fatalf("plan 'to do' blocked=%q, want To Do", got)
	}
	if names := plan.StatusNamesFor("In Review"); len(names) != 2 {
		t.Fatalf("StatusNamesFor(In Review)=%v, want 2 names", names)
	}
}

func TestParse_DuplicateStatusWithinAgentRejected(t *testing.T) {
	_, err := Parse("test", []byte(`
workflows:
  taskDevelopment:
    jql: project = FOO
    issueTypes: [Task]
    closeOn: Done
    agents:
      dev:
        handles:
          - status: To Do
            outcomes: {done: Done}
          - status: to do
            outcomes: {done: Done}
`))
	if err == nil {
		t.Fatal("expected error for duplicate status within one agent's handles")
	}
}

func TestParse_DuplicateStatusAcrossAgentsRejected(t *testing.T) {
	_, err := Parse("test", []byte(`
workflows:
  taskDevelopment:
    jql: project = FOO
    issueTypes: [Task]
    closeOn: Done
    agents:
      a:
        handles:
          - status: To Do
            outcomes: {done: Done}
      b:
        handles:
          - status: To Do
            outcomes: {done: Done}
`))
	if err == nil {
		t.Fatal("expected error for status handled by two agents")
	}
}

func TestParse_EmptyOutcomesRejected(t *testing.T) {
	_, err := Parse("test", []byte(`
workflows:
  taskDevelopment:
    jql: project = FOO
    issueTypes: [Task]
    closeOn: Done
    agents:
      dev:
        handles:
          - status: To Do
`))
	if err == nil {
		t.Fatal("expected error for handle entry without outcomes")
	}
}

func TestParse_OldShapeRejected(t *testing.T) {
	_, err := Parse("test", []byte(`
workflows:
  taskDevelopment:
    jql: project = FOO
    issueTypes: [Task]
    closeOn: Done
    agents:
      dev:
        handles: [To Do]
        outcomes:
          done: Done
`))
	if err == nil {
		t.Fatal("expected error for v2 shape (scalar handles / top-level outcomes)")
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
