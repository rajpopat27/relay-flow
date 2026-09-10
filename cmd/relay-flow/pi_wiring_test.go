package main

import (
	"bytes"
	"path/filepath"
	"strings"
	"testing"

	"github.com/rajpopat27/relay-flow/internal/config"
	"github.com/rajpopat27/relay-flow/internal/harness"
)

func TestPiAppearsInDynamicHarnessSelection(t *testing.T) {
	names := harness.Names()
	if !containsHarnessName(names, "pi") {
		t.Fatalf("harness.Names() = %v, want pi", names)
	}

	var selected string
	field, err := pluginSelectField("Select harness", names, &selected)
	if err != nil {
		t.Fatalf("pluginSelectField: %v", err)
	}
	var output bytes.Buffer
	if err := field.RunAccessible(&output, strings.NewReader("1\n")); err != nil {
		t.Fatalf("RunAccessible: %v", err)
	}
	if !strings.Contains(output.String(), "Select harness") {
		t.Fatalf("selection output %q missing unchanged harness title", output.String())
	}
}

func TestInitPiFlagUsesGenericHarnessSelectionPath(t *testing.T) {
	home := t.TempDir()
	code, output := captureStdout(t, func() int {
		return cli(t, home, "", "init",
			"--task-plugin", "jira",
			"--runner-plugin", "orca",
			"--harness-plugin", "pi")
	})
	if code != 0 {
		t.Fatalf("Pi init exit = %d, want 0", code)
	}
	if !strings.Contains(output, "Harness: pi") {
		t.Fatalf("Pi init output = %q, want generic harness summary", output)
	}

	cfg, err := config.LoadMachine(filepath.Join(home, ".relay-flow", "config.yaml"))
	if err != nil {
		t.Fatalf("load Pi machine config: %v", err)
	}
	if cfg.HarnessPlugin != "pi" {
		t.Fatalf("harnessPlugin = %q, want pi", cfg.HarnessPlugin)
	}
}

func containsHarnessName(names []string, want string) bool {
	for _, name := range names {
		if name == want {
			return true
		}
	}
	return false
}
