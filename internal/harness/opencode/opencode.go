// Package opencode is the built-in launch-time Harness adapter for the
// OpenCode agent runtime. It validates agents, finds prior sessions by
// their stable <ticket>:<node> title, and builds the structured command
// the runner executes. The OpenCode runtime plugin (TypeScript, under
// plugin/) owns message parsing, title pinning, nudge-on-invalid for
// agent nodes, silence for HITL, and exact-report JSON retry with the
// shared backoff constants mirrored in TypeScript.
package opencode

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/rajpopat27/relay-flow/internal/config"
	"github.com/rajpopat27/relay-flow/internal/harness"
	"github.com/rajpopat27/relay-flow/internal/runner"
	"github.com/rajpopat27/relay-flow/internal/workflow"
)

const (
	defaultInitialPrompt = `Task system: {{taskSystem}}
Use the {{taskSystem}} tools to read the parent ticket {{ticket}}.

Your mailbox is {{mailbox}}. Read its description and comments for node instructions and feedback.`
	defaultFeedbackPrompt = `New feedback was added to the comments section of your mailbox subtask {{mailbox}}. Read it.`
	defaultHITLPrompt     = `Return the complete report directly. Relay-flow will show a native TUI approval dialog after the report is valid. Do not use OpenCode's Question tool for relay-flow approval.`
)

var promptVarPattern = regexp.MustCompile(`\{\{([^{}]*)\}\}`)

var knownPromptVars = map[string]bool{
	"taskSystem": true, "ticket": true, "workflow": true, "repo": true,
	"node": true, "nodeType": true, "agent": true, "nodeDescription": true,
	"nextSteps": true, "mailbox": true,
}

// Config is the adapter-owned root harnessConfig.
type Config struct {
	Initial  string `yaml:"initial"`
	Feedback string `yaml:"feedback"`
	HITL     string `yaml:"hitl"`
}

// DefaultConfig is written by relay-flow init and also fills omitted values
// when older or hand-written config is loaded.
func DefaultConfig() config.RawValues {
	return config.RawValues{
		"initial":  defaultInitialPrompt,
		"feedback": defaultFeedbackPrompt,
		"hitl":     defaultHITLPrompt,
	}
}

func init() {
	harness.Register("opencode", harness.Factory{
		DefaultConfig: DefaultConfig,
		New: func(raw config.RawValues) (harness.Harness, error) {
			var cfg Config
			if err := config.DecodeStrict(raw, &cfg); err != nil {
				return nil, fmt.Errorf("opencode harnessConfig: %w", err)
			}
			for name, tmpl := range map[string]string{
				"initial":  cfg.Initial,
				"feedback": cfg.Feedback,
				"hitl":     cfg.HITL,
			} {
				if err := validateTemplate(tmpl); err != nil {
					return nil, fmt.Errorf("opencode harnessConfig templates.%s: %w", name, err)
				}
			}
			return New(cfg), nil
		},
	})
}

// Harness implements harness.Harness for OpenCode. It is safe for
// concurrent use; the seams below are only swapped by tests before use.
type Harness struct {
	templates Config
	// listAgents is the test seam; nil → real `opencode agent list`.
	listAgents func(ctx context.Context) ([]string, error)
}

// New returns the production Harness.
func New(cfg ...Config) *Harness {
	if len(cfg) > 0 {
		return &Harness{templates: cfg[0]}
	}
	return &Harness{templates: Config{
		Initial: defaultInitialPrompt, Feedback: defaultFeedbackPrompt, HITL: defaultHITLPrompt,
	}}
}

// SetupRepo ensures the relay-flow runtime plugin is configured for OpenCode
// in the registered repository.
func (h *Harness) SetupRepo(_ context.Context, repoPath string) error {
	return setupRepo(repoPath)
}

// ValidateAgent reports whether name is a known OpenCode agent for the
// repo, per `opencode agent list` (agent names are the unindented first
// tokens).
func (h *Harness) ValidateAgent(ctx context.Context, _ string, agent string) error {
	if agent == "" {
		return fmt.Errorf("opencode: agent name is empty")
	}
	list := h.listAgents
	if list == nil {
		list = listAgents
	}
	names, err := list(ctx)
	if err != nil {
		return err
	}
	for _, n := range names {
		if n == agent {
			return nil
		}
	}
	return fmt.Errorf("opencode: unknown agent %q", agent)
}

// FindSession is intentionally discovery-free. Normal execution resumes only
// the session ID persisted from an OpenCode event.
func (h *Harness) FindSession(context.Context, string, string) (harness.Session, bool, error) {
	return harness.Session{}, false, nil
}

// RenderPrompt renders the selected session prompt, appends the harness-owned
// HITL/TUI instructions for HITL nodes, then renders and appends the node's
// nudge template.
func (h *Harness) RenderPrompt(kind harness.PromptKind, data harness.PromptData, nudgeTemplate string) (string, error) {
	var tmpl string
	switch kind {
	case harness.PromptInitial:
		tmpl = h.templates.Initial
	case harness.PromptFeedback:
		tmpl = h.templates.Feedback
	default:
		return "", fmt.Errorf("opencode: unknown prompt kind %q", kind)
	}
	prompt := renderTemplate(tmpl, data)
	if data.NodeType == workflow.NodeHITL {
		prompt = appendPrompt(prompt, renderTemplate(h.templates.HITL, data))
	}
	return appendPrompt(prompt, renderTemplate(nudgeTemplate, data)), nil
}

// BuildCommand returns the structured opencode invocation with the
// required RELAY_FLOW_* env. spec.ResumeID non-empty resumes the prior
// session (`opencode --session <id>`); empty is a fresh launch. The
// prompt is delivered as the first message in both cases.
//
// 9.5 external-call logging: this is the harness launch boundary — the
// runner executes the returned command. One debug line carries agent +
// session id + resume/fresh; one info line carries only the outcome.
func (h *Harness) BuildCommand(spec harness.LaunchSpec) (runner.Command, error) {
	mode := "fresh"
	if spec.ResumeID != "" {
		mode = "resume"
	}
	slog.Debug("harness call",
		"op", "launch", "agent", spec.Agent, "session", spec.ResumeID, "mode", mode,
		"ticket", spec.Ticket, "runID", string(spec.RunID),
		"node", spec.Node, "nodeVisitID", string(spec.NodeVisitID))
	if spec.Agent == "" {
		err := fmt.Errorf("opencode: LaunchSpec.Agent is empty")
		slog.Info("harness outcome", "op", "launch", "agent", spec.Agent, "mode", mode, "result", "error", "error", err)
		return runner.Command{}, err
	}
	nextSteps, err := json.Marshal(spec.NextSteps)
	if err != nil {
		slog.Info("harness outcome", "op", "launch", "agent", spec.Agent, "mode", mode, "result", "error", "error", err)
		return runner.Command{}, fmt.Errorf("opencode: marshal next steps: %w", err)
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
	args := []string{}
	if spec.ResumeID != "" {
		args = append(args, "--session", spec.ResumeID)
	}
	args = append(args, "--agent", spec.Agent, "--prompt", spec.Prompt)
	slog.Info("harness outcome",
		"op", "launch", "agent", spec.Agent, "session", spec.ResumeID, "mode", mode,
		"ticket", spec.Ticket, "runID", string(spec.RunID),
		"node", spec.Node, "nodeVisitID", string(spec.NodeVisitID),
		"result", "ok")
	return runner.Command{
		Executable: "opencode",
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
		return "", fmt.Errorf("opencode: resolve relay-flow home: %w", err)
	}
	return filepath.Join(home, ".relay-flow"), nil
}

// listAgents runs `opencode agent list` and returns the agent names
// (the unindented lines' first tokens).
func listAgents(ctx context.Context) ([]string, error) {
	out, err := exec.CommandContext(ctx, "opencode", "agent", "list").Output()
	if err != nil {
		return nil, fmt.Errorf("opencode agent list: %w", err)
	}
	var names []string
	for _, line := range strings.Split(string(out), "\n") {
		if line == "" || line[0] == ' ' || line[0] == '[' || line[0] == '{' {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) > 0 {
			names = append(names, fields[0])
		}
	}
	return names, nil
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
