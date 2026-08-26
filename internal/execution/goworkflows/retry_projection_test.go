package goworkflows_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/rajpopat27/relay-flow/internal/execution/goworkflows"
	"github.com/rajpopat27/relay-flow/internal/run"
)

func TestRunProjectionShowsActiveRetryAndClearsAfterRecovery(t *testing.T) {
	log := newEventLog()
	sys := newFakeTaskSystem(log)
	sys.completeFail = 1
	engine := newEngine(t, goworkflows.Dependencies{
		Repos: repoRegistryWith("payments", sys), Runner: newFakeRunner(log), Harness: newFakeHarness(log),
	})
	rid, err := startRun(engine, linearWorkflow(false))
	if err != nil {
		t.Fatal(err)
	}
	waitFor(t, 10*time.Second, func() bool {
		r, _ := engine.GetRun(context.Background(), rid)
		return r.CurrentNode == "coding" && r.CurrentNodeVisitID != ""
	})
	r, _ := engine.GetRun(context.Background(), rid)
	if _, err := engine.SubmitReport(context.Background(), reportRequest(rid, "coding", successReport("end"))); err != nil {
		t.Fatal(err)
	}

	waitFor(t, 10*time.Second, func() bool {
		r, _ = engine.GetRun(context.Background(), rid)
		return r.Retry != nil
	})
	if r.State != run.StateWaiting {
		t.Fatalf("lifecycle state changed to %q during transient retry; want waiting", r.State)
	}
	if r.Retry.Attempt != 1 {
		t.Fatalf("retry attempt = %d, want 1", r.Retry.Attempt)
	}
	if !strings.Contains(r.Retry.LastError, "jira unavailable") {
		t.Fatalf("retry error = %q, want sanitized provider reason", r.Retry.LastError)
	}
	if !r.Retry.NextRetryAt.After(r.UpdatedAt) {
		t.Fatalf("next retry %s is not after projection update %s", r.Retry.NextRetryAt, r.UpdatedAt)
	}

	listed, err := engine.ListRuns(context.Background(), run.Filter{Repo: "payments"})
	if err != nil || len(listed) != 1 || listed[0].Retry == nil {
		t.Fatalf("ListRuns retry projection = %+v, err=%v", listed, err)
	}
	byTicket, err := engine.FindRunByTicket(context.Background(), "PAY-101")
	if err != nil || byTicket.Retry == nil {
		t.Fatalf("FindRunByTicket retry projection = %+v, err=%v", byTicket, err)
	}

	waitFor(t, 15*time.Second, func() bool {
		r, _ = engine.GetRun(context.Background(), rid)
		return r.State == run.StateCompleted
	})
	if r.Retry != nil {
		t.Fatalf("retry details remained after recovery: %+v", r.Retry)
	}
}

func TestRunProjectionClearsRetryAfterCancellation(t *testing.T) {
	log := newEventLog()
	sys := newFakeTaskSystem(log)
	sys.completeFail = 100
	engine := newEngine(t, goworkflows.Dependencies{
		Repos: repoRegistryWith("payments", sys), Runner: newFakeRunner(log), Harness: newFakeHarness(log),
	})
	rid, _ := startRun(engine, linearWorkflow(false))
	waitFor(t, 10*time.Second, func() bool {
		r, _ := engine.GetRun(context.Background(), rid)
		return r.CurrentNode == "coding" && r.CurrentNodeVisitID != ""
	})
	r, _ := engine.GetRun(context.Background(), rid)
	if _, err := engine.SubmitReport(context.Background(), reportRequest(rid, "coding", successReport("end"))); err != nil {
		t.Fatal(err)
	}
	waitFor(t, 10*time.Second, func() bool {
		r, _ = engine.GetRun(context.Background(), rid)
		return r.Retry != nil
	})
	if err := engine.CancelRun(context.Background(), rid, "operator canceled"); err != nil {
		t.Fatal(err)
	}
	waitFor(t, 10*time.Second, func() bool {
		r, _ = engine.GetRun(context.Background(), rid)
		return r.State == run.StateCanceled
	})
	if r.Retry != nil {
		t.Fatalf("retry details remained after terminal cancellation: %+v", r.Retry)
	}
}
