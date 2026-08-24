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
	"os/exec"
	"strings"

	"github.com/rajpopat27/relay-flow/internal/config"
	"github.com/rajpopat27/relay-flow/internal/harness"
	"github.com/rajpopat27/relay-flow/internal/runner"
)

// Config is the adapter-owned root harnessConfig. OpenCode takes no
// configuration; any key is rejected.
type Config struct{}

func init() {
	harness.Register("opencode", func(raw config.RawValues) (harness.Harness, error) {
		var cfg Config
		if err := config.DecodeStrict(raw, &cfg); err != nil {
			return nil, fmt.Errorf("opencode harnessConfig: %w", err)
		}
		return New(), nil
	})
}

// Harness implements harness.Harness for OpenCode. It is safe for
// concurrent use; the seams below are only swapped by tests before use.
type Harness struct {
	// listSessions is the test seam; nil → real `opencode session list`.
	listSessions func(ctx context.Context) ([]sessionRow, error)
	// listAgents is the test seam; nil → real `opencode agent list`.
	listAgents func(ctx context.Context) ([]string, error)
}

// New returns the production Harness.
func New() *Harness { return &Harness{} }

// sessionRow is the subset of `opencode session list --format json` the
// harness consumes.
type sessionRow struct {
	ID        string `json:"id"`
	Title     string `json:"title"`
	Directory string `json:"directory"`
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

// FindSession returns the most recent live session with the exact title,
// scoped to the worktree directory (repoPath) so identically-titled
// sessions in another run's worktree never collide. ok=false means no
// anchor exists (fresh launch).
func (h *Harness) FindSession(ctx context.Context, repoPath, title string) (harness.Session, bool, error) {
	slog.Debug("harness call", "op", "find-session", "title", title, "repoPath", repoPath)
	list := h.listSessions
	if list == nil {
		list = listSessions
	}
	rows, err := list(ctx)
	if err != nil {
		slog.Info("harness outcome", "op", "find-session", "title", title, "result", "error", "error", err)
		return harness.Session{}, false, err
	}
	for _, r := range rows { // opencode session list is most-recent-first
		if r.Title == title && r.Directory == repoPath {
			slog.Info("harness outcome", "op", "find-session", "title", title, "result", "found", "session", r.ID)
			return harness.Session{ID: r.ID, Title: r.Title}, true, nil
		}
	}
	slog.Info("harness outcome", "op", "find-session", "title", title, "result", "absent")
	return harness.Session{}, false, nil
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
	env := map[string]string{
		"RELAY_FLOW_RUN_ID":          string(spec.RunID),
		"RELAY_FLOW_NODE_VISIT_ID":   string(spec.NodeVisitID),
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
	args = append(args, "--agent", spec.Agent, spec.Prompt)
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

// listSessions runs `opencode session list --format json`.
func listSessions(ctx context.Context) ([]sessionRow, error) {
	out, err := exec.CommandContext(ctx, "opencode", "session", "list", "--format", "json").Output()
	if err != nil {
		return nil, fmt.Errorf("opencode session list: %w", err)
	}
	var rows []sessionRow
	if err := json.Unmarshal(out, &rows); err != nil {
		return nil, fmt.Errorf("opencode session list: parse json: %w", err)
	}
	return rows, nil
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
