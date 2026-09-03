package pi

import (
	"strings"
	"testing"

	"github.com/rajpopat27/relay-flow/internal/config"
	"github.com/rajpopat27/relay-flow/internal/harness"
	"github.com/rajpopat27/relay-flow/internal/workflow"
)

func TestPiRenderPromptSubstitutesInitialAndFeedbackData(t *testing.T) {
	h := newPiHarness(t)
	data := harness.PromptData{
		TaskSystem:      "jira",
		Ticket:          "PAY-101",
		Workflow:        "basicFlow",
		Repo:            "payments",
		Node:            "implement",
		NodeType:        workflow.NodeAgent,
		Agent:           "default",
		NodeDescription: "Implement the requested change.",
		NextSteps:       "review (when: ready)",
		Mailbox:         "PAY-234",
	}
	nudge := "nudge {{taskSystem}}|{{ticket}}|{{workflow}}|{{repo}}|{{node}}|{{nextSteps}}"

	initial, err := h.RenderPrompt(harness.PromptInitial, data, nudge)
	if err != nil {
		t.Fatalf("RenderPrompt(initial): %v", err)
	}
	wantInitial := "Task system: jira\nUse the jira tools to read the parent ticket PAY-101.\n\nYour mailbox is PAY-234. Read its description and comments for node instructions and feedback.\n\nnudge jira|PAY-101|basicFlow|payments|implement|review (when: ready)"
	if initial != wantInitial {
		t.Fatalf("initial prompt = %q, want %q", initial, wantInitial)
	}

	feedback, err := h.RenderPrompt(harness.PromptFeedback, data, nudge)
	if err != nil {
		t.Fatalf("RenderPrompt(feedback): %v", err)
	}
	wantFeedback := "New feedback was added to the comments section of your mailbox subtask PAY-234. Read it.\n\nnudge jira|PAY-101|basicFlow|payments|implement|review (when: ready)"
	if feedback != wantFeedback {
		t.Fatalf("feedback prompt = %q, want %q", feedback, wantFeedback)
	}
}

func TestPiRenderPromptHonorsNodeNudgeTimingInput(t *testing.T) {
	h, err := harness.New("pi", config.RawValues{
		"initial":  "initial {{node}}",
		"feedback": "feedback {{node}}",
	})
	if err != nil {
		t.Fatalf("harness.New(pi): %v", err)
	}
	data := harness.PromptData{Node: "implement", Agent: "default"}

	for _, tt := range []struct {
		name  string
		kind  harness.PromptKind
		want  string
		nudge string
	}{
		{name: "initial without nudge", kind: harness.PromptInitial, want: "initial implement"},
		{name: "initial with nudge", kind: harness.PromptInitial, want: "initial implement\n\nnudge implement", nudge: "nudge {{node}}"},
		{name: "feedback without nudge", kind: harness.PromptFeedback, want: "feedback implement"},
		{name: "feedback with nudge", kind: harness.PromptFeedback, want: "feedback implement\n\nnudge implement", nudge: "nudge {{node}}"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			got, err := h.RenderPrompt(tt.kind, data, tt.nudge)
			if err != nil {
				t.Fatalf("RenderPrompt: %v", err)
			}
			if got != tt.want {
				t.Fatalf("prompt = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestPiPromptRetainsDefaultAgentLabel(t *testing.T) {
	h, err := harness.New("pi", config.RawValues{
		"initial":  "agent={{agent}} node={{node}}",
		"feedback": "feedback {{agent}}",
	})
	if err != nil {
		t.Fatalf("harness.New(pi): %v", err)
	}
	got, err := h.RenderPrompt(harness.PromptInitial, harness.PromptData{
		Agent: "default",
		Node:  "implement",
	}, "")
	if err != nil {
		t.Fatalf("RenderPrompt: %v", err)
	}
	if got != "agent=default node=implement" {
		t.Fatalf("prompt = %q, want default agent label", got)
	}
}

func TestPiPromptsDoNotIncludeOpenCodeQuestionInstructions(t *testing.T) {
	h := newPiHarness(t)
	data := harness.PromptData{
		TaskSystem: "jira",
		Ticket:     "PAY-101",
		Node:       "review",
		NodeType:   workflow.NodeHITL,
		Agent:      "default",
		Mailbox:    "PAY-235",
	}
	for _, kind := range []harness.PromptKind{harness.PromptInitial, harness.PromptFeedback} {
		got, err := h.RenderPrompt(kind, data, "")
		if err != nil {
			t.Fatalf("RenderPrompt(%q): %v", kind, err)
		}
		for _, forbidden := range []string{"OpenCode", "Question tool", "Approve", "Reject"} {
			if strings.Contains(got, forbidden) {
				t.Fatalf("%q prompt contains OpenCode HITL instruction %q: %q", kind, forbidden, got)
			}
		}
	}
}
