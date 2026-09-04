package opencode_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/rajpopat27/relay-flow/internal/config"
	"github.com/rajpopat27/relay-flow/internal/harness"
	"github.com/rajpopat27/relay-flow/internal/harness/opencode"
	"github.com/rajpopat27/relay-flow/internal/workflow"
)

const configuredPlugin = "relay-flow-plugin@0.2.4-alpha"

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

func TestRenderPromptTemplatesExposeAllValues(t *testing.T) {
	raw := config.RawValues{
		"initial":  "initial {{taskSystem}}|{{ticket}}|{{workflow}}|{{repo}}|{{node}}|{{nodeType}}|{{agent}}|{{nodeDescription}}|{{nextSteps}}|{{mailbox}}",
		"feedback": "feedback {{mailbox}}",
	}
	h, err := harness.New("opencode", raw)
	if err != nil {
		t.Fatal(err)
	}
	data := harness.PromptData{
		TaskSystem: "linear", Ticket: "PAY-101", Workflow: "basicFlow", Repo: "payments",
		Node: "review", NodeType: workflow.NodeHITL, Agent: "build", NodeDescription: "Review it.",
		NextSteps: "end (when: approved)", Mailbox: "PAY-234",
	}
	nudge := "nudge {{taskSystem}}|{{ticket}}|{{workflow}}|{{repo}}|{{node}}|{{nextSteps}}"
	initial, err := h.RenderPrompt(harness.PromptInitial, data, nudge)
	if err != nil {
		t.Fatal(err)
	}
	wantInitial := "initial linear|PAY-101|basicFlow|payments|review|hitl|build|Review it.|end (when: approved)|PAY-234\n\nReturn the complete report directly. Relay-flow will show a native TUI approval dialog after the report is valid. Do not use OpenCode's Question tool for relay-flow approval.\n\nnudge linear|PAY-101|basicFlow|payments|review|end (when: approved)"
	if initial != wantInitial {
		t.Fatalf("initial prompt = %q, want %q", initial, wantInitial)
	}
	feedback, err := h.RenderPrompt(harness.PromptFeedback, data, nudge)
	if err != nil {
		t.Fatal(err)
	}
	if want := "feedback PAY-234\n\nReturn the complete report directly. Relay-flow will show a native TUI approval dialog after the report is valid. Do not use OpenCode's Question tool for relay-flow approval.\n\nnudge linear|PAY-101|basicFlow|payments|review|end (when: approved)"; feedback != want {
		t.Fatalf("feedback prompt = %q, want %q", feedback, want)
	}
}

func TestHarnessConfigRejectsUnknownPromptVariable(t *testing.T) {
	_, err := harness.New("opencode", config.RawValues{"initial": "{{unknown}}"})
	if err == nil || !strings.Contains(err.Error(), "unknown template variable {{unknown}}") {
		t.Fatalf("harness.New error = %v", err)
	}
}

func TestSetupRepoCreatesOpenCodeJSON(t *testing.T) {
	dir := t.TempDir()
	setupTwice(t, dir)
	path := filepath.Join(dir, "opencode.json")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var cfg struct {
		Plugin []string `json:"plugin"`
	}
	if err := json.Unmarshal(data, &cfg); err != nil {
		t.Fatalf("created config is invalid JSON: %v\n%s", err, data)
	}
	if !reflect.DeepEqual(cfg.Plugin, []string{configuredPlugin}) {
		t.Fatalf("plugin = %v", cfg.Plugin)
	}
}

func TestSetupRepoUpdatesExistingJSONAndPreservesContent(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "opencode.json")
	original := "{\n  \"theme\": \"catppuccin\",\n  \"plugin\": [\"keep-me\", \"relay-flow-plugin@0.1.0\"],\n  \"nested\": {\"enabled\": true}\n}\n"
	if err := os.WriteFile(path, []byte(original), 0o640); err != nil {
		t.Fatal(err)
	}
	setupTwice(t, dir)
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var cfg struct {
		Theme  string          `json:"theme"`
		Plugin []string        `json:"plugin"`
		Nested map[string]bool `json:"nested"`
	}
	if err := json.Unmarshal(data, &cfg); err != nil {
		t.Fatalf("updated config is invalid JSON: %v\n%s", err, data)
	}
	if cfg.Theme != "catppuccin" || !cfg.Nested["enabled"] {
		t.Fatalf("existing config changed: %+v", cfg)
	}
	if !reflect.DeepEqual(cfg.Plugin, []string{"keep-me", configuredPlugin}) {
		t.Fatalf("plugin = %v", cfg.Plugin)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o640 {
		t.Fatalf("mode = %o, want 640", info.Mode().Perm())
	}
}

func TestSetupRepoUpdatesJSONCOnceAndPreservesComments(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "opencode.jsonc")
	original := `{
  // theme stays
  "theme": "catppuccin",
  "plugin": [
    "keep-me", // existing plugin stays
    "relay-flow-plugin",
    /* duplicate old relay-flow entry */
    "relay-flow-plugin@0.1.0",
  ],
  "nested": {"enabled": true}, // trailing comment stays
}
`
	if err := os.WriteFile(path, []byte(original), 0o644); err != nil {
		t.Fatal(err)
	}
	setupTwice(t, dir)
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	text := string(data)
	for _, preserved := range []string{"// theme stays", "// existing plugin stays", "/* duplicate old relay-flow entry */", "// trailing comment stays", "\"keep-me\"", "\"nested\": {\"enabled\": true}"} {
		if !strings.Contains(text, preserved) {
			t.Fatalf("config lost %q:\n%s", preserved, text)
		}
	}
	if strings.Count(text, configuredPlugin) != 1 || strings.Contains(text, "relay-flow-plugin@0.1.0") {
		t.Fatalf("relay-flow plugin not updated exactly once:\n%s", text)
	}
	if _, err := os.Stat(filepath.Join(dir, "opencode.json")); !os.IsNotExist(err) {
		t.Fatalf("unexpected opencode.json created: %v", err)
	}
}

func TestSetupRepoAddsPluginPropertyToJSONCWithComments(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "opencode.jsonc")
	original := "{\n  \"theme\": \"catppuccin\" // keep at end\n}\n"
	if err := os.WriteFile(path, []byte(original), 0o644); err != nil {
		t.Fatal(err)
	}
	setupTwice(t, dir)
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	text := string(data)
	if !strings.Contains(text, "// keep at end") || strings.Count(text, configuredPlugin) != 1 {
		t.Fatalf("config not preserved and updated:\n%s", text)
	}
}

func TestSetupRepoCreatesOpenCodeTUIConfig(t *testing.T) {
	dir := t.TempDir()
	if err := opencode.New().SetupRepo(t.Context(), dir); err != nil {
		t.Fatalf("SetupRepo: %v", err)
	}
	path := filepath.Join(dir, ".opencode", "tui.json")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var cfg struct {
		Schema string   `json:"$schema"`
		Plugin []string `json:"plugin"`
	}
	if err := json.Unmarshal(data, &cfg); err != nil {
		t.Fatalf("created TUI config is invalid JSON: %v\n%s", err, data)
	}
	if cfg.Schema != "https://opencode.ai/tui.json" {
		t.Fatalf("$schema = %q", cfg.Schema)
	}
	if !reflect.DeepEqual(cfg.Plugin, []string{configuredPlugin}) {
		t.Fatalf("plugin = %v", cfg.Plugin)
	}
}

func TestSetupRepoUpdatesExistingOpenCodeTUIJSONC(t *testing.T) {
	dir := t.TempDir()
	configDir := filepath.Join(dir, ".opencode")
	if err := os.MkdirAll(configDir, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(configDir, "tui.jsonc")
	original := `{
  // keep the local theme
  "theme": "catppuccin"
}
`
	if err := os.WriteFile(path, []byte(original), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := opencode.New().SetupRepo(t.Context(), dir); err != nil {
		t.Fatalf("SetupRepo: %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	text := string(data)
	if !strings.Contains(text, "// keep the local theme") || !strings.Contains(text, configuredPlugin) {
		t.Fatalf("TUI config was not preserved and updated:\n%s", text)
	}
	if _, err := os.Stat(filepath.Join(configDir, "tui.json")); !os.IsNotExist(err) {
		t.Fatalf("unexpected tui.json created: %v", err)
	}
}

func setupTwice(t *testing.T, dir string) {
	t.Helper()
	h := opencode.New()
	if err := h.SetupRepo(t.Context(), dir); err != nil {
		t.Fatalf("first SetupRepo: %v", err)
	}
	path := filepath.Join(dir, "opencode.json")
	if _, err := os.Stat(path); os.IsNotExist(err) {
		path = filepath.Join(dir, "opencode.jsonc")
	}
	first, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := h.SetupRepo(t.Context(), dir); err != nil {
		t.Fatalf("second SetupRepo: %v", err)
	}
	second, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(first, second) {
		t.Fatalf("SetupRepo is not idempotent\nfirst:\n%s\nsecond:\n%s", first, second)
	}
}
