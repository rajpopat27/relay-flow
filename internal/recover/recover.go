// Package recover implements `serve --recover` (5.6, design.md decision 22,
// docs/structs-methods-interfaces.md lines 946-965). It runs once, inline,
// after the engine is open and BEFORE normal pollers start, so recovered
// runs exist before any poll cycle observes them.
//
// Steps per selected (repo, workflow, labeled-parent):
//  1. Poll active parents (task systems already exclude parents completed
//     through `end`).
//  2. Filter to parents whose wf:<name> claim resolves to exactly one bound
//     workflow via the router.
//  3. Skip parents carrying the stable <runID>:cancellation marker.
//  4. CloseTerminals on the run (preserves worktrees/branches/code).
//  5. EnsureMailboxes (find existing, create only missing).
//  6. ResetForRecovery on parent + mailboxes (adapter-owned; Jira moves
//     mailboxes to `To Do` — core never names a status).
//  7. EnsureRun creates a fresh deterministic run with fresh NodeVisitIDs
//     and processes from `start`. Old routes/nodes/visits/timers are never
//     resumed.
//
// Comments, labels, worktrees, branches, and code are preserved. Recovery
// never runs automatically; database loss is never inferred.
package recover

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sort"

	"github.com/rajpopat27/relay-flow/internal/identity"
	"github.com/rajpopat27/relay-flow/internal/repo"
	"github.com/rajpopat27/relay-flow/internal/router"
	"github.com/rajpopat27/relay-flow/internal/run"
	"github.com/rajpopat27/relay-flow/internal/runner"
	"github.com/rajpopat27/relay-flow/internal/task"
	"github.com/rajpopat27/relay-flow/internal/workflow"
)

// MailboxSpecFor builds one spec per agent/HITL work node for a ticket. The
// engine's MailboxSpecs supplies the production description content; this
// indirection keeps recover decoupled from the engine package (which imports
// internal/run and would otherwise form a cycle).
type MailboxSpecFor func(wf *workflow.Workflow, ticketKey string) []task.MailboxSpec

// FromTaskSystem is the `serve --recover` composition. Explicit flag only;
// never automatic; database loss is never inferred.
func FromTaskSystem(ctx context.Context, repoReg *repo.Registry, rnr runner.Runner, runManager *run.RunManager, specsFor MailboxSpecFor) error {
	for _, rp := range repoReg.List() {
		tickets, err := rp.TaskSystem.Poll(ctx)
		if err != nil {
			return fmt.Errorf("repo %q poll: %w", rp.Name, err)
		}
		for _, ticket := range tickets {
			// Use the router's exact claim semantics: zero claims or zero
			// matcher hits → ErrNoMatch (not ours, skip); multiple claims
			// or an unknown/unbound claim → InvalidClaimError (ambiguous
			// ownership, skip with a warn). Only tickets that resolve to
			// exactly one bound workflow proceed.
			wf, err := router.ResolveWorkflow(rp, ticket)
			if errors.Is(err, router.ErrNoMatch) {
				continue
			}
			if err != nil {
				slog.Warn("recover: skip ticket with invalid claim", "ticket", ticket.Key, "error", err)
				continue
			}
			// Skip canceled parents: the cancellation marker is the
			// task-system recovery record.
			runID := identity.NewRunID(rp.Name, wf.Name, ticket.Key)
			marked, err := rp.TaskSystem.HasComment(ctx, task.Target{Parent: ticket.Ref()}, run.CancellationMarker(runID))
			if err != nil {
				return fmt.Errorf("repo %q ticket %s check cancellation marker: %w", rp.Name, ticket.Key, err)
			}
			if marked {
				continue
			}
			// Close stale agent terminals; preserve workspace/code.
			if err := rnr.CloseTerminals(ctx, runner.RunSpec{
				RunID:     runID,
				RepoName:  rp.Name,
				RepoPath:  rp.Path,
				TicketKey: ticket.Key,
			}); err != nil {
				return fmt.Errorf("repo %q ticket %s close terminals: %w", rp.Name, ticket.Key, err)
			}
			// EnsureMailboxes: find existing, create only missing. Specs are
			// built by the caller (the engine supplies production content).
			mailboxes, err := rp.TaskSystem.EnsureMailboxes(ctx, ticket.Ref(), wf.Name, specsFor(wf, ticket.Key))
			if err != nil {
				return fmt.Errorf("repo %q ticket %s ensure mailboxes: %w", rp.Name, ticket.Key, err)
			}
			// Deterministic ordering: sort mailbox keys so adapter-side
			// effect ordering is reproducible across recoveries.
			nodeNames := make([]string, 0, len(mailboxes))
			for node := range mailboxes {
				nodeNames = append(nodeNames, node)
			}
			sort.Strings(nodeNames)
			mailboxList := make([]task.Mailbox, 0, len(mailboxes))
			for _, node := range nodeNames {
				mailboxList = append(mailboxList, mailboxes[node])
			}
			// ResetForRecovery: adapter-owned; resets parent + mailboxes.
			if err := rp.TaskSystem.ResetForRecovery(ctx, ticket.Ref(), mailboxList, wf.TaskConfig); err != nil {
				return fmt.Errorf("repo %q ticket %s reset for recovery: %w", rp.Name, ticket.Key, err)
			}
			// Fresh deterministic run; fresh NodeVisitIDs; from `start`.
			if err := runManager.EnsureRun(ctx, rp, wf, ticket); err != nil {
				return fmt.Errorf("repo %q ticket %s ensure run: %w", rp.Name, ticket.Key, err)
			}
		}
	}
	return nil
}
