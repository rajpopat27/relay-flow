package temporal

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"github.com/rajpopat27/relay-flow/internal/execution/projection"
	"github.com/rajpopat27/relay-flow/internal/run"
	"github.com/rajpopat27/relay-flow/internal/task"
	"github.com/rajpopat27/relay-flow/internal/workflow"
	commonpb "go.temporal.io/api/common/v1"
	workflowpb "go.temporal.io/api/workflow/v1"
	_ "modernc.org/sqlite"
)

func TestValidateTemporalExecutionIdentity(t *testing.T) {
	valid := func(workflowID, workflowType, taskQueue string) *workflowpb.WorkflowExecutionInfo {
		return &workflowpb.WorkflowExecutionInfo{
			Execution: &commonpb.WorkflowExecution{WorkflowId: workflowID},
			Type:      &commonpb.WorkflowType{Name: workflowType}, TaskQueue: taskQueue,
		}
	}
	if err := validateTemporalExecutionInfo(valid("repo/wf/TICKET", TicketWorkflowName, TaskQueue), run.ID("repo/wf/TICKET")); err != nil {
		t.Fatalf("valid execution identity: %v", err)
	}
	if err := validateTemporalExecutionInfo(valid("repo/wf/TICKET", "OtherWorkflow", TaskQueue), run.ID("repo/wf/TICKET")); err == nil {
		t.Fatal("wrong workflow type was accepted")
	}
	if err := validateTemporalExecutionInfo(valid("repo/wf/TICKET", TicketWorkflowName, "other-queue"), run.ID("repo/wf/TICKET")); err == nil {
		t.Fatal("wrong task queue was accepted")
	}
	if err := validateTemporalExecutionInfo(valid("other/wf/TICKET", TicketWorkflowName, TaskQueue), run.ID("repo/wf/TICKET")); err == nil {
		t.Fatal("wrong Workflow ID was accepted")
	}
}

func TestProjectionBookkeepingErrorsPropagateToTemporalActivities(t *testing.T) {
	path := t.TempDir() + "/state.db"
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	proj := &projection.RunProjection{DB: db}
	if err := proj.Migrate(); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	activities := &Activities{Runs: proj}
	id := run.ID("repo/wf/TICKET")
	if err := activities.ProjectionUpdateNode(context.Background(), id, run.StateWaiting, "node", "visit"); err == nil {
		t.Fatal("ProjectionUpdateNode swallowed a closed-database error")
	}
	if err := activities.ProjectionUpdateNodeRuntimeVisit(context.Background(), id, "node", "visit"); err == nil {
		t.Fatal("ProjectionUpdateNodeRuntimeVisit swallowed a closed-database error")
	}
	if err := activities.ProjectionRecordProcessedReport(context.Background(), id, "visit", "report"); err == nil {
		t.Fatal("ProjectionRecordProcessedReport swallowed a closed-database error")
	}
	if err := activities.ProjectionUpdateState(context.Background(), id, run.StateWaiting, "", nil); err == nil {
		t.Fatal("ProjectionUpdateState swallowed a closed-database error")
	}
}

func TestCancelRunFencesTerminalAttempt(t *testing.T) {
	path := t.TempDir() + "/state.db"
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	proj := &projection.RunProjection{DB: db}
	if err := proj.Migrate(); err != nil {
		t.Fatal(err)
	}
	start := run.Start{
		ID: "repo/workflow/TICKET", Repo: "repo", Workflow: workflow.Workflow{Name: "workflow"},
		Ticket: task.TicketRef{ID: "TICKET", Key: "TICKET"},
	}
	if err := proj.InsertStart(context.Background(), start, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	finished := time.Now().UTC()
	if err := proj.UpdateState(context.Background(), start.ID, run.StateCanceled, "canceled", &finished); err != nil {
		t.Fatal(err)
	}
	engine := &Engine{runs: proj}
	if err := engine.CancelRun(context.Background(), start.ID, "stale cancellation"); err != nil {
		t.Fatalf("CancelRun terminal attempt: %v", err)
	}
	got, err := proj.Get(context.Background(), start.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.State != run.StateCanceled || got.LastError != "canceled" {
		t.Fatalf("terminal attempt changed: %+v", got)
	}
}
