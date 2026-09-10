package pi

import (
	"testing"
)

// TestBuildCommandCarriesTaskSystemEnvironment asserts the task-system
// workspace environment reaches the runner command so agent task commands
// address the same workspace as relay-flow, and that relay-flow's own
// variables cannot be overwritten by the task system.
func TestBuildCommandCarriesTaskSystemEnvironment(t *testing.T) {
	t.Setenv("RELAY_FLOW_HOME", "/var/lib/relay-flow-test")

	spec := launchSpec(t)
	spec.TaskEnv = map[string]string{
		"BEADS_DIR":       "/var/lib/beads/payments/.beads",
		"BEADS_DB":        "",
		"BD_DB":           "",
		"RELAY_FLOW_NODE": "hijacked",
	}
	cmd, err := (&Harness{}).BuildCommand(spec)
	if err != nil {
		t.Fatalf("BuildCommand: %v", err)
	}
	if cmd.Env["BEADS_DIR"] != "/var/lib/beads/payments/.beads" {
		t.Fatalf("BEADS_DIR = %q", cmd.Env["BEADS_DIR"])
	}
	for _, key := range []string{"BEADS_DB", "BD_DB"} {
		value, ok := cmd.Env[key]
		if !ok || value != "" {
			t.Fatalf("%s = %q, present=%v; want empty selector", key, value, ok)
		}
	}
	if cmd.Env["RELAY_FLOW_NODE"] != spec.Node {
		t.Fatalf("RELAY_FLOW_NODE = %q, want %q", cmd.Env["RELAY_FLOW_NODE"], spec.Node)
	}
}

// TestBuildCommandWithoutTaskEnvKeepsRelayVariablesOnly asserts an adapter
// that exposes no workspace environment adds nothing to the command.
func TestBuildCommandWithoutTaskEnvKeepsRelayVariablesOnly(t *testing.T) {
	t.Setenv("RELAY_FLOW_HOME", "/var/lib/relay-flow-test")

	cmd, err := (&Harness{}).BuildCommand(launchSpec(t))
	if err != nil {
		t.Fatalf("BuildCommand: %v", err)
	}
	if len(cmd.Env) != 9 {
		t.Fatalf("Env = %#v, want only the nine relay-flow variables", cmd.Env)
	}
}
