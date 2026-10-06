package orca

import (
	"context"
	"errors"
	"testing"

	"github.com/rajpopat27/relay-flow/internal/runner"
	"github.com/rajpopat27/relay-flow/internal/runner/orca/orcacli"
)

type startupCLI struct {
	*fakeCLI
	calls int
	err   error
}

func (c *startupCLI) ListRepos(ctx context.Context) ([]orcacli.Repo, error) {
	c.calls++
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	return c.repos, c.err
}

func TestStartupProbeReadsRepoListOnceWithoutWrites(t *testing.T) {
	for _, repos := range [][]orcacli.Repo{nil, {{ID: "r1", Path: "/work/app", DisplayName: "app"}}} {
		c := &startupCLI{fakeCLI: &fakeCLI{repos: repos}}
		a, err := New(c, nil)
		if err != nil {
			t.Fatal(err)
		}
		if err := runner.ProbeStartup(context.Background(), a); err != nil {
			t.Fatal(err)
		}
		if c.calls != 1 || c.createN != 0 || len(c.closedHandles) != 0 || len(c.deletedWorktrees) != 0 || len(c.worktrees) != 0 || c.status != "" {
			t.Fatalf("unexpected startup effects: %+v", c)
		}
	}
}

func TestStartupProbePropagatesFailureAndCancellation(t *testing.T) {
	failure := errors.New("orca connection unavailable")
	c := &startupCLI{fakeCLI: &fakeCLI{}, err: failure}
	a, err := New(c, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := runner.ProbeStartup(context.Background(), a); !errors.Is(err, failure) || c.calls != 1 {
		t.Fatalf("failure = %v, calls=%d", err, c.calls)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := runner.ProbeStartup(ctx, a); !errors.Is(err, context.Canceled) || c.calls != 2 {
		t.Fatalf("cancellation = %v, calls=%d", err, c.calls)
	}
}
