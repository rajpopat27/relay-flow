package projection_test

import (
	"context"
	"testing"
	"time"

	"github.com/rajpopat27/relay-flow/internal/run"
)

func TestLegacyNullCreationAndDurationRemainUnavailable(t *testing.T) {
	ctx := context.Background()
	p, db := openProjection(t)
	now := time.Now().UTC()
	if _, err := db.Exec(`INSERT INTO relay_runs (id, repo, workflow, ticket_id, ticket_key, state, started_at, updated_at) VALUES (?, 'repo', 'workflow', 'T-LEGACY', 'T-LEGACY', 'waiting', ?, ?)`, "legacy-null", now, now); err != nil {
		t.Fatal(err)
	}
	detail, err := p.GetDetail(ctx, "legacy-null")
	if err != nil {
		t.Fatal(err)
	}
	if detail.CreatedAt != nil {
		t.Fatalf("legacy CreatedAt = %v, want nil", detail.CreatedAt)
	}
	if err := p.UpsertStep(ctx, run.StepEntry{RunID: "legacy-null", Sequence: 1, Node: "coding", Status: run.StepRunning}); err != nil {
		t.Fatal(err)
	}
	steps, err := p.ListSteps(ctx, "legacy-null")
	if err != nil {
		t.Fatal(err)
	}
	if len(steps) != 1 || steps[0].DurationKnown {
		t.Fatalf("legacy unknown duration = %#v", steps)
	}
}

func TestStepProjectionIsIdempotentAndDerivesDetail(t *testing.T) {
	ctx := context.Background()
	p, _ := openProjection(t)
	start := projectionStart("detail-run", "PAY-DETAIL")
	started := time.Now().UTC().Add(-2 * time.Minute)
	if err := p.InsertStart(ctx, start, started); err != nil {
		t.Fatal(err)
	}
	finished := started.Add(time.Minute)
	step := run.StepEntry{
		RunID: start.ID, Sequence: 1, Node: "coding", NodeVisitID: "visit-1", NodeType: "agent",
		Status: run.StepSucceeded, StartedAt: &started, FinishedAt: &finished,
		Message: "report accepted", Route: "end", Runtime: "coder", Resource: "harness",
	}
	if err := p.UpsertStep(ctx, step); err != nil {
		t.Fatal(err)
	}
	if err := p.UpsertStep(ctx, step); err != nil {
		t.Fatal(err)
	}
	steps, err := p.ListSteps(ctx, start.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(steps) != 1 || steps[0].NodeVisitID != step.NodeVisitID || steps[0].Route != "end" || steps[0].Duration <= 0 {
		t.Fatalf("steps = %#v", steps)
	}
	detail, err := p.GetDetail(ctx, start.ID)
	if err != nil {
		t.Fatal(err)
	}
	if detail.Progress.Completed != 1 || detail.Progress.Total != 1 || detail.ResourcesDuration.Harness <= 0 {
		t.Fatalf("detail = %#v", detail)
	}
	if detail.Conditions.Completed {
		t.Fatal("starting run was reported completed")
	}
}

func TestStepUpsertDoesNotRegressTerminalVisit(t *testing.T) {
	ctx := context.Background()
	p, _ := openProjection(t)
	start := projectionStart("monotonic-run", "PAY-MONOTONIC")
	if err := p.InsertStart(ctx, start, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	finished := time.Now().UTC()
	terminal := run.StepEntry{RunID: start.ID, Sequence: 1, Node: "coding", NodeVisitID: "visit-1", Status: run.StepSucceeded, FinishedAt: &finished, Message: "accepted", Route: "end"}
	if err := p.UpsertStep(ctx, terminal); err != nil {
		t.Fatal(err)
	}
	stale := terminal
	stale.Status, stale.Message, stale.Route = run.StepRunning, "stale retry", "coding"
	if err := p.UpsertStep(ctx, stale); err != nil {
		t.Fatal(err)
	}
	steps, err := p.ListSteps(ctx, start.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(steps) != 1 || steps[0].Status != run.StepSucceeded || steps[0].Message != "accepted" || steps[0].Route != "end" {
		t.Fatalf("stale write regressed terminal step: %#v", steps)
	}
}

func TestBranchLoopVisitsRemainDistinctAndNested(t *testing.T) {
	ctx := context.Background()
	p, _ := openProjection(t)
	start := projectionStart("branch-loop", "PAY-BRANCH")
	if err := p.InsertStart(ctx, start, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	steps := []run.StepEntry{
		{RunID: start.ID, Sequence: 1, Node: "coding", NodeVisitID: "visit-coding-1", Status: run.StepSucceeded, Depth: 1},
		{RunID: start.ID, Sequence: 2, Node: "review", NodeVisitID: "visit-review-1", Status: run.StepSucceeded, Depth: 1},
		{RunID: start.ID, Sequence: 3, Node: "coding", NodeVisitID: "visit-coding-2", ParentSequence: 1, Status: run.StepWaiting, Depth: 2},
	}
	for _, step := range steps {
		if err := p.UpsertStep(ctx, step); err != nil {
			t.Fatal(err)
		}
	}
	got, err := p.ListSteps(ctx, start.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != len(steps) || got[2].NodeVisitID != "visit-coding-2" || got[2].ParentSequence != 1 || got[2].Depth != 2 {
		t.Fatalf("branch/loop steps = %#v", got)
	}
}

func TestStepProjectionFailureDoesNotBlockAuthoritativeRunState(t *testing.T) {
	ctx := context.Background()
	p, db := openProjection(t)
	start := projectionStart("projection-failure", "PAY-PROJECTION")
	if err := p.InsertStart(ctx, start, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`DROP TABLE relay_run_steps`); err != nil {
		t.Fatal(err)
	}
	if err := p.UpdateState(ctx, start.ID, run.StateWaiting, "", nil); err != nil {
		t.Fatalf("authoritative state update failed with missing display table: %v", err)
	}
	got, err := p.Get(ctx, start.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.State != run.StateWaiting {
		t.Fatalf("state = %s, want waiting", got.State)
	}
}

func TestTerminalStepTimingIsStableAcrossLaterRunFinalization(t *testing.T) {
	ctx := context.Background()
	p, _ := openProjection(t)
	start := projectionStart("stable-terminal", "PAY-STABLE")
	t0 := time.Now().UTC().Add(-2 * time.Minute)
	t1 := t0.Add(20 * time.Second)
	if err := p.InsertStart(ctx, start, t0); err != nil {
		t.Fatal(err)
	}
	if err := p.UpsertStep(ctx, run.StepEntry{RunID: start.ID, Sequence: 1, Node: "coding", Status: run.StepSucceeded, StartedAt: &t0, FinishedAt: &t1}); err != nil {
		t.Fatal(err)
	}
	before, err := p.ListSteps(ctx, start.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(before) != 1 || before[0].FinishedAt == nil || before[0].Duration <= 0 {
		t.Fatalf("initial terminal step = %#v", before)
	}
	finished := t1.Add(time.Minute)
	if err := p.UpdateState(ctx, start.ID, run.StateCanceled, "canceled", &finished); err != nil {
		t.Fatal(err)
	}
	if err := p.UpdateState(ctx, start.ID, run.StateCanceled, "canceled", &finished); err != nil {
		t.Fatal(err)
	}
	after, err := p.ListSteps(ctx, start.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(after) != 1 || !after[0].FinishedAt.Equal(*before[0].FinishedAt) || after[0].Duration != before[0].Duration {
		t.Fatalf("terminal timing changed after run finalization: before=%#v after=%#v", before, after)
	}
}

func TestCompletedRunFinalizesActiveStepWhenFinalUpsertIsMissing(t *testing.T) {
	ctx := context.Background()
	p, _ := openProjection(t)
	start := projectionStart("complete-detail", "PAY-COMPLETE-DETAIL")
	started := time.Now().UTC().Add(-time.Minute)
	if err := p.InsertStart(ctx, start, started); err != nil {
		t.Fatal(err)
	}
	if err := p.UpsertStep(ctx, run.StepEntry{RunID: start.ID, Sequence: 1, Node: "end", Status: run.StepRunning, StartedAt: &started}); err != nil {
		t.Fatal(err)
	}
	finished := time.Now().UTC()
	if err := p.UpdateState(ctx, start.ID, run.StateCompleted, "", &finished); err != nil {
		t.Fatal(err)
	}
	steps, err := p.ListSteps(ctx, start.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(steps) != 1 || steps[0].Status != run.StepSucceeded || steps[0].FinishedAt == nil || steps[0].Duration <= 0 {
		t.Fatalf("completed step = %#v", steps)
	}
}

func TestCanceledRunFinalizesCurrentStepTiming(t *testing.T) {
	ctx := context.Background()
	p, _ := openProjection(t)
	start := projectionStart("cancel-detail", "PAY-CANCEL-DETAIL")
	started := time.Now().UTC().Add(-time.Minute)
	if err := p.InsertStart(ctx, start, started); err != nil {
		t.Fatal(err)
	}
	if err := p.UpsertStep(ctx, run.StepEntry{RunID: start.ID, Sequence: 1, Node: "coding", Status: run.StepRunning, StartedAt: &started}); err != nil {
		t.Fatal(err)
	}
	if err := p.UpdateState(ctx, start.ID, run.StateCanceling, "operator canceled", nil); err != nil {
		t.Fatal(err)
	}
	finished := time.Now().UTC()
	if err := p.UpdateState(ctx, start.ID, run.StateCanceled, "operator canceled", &finished); err != nil {
		t.Fatal(err)
	}
	steps, err := p.ListSteps(ctx, start.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(steps) != 1 || steps[0].Status != run.StepCanceled || steps[0].FinishedAt == nil || steps[0].Duration <= 0 {
		t.Fatalf("canceled step = %#v", steps)
	}
}

func TestSetupConflictMarksCurrentStepBlocked(t *testing.T) {
	ctx := context.Background()
	p, _ := openProjection(t)
	start := projectionStart("setup-conflict", "PAY-SETUP")
	if err := p.InsertStart(ctx, start, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	if err := p.UpsertStep(ctx, run.StepEntry{RunID: start.ID, Sequence: 1, Node: "coding", Status: run.StepRunning}); err != nil {
		t.Fatal(err)
	}
	if err := p.UpdateState(ctx, start.ID, run.StateBlocked, "runner setup conflict", nil); err != nil {
		t.Fatal(err)
	}
	steps, err := p.ListSteps(ctx, start.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(steps) != 1 || steps[0].Status != run.StepBlocked || steps[0].Message != "runner setup conflict" {
		t.Fatalf("blocked setup step = %#v", steps)
	}
	if steps[0].DurationKnown {
		t.Fatal("unknown setup duration was fabricated as known")
	}
}
