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

func TestADFMarkerRoundTrip(t *testing.T) {
	const marker = "<!-- run-1:summary -->"
	raw, err := json.Marshal(ADF("Done.\n\n" + marker))
	if err != nil {
		t.Fatal(err)
	}
	if got := ADFText(raw); !strings.Contains(got, marker) {
		t.Fatalf("ADFText() = %q, want marker %q", got, marker)
	}
}
