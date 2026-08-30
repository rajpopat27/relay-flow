package config_test

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/rajpopat27/relay-flow/internal/config"
)

// 3.4: Merge behavior per specs/workflow-definition "Task config merge
// behavior is deterministic": root -> repo -> workflow -> node precedence,
// recursive map merge, later scalar/list replaces, omitted keys inherit,
// explicit YAML null rejected.

func TestMergePrecedenceRootToNode(t *testing.T) {
	root := config.RawValues{"assignee": "root-bot", "severity": "low"}
	repo := config.RawValues{"severity": "medium"}
	wf := config.RawValues{"severity": "high"}
	node := config.RawValues{"severity": "critical"}

	got := config.Merge(root, repo, wf, node)
	if got["severity"] != "critical" {
		t.Fatalf("severity = %v, want node value critical", got["severity"])
	}
	if got["assignee"] != "root-bot" {
		t.Fatalf("assignee = %v, want inherited root-bot", got["assignee"])
	}
}

func TestMergeMapsRecursively(t *testing.T) {
	root := config.RawValues{
		"transitionTo": map[string]any{"parentStatus": "In Progress", "taskStatus": "To Do"},
	}
	node := config.RawValues{
		"transitionTo": map[string]any{"taskStatus": "In Review"},
	}
	got := config.Merge(root, node)
	tr, ok := got["transitionTo"].(map[string]any)
	if !ok {
		t.Fatalf("transitionTo not a map: %T", got["transitionTo"])
	}
	if tr["parentStatus"] != "In Progress" {
		t.Fatalf("nested parentStatus = %v, want inherited In Progress", tr["parentStatus"])
	}
	if tr["taskStatus"] != "In Review" {
		t.Fatalf("nested taskStatus = %v, want node override In Review", tr["taskStatus"])
	}
}

func TestMergeListReplaces(t *testing.T) {
	root := config.RawValues{"labels": []any{"a", "b"}}
	wf := config.RawValues{"labels": []any{"c"}}
	got := config.Merge(root, wf)
	if !reflect.DeepEqual(got["labels"], []any{"c"}) {
		t.Fatalf("labels = %v, want replacement [c] not append", got["labels"])
	}
}

func TestMergeScalarReplaces(t *testing.T) {
	root := config.RawValues{"retries": "1"}
	node := config.RawValues{"retries": "3"}
	got := config.Merge(root, node)
	if got["retries"] != "3" {
		t.Fatalf("retries = %v, want 3", got["retries"])
	}
}

func TestMergeOmittedKeyInherits(t *testing.T) {
	root := config.RawValues{"project": "PAY", "component": "api"}
	repo := config.RawValues{"component": "web"}
	got := config.Merge(root, repo)
	if got["project"] != "PAY" {
		t.Fatalf("project = %v, want inherited PAY", got["project"])
	}
	if got["component"] != "web" {
		t.Fatalf("component = %v, want web", got["component"])
	}
}

func TestMergeRejectsExplicitNull(t *testing.T) {
	// Explicit YAML null must be rejected by the documented validation path,
	// not silently merged or dropped. DecodeStrict is the strict decode the
	// adapters use; a null value is not a valid scalar/map/list.
	node := config.RawValues{"assignee": nil}
	var dst struct {
		Assignee string `yaml:"assignee"`
	}
	if err := config.DecodeStrict(node, &dst); err == nil {
		t.Fatal("explicit YAML null accepted by DecodeStrict; null must be rejected")
	}
}

func TestMergeNullLayerRejectedAtValidation(t *testing.T) {
	// Machine config containing an explicit null fails loading.
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	yaml := "taskPlugin: jira\nrunnerPlugin: orca\nharnessPlugin: opencode\ntaskConfig:\n  assignee: null\n"
	if err := os.WriteFile(path, []byte(yaml), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := config.LoadMachine(path); err == nil {
		t.Fatal("machine config with explicit null loaded; null must be rejected")
	}
}

func TestMergeDoesNotMutateInputs(t *testing.T) {
	root := config.RawValues{"m": map[string]any{"a": "1"}}
	node := config.RawValues{"m": map[string]any{"b": "2"}}
	_ = config.Merge(root, node)
	if len(root["m"].(map[string]any)) != 1 {
		t.Fatalf("root input mutated: %v", root)
	}
	if len(node["m"].(map[string]any)) != 1 {
		t.Fatalf("node input mutated: %v", node)
	}
}
