package goworkflows

import (
	"context"
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
	Repos      *repo.Registry
	Runner     runner.Runner
	Harness    harness.Harness
	TaskSystem string
	Runs       *RunProjection
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
	for i := range specs {
		data := specs[i].TextData
		data.RunID = string(w.RunID)
		data.Repo = w.Repo
		custom, err := sys.RenderText(task.TextMailboxDescription, data)
		if err != nil {
			return nil, fmt.Errorf("render mailbox %q description: %w", specs[i].Node, err)
		}
		specs[i].Description = appendText(custom, specs[i].Description)
	}
	return sys.EnsureMailboxes(ctx, w.Parent, w.Workflow, specs)
}

// PrepareRestart reopens mailbox state through the optional task-system
// capability, then closes any surviving run-owned terminals while preserving
// the ticket worktree. Both operations are idempotent/retryable and remain
// behind their respective task and runner interfaces.
func (a *Activities) PrepareRestart(ctx context.Context, w run.Work, repoPath string, mailboxes []task.Mailbox) error {
	sys, err := a.taskSystem(w.Repo)
	if err != nil {
		return err
	}
	if preparer, ok := sys.(task.RestartPreparer); ok {
		if err := preparer.PrepareRestart(ctx, w.Parent, mailboxes); err != nil {
			return err
		}
	}
	spec := a.runSpec(w)
	spec.RepoPath = repoPath
	return a.Runner.CloseTerminals(ctx, spec)
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

func (a *Activities) SetEnvironmentStatus(ctx context.Context, w run.Work, repoPath, status string) error {
	spec := a.runSpec(w)
	spec.RepoPath = repoPath
	env, err := a.Runner.EnsureEnvironment(ctx, spec)
	if err != nil {
		return err
	}
	return a.Runner.SetEnvironmentStatus(ctx, env, status)
}

func (a *Activities) LoadNodeRuntime(ctx context.Context, id run.ID, node string) (NodeRuntime, error) {
	return a.Runs.loadNodeRuntime(ctx, id, node)
}

// EnsureNodeRuntime uses only persisted terminal/session IDs on the normal
// path. A live terminal is rebound to the new visit; otherwise EnsureTerminal
// creates a replacement and its direct ID is persisted immediately.
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
	rs := a.runSpec(nw.Work)
	rs.RepoPath = repoPath
	env, err := a.Runner.EnsureEnvironment(ctx, rs)
	if err != nil {
		return err
	}
	status := runner.WorkspaceStatusInProgress
	if spec.NodeType == workflow.NodeHITL {
		status = runner.WorkspaceStatusInReview
	}
	if err := a.Runner.SetEnvironmentStatus(ctx, env, status); err != nil {
		return err
	}
	// IDs come from the guarded current row; the activity input's prior visit
	// is used only to decide whether a live process needs rebinding.
	rt.TerminalID = currentRuntime.TerminalID
	rt.SessionID = currentRuntime.SessionID
	spec.ResumeID = rt.SessionID
	stored := runner.Terminal{ID: rt.TerminalID, Title: spec.Title}
	terminal, live, err := a.Runner.FindTerminal(ctx, stored)
	if err != nil {
		return err
	}
	if live {
		if err := a.Runs.replaceNodeRuntime(ctx, nw.RunID, nw.Node, nw.NodeVisitID,
			terminal.ID, rt.SessionID, rt.SessionID); err != nil {
			return err
		}
		if !revisit {
			// Same-visit retry/restart is silent: do not render, build, or send.
			return nil
		}
		prompt, err := a.Harness.RenderPrompt(harness.PromptFeedback, spec.PromptData, spec.NudgePrompt)
		if err != nil {
			return err
		}
		if err := a.Runner.SendTerminal(ctx, terminal, prompt); err == nil {
			return nil
		}
		// Direct use failed. Close the known live terminal before replacing it
		// so a second agent process cannot be left running.
		if err := a.Runner.CloseTerminal(ctx, terminal); err != nil {
			return err
		}
	}

	// An initial or replacement terminal resumes the stored session and
	// receives the rendered initial prompt. Same-visit replacements omit the
	// node nudge; a new visit includes it.
	nudge := ""
	if rt.NodeVisitID == "" || revisit {
		nudge = spec.NudgePrompt
	}
	spec.Prompt, err = a.Harness.RenderPrompt(harness.PromptInitial, spec.PromptData, nudge)
	if err != nil {
		return err
	}
	cmd, err := a.Harness.BuildCommand(spec)
	if err != nil {
		return err
	}
	replacement, err := a.Runner.EnsureTerminal(ctx, env, stored, spec.Title, cmd)
	if err != nil {
		return err
	}
	// Persist a newly created/replacement handle before any later external
	// effect. Runtime session registration may update SessionID independently.
	if err := a.Runs.replaceNodeRuntime(ctx, nw.RunID, nw.Node, nw.NodeVisitID,
		replacement.ID, rt.SessionID, rt.SessionID); err != nil {
		if replacement.ID != rt.TerminalID {
			_ = a.Runner.CloseTerminal(ctx, replacement)
		}
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
	body := cw.Body
	if cw.TextKind != "" {
		body, err = sys.RenderText(cw.TextKind, cw.TextData)
		if err != nil {
			return fmt.Errorf("render %s: %w", cw.TextKind, err)
		}
	}
	if err := sys.Comment(ctx, cw.Item, body, cw.Marker); err != nil {
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

func (a *Activities) ProjectionRecordProcessedReport(ctx context.Context, id run.ID, visit run.NodeVisitID, reportID string) error {
	return a.Runs.recordProcessedReport(ctx, id, visit, reportID)
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
	b.WriteString(`Required report format:

STATUS: success | failure
NEXT STEP: <one valid node name>

SUMMARY:
COMPLETED:
COMMITS:
NOT COMPLETED:
ISSUES DISCOVERED:
VERIFICATION:
NOTES:

FEEDBACK:
REASON FOR NEXT STEP:
REQUIRED ACTIONS:
RELEVANT CONTEXT:
EXPECTED RESULT:

Every field is required; use None for an intentionally empty section. COMMITS must contain the relevant commit IDs or None.

Node names identify workflow stages; they are not task-system statuses. STATUS describes whether the work at this node succeeded or failed, not the status of the parent or mailbox. NEXT STEP must name exactly one target listed below for that STATUS. Submit one report only: its SUMMARY is written to this current mailbox, while its FEEDBACK is written only to the selected next node's mailbox. For review nodes, put requested changes in FEEDBACK and select the node responsible for acting on them. Relay-flow and the task system own parent and mailbox status changes. When NEXT STEP is end, every FEEDBACK field must be None.`)
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
	successRoutes := routesText(n.OnSuccess)
	failureRoutes := routesText(n.OnFailure)
	return task.MailboxSpec{
		Node:        name,
		Title:       ticketKey + ":" + name,
		Description: b.String(),
		TaskConfig:  n.TaskConfig,
		TextData: task.TextData{
			Ticket: ticketKey, Workflow: wf.Name, Node: name, NodeType: string(n.Type),
			Agent: n.Agent, NodeDescription: n.Description,
			NextSteps:     nextStepsText(append(append([]workflow.Route{}, n.OnSuccess...), n.OnFailure...)),
			SuccessRoutes: successRoutes, FailureRoutes: failureRoutes,
			Mailbox: ticketKey + ":" + name,
		},
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

// RenderMailboxSpecs asks the selected task system to render customizable
// mailbox text, then appends the fixed report contract and legal routes.
// It is shared by normal execution and explicit database-loss recovery.
func RenderMailboxSpecs(sys task.System, w run.Work, wf *workflow.Workflow) ([]task.MailboxSpec, error) {
	specs := MailboxSpecs(wf, w.Parent.Key)
	for i := range specs {
		data := specs[i].TextData
		data.RunID = string(w.RunID)
		data.Repo = w.Repo
		custom, err := sys.RenderText(task.TextMailboxDescription, data)
		if err != nil {
			return nil, fmt.Errorf("render mailbox %q description: %w", specs[i].Node, err)
		}
		specs[i].Description = appendText(custom, specs[i].Description)
	}
	return specs, nil
}

func routesText(routes []workflow.Route) string {
	var b strings.Builder
	for i, route := range routes {
		if i > 0 {
			b.WriteString("\n")
		}
		b.WriteString(route.Target)
		if route.When != "" {
			b.WriteString(" — when: " + route.When)
		}
	}
	return b.String()
}

func appendText(first, second string) string {
	if first == "" {
		return second
	}
	if second == "" {
		return first
	}
	return first + "\n\n" + second
}

// mergeTaskConfig overlays node task config onto workflow task config using
// the shared deterministic merge (maps recursively, scalar/list replace).
func mergeTaskConfig(wfCfg, nodeCfg map[string]any) map[string]any {
	if len(wfCfg) == 0 && len(nodeCfg) == 0 {
		return nil
	}
	return config.Merge(wfCfg, nodeCfg)
}
