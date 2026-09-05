package temporal

import (
	"bytes"
	"context"
	"testing"
	"time"

	commonpb "go.temporal.io/api/common/v1"
	enumspb "go.temporal.io/api/enums/v1"
	workflowpb "go.temporal.io/api/workflow/v1"
	workflowservice "go.temporal.io/api/workflowservice/v1"
	"google.golang.org/protobuf/types/known/timestamppb"
)

func TestRecoveryWorkerOptionsAreWorkflowOnly(t *testing.T) {
	recovery := workerOptions(nil, true)
	if !recovery.LocalActivityWorkerOnly {
		t.Fatal("recovery worker enables non-local activities")
	}
	if recovery.MaxConcurrentWorkflowTaskExecutionSize != 10 || recovery.MaxConcurrentWorkflowTaskPollers != 2 {
		t.Fatalf("recovery workflow options = %+v", recovery)
	}
	normal := workerOptions(nil, false)
	if normal.LocalActivityWorkerOnly {
		t.Fatal("normal worker was restricted to local activities")
	}
}

func TestShouldRestoreTemporalExecutionHonorsLocalRetention(t *testing.T) {
	now := time.Date(2026, 9, 4, 0, 0, 0, 0, time.UTC)
	retention := 30 * 24 * time.Hour
	if !shouldRestoreTemporalExecution(&workflowpb.WorkflowExecutionInfo{Status: enumspb.WORKFLOW_EXECUTION_STATUS_RUNNING}, now, retention) {
		t.Fatal("active execution was filtered by local retention")
	}
	recent := timestamppb.New(now.Add(-retention + time.Minute))
	if !shouldRestoreTemporalExecution(&workflowpb.WorkflowExecutionInfo{Status: enumspb.WORKFLOW_EXECUTION_STATUS_COMPLETED, CloseTime: recent}, now, retention) {
		t.Fatal("recent closed execution was filtered")
	}
	old := timestamppb.New(now.Add(-retention - time.Minute))
	if shouldRestoreTemporalExecution(&workflowpb.WorkflowExecutionInfo{Status: enumspb.WORKFLOW_EXECUTION_STATUS_COMPLETED, CloseTime: old}, now, retention) {
		t.Fatal("expired closed execution was restored")
	}
}

func TestSelectTemporalExecutionsKeepsOneCurrentHistoryPerWorkflowID(t *testing.T) {
	now := time.Date(2026, 9, 4, 0, 0, 0, 0, time.UTC)
	info := func(runID string, status enumspb.WorkflowExecutionStatus, started time.Time) *workflowpb.WorkflowExecutionInfo {
		return &workflowpb.WorkflowExecutionInfo{
			Execution: &commonpb.WorkflowExecution{WorkflowId: "same-id", RunId: runID},
			Type:      &commonpb.WorkflowType{Name: TicketWorkflowName}, TaskQueue: TaskQueue,
			Status: status, StartTime: timestamppb.New(started),
		}
	}
	selected := selectTemporalExecutions([]*workflowpb.WorkflowExecutionInfo{
		info("old", enumspb.WORKFLOW_EXECUTION_STATUS_COMPLETED, now.Add(-time.Hour)),
		info("current", enumspb.WORKFLOW_EXECUTION_STATUS_RUNNING, now),
		{
			Execution: &commonpb.WorkflowExecution{WorkflowId: "other", RunId: "other"},
			Type:      &commonpb.WorkflowType{Name: "OtherWorkflow"}, TaskQueue: TaskQueue,
			Status: enumspb.WORKFLOW_EXECUTION_STATUS_RUNNING, StartTime: timestamppb.New(now),
		},
	}, now, 30*24*time.Hour)
	if len(selected) != 1 || selected[0].Execution.RunId != "current" {
		t.Fatalf("selected histories = %#v", selected)
	}
}

func TestListWorkflowExecutionsPaginatesWithExactFilter(t *testing.T) {
	var requests []*workflowservice.ListWorkflowExecutionsRequest
	list := func(_ context.Context, request *workflowservice.ListWorkflowExecutionsRequest) (*workflowservice.ListWorkflowExecutionsResponse, error) {
		requests = append(requests, request)
		if len(requests) == 1 {
			return &workflowservice.ListWorkflowExecutionsResponse{
				Executions:    []*workflowpb.WorkflowExecutionInfo{{Execution: &commonpb.WorkflowExecution{WorkflowId: "first"}}},
				NextPageToken: []byte("next-page"),
			}, nil
		}
		return &workflowservice.ListWorkflowExecutionsResponse{
			Executions: []*workflowpb.WorkflowExecutionInfo{{Execution: &commonpb.WorkflowExecution{WorkflowId: "second"}}},
		}, nil
	}
	executions, err := listWorkflowExecutions(context.Background(), list)
	if err != nil {
		t.Fatal(err)
	}
	if len(executions) != 2 || executions[0].Execution.WorkflowId != "first" || executions[1].Execution.WorkflowId != "second" {
		t.Fatalf("executions = %#v", executions)
	}
	if len(requests) != 2 || requests[0].PageSize != 100 || requests[1].PageSize != 100 {
		t.Fatalf("pagination requests = %#v", requests)
	}
	wantQuery := "WorkflowType = '" + TicketWorkflowName + "' AND TaskQueue = '" + TaskQueue + "'"
	for i, request := range requests {
		if request.Query != wantQuery {
			t.Fatalf("request %d query = %q, want %q", i, request.Query, wantQuery)
		}
	}
	if !bytes.Equal(requests[1].NextPageToken, []byte("next-page")) {
		t.Fatalf("second page token = %q", requests[1].NextPageToken)
	}
}
