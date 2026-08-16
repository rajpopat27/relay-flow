package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestDemoWorkflowYAML(t *testing.T) {
	b, err := os.ReadFile(filepath.Join("..", "..", "..", ".workflow", "workflow.yaml"))
	if err != nil {
		t.Skip("demo yaml not present")
	}
	if _, err := Parse("demo", b); err != nil {
		t.Fatalf("demo .workflow/workflow.yaml must validate: %v", err)
	}
}
