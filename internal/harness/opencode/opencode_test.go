package opencode_test

import (
	"reflect"
	"testing"

	"github.com/rajpopat27/relay-flow/internal/harness"
	"github.com/rajpopat27/relay-flow/internal/harness/opencode"
)

func TestBuildCommandArgv(t *testing.T) {
	t.Setenv("RELAY_FLOW_HOME", "/var/lib/relay-flow-test")
	tests := []struct {
		name     string
		resumeID string
		want     []string
	}{
		{
			name: "fresh",
			want: []string{"--agent", "build", "--prompt", "implement the ticket"},
		},
		{
			name:     "resumed",
			resumeID: "session-123",
			want:     []string{"--session", "session-123", "--agent", "build", "--prompt", "implement the ticket"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cmd, err := opencode.New().BuildCommand(harness.LaunchSpec{
				Agent:    "build",
				Prompt:   "implement the ticket",
				ResumeID: tt.resumeID,
			})
			if err != nil {
				t.Fatalf("BuildCommand: %v", err)
			}
			if cmd.Executable != "opencode" {
				t.Fatalf("Executable = %q, want opencode", cmd.Executable)
			}
			if !reflect.DeepEqual(cmd.Args, tt.want) {
				t.Fatalf("Args = %#v, want %#v", cmd.Args, tt.want)
			}
			if cmd.Env["RELAY_FLOW_HOME"] != "/var/lib/relay-flow-test" {
				t.Fatalf("RELAY_FLOW_HOME = %q", cmd.Env["RELAY_FLOW_HOME"])
			}
		})
	}
}
