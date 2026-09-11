package temporal

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/rajpopat27/relay-flow/internal/run"
	"github.com/rajpopat27/relay-flow/internal/runner"
	"github.com/rajpopat27/relay-flow/internal/task"
	"github.com/rajpopat27/relay-flow/internal/workflow"
	"go.temporal.io/sdk/testsuite"
	temporalworkflow "go.temporal.io/sdk/workflow"
)

// The workflow uses a start-to-end graph so the test isolates end cleanup;
// terminalLive models the persisted node terminal that finalization would close.
type endCleanupActivities struct {
	events                      []string
	cleanupDirty                bool
	terminalLive                bool
	completionObserved          bool
	completionObservedOnFailure bool
}

func (a *endCleanupActivities) EnsureMailboxes(context.Context, run.Work, []task.MailboxSpec) (map[string]task.Mailbox, error) {
	return map[string]task.Mailbox{}, nil
}

func (a *endCleanupActivities) ValidateAgents(context.Context, string, []string) error {
	return nil
}

func (a *endCleanupActivities) ProjectionUpsertStep(context.Context, run.StepEntry) error {
	return nil
}

func (a *endCleanupActivities) ApplyTaskConfig(context.Context, run.Work, string, *task.Mailbox, map[string]any) error {
	return nil
}

func (a *endCleanupActivities) EnsureEnvironment(context.Context, run.Work, string) (runner.Environment, error) {
	return runner.Environment{}, nil
}

func (a *endCleanupActivities) SetEnvironmentStatus(context.Context, run.Work, string, string) error {
	return nil
}

func (a *endCleanupActivities) CleanupRun(context.Context, run.Work, string) error {
	if a.cleanupDirty {
		a.cleanupDirty = false
		a.completionObservedOnFailure = a.completionObserved
		if a.terminalLive {
			a.events = append(a.events, "cleanup-failed-terminal-live")
		} else {
			a.events = append(a.events, "cleanup-failed-terminal-closed")
		}
		return errors.New("ticket checkout is dirty; commit required before runner cleanup")
	}
	a.events = append(a.events, "cleanup")
	return nil
}

func (a *endCleanupActivities) FinalizeNodeRuntimes(context.Context, run.Work, string, run.RuntimePolicy) error {
	if a.terminalLive {
		a.terminalLive = false
		a.events = append(a.events, "finalize-terminal-closed")
	} else {
		a.events = append(a.events, "finalize-terminal-already-closed")
	}
	return nil
}

func (a *endCleanupActivities) ProjectionUpdateRetry(context.Context, run.ID, *run.RetryStatus) error {
	return nil
}

func (a *endCleanupActivities) ProjectionUpdateState(_ context.Context, _ run.ID, state run.State, _ string, _ *time.Time) error {
	if state == run.StateCompleted {
		a.completionObserved = true
		if a.terminalLive {
			a.events = append(a.events, "completed-terminal-live")
		} else {
			a.events = append(a.events, "completed")
		}
	}
	return nil
}

func endCleanupOrderingWorkflow(ctx temporalworkflow.Context, start run.Start) error {
	state := newWorkflowState(ctx, start)
	reason := "canceled"
	return runGraph(ctx, start, state, temporalworkflow.GetSignalChannel(ctx, cancelReasonSignalName), &reason)
}

func TestTemporalEndCleanupRunsBeforeRuntimeFinalization(t *testing.T) {
	for _, cleanup := range []bool{true, false} {
		t.Run(map[bool]string{true: "enabled", false: "disabled"}[cleanup], func(t *testing.T) {
			activities := &endCleanupActivities{cleanupDirty: cleanup, terminalLive: true}
			var suite testsuite.WorkflowTestSuite
			env := suite.NewTestWorkflowEnvironment()
			env.RegisterWorkflow(endCleanupOrderingWorkflow)
			env.RegisterActivity(activities)
			start := run.Start{
				ID: "repo/cleanup/T-1", Repo: "repo", RepoPath: t.TempDir(),
				Workflow: workflow.Workflow{
					Name: "cleanup", CleanupRunnerOnEnd: cleanup,
					Nodes: map[string]workflow.Node{
						"start": {OnSuccess: []workflow.Route{{Target: "end"}}},
						"end":   {},
					},
				},
				Ticket: task.TicketRef{ID: "T-1", Key: "T-1"},
			}
			env.ExecuteWorkflow(endCleanupOrderingWorkflow, start)
			if err := env.GetWorkflowError(); err != nil {
				t.Fatalf("workflow error: %v", err)
			}
			if cleanup {
				if activities.completionObservedOnFailure {
					t.Fatal("run was marked completed while dirty cleanup was retrying")
				}
				want := []string{"cleanup-failed-terminal-live", "cleanup", "finalize-terminal-closed", "completed"}
				if len(activities.events) != len(want) {
					t.Fatalf("activity order = %v, want %v", activities.events, want)
				}
				for i := range want {
					if activities.events[i] != want[i] {
						t.Fatalf("activity order = %v, want %v", activities.events, want)
					}
				}
			} else {
				want := []string{"finalize-terminal-closed", "completed"}
				if len(activities.events) != len(want) {
					t.Fatalf("cleanup-disabled activity order = %v, want %v", activities.events, want)
				}
				for i := range want {
					if activities.events[i] != want[i] {
						t.Fatalf("cleanup-disabled activity order = %v, want %v", activities.events, want)
					}
				}
			}
		})
	}
}
