package temporal

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/rajpopat27/relay-flow/internal/execution/projection"
	"github.com/rajpopat27/relay-flow/internal/identity"
	"github.com/rajpopat27/relay-flow/internal/run"
	"github.com/rajpopat27/relay-flow/internal/runner"
	domainworkflow "github.com/rajpopat27/relay-flow/internal/workflow"
	enumspb "go.temporal.io/api/enums/v1"
	"go.temporal.io/api/serviceerror"
	workflowpb "go.temporal.io/api/workflow/v1"
	"go.temporal.io/sdk/client"
	"go.temporal.io/sdk/converter"
	"google.golang.org/protobuf/types/known/timestamppb"
)

func (e *Engine) ready() (client.Client, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.client == nil {
		return nil, errors.New("temporal engine is not connected")
	}
	return e.client, nil
}

func isWorkflowNotFound(err error) bool {
	var notFound *serviceerror.NotFound
	return errors.As(err, &notFound)
}

func isAlreadyStarted(err error) bool {
	var alreadyStarted *serviceerror.WorkflowExecutionAlreadyStarted
	return errors.As(err, &alreadyStarted)
}

func validateTemporalExecutionInfo(info *workflowpb.WorkflowExecutionInfo, expectedID run.ID) error {
	if info == nil || info.Execution == nil || info.Type == nil {
		return errors.New("Temporal execution identity is incomplete")
	}
	if expectedID != "" && info.Execution.WorkflowId != string(expectedID) {
		return fmt.Errorf("Temporal execution Workflow ID %q does not match %q", info.Execution.WorkflowId, expectedID)
	}
	if info.Type.Name != TicketWorkflowName || info.TaskQueue != TaskQueue {
		return fmt.Errorf("Temporal execution %q has unexpected type/task queue (%q/%q)", info.Execution.WorkflowId, info.Type.Name, info.TaskQueue)
	}
	return nil
}

func (e *Engine) describe(ctx context.Context, id run.ID) (*client.WorkflowExecutionDescription, error) {
	c, err := e.ready()
	if err != nil {
		return nil, err
	}
	return c.DescribeWorkflow(ctx, string(id), "")
}

func (e *Engine) describeInfo(ctx context.Context, id run.ID) (*workflowpb.WorkflowExecutionInfo, error) {
	c, err := e.ready()
	if err != nil {
		return nil, err
	}
	response, err := c.DescribeWorkflowExecution(ctx, string(id), "")
	if err != nil {
		return nil, err
	}
	if response == nil || response.WorkflowExecutionInfo == nil {
		return nil, fmt.Errorf("Temporal returned no execution info for %s", id)
	}
	return response.WorkflowExecutionInfo, nil
}

// EnsureRun uses the relay run ID as the Temporal Workflow ID. Describe is
// performed before a new start so an ambiguous projection write or process
// crash cannot create a second execution.
func (e *Engine) EnsureRun(ctx context.Context, start run.Start) (bool, error) {
	c, err := e.ready()
	if err != nil {
		return false, err
	}
	if start.LogicalID == "" {
		start.LogicalID = run.ID(identity.LogicalRunID(start.ID))
	}
	if start.AttemptID == 0 {
		start.AttemptID = 1
	}
	start.Runtime = e.runtime

	local, localErr := e.runs.Get(ctx, start.ID)
	if localErr != nil && !projection.IsNotFound(localErr) {
		return false, localErr
	}
	info, describeErr := c.DescribeWorkflowExecution(ctx, string(start.ID), "")
	if describeErr == nil && info != nil && info.WorkflowExecutionInfo != nil {
		if err := validateTemporalExecutionInfo(info.WorkflowExecutionInfo, start.ID); err != nil {
			return false, err
		}
		if projection.IsNotFound(localErr) {
			if err := e.restoreProjection(ctx, info.WorkflowExecutionInfo); err != nil {
				return false, fmt.Errorf("restore Temporal run %s projection: %w", start.ID, err)
			}
			local, localErr = e.runs.Get(ctx, start.ID)
		}
		status := info.WorkflowExecutionInfo.Status
		if status == enumspb.WORKFLOW_EXECUTION_STATUS_RUNNING {
			if err := e.reconcileRunningTerminal(ctx, start, info.WorkflowExecutionInfo.Execution.RunId); err != nil {
				return false, err
			}
			return false, nil
		}
		if localErr == nil && local.State != run.StateCompleted && local.State != run.StateCanceled {
			state, finished := stateForTemporalStatus(status, info.WorkflowExecutionInfo.CloseTime)
			if err := e.runs.UpdateState(ctx, local.ID, state, "", finished); err != nil {
				return false, err
			}
		}
		return false, nil
	}
	if describeErr != nil && !isWorkflowNotFound(describeErr) {
		return false, fmt.Errorf("describe Temporal workflow %s: %w", start.ID, describeErr)
	}
	if localErr == nil && (local.State == run.StateCompleted || local.State == run.StateCanceled) {
		return false, nil
	}
	if localErr == nil && local.State != run.StateStarting {
		return false, fmt.Errorf("Temporal workflow %s is missing while projection state is %q; refusing replacement execution", start.ID, local.State)
	}
	if projection.IsNotFound(localErr) {
		if err := e.runs.InsertStart(ctx, start, time.Now().UTC()); err != nil {
			return false, fmt.Errorf("insert Temporal run %s: %w", start.ID, err)
		}
	}
	_, err = c.ExecuteWorkflow(ctx, client.StartWorkflowOptions{
		ID:                                       string(start.ID),
		TaskQueue:                                TaskQueue,
		WorkflowIDReusePolicy:                    enumspb.WORKFLOW_ID_REUSE_POLICY_ALLOW_DUPLICATE_FAILED_ONLY,
		WorkflowExecutionErrorWhenAlreadyStarted: true,
	}, TicketWorkflow, start)
	if err != nil {
		if isAlreadyStarted(err) {
			return false, nil
		}
		return false, fmt.Errorf("start Temporal workflow %s: %w", start.ID, err)
	}
	return true, nil
}

func (e *Engine) reconcileRunningTerminal(ctx context.Context, start run.Start, temporalRunID string) error {
	state, err := e.queryRunState(ctx, start.ID, temporalRunID)
	if err != nil {
		return fmt.Errorf("query Temporal run state for terminal reconciliation %s: %w", start.ID, err)
	}
	if state.Run.State == run.StateCompleted || state.Run.State == run.StateCanceled || state.Run.State == run.StateCanceling || state.Run.CurrentNode == "" || state.Run.CurrentNode == "end" || state.Run.CurrentNodeVisitID == "" {
		return nil
	}
	var binding NodeRuntimeBinding
	for _, candidate := range state.RuntimeBindings {
		if candidate.Node == state.Run.CurrentNode {
			binding = candidate
			break
		}
	}
	if binding.TerminalID != "" {
		terminal, live, err := e.deps.Runner.FindTerminal(ctx, runner.Terminal{
			ID: binding.TerminalID, Title: start.Ticket.Key + ":" + state.Run.CurrentNode,
		})
		if err != nil {
			return fmt.Errorf("find current Temporal run terminal %s: %w", start.ID, err)
		}
		if live && terminal.ID != "" {
			return nil
		}
	}
	c, err := e.ready()
	if err != nil {
		return err
	}
	if err := c.SignalWorkflow(ctx, string(start.ID), temporalRunID, reconcileSignalName, struct{}{}); err != nil {
		return fmt.Errorf("signal terminal reconciliation for %s: %w", start.ID, err)
	}
	return nil
}

// SubmitReport validates against the workflow's current Temporal query state,
// then acknowledges only after the signal RPC has persisted the report in
// Temporal history. The SQLite receipt is only a fast-path cache.
func (e *Engine) SubmitReport(ctx context.Context, req run.ReportRequest) (run.ReportAck, error) {
	c, err := e.ready()
	if err != nil {
		return run.ReportAck{}, err
	}
	state, err := e.queryReportState(ctx, req.RunID, req.ReportID)
	if err != nil {
		if info, describeErr := e.describeInfo(ctx, req.RunID); describeErr == nil && info.Status != enumspb.WORKFLOW_EXECUTION_STATUS_RUNNING {
			return run.ReportAck{Accepted: true, Duplicate: true}, nil
		}
		return run.ReportAck{}, fmt.Errorf("query Temporal report state for %s: %w", req.RunID, err)
	}
	if state.Processed || state.State == run.StateCompleted || state.State == run.StateCanceled || state.CurrentNode != req.Node || state.CurrentNodeVisitID == "" {
		return run.ReportAck{Accepted: true, Duplicate: true}, nil
	}
	wf, err := e.workflowFromHistory(ctx, req.RunID, "")
	if err != nil {
		return run.ReportAck{}, err
	}
	if err := wf.ValidateReport(req.Node, req.Report); err != nil {
		return run.ReportAck{Accepted: false}, err
	}
	if err := c.SignalWorkflow(ctx, string(req.RunID), "", reportSignalName, reportSignal{
		ReportID: req.ReportID, Node: req.Node, NodeVisitID: state.CurrentNodeVisitID, Report: req.Report,
	}); err != nil {
		if isWorkflowNotFound(err) {
			if info, describeErr := e.describeInfo(ctx, req.RunID); describeErr == nil && info.Status != enumspb.WORKFLOW_EXECUTION_STATUS_RUNNING {
				return run.ReportAck{Accepted: true, Duplicate: true}, nil
			}
		}
		return run.ReportAck{}, fmt.Errorf("signal Temporal report %s for %s: %w", req.ReportID, req.RunID, err)
	}
	return run.ReportAck{Accepted: true}, nil
}

// CancelRun requests cancellation of the exact app Workflow ID. Cleanup and
// the final canceled projection state are performed by the workflow.
func (e *Engine) CancelRun(ctx context.Context, id run.ID, reason string) error {
	current, err := e.runs.Get(ctx, id)
	if err != nil {
		if projection.IsNotFound(err) {
			// A request for an older attempt must never be redirected to the
			// current attempt. It is a stale no-op when a newer logical run is
			// present, otherwise preserve the missing-run error.
			logicalID := run.ID(identity.LogicalRunID(id))
			if latest, lookupErr := e.runs.FindByLogicalID(ctx, logicalID); lookupErr == nil && latest.ID != id {
				return nil
			}
		}
		return fmt.Errorf("resolve run %s: %w", id, err)
	}
	// Terminal and canceling attempts are fenced. In particular, explicit
	// restart creates a distinct attempt ID and stale cancellation must not
	// reach that newer Temporal execution.
	if current.State == run.StateCompleted || current.State == run.StateCanceled || current.State == run.StateCanceling {
		return nil
	}
	if err := e.runs.UpdateState(ctx, id, run.StateCanceling, reason, nil); err != nil {
		return err
	}
	c, err := e.ready()
	if err != nil {
		return err
	}
	// Persist the operator's reason before requesting cancellation. The
	// workflow consumes this internal signal during its disconnected cleanup;
	// the public report/plugin wire remains unchanged.
	if err := c.SignalWorkflow(ctx, string(id), "", cancelReasonSignalName, cancelReasonSignal{Reason: reason}); err != nil {
		if isWorkflowNotFound(err) {
			if info, describeErr := e.describeInfo(ctx, id); describeErr == nil && info.Status != enumspb.WORKFLOW_EXECUTION_STATUS_RUNNING {
				state, finished := stateForTemporalStatus(info.Status, info.CloseTime)
				return e.runs.UpdateState(ctx, id, state, reason, finished)
			}
		}
		return fmt.Errorf("signal cancellation reason for Temporal workflow %s: %w", id, err)
	}
	if err := c.CancelWorkflowWithOptions(ctx, client.CancelWorkflowOptions{WorkflowID: string(id), Reason: reason}); err != nil {
		if isWorkflowNotFound(err) {
			if info, describeErr := e.describeInfo(ctx, id); describeErr == nil && info.Status != enumspb.WORKFLOW_EXECUTION_STATUS_RUNNING {
				state, finished := stateForTemporalStatus(info.Status, info.CloseTime)
				return e.runs.UpdateState(ctx, id, state, reason, finished)
			}
		}
		return fmt.Errorf("cancel Temporal workflow %s: %w", id, err)
	}
	return nil
}

func (e *Engine) queryReportState(ctx context.Context, id run.ID, reportID string) (ReportStateSnapshot, error) {
	c, err := e.ready()
	if err != nil {
		return ReportStateSnapshot{}, err
	}
	encoded, err := c.QueryWorkflow(ctx, string(id), "", reportStateQuery, ReportStateQuery{ReportID: reportID})
	if err != nil {
		return ReportStateSnapshot{}, err
	}
	var state ReportStateSnapshot
	if err := encoded.Get(&state); err != nil {
		return ReportStateSnapshot{}, err
	}
	return state, nil
}

func (e *Engine) queryRunState(ctx context.Context, id run.ID, temporalRunID string) (RunStateSnapshot, error) {
	c, err := e.ready()
	if err != nil {
		return RunStateSnapshot{}, err
	}
	encoded, err := c.QueryWorkflow(ctx, string(id), temporalRunID, runStateQuery)
	if err != nil {
		return RunStateSnapshot{}, err
	}
	var state RunStateSnapshot
	if err := encoded.Get(&state); err != nil {
		return RunStateSnapshot{}, err
	}
	return state, nil
}

func (e *Engine) workflowFromHistory(ctx context.Context, id run.ID, temporalRunID string) (*domainworkflow.Workflow, error) {
	c, err := e.ready()
	if err != nil {
		return nil, err
	}
	iterator := c.GetWorkflowHistory(ctx, string(id), temporalRunID, false, enumspb.HISTORY_EVENT_FILTER_TYPE_ALL_EVENT)
	for iterator.HasNext() {
		event, err := iterator.Next()
		if err != nil {
			return nil, fmt.Errorf("read Temporal history for %s: %w", id, err)
		}
		if event.GetEventType() != enumspb.EVENT_TYPE_WORKFLOW_EXECUTION_STARTED {
			continue
		}
		attrs := event.GetWorkflowExecutionStartedEventAttributes()
		if attrs == nil || attrs.Input == nil {
			continue
		}
		var start run.Start
		if err := converter.GetDefaultDataConverter().FromPayloads(attrs.Input, &start); err != nil {
			return nil, fmt.Errorf("decode Temporal snapshot for %s: %w", id, err)
		}
		wf := start.Workflow
		return &wf, nil
	}
	return nil, fmt.Errorf("Temporal workflow %s has no run.Start snapshot", id)
}

func (e *Engine) workflowStart(ctx context.Context, id, temporalRunID string) (run.Start, error) {
	c, err := e.ready()
	if err != nil {
		return run.Start{}, err
	}
	iterator := c.GetWorkflowHistory(ctx, id, temporalRunID, false, enumspb.HISTORY_EVENT_FILTER_TYPE_ALL_EVENT)
	for iterator.HasNext() {
		event, err := iterator.Next()
		if err != nil {
			return run.Start{}, err
		}
		if event.GetEventType() != enumspb.EVENT_TYPE_WORKFLOW_EXECUTION_STARTED {
			continue
		}
		attrs := event.GetWorkflowExecutionStartedEventAttributes()
		if attrs == nil || attrs.Input == nil {
			continue
		}
		var start run.Start
		if err := converter.GetDefaultDataConverter().FromPayloads(attrs.Input, &start); err != nil {
			return run.Start{}, fmt.Errorf("decode Temporal run.Start %s: %w", id, err)
		}
		if start.ID == "" {
			start.ID = run.ID(id)
		}
		if start.ID != run.ID(id) {
			return run.Start{}, fmt.Errorf("Temporal snapshot ID %s does not match Workflow ID %s", start.ID, id)
		}
		if start.LogicalID == "" {
			start.LogicalID = run.ID(identity.LogicalRunID(start.ID))
		}
		if start.AttemptID == 0 {
			start.AttemptID = 1
		}
		return start, nil
	}
	return run.Start{}, fmt.Errorf("Temporal workflow %s has no WorkflowExecutionStarted snapshot", id)
}

func stateForTemporalStatus(status enumspb.WorkflowExecutionStatus, closeTime *timestamppb.Timestamp) (run.State, *time.Time) {
	var finished *time.Time
	if closeTime != nil {
		t := closeTime.AsTime()
		finished = &t
	}
	if status == enumspb.WORKFLOW_EXECUTION_STATUS_CANCELED || status == enumspb.WORKFLOW_EXECUTION_STATUS_TERMINATED {
		return run.StateCanceled, finished
	}
	return run.StateCompleted, finished
}

// restoreProjection reconstructs the derived row and runtime bindings from
// Temporal state. It never calls a task-system mutation or starts a workflow.
func (e *Engine) restoreProjection(ctx context.Context, info *workflowpb.WorkflowExecutionInfo) error {
	if err := validateTemporalExecutionInfo(info, ""); err != nil {
		return err
	}
	id := run.ID(info.Execution.WorkflowId)
	start, err := e.workflowStart(ctx, string(id), info.Execution.RunId)
	if err != nil {
		return err
	}
	started := time.Now().UTC()
	if info.StartTime != nil {
		started = info.StartTime.AsTime()
	}
	if err := e.runs.InsertStart(ctx, start, started); err != nil {
		return err
	}
	state, queryErr := e.queryRunState(ctx, id, info.Execution.RunId)
	if queryErr == nil {
		if state.Run.CurrentNode != "" && state.Run.CurrentNodeVisitID != "" {
			if err := e.runs.UpdateNode(ctx, id, state.Run.State, state.Run.CurrentNode, state.Run.CurrentNodeVisitID); err != nil {
				return err
			}
		}
		for _, binding := range state.RuntimeBindings {
			if binding.Node == "" || binding.NodeVisitID == "" {
				continue
			}
			if err := e.runs.UpdateNodeRuntime(ctx, projection.NodeRuntime{
				RunID: id, Node: binding.Node, TerminalID: binding.TerminalID,
				SessionID: binding.SessionID, NodeVisitID: binding.NodeVisitID,
				UpdatedAt: time.Now().UTC(),
			}); err != nil {
				return err
			}
		}
		if state.Run.State == run.StateCompleted || state.Run.State == run.StateCanceled {
			finished := state.Run.FinishedAt
			if finished == nil && info.CloseTime != nil {
				closeTime := info.CloseTime.AsTime()
				finished = &closeTime
			}
			return e.runs.UpdateState(ctx, id, state.Run.State, state.Run.LastError, finished)
		}
		if info.Status != enumspb.WORKFLOW_EXECUTION_STATUS_RUNNING {
			stateValue, finished := stateForTemporalStatus(info.Status, info.CloseTime)
			return e.runs.UpdateState(ctx, id, stateValue, state.Run.LastError, finished)
		}
		if err := e.runs.UpdateState(ctx, id, state.Run.State, state.Run.LastError, nil); err != nil {
			return err
		}
		if err := e.runs.UpdateRetry(ctx, id, state.Run.Retry); err != nil {
			return err
		}
		if err := e.rediscoverMissingTerminals(ctx, start, state); err != nil {
			return err
		}
		return nil
	}
	stateValue, finished := stateForTemporalStatus(info.Status, info.CloseTime)
	if info.Status == enumspb.WORKFLOW_EXECUTION_STATUS_RUNNING {
		return fmt.Errorf("query Temporal run-state for active workflow %s: %w", id, queryErr)
	}
	return e.runs.UpdateState(ctx, id, stateValue, "", finished)
}
