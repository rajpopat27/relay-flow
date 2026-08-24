package run

import (
	"context"
	"fmt"
	"log/slog"
	"sync"

	"github.com/rajpopat27/relay-flow/internal/identity"
	"github.com/rajpopat27/relay-flow/internal/repo"
	"github.com/rajpopat27/relay-flow/internal/task"
	"github.com/rajpopat27/relay-flow/internal/workflow"
)

// CancellationMarker is the stable parent comment marker recording that the
// run was canceled; a missing claimed run carrying it is never recreated.
func CancellationMarker(id ID) string {
	return string(id) + ":cancellation"
}

// RunManager performs only assignment and durable-run creation.
type RunManager struct {
	Executor Executor
	Runs     RunQueries
	// Gate, when non-nil, is the lifecycle mutex shared with
	// workflow.Service.Submit/Remove (design.md decision 23): run creation
	// holds it from final workflow resolution through claim + EnsureRun so
	// a run never starts against a workflow definition that is concurrently
	// being replaced or removed. A plain *sync.Mutex — no lock service.
	Gate *sync.Mutex
}

// EnsureRun claims the ticket if unassigned, skips claiming when the ticket
// is already assigned to this workflow, checks the stable cancellation
// marker before recreating a missing claimed run, then ensures the durable
// run with a value snapshot of the workflow.
func (m *RunManager) EnsureRun(ctx context.Context, rp *repo.Repo, wf *workflow.Workflow, ticket task.Ticket) error {
	if m.Gate != nil {
		m.Gate.Lock()
		defer m.Gate.Unlock()
	}
	id := identity.NewRunID(rp.Name, wf.Name, ticket.Key)
	claimed := false
	for _, c := range ticket.WorkflowClaims {
		if c == "wf:"+wf.Name {
			claimed = true
			break
		}
	}
	if !claimed {
		if err := rp.TaskSystem.Claim(ctx, ticket.Ref(), wf.Name); err != nil {
			slog.Info("ensure-run outcome",
				"ticket", ticket.Key, "repo", rp.Name, "workflow", wf.Name, "runID", string(id),
				"outcome", "error", "stage", "claim", "error", err)
			return fmt.Errorf("claim %s for workflow %s: %w", ticket.Key, wf.Name, err)
		}
	} else {
		// Claimed but possibly missing its run (claim-before-run crash gap or
		// retention cleanup): never recreate a canceled run.
		marked, err := rp.TaskSystem.HasComment(ctx, task.Target{Parent: ticket.Ref()}, CancellationMarker(id))
		if err != nil {
			slog.Info("ensure-run outcome",
				"ticket", ticket.Key, "repo", rp.Name, "workflow", wf.Name, "runID", string(id),
				"outcome", "error", "stage", "cancellation-marker", "error", err)
			return fmt.Errorf("check cancellation marker on %s: %w", ticket.Key, err)
		}
		if marked {
			slog.Info("ensure-run outcome",
				"ticket", ticket.Key, "repo", rp.Name, "workflow", wf.Name, "runID", string(id),
				"outcome", "skipped-cancellation-marker")
			return nil
		}
	}
	created, err := m.Executor.EnsureRun(ctx, Start{
		ID:       id,
		Repo:     rp.Name,
		RepoPath: rp.Path,
		Workflow: *wf,
		Ticket:   ticket.Ref(),
	})
	if err != nil {
		slog.Info("ensure-run outcome",
			"ticket", ticket.Key, "repo", rp.Name, "workflow", wf.Name, "runID", string(id),
			"outcome", "error", "stage", "executor", "error", err)
		return fmt.Errorf("ensure run %s: %w", id, err)
	}
	outcome := "exists"
	if created {
		outcome = "created"
	}
	slog.Info("ensure-run outcome",
		"ticket", ticket.Key, "repo", rp.Name, "workflow", wf.Name, "runID", string(id),
		"outcome", outcome)
	return nil
}

// CancelByTicket resolves the active run through FindRunByTicket, then
// calls Executor.CancelRun.
func (m *RunManager) CancelByTicket(ctx context.Context, ticket, reason string) error {
	r, err := m.Runs.FindRunByTicket(ctx, ticket)
	if err != nil {
		return fmt.Errorf("find run for ticket %s: %w", ticket, err)
	}
	if err := m.Executor.CancelRun(ctx, r.ID, reason); err != nil {
		return fmt.Errorf("cancel run %s: %w", r.ID, err)
	}
	return nil
}
