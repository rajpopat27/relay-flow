package rest

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

func TestADFConvertsExplicitMarkdown(t *testing.T) {
	doc := ADF("# Heading\n\nWords with **strong**, *emphasis*, [a link](https://example.com), and `code`.\n\n1. first\n2. second\n\n```go\nfmt.Println(\"ok\")\n```")
	content := doc["content"].([]any)

	if got := content[0].(map[string]any); got["type"] != "heading" || !reflect.DeepEqual(got["attrs"], map[string]any{"level": 1}) {
		t.Fatalf("heading = %#v", got)
	}

	raw, err := json.Marshal(doc)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		`"type":"strong"`,
		`"type":"em"`,
		`"attrs":{"href":"https://example.com"},"type":"link"`,
		`"type":"code"`,
		`"type":"orderedList"`,
		`"attrs":{"language":"go"}`,
		`"type":"codeBlock"`,
	} {
		if !strings.Contains(string(raw), want) {
			t.Errorf("ADF missing %s: %s", want, raw)
		}
	}
}

func TestADFDoesNotInferFormatting(t *testing.T) {
	doc := ADF("SUMMARY\n\nNode: implement\n\nHTTPS://EXAMPLE.COM")
	raw, err := json.Marshal(doc)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), `"type":"heading"`) || strings.Contains(string(raw), `"type":"strong"`) {
		t.Fatalf("plain text was inferred as formatting: %s", raw)
	}
	if got := len(doc["content"].([]any)); got != 3 {
		t.Fatalf("paragraph count = %d, want 3", got)
	}
}

func TestADFPreservesTaskTextLineBreaks(t *testing.T) {
	value := strings.Join([]string{
		"Parent ticket: PAY-1",
		"Workflow: relayFlow",
		"Node: coding",
		"Node type: agent",
		"Agent: coder",
		"Mailbox: PAY-1:coding",
		"Node work: inspect the change",
		"Keep this instruction on its own line",
	}, "\n")
	doc := ADF(value)
	paragraph := doc["content"].([]any)[0].(map[string]any)
	inline := paragraph["content"].([]any)
	hardBreaks := 0
	for _, item := range inline {
		if item.(map[string]any)["type"] == "hardBreak" {
			hardBreaks++
		}
	}
	if hardBreaks != strings.Count(value, "\n") {
		t.Fatalf("hard break count = %d, want %d: %#v", hardBreaks, strings.Count(value, "\n"), inline)
	}
	raw, err := json.Marshal(doc)
	if err != nil {
		t.Fatal(err)
	}
	if got := ADFText(raw); got != value {
		t.Fatalf("ADFText() = %q, want %q", got, value)
	}
}

func TestADFReportLabelsRemainSeparate(t *testing.T) {
	value := strings.Join([]string{
		"STATUS: success",
		"NEXT STEP: reviewer",
		"",
		"SUMMARY:",
		"COMPLETED: implemented",
		"COMMITS: abc123",
		"NOT COMPLETED: None",
		"ISSUES DISCOVERED: None",
		"VERIFICATION: go test ./...",
		"NOTES: None",
		"",
		"FEEDBACK:",
		"REASON FOR NEXT STEP: ready",
		"REQUIRED ACTIONS: None",
		"RELEVANT CONTEXT: None",
		"EXPECTED RESULT: review",
	}, "\n")
	raw, err := json.Marshal(ADF(value))
	if err != nil {
		t.Fatal(err)
	}
	got := ADFText(raw)
	for _, want := range []string{
		"STATUS: success", "NEXT STEP: reviewer", "SUMMARY:",
		"COMPLETED: implemented", "COMMITS: abc123", "NOT COMPLETED: None",
		"ISSUES DISCOVERED: None", "VERIFICATION: go test ./...", "NOTES: None",
		"FEEDBACK:", "REASON FOR NEXT STEP: ready", "REQUIRED ACTIONS: None",
		"RELEVANT CONTEXT: None", "EXPECTED RESULT: review",
	} {
		found := false
		for _, line := range strings.Split(got, "\n") {
			if line == want {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("ADFText() = %q, missing separate report line %q", got, want)
		}
	}
}

func TestADFMarkerRoundTrip(t *testing.T) {
	const marker = "<!-- run-1:summary -->"
	raw, err := json.Marshal(ADF("Done on line one\nDone on line two\n\n" + marker))
	if err != nil {
		t.Fatal(err)
	}
	got := ADFText(raw)
	if !strings.Contains(got, "Done on line one\nDone on line two") {
		t.Fatalf("ADFText() lost hard break: %q", got)
	}
	if !strings.Contains(got, marker) {
		t.Fatalf("ADFText() = %q, want marker %q", got, marker)
	}
}
