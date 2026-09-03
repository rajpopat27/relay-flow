package pi

import (
	"strings"
	"testing"

	"github.com/rajpopat27/relay-flow/internal/config"
	"github.com/rajpopat27/relay-flow/internal/harness"
)

func TestPiHarnessConfigUsesDocumentedDefaults(t *testing.T) {
	defaults, err := harness.Defaults("pi")
	if err != nil {
		t.Fatalf("harness.Defaults(pi): %v", err)
	}
	if defaults["initial"] != defaultInitialPrompt {
		t.Fatalf("initial default = %#v, want %#v", defaults["initial"], defaultInitialPrompt)
	}
	if defaults["feedback"] != defaultFeedbackPrompt {
		t.Fatalf("feedback default = %#v, want %#v", defaults["feedback"], defaultFeedbackPrompt)
	}
	if _, ok := defaults["hitl"]; ok {
		t.Fatalf("Pi defaults unexpectedly contain a HITL prompt: %#v", defaults["hitl"])
	}
}

func TestPiHarnessConfigRejectsNonTemplateFields(t *testing.T) {
	for _, field := range []string{"hitl", "agent", "model"} {
		t.Run(field, func(t *testing.T) {
			_, err := harness.New("pi", config.RawValues{field: "unsupported"})
			if err == nil {
				t.Fatalf("Pi harness accepted unsupported config field %q", field)
			}
			if !strings.Contains(err.Error(), field) {
				t.Fatalf("error = %q, want field name %q", err, field)
			}
		})
	}
}

func TestPiHarnessConfigRejectsExplicitNullTemplate(t *testing.T) {
	_, err := harness.New("pi", config.RawValues{"initial": nil})
	if err == nil || !strings.Contains(err.Error(), "explicit null") {
		t.Fatalf("explicit null template error = %v", err)
	}
}
