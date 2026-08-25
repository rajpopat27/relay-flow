package goworkflows

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
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

func (a *Activities) LoadNodeRuntime(ctx context.Context, id run.ID, node string) (NodeRuntime, error) {
	return a.Runs.loadNodeRuntime(ctx, id, node)
}

// EnsureNodeRuntime uses only persisted terminal/session IDs on the normal
// path. A live terminal is rebound to the new visit; otherwise a fresh
// terminal is created and its direct IDs atomically replace the old binding.
func (a *Activities) EnsureNodeRuntime(ctx context.Context, nw run.NodeWork, repoPath string, spec harness.LaunchSpec, rt NodeRuntime) error {
	a.Runs.runtimeMu.Lock()
	defer a.Runs.runtimeMu.Unlock()
	slog.Info("node entered",
		"ticket", nw.Parent.Key, "runID", string(nw.RunID),
		"repo", nw.Repo, "workflow", nw.Workflow,
		"node", nw.Node, "nodeVisitID", string(nw.NodeVisitID),
		"nodeType", string(spec.NodeType), "agent", spec.Agent)
	revisit := rt.NodeVisitID != "" && rt.NodeVisitID != spec.NodeVisitID
	current, err := a.Runs.nodeRuntimeVisitIsCurrent(ctx, nw.RunID, nw.Node, nw.NodeVisitID)
	if err != nil {
		return err
	}
	if !current {
		return fmt.Errorf("node runtime %s/%s visit %s is stale", nw.RunID, nw.Node, nw.NodeVisitID)
	}
	currentRuntime, err := a.Runs.loadNodeRuntime(ctx, nw.RunID, nw.Node)
	if err != nil {
		return err
	}
	// IDs come from the guarded current row; the activity input's prior visit
	// is used only to decide whether a live process needs rebinding.
	rt.TerminalID = currentRuntime.TerminalID
	rt.SessionID = currentRuntime.SessionID
	freshSession := false
	if rt.TerminalID != "" {
		terminal := runner.Terminal{ID: rt.TerminalID, Title: spec.Title}
		_, ok, inspectErr := a.Runner.InspectTerminal(ctx, terminal)
		if inspectErr != nil {
			return inspectErr
		}
		if ok {
			// Same-visit retry/restart leaves the running turn untouched. A
			// revisit sends the new prompt only after the durable visit binding
			// above has rebound the plugin's session lookup.
			if !revisit {
				return a.Runs.replaceNodeRuntime(ctx, nw.RunID, nw.Node, nw.NodeVisitID,
					rt.TerminalID, rt.SessionID, rt.SessionID)
			}
			if err := a.Runner.SendTerminal(ctx, terminal,
				"RELAY_FLOW_REBIND:"+string(spec.NodeVisitID)+"\n"+spec.Prompt); err == nil {
				return a.Runs.replaceNodeRuntime(ctx, nw.RunID, nw.Node, nw.NodeVisitID,
					rt.TerminalID, rt.SessionID, rt.SessionID)
			}
			// Direct use failed. Close the known live terminal before replacing
			// it so a second agent process cannot be left running.
			if err := a.Runner.CloseTerminal(ctx, terminal); err != nil {
				return err
			}
			freshSession = true
		}
	}

	if rt.SessionID != "" && !freshSession {
		spec.ResumeID = rt.SessionID
	}
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
	terminal, err := a.Runner.CreateTerminal(ctx, env, spec.Title, cmd)
	sessionID := rt.SessionID
	if errors.Is(err, runner.ErrSessionUnavailable) && rt.SessionID != "" {
		spec.ResumeID = ""
		cmd, buildErr := a.Harness.BuildCommand(spec)
		if buildErr != nil {
			return buildErr
		}
		terminal, err = a.Runner.CreateTerminal(ctx, env, spec.Title, cmd)
		sessionID = ""
	}
	if freshSession {
		sessionID = ""
	}
	if err != nil {
		return err
	}
	if err := a.Runs.replaceNodeRuntime(ctx, nw.RunID, nw.Node, nw.NodeVisitID,
		terminal.ID, rt.SessionID, sessionID); err != nil {
		// The visit changed after terminal creation. Close the terminal whose
		// binding was rejected so stale work cannot leak or report.
		_ = a.Runner.CloseTerminal(ctx, terminal)
		return err
	}
	return nil
}

// CloseTerminals closes run-owned agent terminals, preserving the
// environment/workspace.
func (a *Activities) CloseTerminals(ctx context.Context, w run.Work, repoPath string) error {
	spec := a.runSpec(w)
	spec.RepoPath = repoPath
	return a.Runner.CloseTerminals(ctx, spec)
}

func (a *Activities) CheckpointNodeRuntime(ctx context.Context, nw run.NodeWork, repoPath string, policy run.RuntimePolicy) error {
	a.Runs.runtimeMu.Lock()
	defer a.Runs.runtimeMu.Unlock()
	rt, err := a.Runs.loadNodeRuntime(ctx, nw.RunID, nw.Node)
	if err != nil {
		return err
	}
	if !policy.KeepTerminalsAlive && rt.TerminalID != "" {
		if err := a.Runner.CloseTerminal(ctx, runner.Terminal{ID: rt.TerminalID, Title: nw.Parent.Key + ":" + nw.Node}); err != nil {
			return err
		}
	}
	return a.Runs.clearNodeRuntime(ctx, nw.RunID, nw.Node,
		!policy.KeepTerminalsAlive, !policy.KeepSessionsAlive)
}

func (a *Activities) FinalizeNodeRuntimes(ctx context.Context, w run.Work, repoPath string, policy run.RuntimePolicy) error {
	a.Runs.runtimeMu.Lock()
	defer a.Runs.runtimeMu.Unlock()
	runtimes, err := a.Runs.listNodeRuntimes(ctx, w.RunID)
	if err != nil {
		return err
	}
	if !policy.KeepTerminalsAlive {
		for _, rt := range runtimes {
			if rt.TerminalID == "" {
				continue
			}
			if err := a.Runner.CloseTerminal(ctx, runner.Terminal{ID: rt.TerminalID, Title: w.Parent.Key + ":" + rt.Node}); err != nil {
				return err
			}
		}
	}
	for _, rt := range runtimes {
		if err := a.Runs.clearNodeRuntime(ctx, w.RunID, rt.Node,
			!policy.KeepTerminalsAlive, !policy.KeepSessionsAlive); err != nil {
			return err
		}
	}
	return nil
}

// CleanupRun removes all runner-owned run resources at end.
func (a *Activities) CleanupRun(ctx context.Context, w run.Work, repoPath string) error {
	spec := a.runSpec(w)
	spec.RepoPath = repoPath
	return a.Runner.CleanupRun(ctx, spec)
}

// Comment writes a marked comment to the target on the run's repo.
//
// 9.3 transition logging: the interpreter uses markers of the form
// "<nodeVisitID>:summary" for the current node summary and
// "<nodeVisitID>:feedback" for the selected-next feedback, so this
// activity emits one info line per effect with the same ticket/runID/node
// attrs as the rest of the run. Cancellation markers and other comments
// still pass through silently (no transition effect).
func (a *Activities) Comment(ctx context.Context, repoName string, cw run.CommentWork) error {
	sys, err := a.taskSystem(repoName)
	if err != nil {
		return err
	}
	if err := sys.Comment(ctx, cw.Item, cw.Body, cw.Marker); err != nil {
		return err
	}
	// Log AFTER the write succeeds so the line is a true effect record.
	// marker is "<nodeVisitID>:summary" | "<nodeVisitID>:feedback" |
	// "<runID>:cancellation". Only the first two are transition effects.
	visit, tag := splitMarker(cw.Marker)
	if tag != "summary" && tag != "feedback" {
		return nil
	}
	attrs := []any{
		"ticket", cw.Item.Parent.Key, "repo", repoName,
		"runID", string(cw.RunID), "nodeVisitID", visit,
	}
	// Workflow attribution comes from the projection; a missing read
	// degrades to the always-known attrs above rather than failing the
	// activity after the comment already landed.
	if a.Runs != nil && a.Runs.DB != nil && cw.RunID != "" {
		if r, err := a.Runs.get(ctx, cw.RunID); err == nil {
			attrs = append(attrs, "workflow", r.Workflow)
		}
	}
	var msg string
	if tag == "summary" {
		msg = "summary written"
	} else {
		msg = "feedback written"
		if cw.Item.Mailbox != nil {
			attrs = append(attrs, "node", cw.Item.Mailbox.Node, "mailbox", cw.Item.Mailbox.Key)
		}
	}
	slog.Info(msg, attrs...)
	return nil
}

// splitMarker splits a "<prefix>:<tag>" marker; returns ("","") when there
// is no colon.
func splitMarker(m string) (string, string) {
	i := strings.LastIndex(m, ":")
	if i < 0 {
		return "", ""
	}
	return m[:i], m[i+1:]
}

// CompleteMailbox marks the current node mailbox complete.
//
// 9.3 transition logging: one info line per completed mailbox carrying the
// same ticket/runID/node attrs.
func (a *Activities) CompleteMailbox(ctx context.Context, w run.Work, mailbox task.Mailbox) error {
	sys, err := a.taskSystem(w.Repo)
	if err != nil {
		return err
	}
	if err := sys.CompleteMailbox(ctx, mailbox); err != nil {
		return err
	}
	slog.Info("mailbox completed",
		"ticket", w.Parent.Key, "runID", string(w.RunID),
		"repo", w.Repo, "workflow", w.Workflow,
		"node", mailbox.Node, "mailbox", mailbox.Key)
	return nil
}

// Projection activities: idempotent read-model updates.

func (a *Activities) ProjectionUpdateNode(ctx context.Context, id run.ID, state run.State, node string, visit run.NodeVisitID) error {
	return a.Runs.updateNode(ctx, id, state, node, visit)
}

func (a *Activities) ProjectionUpdateNodeRuntimeVisit(ctx context.Context, id run.ID, node string, visit run.NodeVisitID) error {
	return a.Runs.updateNodeRuntimeVisit(ctx, id, node, visit)
}

func (a *Activities) ProjectionUpdateState(ctx context.Context, id run.ID, state run.State, lastErr string, finished *time.Time) error {
	if err := a.Runs.updateState(ctx, id, state, lastErr, finished); err != nil {
		return err
	}
	// 9.3 run-lifecycle logging: one info line when a run reaches a
	// terminal state. The projection row carries ticket/repo/workflow so
	// the line is attributable without new plumbing through the workflow.
	if state != run.StateCompleted && state != run.StateCanceled {
		return nil
	}
	r, err := a.Runs.get(ctx, id)
	if err != nil {
		// Projection write succeeded; failure to re-read must not fail
		// the activity. Skip the log line rather than retry forever.
		return nil
	}
	attrs := []any{
		"ticket", r.Ticket.Key, "runID", string(id),
		"repo", r.Repo, "workflow", r.Workflow,
		"state", string(state),
	}
	if r.LastError != "" {
		attrs = append(attrs, "reason", r.LastError)
	}
	msg := "run completed"
	if state == run.StateCanceled {
		msg = "run canceled"
	}
	slog.Info(msg, attrs...)
	return nil
}

func (a *Activities) ProjectionUpdateRetry(ctx context.Context, id run.ID, status *run.RetryStatus) error {
	return a.Runs.updateRetry(ctx, id, status)
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
