package goworkflows

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/rajpopat27/relay-flow/internal/config"
	"github.com/rajpopat27/relay-flow/internal/harness"
	"github.com/rajpopat27/relay-flow/internal/repo"
	"github.com/rajpopat27/relay-flow/internal/run"
	"github.com/rajpopat27/relay-flow/internal/runner"
	"github.com/rajpopat27/relay-flow/internal/task"
	"github.com/rajpopat27/relay-flow/internal/workflow"
)

// Activities holds the replaceable dependencies shared by every durable
// activity. One Activities value is registered with the activity worker.
type Activities struct {
	Repos   *repo.Registry
	Runner  runner.Runner
	Harness harness.Harness
	Runs    *RunProjection
}

func (a *Activities) taskSystem(repoName string) (task.System, error) {
	rp, ok := a.Repos.Get(repoName)
	if !ok {
		return nil, fmt.Errorf("repo %q is not registered", repoName)
	}
	return rp.TaskSystem, nil
}

func (a *Activities) runSpec(w run.Work) runner.RunSpec {
	return runner.RunSpec{
		RunID:     w.RunID,
		RepoName:  w.Repo,
		RepoPath:  "",
		TicketKey: w.Parent.Key,
	}
}

// EnsureMailboxes ensures one mailbox per work node and returns the
// node-to-mailbox map.
func (a *Activities) EnsureMailboxes(ctx context.Context, w run.Work, specs []task.MailboxSpec) (map[string]task.Mailbox, error) {
	sys, err := a.taskSystem(w.Repo)
	if err != nil {
		return nil, err
	}
	return sys.EnsureMailboxes(ctx, w.Parent, w.Workflow, specs)
}

// ValidateAgents validates every referenced agent on the repo.
func (a *Activities) ValidateAgents(ctx context.Context, repoPath string, agents []string) error {
	for _, agent := range agents {
		if err := a.Harness.ValidateAgent(ctx, repoPath, agent); err != nil {
			return fmt.Errorf("validate agent %q: %w", agent, err)
		}
	}
	return nil
}

// ApplyTaskConfig applies adapter-owned task config to the parent and
// optional mailbox target.
func (a *Activities) ApplyTaskConfig(ctx context.Context, w run.Work, node string, mailbox *task.Mailbox, cfg map[string]any) error {
	sys, err := a.taskSystem(w.Repo)
	if err != nil {
		return err
	}
	// Adapters with lifecycle-dependent taskConfig defaults (e.g. Jira
	// transitionTo) expose them via task.LifecycleDefaults; merge them as
	// the lowest layer under the effective node config so explicit values
	// win. Core never learns adapter vocabulary.
	if d, ok := sys.(task.LifecycleDefaults); ok {
		var defaults config.RawValues
		switch {
		case node == "start":
			defaults = d.StartDefaults()
		case node == "end":
			defaults = d.EndDefaults()
		default:
			defaults = d.WorkDefaults()
		}
		cfg = map[string]any(config.Merge(defaults, cfg))
	}
	return sys.ApplyTaskConfig(ctx, task.Target{Parent: w.Parent, Mailbox: mailbox}, cfg)
}

// EnsureEnvironment ensures the ticket-scoped runner environment.
func (a *Activities) EnsureEnvironment(ctx context.Context, w run.Work, repoPath string) (runner.Environment, error) {
	spec := a.runSpec(w)
	spec.RepoPath = repoPath
	return a.Runner.EnsureEnvironment(ctx, spec)
}

// CloseTerminalByTitle finds the live terminal by stable title and closes it
// (checkpoint-close before the next EnsureTerminal).
func (a *Activities) CloseTerminalByTitle(ctx context.Context, w run.Work, repoPath, title string) error {
	spec := a.runSpec(w)
	spec.RepoPath = repoPath
	env, err := a.Runner.EnsureEnvironment(ctx, spec)
	if err != nil {
		return err
	}
	term, ok, err := a.Runner.FindTerminal(ctx, env, title)
	if err != nil {
		return err
	}
	if !ok {
		return nil
	}
	return a.Runner.CloseTerminal(ctx, term)
}

// sessionLookup carries the harness session discovery result across the
// durable boundary.
type sessionLookup struct {
	Found bool
	ID    string
}

// FindNodeSession finds the prior harness session by stable title. A live
// usable session is reused; relaunch happens only when absent/unusable.
func (a *Activities) FindNodeSession(ctx context.Context, w run.Work, repoPath, title string) (sessionLookup, error) {
	s, ok, err := a.Harness.FindSession(ctx, repoPath, title)
	if err != nil {
		return sessionLookup{}, err
	}
	return sessionLookup{Found: ok, ID: s.ID}, nil
}

// EnsureNodeTerminal builds the launch command and ensures the node
// terminal. It reuses a live usable session for the stable title; relaunch
// only happens when the session is absent/unusable (the runner owns
// find-before-create on the terminal).
func (a *Activities) EnsureNodeTerminal(ctx context.Context, nw run.NodeWork, repoPath string, spec harness.LaunchSpec) error {
	cmd, err := a.Harness.BuildCommand(spec)
	if err != nil {
		return err
	}
	rs := a.runSpec(nw.Work)
	rs.RepoPath = repoPath
	env, err := a.Runner.EnsureEnvironment(ctx, rs)
	if err != nil {
		return err
	}
	title := nw.Parent.Key + ":" + nw.Node
	_, err = a.Runner.EnsureTerminal(ctx, env, title, cmd)
	return err
}

// CloseTerminals closes run-owned agent terminals, preserving the
// environment/workspace.
func (a *Activities) CloseTerminals(ctx context.Context, w run.Work, repoPath string) error {
	spec := a.runSpec(w)
	spec.RepoPath = repoPath
	return a.Runner.CloseTerminals(ctx, spec)
}

// CleanupRun removes all runner-owned run resources at end.
func (a *Activities) CleanupRun(ctx context.Context, w run.Work, repoPath string) error {
	spec := a.runSpec(w)
	spec.RepoPath = repoPath
	return a.Runner.CleanupRun(ctx, spec)
}

// Comment writes a marked comment to the target on the run's repo.
func (a *Activities) Comment(ctx context.Context, repoName string, cw run.CommentWork) error {
	sys, err := a.taskSystem(repoName)
	if err != nil {
		return err
	}
	return sys.Comment(ctx, cw.Item, cw.Body, cw.Marker)
}

// CompleteMailbox marks the current node mailbox complete.
func (a *Activities) CompleteMailbox(ctx context.Context, w run.Work, mailbox task.Mailbox) error {
	sys, err := a.taskSystem(w.Repo)
	if err != nil {
		return err
	}
	return sys.CompleteMailbox(ctx, mailbox)
}

// Projection activities: idempotent read-model updates.

func (a *Activities) ProjectionUpdateNode(ctx context.Context, id run.ID, state run.State, node string, visit run.NodeVisitID) error {
	return a.Runs.updateNode(ctx, id, state, node, visit)
}

func (a *Activities) ProjectionUpdateState(ctx context.Context, id run.ID, state run.State, lastErr string, finished *time.Time) error {
	return a.Runs.updateState(ctx, id, state, lastErr, finished)
}

// MailboxSpecForNode builds the mailbox description for a work node. The
// description defines the node work: node identity, parent, type, agent,
// description, and every legal route with its when explanation.
func MailboxSpecForNode(wf *workflow.Workflow, ticketKey, name string, n workflow.Node) task.MailboxSpec {
	var b strings.Builder
	fmt.Fprintf(&b, "Node %s of workflow %s for %s.\n\n", name, wf.Name, ticketKey)
	fmt.Fprintf(&b, "Type: %s\nAgent: %s\n\n", n.Type, n.Agent)
	b.WriteString(n.Description)
	writeRoutes := func(label string, routes []workflow.Route) {
		if len(routes) == 0 {
			return
		}
		fmt.Fprintf(&b, "\n\n%s:", label)
		for _, r := range routes {
			if r.When != "" {
				fmt.Fprintf(&b, "\n- %s — when: %s", r.Target, r.When)
			} else {
				fmt.Fprintf(&b, "\n- %s", r.Target)
			}
		}
	}
	writeRoutes("On success", n.OnSuccess)
	writeRoutes("On failure", n.OnFailure)
	return task.MailboxSpec{
		Node:        name,
		Title:       ticketKey + ":" + name,
		Description: b.String(),
		TaskConfig:  n.TaskConfig,
	}
}

// MailboxSpecs returns one spec per work node, sorted for determinism.
func MailboxSpecs(wf *workflow.Workflow, ticketKey string) []task.MailboxSpec {
	names := make([]string, 0, len(wf.Nodes))
	for name, n := range wf.Nodes {
		if n.Type == workflow.NodeAgent || n.Type == workflow.NodeHITL {
			names = append(names, name)
		}
	}
	sort.Strings(names)
	out := make([]task.MailboxSpec, 0, len(names))
	for _, name := range names {
		out = append(out, MailboxSpecForNode(wf, ticketKey, name, wf.Nodes[name]))
	}
	return out
}

// BuildLaunchSpecPrompt builds the node prompt: node description plus the
// complete report contract plus the valid next steps with their when
// explanations.
func BuildLaunchSpecPrompt(wf *workflow.Workflow, node string, n workflow.Node) string {
	var b strings.Builder
	b.WriteString(n.Description)
	b.WriteString(`

When your work is complete, reply with this exact report contract:

STATUS: success | failure
NEXT STEP: <one valid node name>

SUMMARY:
COMPLETED:
NOT COMPLETED:
ISSUES DISCOVERED:
VERIFICATION:
NOTES:

FEEDBACK:
REASON FOR NEXT STEP:
REQUIRED ACTIONS:
RELEVANT CONTEXT:
EXPECTED RESULT:

Every field is required; use None for an intentionally empty section. NEXT STEP must name exactly one target listed below for your status. When NEXT STEP is end, every FEEDBACK field must be None.
`)
	writeRoutes := func(label string, routes []workflow.Route) {
		fmt.Fprintf(&b, "\nValid next steps on %s:", label)
		for _, r := range routes {
			if r.When != "" {
				fmt.Fprintf(&b, "\n- %s — when: %s", r.Target, r.When)
			} else {
				fmt.Fprintf(&b, "\n- %s", r.Target)
			}
		}
		b.WriteString("\n")
	}
	writeRoutes("success", n.OnSuccess)
	writeRoutes("failure", n.OnFailure)
	return b.String()
}

// mergeTaskConfig overlays node task config onto workflow task config using
// the shared deterministic merge (maps recursively, scalar/list replace).
func mergeTaskConfig(wfCfg, nodeCfg map[string]any) map[string]any {
	if len(wfCfg) == 0 && len(nodeCfg) == 0 {
		return nil
	}
	return config.Merge(wfCfg, nodeCfg)
}
