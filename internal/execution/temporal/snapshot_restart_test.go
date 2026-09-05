package temporal

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/rajpopat27/relay-flow/internal/execution/projection"
	"github.com/rajpopat27/relay-flow/internal/identity"
	"github.com/rajpopat27/relay-flow/internal/repo"
	"github.com/rajpopat27/relay-flow/internal/run"
	"github.com/rajpopat27/relay-flow/internal/task"
)

func TestTemporalWorkflowSnapshotSurvivesWorkerRestart(t *testing.T) {
	if os.Getenv("RELAY_FLOW_TEMPORAL_LIVE") != "1" {
		t.Skip("set RELAY_FLOW_TEMPORAL_LIVE=1 to run Temporal snapshot restart coverage")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	namespace := "relay-flow-snapshot-" + string(identity.NewNodeVisitID())[:12]
	if err := ensureSpikeNamespace(ctx, "localhost:7233", namespace); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "state.db")
	if err := projection.InitDatabaseWithIdentity(path, projection.ExecutorIdentity{ExecutorPlugin: "temporal", TemporalAddress: "localhost:7233", TemporalNamespace: namespace}); err != nil {
		t.Fatal(err)
	}
	sys := &lagTaskSystem{}
	registry := repo.NewRegistry()
	wf := lagWorkflow()
	registry.Replace(&repo.Repo{Name: "repo", Path: "/repo", TaskSystem: sys, Workflows: []repo.WorkflowBinding{{Workflow: &wf}}})
	deps := Dependencies{Repos: registry, Runner: &lagRunner{}, Harness: &lagHarness{}, TaskSystem: "lag-task", TemporalAddress: "localhost:7233", TemporalNamespace: namespace}
	engine, err := New(path, deps)
	if err != nil {
		t.Fatal(err)
	}
	if err := engine.Start(ctx); err != nil {
		t.Fatal(err)
	}
	start := run.Start{ID: identity.NewRunID("repo", wf.Name, "SNAPSHOT-1"), Repo: "repo", RepoPath: "/repo", Workflow: wf, Ticket: task.TicketRef{ID: "SNAPSHOT-1", Key: "SNAPSHOT-1", Title: "Snapshot"}}
	if created, err := engine.EnsureRun(ctx, start); err != nil || !created {
		t.Fatalf("EnsureRun = %v, %v", created, err)
	}
	waitForLagState(t, ctx, engine, start.ID, run.StateWaiting)
	before, err := engine.workflowFromHistory(ctx, start.ID, "")
	if err != nil {
		t.Fatal(err)
	}
	if err := engine.Shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}

	restarted, err := New(path, deps)
	if err != nil {
		t.Fatal(err)
	}
	if err := restarted.Start(ctx); err != nil {
		t.Fatal(err)
	}
	defer restarted.Shutdown(context.Background())
	after, err := restarted.workflowFromHistory(ctx, start.ID, "")
	if err != nil {
		t.Fatal(err)
	}
	if after.Name != before.Name || after.Nodes["work"].Description != before.Nodes["work"].Description || after.Nodes["work"].Agent != before.Nodes["work"].Agent {
		t.Fatalf("snapshot changed across worker restart: before=%+v after=%+v", before, after)
	}
	waitForLagState(t, ctx, restarted, start.ID, run.StateWaiting)
}
