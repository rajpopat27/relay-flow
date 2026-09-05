package temporal

import (
	"crypto/rand"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"time"

	"github.com/rajpopat27/relay-flow/internal/harness"
	"github.com/rajpopat27/relay-flow/internal/identity"
	"github.com/rajpopat27/relay-flow/internal/retry"
	"github.com/rajpopat27/relay-flow/internal/run"
	"github.com/rajpopat27/relay-flow/internal/runner"
	"github.com/rajpopat27/relay-flow/internal/task"
	domainworkflow "github.com/rajpopat27/relay-flow/internal/workflow"
	temporalSDK "go.temporal.io/sdk/temporal"
	temporalworkflow "go.temporal.io/sdk/workflow"
)

const (
	reportSignalName       = "report"
	reconcileSignalName    = "reconcile"
	cancelReasonSignalName = "cancel-reason"
	runStateQuery          = "relay-flow/run-state-v1"
	reportStateQuery       = "relay-flow/report-state-v1"

	activityEnsureMailboxes             = "EnsureMailboxes"
	activityPrepareRestart              = "PrepareRestart"
	activityValidateAgents              = "ValidateAgents"
	activityApplyTaskConfig             = "ApplyTaskConfig"
	activityEnsureEnvironment           = "EnsureEnvironment"
	activitySetEnvironmentStatus        = "SetEnvironmentStatus"
	activityLoadNodeRuntime             = "LoadNodeRuntime"
	activityEnsureNodeRuntime           = "EnsureNodeRuntime"
	activityCloseTerminals              = "CloseTerminals"
	activityCleanupRun                  = "CleanupRun"
	activityCheckpointNodeRuntime       = "CheckpointNodeRuntime"
	activityFinalizeNodeRuntimes        = "FinalizeNodeRuntimes"
	activityComment                     = "Comment"
	activityCompleteMailbox             = "CompleteMailbox"
	activityProjectionUpdateNodeRuntime = "ProjectionUpdateNodeRuntimeVisit"
	activityProjectionRecordReport      = "ProjectionRecordProcessedReport"
	activityProjectionUpdateNode        = "ProjectionUpdateNode"
	activityProjectionUpdateState       = "ProjectionUpdateState"
	activityProjectionUpdateRetry       = "ProjectionUpdateRetry"
)

type reportSignal struct {
	ReportID    string                `json:"reportId"`
	Node        string                `json:"node"`
	NodeVisitID run.NodeVisitID       `json:"nodeVisitId"`
	Report      domainworkflow.Report `json:"report"`
}

type cancelReasonSignal struct {
	Reason string `json:"reason"`
}

// NodeRuntimeBinding is the serializable runtime information returned by the
// run-state query. It contains only runner/session identifiers, never live
// dependency objects.
type NodeRuntimeBinding struct {
	Node        string          `json:"node"`
	TerminalID  string          `json:"terminalId"`
	SessionID   string          `json:"sessionId"`
	NodeVisitID run.NodeVisitID `json:"nodeVisitId"`
}

type RunStateSnapshot struct {
	Run             run.Run              `json:"run"`
	RuntimeBindings []NodeRuntimeBinding `json:"runtimeBindings"`
}

type ReportStateQuery struct {
	ReportID string `json:"reportId"`
}

type ReportStateSnapshot struct {
	CurrentNode        string          `json:"currentNode"`
	CurrentNodeVisitID run.NodeVisitID `json:"currentNodeVisitId"`
	State              run.State       `json:"state"`
	Processed          bool            `json:"processed"`
}

type workflowState struct {
	run       run.Run
	bindings  map[string]NodeRuntimeBinding
	processed map[string]bool
}

func (s *workflowState) snapshot() RunStateSnapshot {
	keys := make([]string, 0, len(s.bindings))
	for node := range s.bindings {
		keys = append(keys, node)
	}
	sort.Strings(keys)
	bindings := make([]NodeRuntimeBinding, 0, len(keys))
	for _, node := range keys {
		bindings = append(bindings, s.bindings[node])
	}
	return RunStateSnapshot{Run: s.run, RuntimeBindings: bindings}
}

var temporalActivityOptions = temporalworkflow.ActivityOptions{
	StartToCloseTimeout: 5 * time.Minute,
	WaitForCancellation: true,
	RetryPolicy:         &temporalSDK.RetryPolicy{MaximumAttempts: 1},
}

func executeActivity[T any](ctx temporalworkflow.Context, name string, args ...interface{}) (T, error) {
	var out T
	activityCtx := temporalworkflow.WithActivityOptions(ctx, temporalActivityOptions)
	err := temporalworkflow.ExecuteActivity(activityCtx, name, args...).Get(activityCtx, &out)
	return out, err
}

func temporalJitter(ctx temporalworkflow.Context) float64 {
	var value float64
	err := temporalworkflow.SideEffect(ctx, func(temporalworkflow.Context) interface{} {
		var b [1]byte
		if _, err := rand.Read(b[:]); err != nil {
			return float64(0)
		}
		return float64(b[0]) / 256
	}).Get(&value)
	if err != nil {
		return 0.5
	}
	return value
}

// TicketWorkflow is the single generic workflow registered by the Temporal
// worker. Its input is an immutable run.Start snapshot; no workflow YAML or
// live dependency is loaded from inside workflow code.
func TicketWorkflow(ctx temporalworkflow.Context, start run.Start) error {
	if start.LogicalID == "" {
		start.LogicalID = run.ID(identity.LogicalRunID(start.ID))
	}
	if start.AttemptID == 0 {
		start.AttemptID = 1
	}
	cancellationReason := "canceled"
	cancelReasonCh := temporalworkflow.GetSignalChannel(ctx, cancelReasonSignalName)
	state := newWorkflowState(ctx, start)
	if err := temporalworkflow.SetQueryHandler(ctx, runStateQuery, func() (RunStateSnapshot, error) {
		return state.snapshot(), nil
	}); err != nil {
		return err
	}
	if err := temporalworkflow.SetQueryHandler(ctx, reportStateQuery, func(query ReportStateQuery) (ReportStateSnapshot, error) {
		return ReportStateSnapshot{
			CurrentNode: state.run.CurrentNode, CurrentNodeVisitID: state.run.CurrentNodeVisitID,
			State: state.run.State, Processed: state.processed[query.ReportID],
		}, nil
	}); err != nil {
		return err
	}
	if err := runGraph(ctx, start, state, cancelReasonCh, &cancellationReason); err != nil {
		if temporalSDK.IsCanceledError(err) || temporalSDK.IsCanceledError(ctx.Err()) {
			work := run.Work{
				RunID: start.ID, LogicalID: start.LogicalID, AttemptID: start.AttemptID,
				Repo: start.Repo, Workflow: start.Workflow.Name, Parent: start.Ticket,
				WorkflowTaskConfig: start.Workflow.TaskConfig, Runtime: start.Runtime,
			}
			var signal cancelReasonSignal
			for cancelReasonCh.ReceiveAsync(&signal) {
				if signal.Reason != "" {
					cancellationReason = signal.Reason
				}
			}
			state.run.State = run.StateCanceling
			state.run.LastError = cancellationReason
			state.run.UpdatedAt = temporalworkflow.Now(ctx)
			if cleanupErr := cancelCleanup(ctx, state, work, start.RepoPath, cancellationReason); cleanupErr != nil {
				return cleanupErr
			}
			state.run.State = run.StateCanceled
			state.run.UpdatedAt = temporalworkflow.Now(ctx)
			now := state.run.UpdatedAt
			state.run.FinishedAt = &now
			for node, binding := range state.bindings {
				binding.TerminalID = ""
				if !work.Runtime.KeepSessionsAlive {
					binding.SessionID = ""
				}
				state.bindings[node] = binding
			}
			return temporalSDK.NewCanceledError()
		}
		return err
	}
	return nil
}

func newWorkflowState(ctx temporalworkflow.Context, start run.Start) *workflowState {
	info := temporalworkflow.GetInfo(ctx)
	return &workflowState{
		run: run.Run{
			ID: start.ID, LogicalID: start.LogicalID, AttemptID: start.AttemptID,
			Repo: start.Repo, Workflow: start.Workflow.Name, Ticket: start.Ticket,
			State: run.StateStarting, StartedAt: info.WorkflowStartTime,
			UpdatedAt: temporalworkflow.Now(ctx),
		},
		bindings:  map[string]NodeRuntimeBinding{},
		processed: map[string]bool{},
	}
}

func runGraph(ctx temporalworkflow.Context, start run.Start, state *workflowState, cancelReasonCh temporalworkflow.ReceiveChannel, cancellationReason *string) error {
	wf := start.Workflow
	work := run.Work{
		RunID: start.ID, LogicalID: start.LogicalID, AttemptID: start.AttemptID,
		Repo: start.Repo, Workflow: wf.Name, Parent: start.Ticket,
		WorkflowTaskConfig: wf.TaskConfig, Runtime: start.Runtime,
	}

	specs := MailboxSpecs(&wf, start.Ticket.Key)
	mailboxes, err := retryActivity(ctx, state, work, "", func() (map[string]task.Mailbox, error) {
		return executeActivity[map[string]task.Mailbox](ctx, activityEnsureMailboxes, work, specs)
	})
	if err != nil {
		return err
	}
	if start.AttemptID > 1 {
		mailboxList := make([]task.Mailbox, 0, len(mailboxes))
		for _, mailbox := range mailboxes {
			mailboxList = append(mailboxList, mailbox)
		}
		sort.Slice(mailboxList, func(i, j int) bool { return mailboxList[i].Node < mailboxList[j].Node })
		if _, err := retryActivity(ctx, state, work, "start", func() (struct{}, error) {
			return executeActivity[struct{}](ctx, activityPrepareRestart, work, start.RepoPath, mailboxList)
		}); err != nil {
			return err
		}
	}

	agentSet := map[string]bool{}
	for _, node := range wf.Nodes {
		if node.Agent != "" {
			agentSet[node.Agent] = true
		}
	}
	agents := make([]string, 0, len(agentSet))
	for agent := range agentSet {
		agents = append(agents, agent)
	}
	sort.Strings(agents)
	if _, err := retryActivity(ctx, state, work, "", func() (struct{}, error) {
		return executeActivity[struct{}](ctx, activityValidateAgents, start.RepoPath, agents)
	}); err != nil {
		return err
	}

	startNode := wf.Nodes["start"]
	if _, err := retryActivity(ctx, state, work, "start", func() (struct{}, error) {
		return executeActivity[struct{}](ctx, activityApplyTaskConfig, work, "start", (*task.Mailbox)(nil), mergeTaskConfig(wf.TaskConfig, startNode.TaskConfig))
	}); err != nil {
		return err
	}
	if _, err := retryActivity(ctx, state, work, "", func() (runner.Environment, error) {
		return executeActivity[runner.Environment](ctx, activityEnsureEnvironment, work, start.RepoPath)
	}); err != nil {
		return err
	}
	target, err := wf.StartTarget()
	if err != nil {
		return err
	}

	current := target
	for current != "end" {
		node := wf.Nodes[current]
		var visitID identity.NodeVisitID
		if err := temporalworkflow.SideEffect(ctx, func(temporalworkflow.Context) interface{} {
			return identity.NewNodeVisitID()
		}).Get(&visitID); err != nil {
			return err
		}
		visit := run.NodeVisitID(visitID)
		runtime, err := retryActivity(ctx, state, work, current, func() (NodeRuntime, error) {
			return executeActivity[NodeRuntime](ctx, activityLoadNodeRuntime, start.ID, current)
		})
		if err != nil {
			return err
		}
		if _, err := retryActivity(ctx, state, work, current, func() (struct{}, error) {
			return executeActivity[struct{}](ctx, activityProjectionUpdateNodeRuntime, start.ID, current, visit)
		}); err != nil {
			return err
		}

		mailbox := mailboxes[current]
		nodeCfg := mergeTaskConfig(wf.TaskConfig, node.TaskConfig)
		if _, err := retryActivity(ctx, state, work, current, func() (struct{}, error) {
			return executeActivity[struct{}](ctx, activityApplyTaskConfig, work, current, &mailbox, nodeCfg)
		}); err != nil {
			return err
		}
		if _, err := retryActivity(ctx, state, work, current, func() (runner.Environment, error) {
			return executeActivity[runner.Environment](ctx, activityEnsureEnvironment, work, start.RepoPath)
		}); err != nil {
			return err
		}

		nextSteps := append(append([]domainworkflow.Route{}, node.OnSuccess...), node.OnFailure...)
		spec := harness.LaunchSpec{
			RunID: start.ID, NodeVisitID: visit, RepoName: start.Repo, RepoPath: start.RepoPath,
			Workflow: wf.Name, Ticket: start.Ticket.Key, Node: current, NodeType: node.Type,
			Agent: node.Agent, Title: start.Ticket.Key + ":" + current, NudgePrompt: node.NudgePrompt,
			PromptData: harness.PromptData{
				TaskSystem: "", Ticket: start.Ticket.Key, Workflow: wf.Name, Repo: start.Repo,
				Node: current, NodeType: node.Type, Agent: node.Agent, NodeDescription: node.Description,
				NextSteps: nextStepsText(nextSteps), Mailbox: mailbox.Key,
			},
			NextSteps: nextSteps,
		}
		// TaskSystem is an adapter name rather than workflow state. The activity
		// owns the concrete configured value; this field remains informational.
		nodeWork := run.NodeWork{Work: work, Node: current, NodeVisitID: visit, Mailbox: mailbox, NodeTaskConfig: nodeCfg}
		if runtime.SessionID != "" {
			spec.ResumeID = runtime.SessionID
		}
		binding, err := retryActivity(ctx, state, work, current, func() (NodeRuntime, error) {
			return executeActivity[NodeRuntime](ctx, activityEnsureNodeRuntime, nodeWork, start.RepoPath, spec, runtime)
		})
		if err != nil {
			return err
		}
		state.bindings[current] = NodeRuntimeBinding{Node: current, TerminalID: binding.TerminalID, SessionID: binding.SessionID, NodeVisitID: visit}

		if _, err := retryActivity(ctx, state, work, current, func() (struct{}, error) {
			return executeActivity[struct{}](ctx, activityProjectionUpdateNode, start.ID, run.StateRunning, current, visit)
		}); err != nil {
			return err
		}
		state.run.State = run.StateRunning
		state.run.CurrentNode = current
		state.run.CurrentNodeVisitID = visit
		state.run.UpdatedAt = temporalworkflow.Now(ctx)
		if _, err := retryActivity(ctx, state, work, current, func() (struct{}, error) {
			return executeActivity[struct{}](ctx, activityProjectionUpdateState, start.ID, run.StateWaiting, "", (*time.Time)(nil))
		}); err != nil {
			return err
		}
		state.run.State = run.StateWaiting
		state.run.UpdatedAt = temporalworkflow.Now(ctx)

		reportCh := temporalworkflow.GetSignalChannel(ctx, reportSignalName)
		reconcileCh := temporalworkflow.GetSignalChannel(ctx, reconcileSignalName)
		var accepted reportSignal
		gotReport := false
		var reconcileErr error
		for !gotReport {
			selector := temporalworkflow.NewSelector(ctx)
			selector.AddReceive(reportCh, func(channel temporalworkflow.ReceiveChannel, more bool) {
				if !more {
					return
				}
				var signal reportSignal
				channel.Receive(ctx, &signal)
				if state.processed[signal.ReportID] || signal.Node != current || signal.NodeVisitID != visit {
					return
				}
				accepted = signal
				gotReport = true
			})
			selector.AddReceive(reconcileCh, func(channel temporalworkflow.ReceiveChannel, more bool) {
				if !more {
					return
				}
				var ignored struct{}
				channel.Receive(ctx, &ignored)
				if reconcileErr != nil {
					return
				}
				var rebound NodeRuntime
				rebound, reconcileErr = retryActivity(ctx, state, work, current, func() (NodeRuntime, error) {
					return executeActivity[NodeRuntime](ctx, activityEnsureNodeRuntime, nodeWork, start.RepoPath, spec, NodeRuntime{NodeVisitID: visit})
				})
				if reconcileErr == nil {
					state.bindings[current] = NodeRuntimeBinding{Node: current, TerminalID: rebound.TerminalID, SessionID: rebound.SessionID, NodeVisitID: visit}
				}
			})
			selector.AddReceive(cancelReasonCh, func(channel temporalworkflow.ReceiveChannel, more bool) {
				if !more {
					return
				}
				var signal cancelReasonSignal
				channel.Receive(ctx, &signal)
				if signal.Reason != "" && cancellationReason != nil {
					*cancellationReason = signal.Reason
				}
			})
			selector.AddReceive(ctx.Done(), func(channel temporalworkflow.ReceiveChannel, more bool) {
				if more {
					channel.Receive(ctx, nil)
				}
			})
			selector.Select(ctx)
			if reconcileErr != nil {
				return reconcileErr
			}
			if temporalSDK.IsCanceledError(ctx.Err()) {
				return ctx.Err()
			}
		}
		if err := wf.ValidateReport(current, accepted.Report); err != nil {
			return fmt.Errorf("workflow %q node %q: invalid accepted report: %w", wf.Name, current, err)
		}
		state.processed[accepted.ReportID] = true
		state.run.UpdatedAt = temporalworkflow.Now(ctx)
		if _, err := retryActivity(ctx, state, work, current, func() (struct{}, error) {
			return executeActivity[struct{}](ctx, activityProjectionRecordReport, start.ID, visit, accepted.ReportID)
		}); err != nil {
			return err
		}

		if _, err := retryActivity(ctx, state, work, current, func() (struct{}, error) {
			return executeActivity[struct{}](ctx, activityComment, start.Repo, run.CommentWork{
				RunID: start.ID, Item: task.Target{Parent: work.Parent, Mailbox: &mailbox},
				TextKind: task.TextSummaryComment,
				TextData: task.TextData{RunID: string(start.ID), Ticket: work.Parent.Key, Workflow: wf.Name,
					Repo: start.Repo, Node: current, NodeType: string(node.Type), Agent: node.Agent,
					NodeDescription: node.Description, Mailbox: mailbox.Key, SourceNode: current,
					TargetNode: current, SummaryReport: renderSummaryReport(accepted.Report)},
				Marker: string(visit) + ":summary",
			})
		}); err != nil {
			return err
		}
		next := accepted.Report.NextStep
		if next != "end" {
			nextMailbox := mailboxes[next]
			if _, err := retryActivity(ctx, state, work, current, func() (struct{}, error) {
				return executeActivity[struct{}](ctx, activityComment, start.Repo, run.CommentWork{
					RunID: start.ID, Item: task.Target{Parent: work.Parent, Mailbox: &nextMailbox},
					TextKind: task.TextFeedbackComment,
					TextData: task.TextData{RunID: string(start.ID), Ticket: work.Parent.Key, Workflow: wf.Name,
						Repo: start.Repo, Node: next, NodeType: string(wf.Nodes[next].Type), Agent: wf.Nodes[next].Agent,
						NodeDescription: wf.Nodes[next].Description, Mailbox: nextMailbox.Key, SourceNode: current,
						TargetNode: next, SummaryReport: renderSummaryReport(accepted.Report),
						FeedbackReport: renderFeedbackReport(accepted.Report)},
					Marker: string(visit) + ":feedback",
				})
			}); err != nil {
				return err
			}
		}
		if _, err := retryActivity(ctx, state, work, current, func() (struct{}, error) {
			return executeActivity[struct{}](ctx, activityCompleteMailbox, work, mailbox)
		}); err != nil {
			return err
		}
		if _, err := retryActivity(ctx, state, work, current, func() (struct{}, error) {
			return executeActivity[struct{}](ctx, activityCheckpointNodeRuntime, nodeWork, start.RepoPath, work.Runtime)
		}); err != nil {
			return err
		}
		applyRuntimePolicy(state, current, work.Runtime)
		current = next
	}

	endNode := wf.Nodes["end"]
	if _, err := retryActivity(ctx, state, work, "end", func() (struct{}, error) {
		return executeActivity[struct{}](ctx, activityApplyTaskConfig, work, "end", (*task.Mailbox)(nil), mergeTaskConfig(wf.TaskConfig, endNode.TaskConfig))
	}); err != nil {
		return err
	}
	if _, err := retryActivity(ctx, state, work, "end", func() (struct{}, error) {
		return executeActivity[struct{}](ctx, activitySetEnvironmentStatus, work, start.RepoPath, runner.WorkspaceStatusCompleted)
	}); err != nil {
		return err
	}
	finalPolicy := work.Runtime
	if wf.CleanupRunnerOnEnd {
		finalPolicy.KeepTerminalsAlive = false
	}
	if _, err := retryActivity(ctx, state, work, "", func() (struct{}, error) {
		return executeActivity[struct{}](ctx, activityFinalizeNodeRuntimes, work, start.RepoPath, finalPolicy)
	}); err != nil {
		return err
	}
	for node := range state.bindings {
		applyRuntimePolicy(state, node, finalPolicy)
	}
	if wf.CleanupRunnerOnEnd {
		if _, err := retryActivity(ctx, state, work, "", func() (struct{}, error) {
			return executeActivity[struct{}](ctx, activityCleanupRun, work, start.RepoPath)
		}); err != nil {
			return err
		}
	}
	now := temporalworkflow.Now(ctx).UTC()
	if _, err := retryActivity(ctx, state, work, "", func() (struct{}, error) {
		return executeActivity[struct{}](ctx, activityProjectionUpdateState, start.ID, run.StateCompleted, "", &now)
	}); err != nil {
		return err
	}
	state.run.State = run.StateCompleted
	state.run.UpdatedAt = now
	state.run.FinishedAt = &now
	return nil
}

// retryProjectionActivity uses the same durable timer/backoff policy for
// relay projection bookkeeping. It intentionally does not recursively write
// retry metadata because this is the fallback path when that metadata write
// itself failed; Temporal history remains authoritative while SQLite heals.
func retryProjectionActivity[T any](ctx temporalworkflow.Context, state *workflowState, work run.Work, node string, action func() (T, error)) (T, error) {
	var zero T
	attempt := 0
	for {
		if err := ctx.Err(); err != nil {
			return zero, err
		}
		result, err := action()
		if err == nil {
			return result, nil
		}
		if temporalSDK.IsCanceledError(err) || temporalSDK.IsCanceledError(ctx.Err()) {
			if ctx.Err() != nil {
				return zero, ctx.Err()
			}
			return zero, err
		}
		failure := classifyTemporalError(err)
		delay := retry.DefaultBackoffPolicy.Delay(attempt, temporalJitter(ctx))
		state.run.Retry = &run.RetryStatus{Attempt: attempt + 1, LastError: sanitizeRetryMessage(failure.Message), NextRetryAt: temporalworkflow.Now(ctx).UTC().Add(delay)}
		state.run.LastError = state.run.Retry.LastError
		state.run.UpdatedAt = temporalworkflow.Now(ctx)
		logRetry(ctx, work, node, failure, delay)
		if err := temporalworkflow.NewTimer(ctx, delay).Get(ctx, nil); err != nil {
			return zero, err
		}
		attempt++
	}
}

func retryActivity[T any](ctx temporalworkflow.Context, state *workflowState, work run.Work, node string, action func() (T, error)) (T, error) {
	var zero T
	attempt := 0
	blocked := false
	for {
		if err := ctx.Err(); err != nil {
			return zero, err
		}
		result, err := action()
		if err == nil {
			if attempt > 0 {
				if _, clearErr := retryProjectionActivity(ctx, state, work, node, func() (struct{}, error) {
					return executeActivity[struct{}](ctx, activityProjectionUpdateRetry, work.RunID, (*run.RetryStatus)(nil))
				}); clearErr != nil {
					return zero, clearErr
				}
				state.run.Retry = nil
				state.run.LastError = ""
				state.run.UpdatedAt = temporalworkflow.Now(ctx)
			}
			if blocked {
				if _, stateErr := retryProjectionActivity(ctx, state, work, node, func() (struct{}, error) {
					return executeActivity[struct{}](ctx, activityProjectionUpdateState, work.RunID, run.StateWaiting, "", (*time.Time)(nil))
				}); stateErr != nil {
					return zero, stateErr
				}
				state.run.State = run.StateWaiting
			}
			return result, nil
		}
		if temporalSDK.IsCanceledError(err) || temporalSDK.IsCanceledError(ctx.Err()) {
			if ctx.Err() != nil {
				return zero, ctx.Err()
			}
			return zero, err
		}
		failure := classifyTemporalError(err)
		if failure.Kind == retry.Conflict {
			failure.Message = blockedMessage(work, node, failure.Message)
			blocked = true
			state.run.State = run.StateBlocked
			if _, stateErr := retryProjectionActivity(ctx, state, work, node, func() (struct{}, error) {
				return executeActivity[struct{}](ctx, activityProjectionUpdateState, work.RunID, run.StateBlocked, failure.Message, (*time.Time)(nil))
			}); stateErr != nil {
				return zero, stateErr
			}
		}
		delay := retry.DefaultBackoffPolicy.Delay(attempt, temporalJitter(ctx))
		logRetry(ctx, work, node, failure, delay)
		nextRetry := temporalworkflow.Now(ctx).UTC().Add(delay)
		status := &run.RetryStatus{Attempt: attempt + 1, LastError: sanitizeRetryMessage(failure.Message), NextRetryAt: nextRetry}
		state.run.Retry = status
		state.run.LastError = status.LastError
		state.run.UpdatedAt = temporalworkflow.Now(ctx)
		if _, projectionErr := retryProjectionActivity(ctx, state, work, node, func() (struct{}, error) {
			return executeActivity[struct{}](ctx, activityProjectionUpdateRetry, work.RunID, status)
		}); projectionErr != nil {
			return zero, projectionErr
		}
		if err := temporalworkflow.NewTimer(ctx, delay).Get(ctx, nil); err != nil {
			return zero, err
		}
		attempt++
	}
}

func blockedMessage(work run.Work, node, message string) string {
	message = strings.TrimRight(message, ". ")
	lower := strings.ToLower(message)
	if node == "start" && !strings.Contains(lower, "mailbox") {
		return fmt.Sprintf("%s. Move ticket %s to an allowed active start status; relay-flow will retry automatically", message, work.Parent.Key)
	}
	if node != "" {
		return fmt.Sprintf("%s. Restore the task-system state required for node %s; relay-flow will retry automatically", message, node)
	}
	return fmt.Sprintf("%s. Restore the task-system state required by this operation; relay-flow will retry automatically", message)
}

func classifyTemporalError(err error) retry.Failure {
	if err == nil {
		return retry.Failure{}
	}
	var applicationErr *temporalSDK.ApplicationError
	if errors.As(err, &applicationErr) {
		typ := strings.ToLower(applicationErr.Type())
		if typ == string(retry.Conflict) || strings.Contains(typ, "conflicterror") {
			return retry.Failure{Kind: retry.Conflict, Message: applicationErr.Error()}
		}
		if typ == string(retry.Transient) {
			return retry.Failure{Kind: retry.Transient, Message: applicationErr.Error()}
		}
	}
	return retry.Classify(err)
}

func logRetry(ctx temporalworkflow.Context, work run.Work, node string, failure retry.Failure, delay time.Duration) {
	attrs := []interface{}{
		"ticket", work.Parent.Key, "runID", string(work.RunID), "repo", work.Repo,
		"workflow", work.Workflow, "kind", string(failure.Kind), "delayMs", delay.Milliseconds(),
		"error", sanitizeRetryMessage(failure.Message),
	}
	if node != "" {
		attrs = append(attrs, "node", node)
	}
	_ = temporalworkflow.SideEffect(ctx, func(temporalworkflow.Context) interface{} {
		slog.Info("retry scheduled", attrs...)
		return struct{}{}
	})
}

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
		s = strings.TrimSuffix(s[:i], " ") + s[i+j+3:]
	}
}

func cancelCleanup(ctx temporalworkflow.Context, state *workflowState, work run.Work, repoPath, reason string) error {
	dctx, cancel := temporalworkflow.NewDisconnectedContext(ctx)
	defer cancel()
	cleanupPolicy := work.Runtime
	// Cancellation always closes run-owned terminals while preserving the
	// workspace and reusable session metadata. KeepTerminalsAlive controls
	// normal checkpoints/end cleanup, not cancellation cleanup.
	cleanupPolicy.KeepTerminalsAlive = false
	if _, err := retryActivity(dctx, state, work, "", func() (struct{}, error) {
		return executeActivity[struct{}](dctx, activityFinalizeNodeRuntimes, work, repoPath, cleanupPolicy)
	}); err != nil {
		return err
	}
	markerID := work.LogicalID
	if markerID == "" {
		markerID = work.RunID
	}
	if _, err := retryActivity(dctx, state, work, "", func() (struct{}, error) {
		return executeActivity[struct{}](dctx, activityComment, work.Repo, run.CommentWork{
			RunID: work.RunID, Item: task.Target{Parent: work.Parent},
			Body: "Run canceled: " + reason, Marker: run.CancellationMarker(markerID),
		})
	}); err != nil {
		return err
	}
	now := temporalworkflow.Now(dctx).UTC()
	if _, err := retryActivity(dctx, state, work, "", func() (struct{}, error) {
		return executeActivity[struct{}](dctx, activityProjectionUpdateState, work.RunID, run.StateCanceled, "", &now)
	}); err != nil {
		return err
	}
	return nil
}

func nextStepsText(routes []domainworkflow.Route) string {
	var b strings.Builder
	for i, route := range routes {
		if i > 0 {
			b.WriteString("; ")
		}
		b.WriteString(route.Target)
		if route.When != "" {
			b.WriteString(" (when: " + route.When + ")")
		}
	}
	return b.String()
}

func applyRuntimePolicy(state *workflowState, node string, policy run.RuntimePolicy) {
	binding, ok := state.bindings[node]
	if !ok {
		return
	}
	if !policy.KeepTerminalsAlive {
		binding.TerminalID = ""
	}
	if !policy.KeepSessionsAlive {
		binding.SessionID = ""
	}
	state.bindings[node] = binding
}

func renderSummaryReport(r domainworkflow.Report) string {
	return fmt.Sprintf("COMPLETED:\n%s\n\nCOMMITS:\n%s\n\nNOT COMPLETED:\n%s\n\nISSUES DISCOVERED:\n%s\n\nVERIFICATION:\n%s\n\nNOTES:\n%s",
		r.Summary.Completed, r.Summary.Commits, r.Summary.NotCompleted, r.Summary.IssuesDiscovered, r.Summary.Verification, r.Summary.Notes)
}

func renderFeedbackReport(r domainworkflow.Report) string {
	return fmt.Sprintf("COMMITS:\n%s\n\nREASON FOR NEXT STEP:\n%s\n\nREQUIRED ACTIONS:\n%s\n\nRELEVANT CONTEXT:\n%s\n\nEXPECTED RESULT:\n%s",
		r.Summary.Commits, r.Feedback.ReasonForNextStep, r.Feedback.RequiredActions, r.Feedback.RelevantContext, r.Feedback.ExpectedResult)
}
