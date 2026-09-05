package temporal

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"time"

	"github.com/rajpopat27/relay-flow/internal/identity"
	enumspb "go.temporal.io/api/enums/v1"
	"go.temporal.io/api/serviceerror"
	workflowpb "go.temporal.io/api/workflow/v1"
	workflowservice "go.temporal.io/api/workflowservice/v1"
)

// rebuildProjection reconstructs only relay-owned SQLite state. Temporal
// histories remain untouched and task/runner/harness dependencies are never
// called from this path.
func (e *Engine) rebuildProjection(ctx context.Context) error {
	if e.client == nil {
		return fmt.Errorf("Temporal client is not connected")
	}
	// Querying an active workflow requires a workflow worker to replay its
	// history and install the query handlers. LocalActivityWorkerOnly prevents
	// this recovery worker from running any external activity.
	recoveryWorker := e.newWorker(true)
	if err := recoveryWorker.Start(); err != nil {
		stopWorker(recoveryWorker)
		return fmt.Errorf("start Temporal recovery worker: %w", err)
	}
	defer stopWorker(recoveryWorker)

	executions, err := listWorkflowExecutions(ctx, func(callCtx context.Context, request *workflowservice.ListWorkflowExecutionsRequest) (*workflowservice.ListWorkflowExecutionsResponse, error) {
		return e.client.ListWorkflow(callCtx, request)
	})
	if err != nil {
		return fmt.Errorf("enumerate Temporal TicketWorkflow executions: %w", err)
	}
	selected := selectTemporalExecutions(executions, time.Now().UTC(), e.retention)
	// The boolean records whether the selected Visibility history is a
	// confirmed running execution. A retained closed history is deliberately
	// false so claimed-parent reconciliation performs exact DescribeWorkflowExecution
	// and can discover a newer active execution for the same Workflow ID.
	visible := map[string]bool{}
	for _, info := range selected {
		visible[info.Execution.WorkflowId] = info.Status == enumspb.WORKFLOW_EXECUTION_STATUS_RUNNING
		if info.Status == enumspb.WORKFLOW_EXECUTION_STATUS_RUNNING {
			if err := e.restoreProjection(ctx, info); err != nil {
				return fmt.Errorf("rebuild active Temporal workflow %s: %w", info.Execution.WorkflowId, err)
			}
			continue
		}
		if err := e.restoreProjection(ctx, info); err != nil {
			return fmt.Errorf("rebuild Temporal workflow %s: %w", info.Execution.WorkflowId, err)
		}
	}
	return e.reconcileClaimedParents(ctx, visible)
}

func shouldRestoreTemporalExecution(info *workflowpb.WorkflowExecutionInfo, now time.Time, retention time.Duration) bool {
	if info == nil || info.Status == enumspb.WORKFLOW_EXECUTION_STATUS_RUNNING || info.CloseTime == nil {
		return true
	}
	return !info.CloseTime.AsTime().Before(now.Add(-retention))
}

func selectTemporalExecutions(infos []*workflowpb.WorkflowExecutionInfo, now time.Time, retention time.Duration) []*workflowpb.WorkflowExecutionInfo {
	selected := make(map[string]*workflowpb.WorkflowExecutionInfo)
	for _, info := range infos {
		if info == nil || info.Execution == nil || info.Type == nil || info.Type.Name != TicketWorkflowName || info.TaskQueue != TaskQueue || !shouldRestoreTemporalExecution(info, now, retention) {
			continue
		}
		id := info.Execution.WorkflowId
		current, ok := selected[id]
		if !ok {
			selected[id] = info
			continue
		}
		other := info
		if temporalExecutionPreferred(info, current) {
			selected[id] = info
			other = current
		}
		slog.Warn("multiple Temporal executions for Workflow ID; selecting one", "workflowID", id, "selectedRunID", selected[id].Execution.RunId, "otherRunID", other.Execution.RunId, "selectedStatus", selected[id].Status, "otherStatus", other.Status)
	}
	ids := make([]string, 0, len(selected))
	for id := range selected {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	out := make([]*workflowpb.WorkflowExecutionInfo, 0, len(ids))
	for _, id := range ids {
		out = append(out, selected[id])
	}
	return out
}

func temporalExecutionPreferred(candidate, current *workflowpb.WorkflowExecutionInfo) bool {
	candidateRunning := candidate.Status == enumspb.WORKFLOW_EXECUTION_STATUS_RUNNING
	currentRunning := current.Status == enumspb.WORKFLOW_EXECUTION_STATUS_RUNNING
	if candidateRunning != currentRunning {
		return candidateRunning
	}
	if candidate.StartTime != nil && current.StartTime != nil && !candidate.StartTime.AsTime().Equal(current.StartTime.AsTime()) {
		return candidate.StartTime.AsTime().After(current.StartTime.AsTime())
	}
	if candidate.CloseTime != nil && current.CloseTime != nil && !candidate.CloseTime.AsTime().Equal(current.CloseTime.AsTime()) {
		return candidate.CloseTime.AsTime().After(current.CloseTime.AsTime())
	}
	return candidate.Execution.RunId > current.Execution.RunId
}

func listWorkflowExecutions(ctx context.Context, list func(context.Context, *workflowservice.ListWorkflowExecutionsRequest) (*workflowservice.ListWorkflowExecutionsResponse, error)) ([]*workflowpb.WorkflowExecutionInfo, error) {
	var token []byte
	var executions []*workflowpb.WorkflowExecutionInfo
	for {
		response, err := list(ctx, &workflowservice.ListWorkflowExecutionsRequest{
			PageSize:      100,
			Query:         fmt.Sprintf("WorkflowType = '%s' AND TaskQueue = '%s'", TicketWorkflowName, TaskQueue),
			NextPageToken: token,
		})
		if err != nil {
			return nil, err
		}
		if response == nil {
			return nil, fmt.Errorf("empty response")
		}
		executions = append(executions, response.Executions...)
		if len(response.NextPageToken) == 0 {
			return executions, nil
		}
		token = response.NextPageToken
	}
}

// reconcileClaimedParents closes the Visibility lag window without creating
// anything. Task-system polling is read-only; an exact deterministic Workflow
// ID is described and restored only when Temporal already owns that execution.
func (e *Engine) reconcileClaimedParents(ctx context.Context, visible map[string]bool) error {
	for _, rp := range e.deps.Repos.List() {
		tickets, err := rp.TaskSystem.Poll(ctx)
		if err != nil {
			return fmt.Errorf("poll repo %q during Temporal recovery: %w", rp.Name, err)
		}
		for _, ticket := range tickets {
			for _, claim := range ticket.WorkflowClaims {
				if !strings.HasPrefix(claim, "wf:") {
					continue
				}
				workflowName := strings.TrimPrefix(claim, "wf:")
				var matched bool
				for _, binding := range rp.Bindings() {
					if binding.Workflow != nil && binding.Workflow.Name == workflowName {
						matched = true
						break
					}
				}
				if !matched {
					continue
				}
				expectedID := identity.NewRunID(rp.Name, workflowName, ticket.Key)
				if visible[string(expectedID)] {
					continue
				}
				info, err := e.client.DescribeWorkflowExecution(ctx, string(expectedID), "")
				if err != nil {
					var notFound *serviceerror.NotFound
					if errors.As(err, &notFound) {
						// This is the documented claim-before-run gap. Record it
						// for operators, but leave creation to normal polling after
						// recovery; this path must never start a replacement.
						slog.Info("Temporal recovery missing claimed execution", "repo", rp.Name, "workflow", workflowName, "ticket", ticket.Key, "runID", string(expectedID))
						continue
					}
					return fmt.Errorf("describe claimed Temporal workflow %s: %w", expectedID, err)
				}
				if info == nil || info.WorkflowExecutionInfo == nil || info.WorkflowExecutionInfo.Execution == nil ||
					info.WorkflowExecutionInfo.Type == nil || info.WorkflowExecutionInfo.Type.Name != TicketWorkflowName || info.WorkflowExecutionInfo.TaskQueue != TaskQueue {
					continue
				}
				if !shouldRestoreTemporalExecution(info.WorkflowExecutionInfo, time.Now().UTC(), e.retention) {
					continue
				}
				if err := e.restoreProjection(ctx, info.WorkflowExecutionInfo); err != nil {
					return fmt.Errorf("restore claimed Temporal workflow %s: %w", expectedID, err)
				}
			}
		}
	}
	return nil
}
