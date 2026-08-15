package config

import (
	"strings"
	"testing"
)

const validYAML = `
name: fooTask
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

const minimalYAML = `name: fooTask
workflows:
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
name: testCfg
assigneeIsAgent: true
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
name: testCfg
assigneeIsAgent: true
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

func TestParse_NameRequired(t *testing.T) {
	yaml := strings.Replace(validYAML, "name: fooTask\n", "", 1)
	if _, err := Parse("test", []byte(yaml)); err == nil {
		t.Fatal("expected error when name missing")
	}
}

func TestParse_NameMustBeCamelCase(t *testing.T) {
	yaml := strings.Replace(validYAML, "name: fooTask", "name: Foo Task", 1)
	if _, err := Parse("test", []byte(yaml)); err == nil {
		t.Fatal("expected error for name with space/uppercase start")
	}
}

func TestParse_NameParsed(t *testing.T) {
	c, err := Parse("test", []byte(validYAML))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if c.Name != "fooTask" {
		t.Fatalf("Name=%q, want fooTask", c.Name)
	}
}

func TestParse_AssigneeIsAgentParsed(t *testing.T) {
	yaml := strings.Replace(validYAML, "name: fooTask", "name: fooTask\nassigneeIsAgent: true", 1)
	c, err := Parse("test", []byte(yaml))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if !c.AssigneeIsAgent {
		t.Fatal("AssigneeIsAgent should be true")
	}
}

func TestParse_AssigneeFieldRejected(t *testing.T) {
	// assignee moved to the machine config — it is personal, not shared.
	yaml := strings.Replace(validYAML, "name: fooTask", "name: fooTask\nassignee: \"Raj Popat\"", 1)
	if _, err := Parse("test", []byte(yaml)); err == nil {
		t.Fatal("expected error: assignee no longer belongs in workflow yaml")
	}
}

func TestParse_JQLMustNotContainAssignee(t *testing.T) {
	yaml := strings.Replace(validYAML, "jql: project = FOO", `jql: project = FOO AND assignee = "X"`, 1)
	if _, err := Parse("test", []byte(yaml)); err == nil {
		t.Fatal("expected error when jql contains assignee (belongs in machine config)")
	}
}

func TestMachineConfig_RoundTrip(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)
	mc := &MachineConfig{Assignee: "Raj Popat"}
	if err := mc.Save(); err != nil {
		t.Fatalf("Save: %v", err)
	}
	got, err := LoadMachineConfig()
	if err != nil {
		t.Fatalf("LoadMachineConfig: %v", err)
	}
	if got.Assignee != "Raj Popat" {
		t.Fatalf("Assignee=%q, want Raj Popat", got.Assignee)
	}
}

func TestMachineConfig_Missing(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)
	if _, err := LoadMachineConfig(); err == nil {
		t.Fatal("expected error when machine config absent")
	}
}

func TestMachineConfig_EmptyAssigneeRejected(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)
	if err := (&MachineConfig{}).Save(); err == nil {
		t.Fatal("expected error saving empty assignee")
	}
}

const perStatusYAML = `
name: testCfg
assigneeIsAgent: true
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
name: testCfg
assigneeIsAgent: true
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
name: testCfg
assigneeIsAgent: true
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
name: testCfg
assigneeIsAgent: true
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
name: testCfg
assigneeIsAgent: true
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
