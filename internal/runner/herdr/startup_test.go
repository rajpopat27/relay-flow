package herdr

import (
	"context"
	"errors"
	"testing"

	"github.com/rajpopat27/relay-flow/internal/runner"
	"github.com/rajpopat27/relay-flow/internal/runner/herdr/herdrcli"
)

type startupClient struct {
	*fakeClient
	snapshotCalls int
	listingCalls  int
}

func (c *startupClient) Snapshot(ctx context.Context) (herdrcli.Snapshot, error) {
	c.snapshotCalls++
	if ctx.Err() != nil {
		return herdrcli.Snapshot{}, ctx.Err()
	}
	return c.snapshot, c.snapshotErr
}

func (c *startupClient) WorktreeList(context.Context, string) (herdrcli.WorktreeListing, error) {
	c.listingCalls++
	return herdrcli.WorktreeListing{}, errors.New("startup must not discover per-workspace repositories")
}

func TestStartupProbeUsesOnlyOneSnapshot(t *testing.T) {
	for _, snapshot := range []herdrcli.Snapshot{{}, {
		Workspaces: []herdrcli.Workspace{{ID: "w1", Label: "app"}},
		Panes:      []herdrcli.Pane{{ID: "p1", WorkspaceID: "w1", CWD: "/work/app"}},
	}} {
		c := &startupClient{fakeClient: &fakeClient{snapshot: snapshot}}
		if err := runner.ProbeStartup(context.Background(), newAdapter(c)); err != nil {
			t.Fatal(err)
		}
		if c.snapshotCalls != 1 || c.listingCalls != 0 || len(c.workspaceCreateCalls) != 0 || len(c.createCalls) != 0 || len(c.runCalls) != 0 || len(c.closedPanes) != 0 || len(c.closedWorkspaces) != 0 {
			t.Fatalf("unexpected startup calls: %+v", c)
		}
	}
}

func TestStartupProbePropagatesSnapshotFailureAndCancellation(t *testing.T) {
	failure := errors.New("herdr socket unavailable")
	c := &startupClient{fakeClient: &fakeClient{snapshotErr: failure}}
	a := newAdapter(c)
	if err := runner.ProbeStartup(context.Background(), a); !errors.Is(err, failure) || c.snapshotCalls != 1 {
		t.Fatalf("failure = %v, calls=%d", err, c.snapshotCalls)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := runner.ProbeStartup(ctx, a); !errors.Is(err, context.Canceled) || c.snapshotCalls != 2 {
		t.Fatalf("cancellation = %v, calls=%d", err, c.snapshotCalls)
	}
}
