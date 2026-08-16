package config

import (
	"strings"
	"testing"
)

const validYAML = `
name: xyzTaskFlow
pollIntervalSeconds: 15
tasks:
  type: jira
  config:
    query: project = xyz
    issueTypes: [Task]
runner:
  type: orca
closeOn: [done]
nodes:
  coding:
    agent: build
    when: "In Progress"
    onSuccess: reviewing
    onFailure: coding
  reviewing:
    agent: build
    when: "In Review"
    onSuccess: done
    onFailure: coding
  done:
    when: "Done"
`

func TestParseValid(t *testing.T) {
	cfg, err := Parse("test", []byte(validYAML))
	if err != nil {
		t.Fatalf("valid config rejected: %v", err)
	}
	if cfg.Name != "xyzTaskFlow" {
		t.Errorf("name = %q", cfg.Name)
	}
	if cfg.PollIntervalSeconds != 15 {
		t.Errorf("pollIntervalSeconds = %d", cfg.PollIntervalSeconds)
	}
	if cfg.Tasks.Type != "jira" || cfg.Runner.Type != "orca" {
		t.Errorf("tasks=%q runner=%q", cfg.Tasks.Type, cfg.Runner.Type)
	}
	if cfg.Tasks.Config["query"] != "project = xyz" {
		t.Errorf("opaque tasks config not preserved: %v", cfg.Tasks.Config)
	}
	if len(cfg.Nodes) != 3 {
		t.Fatalf("nodes = %v", cfg.Nodes)
	}
	n := cfg.Nodes["coding"]
	if n.Agent != "build" || n.When != "In Progress" || n.OnSuccess != "reviewing" || n.OnFailure != "coding" {
		t.Errorf("coding node = %+v", n)
	}
	if cfg.Nodes["done"].Agent != "" {
		t.Errorf("done should have no agent")
	}
	if len(cfg.CloseOn) != 1 || cfg.CloseOn[0] != "done" {
		t.Errorf("closeOn = %v", cfg.CloseOn)
	}
}

func TestParseDefaultPollInterval(t *testing.T) {
	yaml := strings.Replace(validYAML, "pollIntervalSeconds: 15\n", "", 1)
	cfg, err := Parse("test", []byte(yaml))
	if err != nil {
		t.Fatalf("%v", err)
	}
	if cfg.PollIntervalSeconds != 15 {
		t.Errorf("default pollIntervalSeconds = %d, want 15", cfg.PollIntervalSeconds)
	}
}

func TestCloseOnScalar(t *testing.T) {
	yaml := strings.Replace(validYAML, "closeOn: [done]", "closeOn: done", 1)
	cfg, err := Parse("test", []byte(yaml))
	if err != nil {
		t.Fatalf("%v", err)
	}
	if len(cfg.CloseOn) != 1 || cfg.CloseOn[0] != "done" {
		t.Errorf("closeOn = %v", cfg.CloseOn)
	}
}

func TestParseErrors(t *testing.T) {
	rep := func(old, new string) string { return strings.Replace(validYAML, old, new, 1) }
	cases := []struct {
		name string
		yaml string
		want string
	}{
		{"empty name", rep("name: xyzTaskFlow", "name: \"\""), "name"},
		{"bad name", rep("name: xyzTaskFlow", "name: My Flow"), "camelCase"},
		{"unknown top field", validYAML + "bogus: 1\n", "field bogus"},
		{"unknown node field", rep("onFailure: coding\n  reviewing:", "onFailure: coding\n    bogus: 1\n  reviewing:"), "field bogus"},
		{"no tasks type", rep("type: jira", "type: \"\""), "tasks.type"},
		{"no runner type", rep("type: orca", "type: \"\""), "runner.type"},
		{"empty closeOn", rep("closeOn: [done]", "closeOn: []"), "closeOn"},
		{"closeOn unknown node", rep("closeOn: [done]", "closeOn: [nope]"), "closeOn"},
		{"closeOn node has agent", rep("closeOn: [done]", "closeOn: [coding]"), "closeOn"},
		{"agentless node not in closeOn", rep("closeOn: [done]", "closeOn: [reviewing]"), "closeOn"},
		{"edge target missing", rep("onSuccess: reviewing", "onSuccess: nowhere"), "onSuccess"},
		{"agent node missing onSuccess", rep("    onSuccess: reviewing\n", ""), "onSuccess"},
		{"agent node missing onFailure", rep("    onFailure: coding\n  reviewing", "  reviewing"), "onFailure"},
		{"dup when", rep("when: \"In Review\"", "when: \"In Progress\""), "duplicat"},
		{"empty when", rep("when: \"In Progress\"", "when: \"\""), "when"},
		{"no nodes", rep("nodes:\n", "nodes: {}\n#") + "", "nodes"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			yaml := tc.yaml
			if tc.name == "no nodes" {
				// strip the entire nodes block
				yaml = validYAML[:strings.Index(validYAML, "nodes:")]
			}
			_, err := Parse("test", []byte(yaml))
			if err == nil {
				t.Fatalf("expected error containing %q, got nil", tc.want)
			}
			if !strings.Contains(strings.ToLower(err.Error()), strings.ToLower(tc.want)) {
				t.Fatalf("error %q missing %q", err, tc.want)
			}
		})
	}
}

func TestSelfLoopEdgeAllowed(t *testing.T) {
	// coding.onFailure == coding: already in validYAML, must parse.
	if _, err := Parse("test", []byte(validYAML)); err != nil {
		t.Fatalf("%v", err)
	}
}

func TestNodeForState(t *testing.T) {
	cfg, err := Parse("test", []byte(validYAML))
	if err != nil {
		t.Fatalf("%v", err)
	}
	if got := cfg.NodeForState("in progress"); got != "coding" {
		t.Errorf("NodeForState(in progress) = %q, want coding", got)
	}
	if got := cfg.NodeForState("Done"); got != "done" {
		t.Errorf("NodeForState(Done) = %q, want done", got)
	}
	if got := cfg.NodeForState("Backlog"); got != "" {
		t.Errorf("NodeForState(Backlog) = %q, want empty", got)
	}
}

func TestDefaultNudgePrompt(t *testing.T) {
	cfg, err := Parse("test", []byte(validYAML))
	if err != nil {
		t.Fatalf("%v", err)
	}
	p := cfg.Nodes["coding"].NudgePrompt
	if !strings.Contains(p, "{{ticket}}") || !strings.Contains(p, "{{node}}") {
		t.Errorf("default nudgePrompt missing templates: %q", p)
	}
}
