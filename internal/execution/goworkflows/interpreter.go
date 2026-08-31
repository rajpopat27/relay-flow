package goworkflows

import (
	"crypto/rand"
	"fmt"
	"log/slog"
	"reflect"
	"sort"
	"strings"
	"time"

	goworkflow "github.com/cschleiden/go-workflows/workflow"

	"github.com/rajpopat27/relay-flow/internal/harness"
	"github.com/rajpopat27/relay-flow/internal/identity"
	"github.com/rajpopat27/relay-flow/internal/retry"
	"github.com/rajpopat27/relay-flow/internal/run"
	"github.com/rajpopat27/relay-flow/internal/runner"
	"github.com/rajpopat27/relay-flow/internal/task"
	"github.com/rajpopat27/relay-flow/internal/workflow"
)

// Signal names. The workflow deduplicates report IDs across all node visits;
// the reconcile channel asks it to relaunch the current visit's terminal.
const (
	reportSignalName = "report"
	reconcileSignal  = "reconcile"
)

type reportSignal struct {
	ReportID    string
	Node        string
	NodeVisitID run.NodeVisitID
	Report      workflow.Report
}

// noNativeRetries keeps the engine-native activity retry count at one; the
// private typed retry loops below implement the shared backoff policy.
var noNativeRetries = goworkflow.ActivityOptions{
	RetryOptions: goworkflow.RetryOptions{MaxAttempts: 1},
}

// jitter draws a replay-safe random in [0,1) for backoff.
func jitter(ctx goworkflow.Context) (float64, error) {
	return goworkflow.SideEffect(ctx, func(goworkflow.Context) float64 {
		var b [1]byte
		if _, err := rand.Read(b[:]); err != nil {
			return 0
		}
		return float64(b[0]) / 256
	}).Get(ctx)
}

// TicketWorkflow is the one generic interpreter registered with the workflow
// worker. It consumes ONLY the workflow value snapshot carried in start; it
// never re-reads the workflow file or registry mid-run, so replay is
// deterministic. On cancellation it runs cancellation cleanup on a
// disconnected context.
func (a *Activities) TicketWorkflow(ctx goworkflow.Context, start run.Start) error {
	err := a.runGraph(ctx, start)
	if err != nil && ctx.Err() != nil {
		work := run.Work{
			RunID:              start.ID,
			Repo:               start.Repo,
			Workflow:           start.Workflow.Name,
			Parent:             start.Ticket,
			WorkflowTaskConfig: start.Workflow.TaskConfig,
			Runtime:            start.Runtime,
		}
		return a.cancelCleanup(ctx, work, start.RepoPath, "canceled")
	}
	return err
}

func (a *Activities) runGraph(ctx goworkflow.Context, start run.Start) error {
	wf := start.Workflow // value snapshot
	work := run.Work{
		RunID:              start.ID,
		Repo:               start.Repo,
		Workflow:           wf.Name,
		Parent:             start.Ticket,
		WorkflowTaskConfig: wf.TaskConfig,
		Runtime:            start.Runtime,
	}

	// Ensure every work-node mailbox (find existing, create only missing).
	specs := MailboxSpecs(&wf, start.Ticket.Key)
	mailboxes, err := retryLoop(ctx, start.ID, a, work, "",
		func(ctx2 goworkflow.Context) goworkflow.Future[map[string]task.Mailbox] {
			return goworkflow.ExecuteActivity[map[string]task.Mailbox](ctx2, noNativeRetries, a.EnsureMailboxes, work, specs)
		})
	if err != nil {
		return err
	}

	// Validate every referenced agent before the start edge.
	agentSet := map[string]bool{}
	for _, n := range wf.Nodes {
		if n.Agent != "" {
			agentSet[n.Agent] = true
		}
	}
	agentList := make([]string, 0, len(agentSet))
	for agent := range agentSet {
		agentList = append(agentList, agent)
	}
	sort.Strings(agentList)
	if _, err := retryLoop(ctx, start.ID, a, work, "",
		func(ctx2 goworkflow.Context) goworkflow.Future[struct{}] {
			return goworkflow.ExecuteActivity[struct{}](ctx2, noNativeRetries, a.ValidateAgents, start.RepoPath, agentList)
		}); err != nil {
		return err
	}

	// Process the reserved start taskConfig (parent target).
	startNode := wf.Nodes["start"]
	if _, err := retryLoop(ctx, start.ID, a, work, "start",
		func(ctx2 goworkflow.Context) goworkflow.Future[struct{}] {
			return goworkflow.ExecuteActivity[struct{}](ctx2, noNativeRetries, a.ApplyTaskConfig,
				work, "start", (*task.Mailbox)(nil), mergeTaskConfig(wf.TaskConfig, startNode.TaskConfig))
		}); err != nil {
		return err
	}

	// Ensure the runner environment before following the start edge.
	if _, err := retryLoop(ctx, start.ID, a, work, "",
		func(ctx2 goworkflow.Context) goworkflow.Future[runner.Environment] {
			return goworkflow.ExecuteActivity[runner.Environment](ctx2, noNativeRetries, a.EnsureEnvironment, work, start.RepoPath)
		}); err != nil {
		return err
	}

	target, err := wf.StartTarget()
	if err != nil {
		return err
	}

	current := target
	seenReportIDs := map[string]bool{}
	for current != "end" {
		node := wf.Nodes[current]

		// One fresh visit identity per node entry, durable across replay.
		visit, err := goworkflow.SideEffect(ctx, func(goworkflow.Context) identity.NodeVisitID {
			return identity.NewNodeVisitID()
		}).Get(ctx)
		if err != nil {
			return err
		}
		visitID := run.NodeVisitID(visit)
		runtime, err := retryLoop(ctx, start.ID, a, work, current,
			func(ctx2 goworkflow.Context) goworkflow.Future[NodeRuntime] {
				return goworkflow.ExecuteActivity[NodeRuntime](ctx2, noNativeRetries,
					a.LoadNodeRuntime, start.ID, current)
			})
		if err != nil {
			return err
		}

		// Publish the visit guard before launching OpenCode so its earliest
		// session event can register against this exact run/node/visit tuple.
		// Revisit preparation preserves reusable terminal/session IDs.
		if _, err := retryLoop(ctx, start.ID, a, work, current,
			func(ctx2 goworkflow.Context) goworkflow.Future[struct{}] {
				return goworkflow.ExecuteActivity[struct{}](ctx2, noNativeRetries,
					a.ProjectionUpdateNodeRuntimeVisit, start.ID, current, visitID)
			}); err != nil {
			return err
		}

		// Process the node in the task system.
		mb := mailboxes[current]
		nodeCfg := mergeTaskConfig(wf.TaskConfig, node.TaskConfig)
		if _, err := retryLoop(ctx, start.ID, a, work, current,
			func(ctx2 goworkflow.Context) goworkflow.Future[struct{}] {
				return goworkflow.ExecuteActivity[struct{}](ctx2, noNativeRetries, a.ApplyTaskConfig, work, current, &mb, nodeCfg)
			}); err != nil {
			return err
		}

		// Ensure the runner environment for this node entry (idempotent).
		if _, err := retryLoop(ctx, start.ID, a, work, current,
			func(ctx2 goworkflow.Context) goworkflow.Future[runner.Environment] {
				return goworkflow.ExecuteActivity[runner.Environment](ctx2, noNativeRetries, a.EnsureEnvironment, work, start.RepoPath)
			}); err != nil {
			return err
		}

		title := start.Ticket.Key + ":" + current

		// Build task-system-neutral prompt data from the workflow snapshot. The
		// selected harness owns rendering initial, feedback, and HITL text.
		nextSteps := append(append([]workflow.Route{}, node.OnSuccess...), node.OnFailure...)
		spec := harness.LaunchSpec{
			RunID:       start.ID,
			NodeVisitID: visitID,
			RepoName:    start.Repo,
			RepoPath:    start.RepoPath,
			Workflow:    wf.Name,
			Ticket:      start.Ticket.Key,
			Node:        current,
			NodeType:    node.Type,
			Agent:       node.Agent,
			Title:       title,
			NudgePrompt: node.NudgePrompt,
			PromptData: harness.PromptData{
				TaskSystem:      a.TaskSystem,
				Ticket:          start.Ticket.Key,
				Workflow:        wf.Name,
				Repo:            start.Repo,
				Node:            current,
				NodeType:        node.Type,
				Agent:           node.Agent,
				NodeDescription: node.Description,
				NextSteps:       nextStepsText(nextSteps),
				Mailbox:         mb.Key,
			},
			NextSteps: nextSteps,
		}
		if runtime.SessionID != "" {
			spec.ResumeID = runtime.SessionID
		}
		nodeWork := run.NodeWork{
			Work: work, Node: current, NodeVisitID: visitID,
			Mailbox: mb, NodeTaskConfig: nodeCfg,
		}
		if _, err := retryLoop(ctx, start.ID, a, work, current,
			func(ctx2 goworkflow.Context) goworkflow.Future[struct{}] {
				return goworkflow.ExecuteActivity[struct{}](ctx2, noNativeRetries,
					a.EnsureNodeRuntime, nodeWork, start.RepoPath, spec, runtime)
			}); err != nil {
			return err
		}

		// Publish the current node+visit only after the terminal exists, so
		// observers never see a visit whose terminal is not up yet.
		if _, err := retryLoop(ctx, start.ID, a, work, current,
			func(ctx2 goworkflow.Context) goworkflow.Future[struct{}] {
				return goworkflow.ExecuteActivity[struct{}](ctx2, noNativeRetries, a.ProjectionUpdateNode, start.ID, run.StateRunning, current, visitID)
			}); err != nil {
			return err
		}
		if _, err := retryLoop(ctx, start.ID, a, work, current,
			func(ctx2 goworkflow.Context) goworkflow.Future[struct{}] {
				return goworkflow.ExecuteActivity[struct{}](ctx2, noNativeRetries, a.ProjectionUpdateState, start.ID, run.StateWaiting, "", (*time.Time)(nil))
			}); err != nil {
			return err
		}

		// Wait for this visit's report; duplicate report IDs are ignored across
		// the entire run, including later revisits to this node.
		reportCh := goworkflow.NewSignalChannel[reportSignal](ctx, reportSignalName)
		reconcileCh := goworkflow.NewSignalChannel[struct{}](ctx, reconcileSignal)
		var report workflow.Report
		var reportID string
		gotReport := false
		for !gotReport && ctx.Err() == nil {
			goworkflow.Select(ctx,
				goworkflow.Receive(reportCh, func(_ goworkflow.Context, signal reportSignal, ok bool) {
					if seenReportIDs[signal.ReportID] {
						return
					}
					seenReportIDs[signal.ReportID] = true
					if signal.Node != current || signal.NodeVisitID != visitID {
						return
					}
					reportID = signal.ReportID
					report = signal.Report
					gotReport = true
				}),
				goworkflow.Receive(reconcileCh, func(ctx2 goworkflow.Context, _ struct{}, ok bool) {
					// Reconcile addresses only the persisted direct identity;
					// missing/unusable IDs are replaced without discovery.
					_, _ = retryLoop(ctx2, start.ID, a, work, current,
						func(ctx3 goworkflow.Context) goworkflow.Future[struct{}] {
							return goworkflow.ExecuteActivity[struct{}](ctx3, noNativeRetries,
								a.EnsureNodeRuntime, nodeWork, start.RepoPath, spec,
								NodeRuntime{NodeVisitID: visitID})
						})
				}),
				// Wake on cancellation; the loop condition then exits.
				goworkflow.Receive(ctx.Done(), func(goworkflow.Context, struct{}, bool) {}),
			)
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}

		// Validate against the current node; the server boundary already
		// validated, and durable-side validation keeps replay honest.
		if err := wf.ValidateReport(current, report); err != nil {
			return fmt.Errorf("workflow %q node %q: invalid accepted report: %w", wf.Name, current, err)
		}
		if _, err := retryLoop(ctx, start.ID, a, work, current,
			func(ctx2 goworkflow.Context) goworkflow.Future[struct{}] {
				return goworkflow.ExecuteActivity[struct{}](ctx2, noNativeRetries,
					a.ProjectionRecordProcessedReport, start.ID, visitID, reportID)
			}); err != nil {
			return err
		}

		// Ordered transition: summary -> feedback (selected next only) ->
		// complete current -> process next node.
		if _, err := retryLoop(ctx, start.ID, a, work, current,
			func(ctx2 goworkflow.Context) goworkflow.Future[struct{}] {
				summaryReport := renderSummaryReport(report)
				return goworkflow.ExecuteActivity[struct{}](ctx2, noNativeRetries, a.Comment, start.Repo, run.CommentWork{
					RunID: start.ID, Item: task.Target{Parent: work.Parent, Mailbox: &mb},
					TextKind: task.TextSummaryComment,
					TextData: task.TextData{RunID: string(start.ID), Ticket: work.Parent.Key,
						Workflow: wf.Name, Repo: start.Repo, Node: current, NodeType: string(node.Type),
						Agent: node.Agent, NodeDescription: node.Description, Mailbox: mb.Key,
						SourceNode: current, TargetNode: current, SummaryReport: summaryReport},
					Marker: string(visitID) + ":summary",
				})
			}); err != nil {
			return err
		}

		next := report.NextStep
		if next != "end" {
			nextMb := mailboxes[next]
			if _, err := retryLoop(ctx, start.ID, a, work, current,
				func(ctx2 goworkflow.Context) goworkflow.Future[struct{}] {
					feedbackReport := renderFeedbackReport(report)
					return goworkflow.ExecuteActivity[struct{}](ctx2, noNativeRetries, a.Comment, start.Repo, run.CommentWork{
						RunID: start.ID, Item: task.Target{Parent: work.Parent, Mailbox: &nextMb},
						TextKind: task.TextFeedbackComment,
						TextData: task.TextData{RunID: string(start.ID), Ticket: work.Parent.Key,
							Workflow: wf.Name, Repo: start.Repo, Node: next, NodeType: string(wf.Nodes[next].Type),
							Agent: wf.Nodes[next].Agent, NodeDescription: wf.Nodes[next].Description, Mailbox: nextMb.Key,
							SourceNode: current, TargetNode: next, SummaryReport: renderSummaryReport(report),
							FeedbackReport: feedbackReport},
						Marker: string(visitID) + ":feedback",
					})
				}); err != nil {
				return err
			}
		}

		// Complete the current mailbox. A conflict (a human moved it) marks
		// the run blocked and keeps retrying until external state is
		// compatible again; no blind overwrite ever runs.
		if _, err := retryLoop(ctx, start.ID, a, work, current,
			func(ctx2 goworkflow.Context) goworkflow.Future[struct{}] {
				return goworkflow.ExecuteActivity[struct{}](ctx2, noNativeRetries, a.CompleteMailbox, work, mb)
			}); err != nil {
			return err
		}
		if _, err := retryLoop(ctx, start.ID, a, work, current,
			func(ctx2 goworkflow.Context) goworkflow.Future[struct{}] {
				return goworkflow.ExecuteActivity[struct{}](ctx2, noNativeRetries,
					a.CheckpointNodeRuntime, nodeWork, start.RepoPath, work.Runtime)
			}); err != nil {
			return err
		}

		current = next
	}

	// end: apply end task config, then optional runner cleanup, then mark
	// the run completed.
	endNode := wf.Nodes["end"]
	if _, err := retryLoop(ctx, start.ID, a, work, "end",
		func(ctx2 goworkflow.Context) goworkflow.Future[struct{}] {
			return goworkflow.ExecuteActivity[struct{}](ctx2, noNativeRetries, a.ApplyTaskConfig,
				work, "end", (*task.Mailbox)(nil), mergeTaskConfig(wf.TaskConfig, endNode.TaskConfig))
		}); err != nil {
		return err
	}
	if _, err := retryLoop(ctx, start.ID, a, work, "end",
		func(ctx2 goworkflow.Context) goworkflow.Future[struct{}] {
			return goworkflow.ExecuteActivity[struct{}](ctx2, noNativeRetries,
				a.SetEnvironmentStatus, work, start.RepoPath, runner.WorkspaceStatusCompleted)
		}); err != nil {
		return err
	}
	finalPolicy := work.Runtime
	if wf.CleanupRunnerOnEnd {
		finalPolicy.KeepTerminalsAlive = false
	}
	if _, err := retryLoop(ctx, start.ID, a, work, "",
		func(ctx2 goworkflow.Context) goworkflow.Future[struct{}] {
			return goworkflow.ExecuteActivity[struct{}](ctx2, noNativeRetries,
				a.FinalizeNodeRuntimes, work, start.RepoPath, finalPolicy)
		}); err != nil {
		return err
	}
	if wf.CleanupRunnerOnEnd {
		if _, err := retryLoop(ctx, start.ID, a, work, "",
			func(ctx2 goworkflow.Context) goworkflow.Future[struct{}] {
				return goworkflow.ExecuteActivity[struct{}](ctx2, noNativeRetries, a.CleanupRun, work, start.RepoPath)
			}); err != nil {
			return err
		}
	}
	if _, err := retryLoop(ctx, start.ID, a, work, "",
		func(ctx2 goworkflow.Context) goworkflow.Future[struct{}] {
			now := goworkflow.Now(ctx).UTC()
			return goworkflow.ExecuteActivity[struct{}](ctx2, noNativeRetries, a.ProjectionUpdateState, start.ID, run.StateCompleted, "", &now)
		}); err != nil {
		return err
	}
	return nil
}

// retryLoop runs one scheduled activity under the shared policy: transient
// failures retry forever with exponential backoff and replay-safe jitter;
// conflict failures mark the run blocked and keep retrying on the capped
// schedule. Cancellation stops scheduling.
//
// 9.6: each scheduling failure emits ONE info line with the retry
// classification (transient|conflict) and the next retry delay. work
// carries the always-known ticket/repo/workflow context; node is the
// enclosing node when the activity runs inside a node body (empty for
// run-level activities like EnsureMailboxes / start/end ApplyTaskConfig).
// The log is wrapped in a replay-safe SideEffect so replays never re-emit
// it, and the error message is sanitized so info logs never embed argv
// payloads (Jira/Orca request bodies, commands, JQL, and prompt strings).
func retryLoop[T any](ctx goworkflow.Context, id run.ID, a *Activities, work run.Work, node string, schedule func(goworkflow.Context) goworkflow.Future[T]) (T, error) {
	var zero T
	attempt := 0
	blocked := false
	for {
		if err := ctx.Err(); err != nil {
			return zero, err
		}
		result, err := schedule(ctx).Get(ctx)
		if err == nil {
			if attempt > 0 {
				if _, clearErr := goworkflow.ExecuteActivity[struct{}](ctx, noNativeRetries, a.ProjectionUpdateRetry, id, (*run.RetryStatus)(nil)).Get(ctx); clearErr != nil {
					return zero, clearErr
				}
			}
			if blocked {
				// External state is compatible again: leave blocked.
				_, _ = scheduleState(ctx, a, id, run.StateWaiting, "")
			}
			return result, nil
		}
		if ctx.Err() != nil {
			return zero, ctx.Err()
		}
		f := classifyActivityError(err)
		if f.Kind == retry.Conflict {
			// Mark blocked, keep retrying on the capped schedule.
			_, _ = scheduleState(ctx, a, id, run.StateBlocked, f.Message)
			blocked = true
		}
		delay := retry.DefaultBackoffPolicy.Delay(attempt, mustJitter(ctx))
		logRetry(ctx, work, node, f, delay)
		nextRetry := goworkflow.Now(ctx).UTC().Add(delay)
		status := &run.RetryStatus{
			Attempt: attempt + 1, LastError: sanitizeRetryMessage(f.Message), NextRetryAt: nextRetry,
		}
		if _, projectionErr := goworkflow.ExecuteActivity[struct{}](ctx, noNativeRetries, a.ProjectionUpdateRetry, id, status).Get(ctx); projectionErr != nil {
			return zero, projectionErr
		}
		if _, err := goworkflow.ScheduleTimer(ctx, delay).Get(ctx); err != nil {
			return zero, err
		}
		attempt++
	}
}

// logRetry emits the 9.6 retry-classification info line, replay-safe.
// Attrs come from the always-known run.Work value carried by the caller,
// so ticket/repo/workflow are present even if the projection is briefly
// unreadable; node is included whenever the retry runs inside a node body.
func logRetry(ctx goworkflow.Context, work run.Work, node string, f retry.Failure, delay time.Duration) {
	kind := string(f.Kind)
	delayMs := delay.Milliseconds()
	msg := sanitizeRetryMessage(f.Message)
	attrs := []any{
		"ticket", work.Parent.Key,
		"runID", string(work.RunID),
		"repo", work.Repo,
		"workflow", work.Workflow,
		"kind", kind,
		"delayMs", delayMs,
		"error", msg,
	}
	if node != "" {
		attrs = append(attrs, "node", node)
	}
	_, _ = goworkflow.SideEffect(ctx, func(goworkflow.Context) struct{} {
		slog.Info("retry scheduled", attrs...)
		return struct{}{}
	}).Get(ctx)
}

// sanitizeRetryMessage strips each "[...]: " argv-listing span that adapter
// wrappers may embed argv/request context, so the info-level retry
// record never leaks --body/--command/JQL/prompt payloads. Keeps the
// surrounding wrap context and trailing exit status / stderr fragment that
// carry the failure reason. The original error returned to callers is
// unchanged; this sanitizes only the logged string.
func sanitizeRetryMessage(s string) string {
	for {
		i := strings.Index(s, "[")
		if i < 0 {
			return s
		}
		j := strings.Index(s[i:], "]: ")
		if j < 0 {
			return s
		}
		// Remove the "[...]: " span, keeping any preceding text (minus a
		// trailing space) and everything after.
		s = strings.TrimSuffix(s[:i], " ") + s[i+j+3:]
	}
}

// classifyActivityError classifies an activity error after its durable
// round-trip. The engine serializes error causes, so the concrete
// retry conflict wrapper does not survive; its stable type name does.
func classifyActivityError(err error) retry.Failure {
	if err == nil {
		return retry.Failure{}
	}
	if isConflictRoundTrip(err) {
		return retry.Failure{Kind: retry.Conflict, Message: err.Error()}
	}
	return retry.Classify(err)
}

// isConflictRoundTrip reports whether err (or any serialized cause) carries
// the conflict marker type name produced by retry.ConflictError.
func isConflictRoundTrip(err error) bool {
	for e := err; e != nil; {
		v := reflect.ValueOf(e)
		if v.Kind() == reflect.Ptr {
			v = v.Elem()
		}
		if v.IsValid() && v.Kind() == reflect.Struct {
			if f := v.FieldByName("Type"); f.IsValid() && f.Kind() == reflect.String && f.String() == "conflictError" {
				return true
			}
		}
		u, ok := e.(interface{ Unwrap() error })
		if !ok {
			return false
		}
		e = u.Unwrap()
	}
	return false
}

// scheduleState records a state change via the projection activity.
func scheduleState(ctx goworkflow.Context, a *Activities, id run.ID, state run.State, msg string) (struct{}, error) {
	return goworkflow.ExecuteActivity[struct{}](ctx, noNativeRetries, a.ProjectionUpdateState, id, state, msg, (*time.Time)(nil)).Get(ctx)
}

func mustJitter(ctx goworkflow.Context) float64 {
	v, err := jitter(ctx)
	if err != nil {
		return 0
	}
	return v
}

// cancelCleanup runs cancellation cleanup on a disconnected workflow
// context: close run-owned terminals (preserving the workspace), write the
// parent cancellation comment with the stable marker, and mark the run
// canceled. No rollback/compensation ever runs.
func (a *Activities) cancelCleanup(ctx goworkflow.Context, work run.Work, repoPath, reason string) error {
	dctx := goworkflow.NewDisconnectedContext(ctx)
	if _, err := retryLoop(dctx, work.RunID, a, work, "",
		func(ctx2 goworkflow.Context) goworkflow.Future[struct{}] {
			return goworkflow.ExecuteActivity[struct{}](ctx2, noNativeRetries,
				a.FinalizeNodeRuntimes, work, repoPath, work.Runtime)
		}); err != nil {
		return err
	}
	if _, err := retryLoop(dctx, work.RunID, a, work, "",
		func(ctx2 goworkflow.Context) goworkflow.Future[struct{}] {
			return goworkflow.ExecuteActivity[struct{}](ctx2, noNativeRetries, a.Comment, work.Repo, run.CommentWork{
				RunID:  work.RunID,
				Item:   task.Target{Parent: work.Parent},
				Body:   "Run canceled: " + reason,
				Marker: run.CancellationMarker(work.RunID),
			})
		}); err != nil {
		return err
	}
	if _, err := retryLoop(dctx, work.RunID, a, work, "",
		func(ctx2 goworkflow.Context) goworkflow.Future[struct{}] {
			now := goworkflow.Now(dctx).UTC()
			return goworkflow.ExecuteActivity[struct{}](ctx2, noNativeRetries, a.ProjectionUpdateState, work.RunID, run.StateCanceled, "", &now)
		}); err != nil {
		return err
	}
	return nil
}

// nextStepsText renders the legal next steps with their when explanations
// for nudge templates.
func nextStepsText(routes []workflow.Route) string {
	var b strings.Builder
	for i, r := range routes {
		if i > 0 {
			b.WriteString("; ")
		}
		b.WriteString(r.Target)
		if r.When != "" {
			b.WriteString(" (when: " + r.When + ")")
		}
	}
	return b.String()
}

func renderSummaryReport(r workflow.Report) string {
	return fmt.Sprintf("COMPLETED:\n%s\n\nCOMMITS:\n%s\n\nNOT COMPLETED:\n%s\n\nISSUES DISCOVERED:\n%s\n\nVERIFICATION:\n%s\n\nNOTES:\n%s",
		r.Summary.Completed, r.Summary.Commits, r.Summary.NotCompleted, r.Summary.IssuesDiscovered, r.Summary.Verification, r.Summary.Notes)
}

func renderFeedbackReport(r workflow.Report) string {
	return fmt.Sprintf("COMMITS:\n%s\n\nREASON FOR NEXT STEP:\n%s\n\nREQUIRED ACTIONS:\n%s\n\nRELEVANT CONTEXT:\n%s\n\nEXPECTED RESULT:\n%s",
		r.Summary.Commits, r.Feedback.ReasonForNextStep, r.Feedback.RequiredActions, r.Feedback.RelevantContext, r.Feedback.ExpectedResult)
}
