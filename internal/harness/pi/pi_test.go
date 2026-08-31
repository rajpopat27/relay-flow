package pi

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/rajpopat27/relay-flow/internal/harness"
	"github.com/rajpopat27/relay-flow/internal/identity"
	"github.com/rajpopat27/relay-flow/internal/runner"
	"github.com/rajpopat27/relay-flow/internal/workflow"
)

// TestBuildCommandUsesStrictPiCLIContract executes the command returned by
// the adapter against a fake that accepts only the installed Pi 0.84.1 launch
// shape. The fake is intentionally an executable on PATH rather than a
// production lookup seam: Pi availability is covered separately, while this
// test verifies the opaque runner command and its handoff to the ticket
// environment.
func TestBuildCommandUsesStrictPiCLIContract(t *testing.T) {
	fakeDir, capturePath := strictPiCLI(t)
	t.Setenv("PATH", fakeDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("RELAY_FLOW_HOME", "/var/lib/relay-flow-test")

	base := launchSpec(t)
	ticketEnvironment := t.TempDir()
	nextSteps, err := json.Marshal(base.NextSteps)
	if err != nil {
		t.Fatal(err)
	}
	wantEnv := map[string]string{
		"RELAY_FLOW_HOME":            "/var/lib/relay-flow-test",
		"RELAY_FLOW_RUN_ID":          string(base.RunID),
		"RELAY_FLOW_WORKFLOW":        base.Workflow,
		"RELAY_FLOW_REPO":            base.RepoName,
		"RELAY_FLOW_TICKET":          base.Ticket,
		"RELAY_FLOW_NODE":            base.Node,
		"RELAY_FLOW_NODE_TYPE":       string(base.NodeType),
		"RELAY_FLOW_NUDGE_PROMPT":    base.NudgePrompt,
		"RELAY_FLOW_NEXT_STEPS_JSON": string(nextSteps),
	}

	tests := []struct {
		name     string
		resumeID string
		wantArgs []string
	}{
		{
			name: "fresh",
			wantArgs: []string{
				"--name", "PAY-101:implement",
				"first line\nsecond line",
			},
		},
		{
			name:     "resumed",
			resumeID: "session-123",
			wantArgs: []string{
				"--name", "PAY-101:implement",
				"--session-id", "session-123",
				"first line\nsecond line",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			spec := base
			spec.ResumeID = tt.resumeID
			cmd, err := newPiHarness(t).BuildCommand(spec)
			if err != nil {
				t.Fatalf("BuildCommand: %v", err)
			}
			if cmd.Executable != "pi" {
				t.Fatalf("Executable = %q, want pi", cmd.Executable)
			}
			if !reflect.DeepEqual(cmd.Args, tt.wantArgs) {
				t.Fatalf("Args = %#v, want %#v", cmd.Args, tt.wantArgs)
			}
			if !reflect.DeepEqual(cmd.Env, wantEnv) {
				t.Fatalf("Env = %#v, want %#v", cmd.Env, wantEnv)
			}
			if _, ok := cmd.Env["RELAY_FLOW_NODE_VISIT_ID"]; ok {
				t.Fatal("command leaked internal RELAY_FLOW_NODE_VISIT_ID")
			}

			capture := runStrictPi(t, cmd, ticketEnvironment, capturePath)
			if capture.cwd != ticketEnvironment {
				t.Fatalf("fake Pi cwd = %q, want ticket environment %q", capture.cwd, ticketEnvironment)
			}
			if !reflect.DeepEqual(capture.args, tt.wantArgs) {
				t.Fatalf("fake Pi args = %#v, want %#v", capture.args, tt.wantArgs)
			}
			if !reflect.DeepEqual(capture.env, wantEnv) {
				t.Fatalf("fake Pi relay-flow env = %#v, want %#v", capture.env, wantEnv)
			}
		})
	}
}

func TestStrictPiCLIFakeRejectsUnsupportedLaunchFlags(t *testing.T) {
	fakeDir, capturePath := strictPiCLI(t)
	t.Setenv("PATH", fakeDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("RELAY_FLOW_HOME", "/var/lib/relay-flow-test")
	base := launchSpec(t)
	ticketEnvironment := t.TempDir()
	cmd, err := newPiHarness(t).BuildCommand(base)
	if err != nil {
		t.Fatalf("BuildCommand: %v", err)
	}

	for _, args := range [][]string{
		{"--", base.Prompt},
		{"--agent", "default", base.Prompt},
		{"--interactive", base.Prompt},
		{"--report", base.Prompt},
		{"--print", base.Prompt},
		{"--mode", "json", base.Prompt},
		{"--mode", "rpc", base.Prompt},
		{"--extension", "relay-flow-plugin", base.Prompt},
		{"install", "npm:relay-flow-plugin"},
	} {
		bad := exec.Command("pi", args...)
		bad.Dir = ticketEnvironment
		bad.Env = commandEnv(cmd.Env, capturePath)
		if err := bad.Run(); err == nil {
			t.Fatalf("strict fake accepted unsupported args %#v", args)
		}
	}
}

func newPiHarness(t *testing.T) harness.Harness {
	t.Helper()
	h, err := harness.New("pi", nil)
	if err != nil {
		t.Fatalf("harness.New(pi): %v", err)
	}
	return h
}

func launchSpec(t *testing.T) harness.LaunchSpec {
	t.Helper()
	return harness.LaunchSpec{
		RunID:       identity.NewRunID("payments", "basicFlow", "PAY-101"),
		NodeVisitID: identity.NewNodeVisitID(),
		RepoName:    "payments",
		RepoPath:    t.TempDir(),
		Workflow:    "basicFlow",
		Ticket:      "PAY-101",
		Node:        "implement",
		NodeType:    workflow.NodeAgent,
		Agent:       "default",
		Title:       "PAY-101:implement",
		Prompt:      "first line\nsecond line",
		NudgePrompt: "emit the complete report",
		NextSteps: []workflow.Route{
			{Target: "review", When: "implementation complete"},
			{Target: "implement", When: "needs more work"},
		},
	}
}

type piCapture struct {
	cwd  string
	args []string
	env  map[string]string
}

func runStrictPi(t *testing.T, command runner.Command, cwd, capturePath string) piCapture {
	t.Helper()
	process := exec.Command(command.Executable, command.Args...)
	process.Dir = cwd
	process.Env = commandEnv(command.Env, capturePath)
	if output, err := process.CombinedOutput(); err != nil {
		t.Fatalf("strict Pi fake: %v\n%s", err, output)
	}
	data, err := os.ReadFile(capturePath)
	if err != nil {
		t.Fatalf("read strict Pi capture: %v", err)
	}
	fields := strings.Split(string(data), "\x00")
	if len(fields) < 12 || fields[len(fields)-1] != "" {
		t.Fatalf("malformed strict Pi capture: %q", data)
	}
	fields = fields[:len(fields)-1]
	capture := piCapture{
		cwd:  fields[0],
		args: strings.Split(fields[10], "\x1f"),
		env: map[string]string{
			"RELAY_FLOW_HOME":            fields[1],
			"RELAY_FLOW_RUN_ID":          fields[2],
			"RELAY_FLOW_WORKFLOW":        fields[3],
			"RELAY_FLOW_REPO":            fields[4],
			"RELAY_FLOW_TICKET":          fields[5],
			"RELAY_FLOW_NODE":            fields[6],
			"RELAY_FLOW_NODE_TYPE":       fields[7],
			"RELAY_FLOW_NUDGE_PROMPT":    fields[8],
			"RELAY_FLOW_NEXT_STEPS_JSON": fields[9],
		},
	}
	return capture
}

func commandEnv(values map[string]string, capturePath string) []string {
	env := make([]string, 0, len(os.Environ())+len(values)+1)
	for _, value := range os.Environ() {
		key := strings.SplitN(value, "=", 2)[0]
		if _, replaced := values[key]; replaced || key == "PI_FAKE_CAPTURE" {
			continue
		}
		env = append(env, value)
	}
	env = append(env, "PI_FAKE_CAPTURE="+capturePath)
	for key, value := range values {
		env = append(env, key+"="+value)
	}
	return env
}

func strictPiCLI(t *testing.T) (string, string) {
	t.Helper()
	directory := t.TempDir()
	executable := filepath.Join(directory, "pi")
	capture := filepath.Join(directory, "capture")
	const script = `#!/bin/sh
set -eu

capture=${PI_FAKE_CAPTURE:?}
original_args=
for arg in "$@"; do
  if [ -n "$original_args" ]; then original_args="$original_args$(printf '\037')"; fi
  original_args="$original_args$arg"
done
[ "${1:-}" = "--name" ] || exit 2
[ "$#" -ge 3 ] || exit 2
shift 2
if [ "${1:-}" = "--session-id" ]; then
  [ "$#" -ge 3 ] || exit 2
  shift 2
fi
[ "$#" -eq 1 ] || exit 2

[ -n "${RELAY_FLOW_HOME:-}" ] || exit 3
[ -n "${RELAY_FLOW_RUN_ID:-}" ] || exit 3
[ -n "${RELAY_FLOW_WORKFLOW:-}" ] || exit 3
[ -n "${RELAY_FLOW_REPO:-}" ] || exit 3
[ -n "${RELAY_FLOW_TICKET:-}" ] || exit 3
[ -n "${RELAY_FLOW_NODE:-}" ] || exit 3
[ -n "${RELAY_FLOW_NODE_TYPE:-}" ] || exit 3
[ -n "${RELAY_FLOW_NEXT_STEPS_JSON:-}" ] || exit 3

printf '%s\000%s\000%s\000%s\000%s\000%s\000%s\000%s\000%s\000%s\000%s\000' \
  "$PWD" "$RELAY_FLOW_HOME" "$RELAY_FLOW_RUN_ID" "$RELAY_FLOW_WORKFLOW" \
  "$RELAY_FLOW_REPO" "$RELAY_FLOW_TICKET" "$RELAY_FLOW_NODE" "$RELAY_FLOW_NODE_TYPE" \
  "${RELAY_FLOW_NUDGE_PROMPT:-}" "$RELAY_FLOW_NEXT_STEPS_JSON" "$original_args" > "$capture"
`
	if err := os.WriteFile(executable, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	return directory, capture
}
