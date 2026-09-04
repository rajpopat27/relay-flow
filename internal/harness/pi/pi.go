// Package pi is the built-in launch-time Harness adapter for the Pi coding
// agent. Pi supplies one built-in coding agent, represented by the logical
// relay-flow agent name "default", plus repository-owned role prompts under
// .pi/roles. The runtime extension is installed by the user and owns report
// parsing and delivery.
package pi

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/rajpopat27/relay-flow/internal/config"
	"github.com/rajpopat27/relay-flow/internal/harness"
	"github.com/rajpopat27/relay-flow/internal/runner"
)

const (
	defaultInitialPrompt = `Task system: {{taskSystem}}
Use the {{taskSystem}} tools to read the parent ticket {{ticket}}.

Your mailbox is {{mailbox}}. Read its description and comments for node instructions and feedback.`
	defaultFeedbackPrompt = `New feedback was added to the comments section of your mailbox subtask {{mailbox}}. Read it.`
)

var promptVarPattern = regexp.MustCompile(`\{\{([^{}]*)\}\}`)

var knownPromptVars = map[string]bool{
	"taskSystem": true, "ticket": true, "workflow": true, "repo": true,
	"node": true, "nodeType": true, "agent": true, "nodeDescription": true,
	"nextSteps": true, "mailbox": true,
}

// Config is the adapter-owned root harnessConfig. Pi exposes no harness
// prompt for HITL approval because approval is performed through Pi's host
// UI by the runtime extension.
type Config struct {
	Initial  string `yaml:"initial"`
	Feedback string `yaml:"feedback"`
}

// DefaultConfig is written by relay-flow init and fills omitted values when
// configuration is loaded through the harness factory.
func DefaultConfig() config.RawValues {
	return config.RawValues{
		"initial":  defaultInitialPrompt,
		"feedback": defaultFeedbackPrompt,
	}
}

func init() {
	harness.Register("pi", harness.Factory{
		DefaultConfig: DefaultConfig,
		New: func(raw config.RawValues) (harness.Harness, error) {
			var cfg Config
			if err := config.DecodeStrict(raw, &cfg); err != nil {
				return nil, fmt.Errorf("pi harnessConfig: %w", err)
			}
			for name, tmpl := range map[string]string{
				"initial":  cfg.Initial,
				"feedback": cfg.Feedback,
			} {
				if err := validateTemplate(tmpl); err != nil {
					return nil, fmt.Errorf("pi harnessConfig templates.%s: %w", name, err)
				}
			}
			return New(cfg), nil
		},
	})
}

// Harness implements harness.Harness for Pi.
type Harness struct {
	templates Config
}

// New returns the production Pi harness. The no-argument form uses the
// adapter defaults; factory construction supplies an explicitly decoded
// configuration.
func New(cfg ...Config) *Harness {
	if len(cfg) > 0 {
		return &Harness{templates: cfg[0]}
	}
	return &Harness{templates: Config{
		Initial:  defaultInitialPrompt,
		Feedback: defaultFeedbackPrompt,
	}}
}

// SetupRepo is intentionally a no-op. The relay-flow Pi runtime extension is
// installed manually in Pi's global package settings rather than configured
// in each repository.
func (*Harness) SetupRepo(context.Context, string) error { return nil }

// ValidateAgent accepts Pi's built-in logical agent or a repository role
// prompt. Pi has no named-agent listing API, so custom roles are verified by
// checking for a readable, non-empty .pi/roles/<agent>.md file.
func (*Harness) ValidateAgent(_ context.Context, repoPath, agent string) error {
	if _, err := resolveRolePrompt(repoPath, agent); err != nil {
		return err
	}
	if _, err := exec.LookPath("pi"); err != nil {
		return fmt.Errorf("pi: executable unavailable: %w", err)
	}
	return nil
}

func resolveRolePrompt(repoPath, agent string) (string, error) {
	if agent == "default" {
		return "", nil
	}
	if strings.TrimSpace(agent) == "" || strings.TrimSpace(agent) != agent || strings.ContainsAny(agent, `/\\`) || agent == "." || agent == ".." {
		return "", fmt.Errorf("pi: invalid role %q", agent)
	}
	if repoPath == "" {
		return "", fmt.Errorf("pi: role %q requires a repository path", agent)
	}
	root, err := filepath.Abs(repoPath)
	if err != nil {
		return "", fmt.Errorf("pi: resolve repository path for role %q: %w", agent, err)
	}
	path := filepath.Join(root, ".pi", "roles", agent+".md")
	info, err := os.Stat(path)
	if err != nil {
		if os.IsNotExist(err) {
			return "", fmt.Errorf("pi: role %q is unavailable; expected %s", agent, path)
		}
		return "", fmt.Errorf("pi: inspect role %q: %w", agent, err)
	}
	if !info.Mode().IsRegular() {
		return "", fmt.Errorf("pi: role %q is not a regular file: %s", agent, path)
	}
	contents, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("pi: read role %q: %w", agent, err)
	}
	if strings.TrimSpace(string(contents)) == "" {
		return "", fmt.Errorf("pi: role %q is empty: %s", agent, path)
	}
	return path, nil
}

// FindSession is intentionally discovery-free. Normal execution resumes only
// the Pi session ID persisted by runtime registration.
func (*Harness) FindSession(context.Context, string, string) (harness.Session, bool, error) {
	return harness.Session{}, false, nil
}

// RenderPrompt renders the selected initial or feedback template and the
// node's nudge template. HITL approval is not encoded in the prompt; the Pi
// extension asks for approval through ctx.ui.select.
func (h *Harness) RenderPrompt(kind harness.PromptKind, data harness.PromptData, nudgeTemplate string) (string, error) {
	var tmpl string
	switch kind {
	case harness.PromptInitial:
		tmpl = h.templates.Initial
	case harness.PromptFeedback:
		tmpl = h.templates.Feedback
	default:
		return "", fmt.Errorf("pi: unknown prompt kind %q", kind)
	}
	return appendPrompt(renderTemplate(tmpl, data), renderTemplate(nudgeTemplate, data)), nil
}

// BuildCommand returns the interactive Pi invocation. The runner supplies a
// PTY for Pi's stdin/stdout; the rendered prompt is the final positional argv
// value. A custom role adds Pi's --append-system-prompt option with the role
// file from the registered repository. Pi 0.84.1 rejects a bare -- terminator,
// so none is included. A non-empty ResumeID selects Pi's exact session-id
// resume option.
func (*Harness) BuildCommand(spec harness.LaunchSpec) (runner.Command, error) {
	rolePath, err := resolveRolePrompt(spec.RepoPath, spec.Agent)
	if err != nil {
		return runner.Command{}, err
	}
	nextSteps, err := json.Marshal(spec.NextSteps)
	if err != nil {
		return runner.Command{}, fmt.Errorf("pi: marshal next steps: %w", err)
	}
	root, err := relayFlowHome()
	if err != nil {
		return runner.Command{}, err
	}
	env := map[string]string{
		"RELAY_FLOW_HOME":            root,
		"RELAY_FLOW_RUN_ID":          string(spec.RunID),
		"RELAY_FLOW_WORKFLOW":        spec.Workflow,
		"RELAY_FLOW_REPO":            spec.RepoName,
		"RELAY_FLOW_TICKET":          spec.Ticket,
		"RELAY_FLOW_NODE":            spec.Node,
		"RELAY_FLOW_NODE_TYPE":       string(spec.NodeType),
		"RELAY_FLOW_NUDGE_PROMPT":    spec.NudgePrompt,
		"RELAY_FLOW_NEXT_STEPS_JSON": string(nextSteps),
	}
	args := []string{"--name", spec.Title}
	if rolePath != "" {
		args = append(args, "--append-system-prompt", rolePath)
	}
	if spec.ResumeID != "" {
		args = append(args, "--session-id", spec.ResumeID)
	}
	args = append(args, spec.Prompt)
	return runner.Command{
		Executable: "pi",
		Args:       args,
		Env:        env,
	}, nil
}

func relayFlowHome() (string, error) {
	if root := os.Getenv("RELAY_FLOW_HOME"); root != "" {
		return root, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("pi: resolve relay-flow home: %w", err)
	}
	return filepath.Join(home, ".relay-flow"), nil
}

func validateTemplate(tmpl string) error {
	for _, match := range promptVarPattern.FindAllStringSubmatch(tmpl, -1) {
		if !knownPromptVars[match[1]] {
			return fmt.Errorf("unknown template variable {{%s}}", match[1])
		}
	}
	return nil
}

func renderTemplate(tmpl string, data harness.PromptData) string {
	values := map[string]string{
		"taskSystem":      data.TaskSystem,
		"ticket":          data.Ticket,
		"workflow":        data.Workflow,
		"repo":            data.Repo,
		"node":            data.Node,
		"nodeType":        string(data.NodeType),
		"agent":           data.Agent,
		"nodeDescription": data.NodeDescription,
		"nextSteps":       data.NextSteps,
		"mailbox":         data.Mailbox,
	}
	return promptVarPattern.ReplaceAllStringFunc(tmpl, func(match string) string {
		return values[promptVarPattern.FindStringSubmatch(match)[1]]
	})
}

func appendPrompt(prompt, extra string) string {
	if extra == "" {
		return prompt
	}
	if prompt == "" {
		return extra
	}
	return prompt + "\n\n" + extra
}
