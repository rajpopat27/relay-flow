package workflow_test

import (
	"strings"
	"testing"

	"github.com/rajpopat27/relay-flow/internal/workflow"
)

// 3.1: parse/validate behavior per specs/workflow-definition.

const minimalValid = `
name: basicFlow
repos: [payments]
nodes:
  start:
    onSuccess:
      - target: coding
  coding:
    type: agent
    agent: build
    description: Do the coding work.
    onSuccess:
      - target: end
    onFailure:
      - target: coding
  end: {}
`

func parse(t *testing.T, name, yaml string) *workflow.Workflow {
	t.Helper()
	wf, err := workflow.Parse(name, []byte(yaml))
	if err != nil {
		t.Fatalf("Parse(%q) failed: %v", name, err)
	}
	return wf
}

func TestParseMinimalValid(t *testing.T) {
	wf := parse(t, "basicFlow", minimalValid)
	if err := wf.Validate(); err != nil {
		t.Fatalf("Validate failed: %v", err)
	}
	if wf.Name != "basicFlow" {
		t.Fatalf("Name = %q, want basicFlow", wf.Name)
	}
}

func TestParseRejectsUnknownRootFields(t *testing.T) {
	// Legacy fields and plugin selectors are rejected by strict parsing.
	for _, tc := range []struct{ name, yaml string }{
		{"legacy closeOn", strings.Replace(minimalValid, "repos: [payments]", "repos: [payments]\ncloseOn: [end]", 1)},
		{"legacy tasks", strings.Replace(minimalValid, "repos: [payments]", "repos: [payments]\ntasks: {}", 1)},
		{"runner plugin selector", strings.Replace(minimalValid, "repos: [payments]", "repos: [payments]\nrunnerPlugin: orca", 1)},
		{"harness plugin selector", strings.Replace(minimalValid, "repos: [payments]", "repos: [payments]\nharnessPlugin: opencode", 1)},
		{"task plugin selector", strings.Replace(minimalValid, "repos: [payments]", "repos: [payments]\ntaskPlugin: jira", 1)},
		{"poll interval override", strings.Replace(minimalValid, "repos: [payments]", "repos: [payments]\npollIntervalSeconds: 5", 1)},
		{"arbitrary query field", strings.Replace(minimalValid, "repos: [payments]", "repos: [payments]\njql: project = PAY", 1)},
		{"legacy node-level when", strings.Replace(minimalValid, "    description: Do the coding work.", "    description: Do the coding work.\n    when: In Progress", 1)},
		{"unknown node-level field", strings.Replace(minimalValid, "    description: Do the coding work.", "    description: Do the coding work.\n    timeout: 30", 1)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := workflow.Parse("basicFlow", []byte(tc.yaml)); err == nil {
				t.Fatal("Parse succeeded, want strict-schema rejection")
			}
		})
	}
}

func TestValidateWorkflowName(t *testing.T) {
	for _, bad := range []string{"BasicFlow", "basic flow", "basic-flow", "basic_flow", "1flow", ""} {
		yaml := strings.Replace(minimalValid, "name: basicFlow", "name: "+bad, 1)
		wf, err := workflow.Parse(bad, []byte(yaml))
		if err == nil {
			err = wf.Validate()
		}
		if err == nil {
			t.Fatalf("name %q accepted, want rejection", bad)
		}
	}
	if err := parse(t, "basicFlow", minimalValid).Validate(); err != nil {
		t.Fatalf("basicFlow rejected: %v", err)
	}
}

func TestValidateReposRequiredAndUnique(t *testing.T) {
	t.Run("no repos", func(t *testing.T) {
		yaml := strings.Replace(minimalValid, "repos: [payments]", "repos: []", 1)
		wf, _ := workflow.Parse("basicFlow", []byte(yaml))
		if err := wf.Validate(); err == nil {
			t.Fatal("empty repos accepted")
		}
	})
	t.Run("duplicate repo", func(t *testing.T) {
		yaml := strings.Replace(minimalValid, "repos: [payments]", "repos: [payments, payments]", 1)
		wf, _ := workflow.Parse("basicFlow", []byte(yaml))
		if err := wf.Validate(); err == nil {
			t.Fatal("duplicate repo accepted")
		}
	})
}

func TestValidateStartNode(t *testing.T) {
	t.Run("missing start", func(t *testing.T) {
		yaml := strings.Replace(minimalValid, "  start:\n    onSuccess:\n      - target: coding\n", "", 1)
		wf, _ := workflow.Parse("basicFlow", []byte(yaml))
		if err := wf.Validate(); err == nil {
			t.Fatal("workflow without start accepted")
		}
	})
	t.Run("start with several targets", func(t *testing.T) {
		yaml := strings.Replace(minimalValid, "      - target: coding\n  coding:", "      - target: coding\n      - target: end\n  coding:", 1)
		wf, _ := workflow.Parse("basicFlow", []byte(yaml))
		if err := wf.Validate(); err == nil {
			t.Fatal("start with two success routes accepted")
		}
	})
	t.Run("start with agent", func(t *testing.T) {
		yaml := strings.Replace(minimalValid, "  start:\n    onSuccess:", "  start:\n    agent: build\n    onSuccess:", 1)
		wf, _ := workflow.Parse("basicFlow", []byte(yaml))
		if err := wf.Validate(); err == nil {
			t.Fatal("start with agent accepted")
		}
	})
	t.Run("start with type", func(t *testing.T) {
		yaml := strings.Replace(minimalValid, "  start:\n    onSuccess:", "  start:\n    type: agent\n    onSuccess:", 1)
		wf, _ := workflow.Parse("basicFlow", []byte(yaml))
		if err := wf.Validate(); err == nil {
			t.Fatal("start with type accepted")
		}
	})
	t.Run("start with description", func(t *testing.T) {
		yaml := strings.Replace(minimalValid, "  start:\n    onSuccess:", "  start:\n    description: nope\n    onSuccess:", 1)
		wf, _ := workflow.Parse("basicFlow", []byte(yaml))
		if err := wf.Validate(); err == nil {
			t.Fatal("start with description accepted")
		}
	})
	t.Run("start with nudge prompt", func(t *testing.T) {
		yaml := strings.Replace(minimalValid, "  start:\n    onSuccess:", "  start:\n    nudgePrompt: nope\n    onSuccess:", 1)
		wf, _ := workflow.Parse("basicFlow", []byte(yaml))
		if err := wf.Validate(); err == nil {
			t.Fatal("start with nudgePrompt accepted")
		}
	})
	t.Run("start with failure route", func(t *testing.T) {
		yaml := strings.Replace(minimalValid, "  start:\n    onSuccess:\n      - target: coding", "  start:\n    onSuccess:\n      - target: coding\n    onFailure:\n      - target: coding", 1)
		wf, _ := workflow.Parse("basicFlow", []byte(yaml))
		if err := wf.Validate(); err == nil {
			t.Fatal("start with onFailure accepted")
		}
	})
	t.Run("start with taskConfig ok", func(t *testing.T) {
		yaml := strings.Replace(minimalValid, "  start:\n    onSuccess:", "  start:\n    taskConfig:\n      transitionTo:\n        parentStatus: In Progress\n    onSuccess:", 1)
		wf := parse(t, "basicFlow", yaml)
		if err := wf.Validate(); err != nil {
			t.Fatalf("start with taskConfig rejected: %v", err)
		}
	})
}

func TestValidateEndNode(t *testing.T) {
	t.Run("missing end", func(t *testing.T) {
		yaml := strings.Replace(minimalValid, "  end: {}\n", "", 1)
		// coding needs a success target that exists; point it at itself.
		yaml = strings.Replace(yaml, "      - target: end", "      - target: coding", 1)
		wf, _ := workflow.Parse("basicFlow", []byte(yaml))
		if err := wf.Validate(); err == nil {
			t.Fatal("workflow without end accepted")
		}
	})
	t.Run("end with route", func(t *testing.T) {
		yaml := strings.Replace(minimalValid, "  end: {}", "  end:\n    onSuccess:\n      - target: coding", 1)
		wf, _ := workflow.Parse("basicFlow", []byte(yaml))
		if err := wf.Validate(); err == nil {
			t.Fatal("end with outgoing route accepted")
		}
	})
	t.Run("end with type", func(t *testing.T) {
		yaml := strings.Replace(minimalValid, "  end: {}", "  end:\n    type: agent", 1)
		wf, _ := workflow.Parse("basicFlow", []byte(yaml))
		if err := wf.Validate(); err == nil {
			t.Fatal("end with type accepted")
		}
	})
	t.Run("end with agent", func(t *testing.T) {
		yaml := strings.Replace(minimalValid, "  end: {}", "  end:\n    agent: build", 1)
		wf, _ := workflow.Parse("basicFlow", []byte(yaml))
		if err := wf.Validate(); err == nil {
			t.Fatal("end with agent accepted")
		}
	})
	t.Run("end with description", func(t *testing.T) {
		yaml := strings.Replace(minimalValid, "  end: {}", "  end:\n    description: nope", 1)
		wf, _ := workflow.Parse("basicFlow", []byte(yaml))
		if err := wf.Validate(); err == nil {
			t.Fatal("end with description accepted")
		}
	})
	t.Run("end with nudge prompt", func(t *testing.T) {
		yaml := strings.Replace(minimalValid, "  end: {}", "  end:\n    nudgePrompt: nope", 1)
		wf, _ := workflow.Parse("basicFlow", []byte(yaml))
		if err := wf.Validate(); err == nil {
			t.Fatal("end with nudgePrompt accepted")
		}
	})
	t.Run("end with failure route", func(t *testing.T) {
		yaml := strings.Replace(minimalValid, "  end: {}", "  end:\n    onFailure:\n      - target: coding", 1)
		wf, _ := workflow.Parse("basicFlow", []byte(yaml))
		if err := wf.Validate(); err == nil {
			t.Fatal("end with failure route accepted")
		}
	})
	t.Run("terminal is not a lifecycle node", func(t *testing.T) {
		yaml := strings.Replace(minimalValid, "  end: {}", "  terminal: {}", 1)
		yaml = strings.Replace(yaml, "      - target: end", "      - target: terminal", 1)
		wf, _ := workflow.Parse("basicFlow", []byte(yaml))
		if err := wf.Validate(); err == nil {
			t.Fatal("workflow using terminal as lifecycle exit accepted; reserved exit is end")
		}
	})
}

func TestValidateWorkNodes(t *testing.T) {
	replaceCoding := func(node string) string {
		return strings.Replace(minimalValid, "  coding:\n    type: agent\n    agent: build\n    description: Do the coding work.\n    onSuccess:\n      - target: end\n    onFailure:\n      - target: coding", node, 1)
	}
	t.Run("missing type", func(t *testing.T) {
		wf, _ := workflow.Parse("basicFlow", []byte(replaceCoding("  coding:\n    agent: build\n    description: work\n    onSuccess:\n      - target: end\n    onFailure:\n      - target: coding")))
		if err := wf.Validate(); err == nil {
			t.Fatal("work node without type accepted")
		}
	})
	t.Run("missing agent", func(t *testing.T) {
		wf, _ := workflow.Parse("basicFlow", []byte(replaceCoding("  coding:\n    type: agent\n    description: work\n    onSuccess:\n      - target: end\n    onFailure:\n      - target: coding")))
		if err := wf.Validate(); err == nil {
			t.Fatal("work node without agent accepted")
		}
	})
	t.Run("missing description", func(t *testing.T) {
		wf, _ := workflow.Parse("basicFlow", []byte(replaceCoding("  coding:\n    type: agent\n    agent: build\n    onSuccess:\n      - target: end\n    onFailure:\n      - target: coding")))
		if err := wf.Validate(); err == nil {
			t.Fatal("work node without description accepted")
		}
	})
	t.Run("missing failure route", func(t *testing.T) {
		wf, _ := workflow.Parse("basicFlow", []byte(replaceCoding("  coding:\n    type: agent\n    agent: build\n    description: work\n    onSuccess:\n      - target: end")))
		if err := wf.Validate(); err == nil {
			t.Fatal("work node without failure route accepted")
		}
	})
	t.Run("missing success route", func(t *testing.T) {
		wf, _ := workflow.Parse("basicFlow", []byte(replaceCoding("  coding:\n    type: agent\n    agent: build\n    description: work\n    onFailure:\n      - target: coding")))
		if err := wf.Validate(); err == nil {
			t.Fatal("work node without success route accepted")
		}
	})
	t.Run("valid hitl node", func(t *testing.T) {
		wf := parse(t, "basicFlow", replaceCoding("  coding:\n    type: hitl\n    agent: build\n    description: human-guided work\n    onSuccess:\n      - target: end\n    onFailure:\n      - target: coding"))
		if err := wf.Validate(); err != nil {
			t.Fatalf("valid hitl node rejected: %v", err)
		}
		if wf.Nodes["coding"].Type != workflow.NodeHITL {
			t.Fatalf("Type = %q, want hitl", wf.Nodes["coding"].Type)
		}
	})
}

func TestValidateRoutes(t *testing.T) {
	t.Run("dangling target", func(t *testing.T) {
		yaml := strings.Replace(minimalValid, "      - target: end", "      - target: nowhere", 1)
		wf, _ := workflow.Parse("basicFlow", []byte(yaml))
		if err := wf.Validate(); err == nil {
			t.Fatal("route to unknown node accepted")
		}
	})
	t.Run("route targets start", func(t *testing.T) {
		yaml := strings.Replace(minimalValid, "      - target: coding\n  end:", "      - target: start\n  end:", 1)
		wf, _ := workflow.Parse("basicFlow", []byte(yaml))
		if err := wf.Validate(); err == nil {
			t.Fatal("route targeting start accepted")
		}
	})
	t.Run("failure route targets start", func(t *testing.T) {
		yaml := strings.Replace(minimalValid, "    onFailure:\n      - target: coding", "    onFailure:\n      - target: start", 1)
		wf, _ := workflow.Parse("basicFlow", []byte(yaml))
		if err := wf.Validate(); err == nil {
			t.Fatal("failure route targeting start accepted")
		}
	})
	t.Run("when is allowed and not a condition", func(t *testing.T) {
		yaml := strings.Replace(minimalValid, "      - target: end", "      - target: end\n        when: work is complete", 1)
		wf := parse(t, "basicFlow", yaml)
		if err := wf.Validate(); err != nil {
			t.Fatalf("route with when rejected: %v", err)
		}
		routes, err := wf.Routes("coding", workflow.OutcomeSuccess)
		if err != nil {
			t.Fatalf("Routes failed: %v", err)
		}
		if len(routes) != 1 || routes[0].When != "work is complete" {
			t.Fatalf("Routes = %+v", routes)
		}
	})
	t.Run("several routes for one outcome", func(t *testing.T) {
		yaml := strings.Replace(minimalValid, "    onFailure:\n      - target: coding", "    onFailure:\n      - target: coding\n        when: retry\n      - target: end\n        when: give up", 1)
		wf := parse(t, "basicFlow", yaml)
		if err := wf.Validate(); err != nil {
			t.Fatalf("two failure routes rejected: %v", err)
		}
		routes, err := wf.Routes("coding", workflow.OutcomeFailure)
		if err != nil || len(routes) != 2 {
			t.Fatalf("Routes = %+v, err=%v", routes, err)
		}
	})
}

func TestValidateGraphReachability(t *testing.T) {
	t.Run("unreachable node", func(t *testing.T) {
		yaml := minimalValid + `
  orphan:
    type: agent
    agent: build
    description: never reached
    onSuccess:
      - target: end
    onFailure:
      - target: orphan
`
		wf, _ := workflow.Parse("basicFlow", []byte(yaml))
		if err := wf.Validate(); err == nil {
			t.Fatal("unreachable node accepted")
		}
	})
	t.Run("end unreachable", func(t *testing.T) {
		yaml := strings.Replace(minimalValid, "      - target: end", "      - target: coding", 1)
		wf, _ := workflow.Parse("basicFlow", []byte(yaml))
		if err := wf.Validate(); err == nil {
			t.Fatal("workflow with unreachable end accepted")
		}
	})
	t.Run("self loop allowed", func(t *testing.T) {
		// minimalValid already has coding onFailure -> coding.
		wf := parse(t, "basicFlow", minimalValid)
		if err := wf.Validate(); err != nil {
			t.Fatalf("self-loop rejected: %v", err)
		}
	})
}

func TestValidateNudgeTemplate(t *testing.T) {
	t.Run("supported variables", func(t *testing.T) {
		yaml := strings.Replace(minimalValid, "    description: Do the coding work.", "    description: Do the coding work.\n    nudgePrompt: \"{{ticket}} {{workflow}} {{repo}} {{node}} {{nextSteps}}\"", 1)
		wf := parse(t, "basicFlow", yaml)
		if err := wf.Validate(); err != nil {
			t.Fatalf("supported nudge variables rejected: %v", err)
		}
	})
	t.Run("unknown variable rejected", func(t *testing.T) {
		yaml := strings.Replace(minimalValid, "    description: Do the coding work.", "    description: Do the coding work.\n    nudgePrompt: \"hello {{assignee}}\"", 1)
		wf, _ := workflow.Parse("basicFlow", []byte(yaml))
		if err := wf.Validate(); err == nil {
			t.Fatal("nudge with unknown variable {{assignee}} accepted")
		}
	})
}

func TestStartTarget(t *testing.T) {
	wf := parse(t, "basicFlow", minimalValid)
	target, err := wf.StartTarget()
	if err != nil {
		t.Fatalf("StartTarget failed: %v", err)
	}
	if target != "coding" {
		t.Fatalf("StartTarget = %q, want coding", target)
	}
}

func TestCleanupRunnerOnEndDefaultsFalse(t *testing.T) {
	wf := parse(t, "basicFlow", minimalValid)
	if wf.CleanupRunnerOnEnd {
		t.Fatal("CleanupRunnerOnEnd defaulted to true, want false")
	}
	yaml := strings.Replace(minimalValid, "repos: [payments]", "repos: [payments]\ncleanupRunnerOnEnd: true", 1)
	wf = parse(t, "basicFlow", yaml)
	if !wf.CleanupRunnerOnEnd {
		t.Fatal("CleanupRunnerOnEnd true not parsed")
	}
}

func TestRenderNudge(t *testing.T) {
	yaml := strings.Replace(minimalValid, "    description: Do the coding work.", "    description: Do the coding work.\n    nudgePrompt: \"ticket={{ticket}} wf={{workflow}} repo={{repo}} node={{node}} steps={{nextSteps}}\"", 1)
	wf := parse(t, "basicFlow", yaml)
	out, err := wf.RenderNudge("coding", workflow.NudgeTemplateData{
		Ticket: "PAY-101", Workflow: "basicFlow", Repo: "payments", Node: "coding", NextSteps: "end",
	})
	if err != nil {
		t.Fatalf("RenderNudge failed: %v", err)
	}
	want := "ticket=PAY-101 wf=basicFlow repo=payments node=coding steps=end"
	if out != want {
		t.Fatalf("RenderNudge = %q, want %q", out, want)
	}

	wf.Nodes["coding"] = workflow.Node{Type: workflow.NodeAgent}
	out, err = wf.RenderNudge("coding", workflow.NudgeTemplateData{})
	if err != nil || out != "" {
		t.Fatalf("empty RenderNudge = %q, %v; want empty", out, err)
	}
}
