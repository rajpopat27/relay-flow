package runner_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/rajpopat27/relay-flow/internal/runner"
)

type startupRunner struct {
	*fakeRunner
	calls int
	probe func(context.Context) error
}

func (r *startupRunner) ProbeStartup(ctx context.Context) error {
	r.calls++
	return r.probe(ctx)
}

func TestStartupProbeRequiresSelectedRunnerSupport(t *testing.T) {
	if err := runner.ProbeStartup(context.Background(), newFakeRunner()); err == nil || !strings.Contains(err.Error(), "startup probing") {
		t.Fatalf("unsupported selected runner = %v", err)
	}
}

func TestStartupProbeIsSingleAttemptWithBoundedDeadline(t *testing.T) {
	failure := errors.New("runtime unavailable")
	r := &startupRunner{fakeRunner: newFakeRunner(), probe: func(ctx context.Context) error {
		deadline, ok := ctx.Deadline()
		if !ok || time.Until(deadline) > 5*time.Second {
			t.Fatalf("probe deadline = %v, %v", deadline, ok)
		}
		return failure
	}}
	if err := runner.ProbeStartup(context.Background(), r); !errors.Is(err, failure) || r.calls != 1 {
		t.Fatalf("ProbeStartup = %v, calls=%d", err, r.calls)
	}
}

func TestStartupProbeHonorsEarlierAggregateDeadline(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Millisecond)
	defer cancel()
	deadline, _ := ctx.Deadline()
	r := &startupRunner{fakeRunner: newFakeRunner(), probe: func(probeCtx context.Context) error {
		got, _ := probeCtx.Deadline()
		if !got.Equal(deadline) {
			t.Fatalf("probe extended aggregate deadline: %v > %v", got, deadline)
		}
		<-probeCtx.Done()
		return probeCtx.Err()
	}}
	if err := runner.ProbeStartup(ctx, r); !errors.Is(err, context.DeadlineExceeded) || r.calls != 1 {
		t.Fatalf("ProbeStartup = %v, calls=%d", err, r.calls)
	}
}
