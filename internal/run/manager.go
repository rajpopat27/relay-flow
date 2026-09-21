package run

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/rajpopat27/relay-flow/internal/identity"
	"github.com/rajpopat27/relay-flow/internal/repo"
	"github.com/rajpopat27/relay-flow/internal/task"
	"github.com/rajpopat27/relay-flow/internal/workflow"
)

// CancellationMarker is the stable parent comment marker recording that the
// logical run was canceled; a missing claimed run carrying it is never
// recreated by normal polling.
func CancellationMarker(id ID) string {
	return string(id) + ":cancellation"
}

// RunManager performs assignment and durable-run creation. Repos and
// Workflows are only needed by the explicit restart operation; normal poll
// handling continues to receive the already-resolved repo/workflow values.
type RunManager struct {
	Executor Executor
	Runs     RunQueries
	// Gate, when non-nil, is the lifecycle mutex shared with
	// workflow.Service.Submit/Remove (design.md decision 23): run creation
	// holds it from final workflow resolution through claim + EnsureRun so
	// a run never starts against a workflow definition that is concurrently
	// being replaced or removed. A plain *sync.Mutex — no lock service.
	Gate *sync.Mutex

	// Repos and Workflows resolve the current task-system repo and latest
	// workflow snapshot for an explicit restart. They are concrete registries,
	// not task/runner/harness-specific dependencies.
	Repos     *repo.Registry
	Workflows *workflow.Registry
}

func terminalState(state State) bool {
	return state == StateCompleted || state == StateCanceled
}

func activeState(state State) bool {
	return !terminalState(state) && state != StateCanceling
}

func newerRun(candidate, current Run) bool {
	if candidate.StartedAt.After(current.StartedAt) {
		return true
	}
	if candidate.StartedAt.Equal(current.StartedAt) && candidate.UpdatedAt.After(current.UpdatedAt) {
		return true
	}
	return false
}

// EnsureRun claims the ticket if unassigned, skips claiming when the ticket
// is already assigned to this workflow, reuses the newest active execution
// attempt, checks the stable logical cancellation marker before recreating a
// missing claimed run, then ensures the durable run with a value snapshot of
// the workflow. If an already-running ticket is later reassigned, automatic
// polling keeps the original claim/run with its original owner and refuses
// to reconcile it; ownership transfer is an explicit operator action, never
// an automatic duplicate run or cross-database handoff.
func (m *RunManager) EnsureRun(ctx context.Context, rp *repo.Repo, wf *workflow.Workflow, ticket task.Ticket) error {
	if rp == nil {
		return fmt.Errorf("ensure run: repository is unavailable")
	}
	if rp.TaskSystem == nil {
		if rp.TaskSystemError != nil {
			return fmt.Errorf("ensure run repo %q: task system unavailable: %w", rp.Name, rp.TaskSystemError)
		}
		return fmt.Errorf("ensure run repo %q: task system unavailable", rp.Name)
	}
	if wf == nil {
		return fmt.Errorf("ensure run repo %q: workflow is unavailable", rp.Name)
	}
	capabilities, ok := rp.TaskSystem.(task.OwnershipCapabilities)
	if !ok {
		return fmt.Errorf("ensure run repo %q: task system lacks required ownership capabilities", rp.Name)
	}
	if m.Gate != nil {
		m.Gate.Lock()
		defer m.Gate.Unlock()
	}
	// Poll routing resolves a binding before entering this method. Resolve
	// the registry again while holding the same lifecycle gate used by submit
	// and remove so a concurrent replacement cannot create a run from the old
	// workflow snapshot.
	if m.Workflows != nil {
		current, ok := m.Workflows.Get(wf.Name)
		if !ok {
			return fmt.Errorf("ensure run repo %q: workflow %q is no longer stored", rp.Name, wf.Name)
		}
		wf = current
	}
	if !wf.IsRoutable() {
		return fmt.Errorf("ensure run repo %q: workflow %q is %s and cannot start a new run", rp.Name, wf.Name, wf.Status)
	}
	if m.Workflows != nil {
		targeted := false
		for _, repoName := range wf.Repos {
			if repoName == rp.Name {
				targeted = true
				break
			}
		}
		if !targeted {
			return fmt.Errorf("ensure run repo %q: workflow %q no longer targets this repository", rp.Name, wf.Name)
		}
	}
	id := identity.NewRunID(rp.Name, wf.Name, ticket.Key)
	claimed := false
	for _, c := range ticket.WorkflowClaims {
		if c == "wf:"+wf.Name {
			claimed = true
			break
		}
	}

	// Router ownership is evaluated from the poll snapshot. Revalidate a
	// claimed ticket through the adapter before any run lookup, cancellation
	// marker check, or durable execution creation so a reassignment cannot
	// reach the claim-before-run recovery path. Explicit run restart is a
	// separate operator-authorized path and does not use this automatic gate.
	if claimed {
		// Re-apply the published poll-snapshot matcher as a guard for direct
		// callers that bypass the router. The adapter re-read below remains the
		// race-closing check for provider state that changed after Poll.
		for _, binding := range rp.Bindings() {
			if binding.Workflow == nil || binding.Workflow.Name != wf.Name || binding.Ownership == nil {
				continue
			}
			if !binding.Ownership(ticket) {
				err := &task.OwnershipMismatchError{Ticket: ticket.Key, Workflow: wf.Name}
				slog.Info("ensure-run outcome",
					"ticket", ticket.Key, "repo", rp.Name, "workflow", wf.Name,
					"runID", string(id), "outcome", "claim-owner-mismatch", "stage", "ownership", "error", err)
				return fmt.Errorf("validate ownership for claimed ticket %s: %w", ticket.Key, err)
			}
			break
		}
		if err := capabilities.ValidateOwnership(ctx, ticket.Ref(), wf.Name, wf.TaskConfig); err != nil {
			outcome := "ownership-error"
			if errors.Is(err, task.ErrOwnershipMismatch) {
				outcome = "claim-owner-mismatch"
			}
			slog.Info("ensure-run outcome",
				"ticket", ticket.Key, "repo", rp.Name, "workflow", wf.Name,
				"runID", string(id), "outcome", outcome, "stage", "ownership", "error", err)
			return fmt.Errorf("validate ownership for claimed ticket %s: %w", ticket.Key, err)
		}
	}

	var candidates []Run
	if m.Runs != nil {
		var err error
		candidates, err = m.Runs.ListRuns(ctx, Filter{Repo: rp.Name, Workflow: wf.Name, Ticket: ticket.Key})
		if err != nil {
			return fmt.Errorf("check existing run %s: %w", id, err)
		}
	}
	var latest Run
	latestSet := false
	var active *Run
	for _, candidate := range candidates {
		if candidate.LogicalID == "" {
			candidate.LogicalID = id
		}
		if candidate.AttemptID == 0 {
			candidate.AttemptID = 1
		}
		if !latestSet || newerRun(candidate, latest) {
			latest = candidate
			latestSet = true
		}
		if activeState(candidate.State) && candidate.ID != "" && (active == nil || newerRun(candidate, *active)) {
			copy := candidate
			active = &copy
		}
	}

	// A restarted attempt (including a blocked one) is the current execution
	// and must be ensured by its fenced ID. Do not inspect the cancellation
	// marker for an active attempt.
	if active != nil {
		return m.ensure(ctx, Start{
			ID: active.ID, LogicalID: active.LogicalID, AttemptID: active.AttemptID,
			Repo: rp.Name, RepoPath: rp.Path, Workflow: *wf, Ticket: ticket.Ref(),
		})
	}

	// Canceling/canceled/completed executions are terminal for normal polling.
	// Only the explicit restart operation may create a new attempt.
	if latestSet && (latest.State == StateCanceling || terminalState(latest.State)) {
		return nil
	}

	if !claimed {
		err := capabilities.ClaimIfOwned(ctx, ticket.Ref(), wf.Name, wf.TaskConfig)
		if err != nil {
			outcome := "error"
			if errors.Is(err, task.ErrOwnershipMismatch) {
				outcome = "claim-owner-mismatch"
			}
			slog.Info("ensure-run outcome",
				"ticket", ticket.Key, "repo", rp.Name, "workflow", wf.Name, "runID", string(id),
				"outcome", outcome, "stage", "claim", "error", err)
			return fmt.Errorf("claim %s for workflow %s: %w", ticket.Key, wf.Name, err)
		}
	} else {
		// Claimed but missing its run (claim-before-run crash gap or retention
		// cleanup): never recreate a canceled logical run.
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
	return m.ensure(ctx, Start{
		ID: id, LogicalID: id, AttemptID: 1,
		Repo: rp.Name, RepoPath: rp.Path, Workflow: *wf, Ticket: ticket.Ref(),
	})
}

func (m *RunManager) ensure(ctx context.Context, start Start) error {
	if m.Executor == nil {
		return fmt.Errorf("ensure run %s: executor is not configured", start.ID)
	}
	created, err := m.Executor.EnsureRun(ctx, start)
	if err != nil {
		slog.Info("ensure-run outcome",
			"ticket", start.Ticket.Key, "repo", start.Repo, "workflow", start.Workflow.Name, "runID", string(start.ID),
			"outcome", "error", "stage", "executor", "error", err)
		return fmt.Errorf("ensure run %s: %w", start.ID, err)
	}
	outcome := "exists"
	if created {
		outcome = "created"
	}
	slog.Info("ensure-run outcome",
		"ticket", start.Ticket.Key, "repo", start.Repo, "workflow", start.Workflow.Name, "runID", string(start.ID),
		"outcome", outcome)
	return nil
}

// BackfillClaimOwner performs an explicit provider-side repair for a legacy
// workflow claim. It only adds adapter-owned provenance; it never routes,
// starts, cancels, or recreates a run and never removes the wf: claim.
func (m *RunManager) BackfillClaimOwner(ctx context.Context, repoName, ticket, workflowName string) error {
	if m.Gate != nil {
		m.Gate.Lock()
		defer m.Gate.Unlock()
	}
	if m.Repos == nil || m.Workflows == nil {
		return fmt.Errorf("backfill claim owner: repository/workflow registries are not configured")
	}
	rp, ok := m.Repos.Get(repoName)
	if !ok {
		return fmt.Errorf("backfill claim owner: repo %q is not registered", repoName)
	}
	wf, ok := m.Workflows.Get(workflowName)
	if !ok {
		return fmt.Errorf("backfill claim owner: workflow %q is not stored", workflowName)
	}
	bound := false
	for _, name := range wf.Repos {
		if name == repoName {
			bound = true
			break
		}
	}
	if !bound {
		return fmt.Errorf("backfill claim owner: workflow %q does not target repo %q", workflowName, repoName)
	}
	backfiller, ok := rp.TaskSystem.(task.ClaimOwnerBackfiller)
	if !ok {
		return fmt.Errorf("backfill claim owner: task system for repo %q does not support explicit ownership backfill", repoName)
	}
	if err := backfiller.BackfillClaimOwner(ctx, task.TicketRef{Key: ticket}, workflowName, wf.TaskConfig); err != nil {
		return fmt.Errorf("backfill claim owner for %s: %w", ticket, err)
	}
	slog.Info("claim-owner backfill", "repo", repoName, "ticket", ticket, "workflow", workflowName, "outcome", "provenance-added")
	return nil
}

// RestartByTicket creates or returns the one active fresh attempt for a
// canceled ticket. It resolves the repo and latest workflow from the current
// registries, so the new durable snapshot is never copied from the canceled
// attempt. External task/runner/harness behavior remains behind their normal
// boundaries and is performed by the durable workflow after creation.
func (m *RunManager) RestartByTicket(ctx context.Context, ticket string) (Run, error) {
	if m.Gate != nil {
		m.Gate.Lock()
		defer m.Gate.Unlock()
	}
	if m.Runs == nil {
		return Run{}, fmt.Errorf("restart %s: run queries are not configured", ticket)
	}
	previous, err := m.Runs.FindRunByTicket(ctx, ticket)
	if err != nil {
		return Run{}, fmt.Errorf("find run for ticket %s: %w", ticket, err)
	}

	// Repeating the command while the fresh attempt is active is idempotent.
	// A canceling attempt is deliberately not treated as active: callers must
	// wait for cancellation cleanup to finish before starting another attempt.
	if activeState(previous.State) && previous.AttemptID != 0 {
		return previous, nil
	}
	if previous.State == StateCanceling {
		return Run{}, fmt.Errorf("%w: run %s is still canceling; wait for cancellation to finish", ErrRestartConflict, previous.ID)
	}
	if previous.State != StateCanceled {
		return Run{}, fmt.Errorf("%w: run %s is %s; only canceled runs can be restarted", ErrRestartConflict, previous.ID, previous.State)
	}
	if m.Executor == nil || m.Repos == nil || m.Workflows == nil {
		return Run{}, fmt.Errorf("restart %s: restart dependencies are not configured", ticket)
	}

	rp, ok := m.Repos.Get(previous.Repo)
	if !ok {
		return Run{}, fmt.Errorf("%w: repo %q for canceled run %s is no longer registered", ErrRestartConflict, previous.Repo, previous.ID)
	}
	wf, ok := m.Workflows.Get(previous.Workflow)
	if !ok {
		return Run{}, fmt.Errorf("%w: workflow %q for canceled run %s is no longer stored", ErrRestartConflict, previous.Workflow, previous.ID)
	}
	if !wf.IsRoutable() {
		return Run{}, fmt.Errorf("%w: workflow %q is %s and cannot be restarted", ErrRestartConflict, wf.Name, wf.Status)
	}
	bound := false
	for _, name := range wf.Repos {
		if name == previous.Repo {
			bound = true
			break
		}
	}
	if !bound {
		return Run{}, fmt.Errorf("%w: workflow %q no longer targets repo %q", ErrRestartConflict, wf.Name, previous.Repo)
	}

	logicalID := previous.LogicalID
	if logicalID == "" {
		logicalID = identity.NewRunID(previous.Repo, previous.Workflow, previous.Ticket.Key)
	}
	// The lifecycle gate makes max+1 allocation single-writer within the
	// server. Because every prior attempt is persisted in relay_runs, the
	// number remains stable across process restarts and repeated commands.
	attempts, err := m.Runs.ListRuns(ctx, Filter{Repo: previous.Repo, Workflow: previous.Workflow, Ticket: ticket})
	if err != nil {
		return Run{}, fmt.Errorf("allocate restart attempt for %s: %w", ticket, err)
	}
	var attemptID AttemptID = 1
	for _, candidate := range attempts {
		candidateAttempt := candidate.AttemptID
		if candidateAttempt == 0 {
			candidateAttempt = 1
		}
		if candidateAttempt >= attemptID {
			if candidateAttempt == ^AttemptID(0) {
				return Run{}, fmt.Errorf("%w: attempt number exhausted for %s", ErrRestartConflict, ticket)
			}
			attemptID = candidateAttempt + 1
		}
	}
	executionID := identity.NewAttemptRunID(logicalID, attemptID)
	start := Start{
		ID: executionID, LogicalID: logicalID, AttemptID: attemptID,
		Repo: previous.Repo, RepoPath: rp.Path, Workflow: *wf, Ticket: previous.Ticket,
	}
	if start.Ticket.Key == "" {
		start.Ticket.Key = ticket
	}
	if err := m.ensure(ctx, start); err != nil {
		return Run{}, err
	}

	// The real projection is inserted before the durable instance is started.
	// A fake or an engine that cannot read it yet still gets a useful command
	// result; a later poll/EnsureRun repairs a missing workflow instance.
	if current, err := m.Runs.GetRun(ctx, executionID); err == nil {
		return current, nil
	}
	now := time.Now().UTC()
	return Run{
		ID: executionID, LogicalID: logicalID, AttemptID: attemptID,
		Repo: start.Repo, Workflow: start.Workflow.Name, Ticket: start.Ticket,
		State: StateStarting, StartedAt: now, UpdatedAt: now,
	}, nil
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
