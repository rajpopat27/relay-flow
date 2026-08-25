package goworkflows

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/rajpopat27/relay-flow/internal/run"
	"github.com/rajpopat27/relay-flow/internal/task"
	"github.com/rajpopat27/relay-flow/internal/workflow"
)

func TestNodeRuntimeMigrationAndRestart(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "state.db")
	db := openProjectionDB(t, path)

	// Simulate an existing database that predates relay_node_runtime.
	if _, err := db.Exec(`CREATE TABLE relay_runs (
		id TEXT PRIMARY KEY, repo TEXT NOT NULL, workflow TEXT NOT NULL,
		ticket_id TEXT NOT NULL, ticket_key TEXT NOT NULL, state TEXT NOT NULL,
		current_node TEXT, current_node_visit_id TEXT, last_error TEXT,
		retry_error TEXT, retry_attempt INTEGER, next_retry_at DATETIME,
		started_at DATETIME NOT NULL, updated_at DATETIME NOT NULL, finished_at DATETIME
	)`); err != nil {
		t.Fatal(err)
	}
	p := &RunProjection{DB: db}
	if err := p.migrate(); err != nil {
		t.Fatalf("migrate existing database: %v", err)
	}

	id := run.ID("payments/basic/PAY-101")
	if err := p.insertStart(ctx, run.Start{
		ID: id, Repo: "payments", Workflow: workflow.Workflow{Name: "basic"},
		Ticket: task.TicketRef{ID: "1", Key: "PAY-101"},
	}, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	firstVisit := run.NodeVisitID("visit-1")
	if err := p.updateNode(ctx, id, run.StateRunning, "implement", firstVisit); err != nil {
		t.Fatal(err)
	}
	if err := p.updateNodeRuntime(ctx, NodeRuntime{
		RunID: id, Node: "implement", TerminalID: "term-1", SessionID: "session-1", NodeVisitID: firstVisit,
	}); err != nil {
		t.Fatal(err)
	}

	// Moving to another node retains implement's runtime row.
	if err := p.updateNode(ctx, id, run.StateRunning, "verify", "visit-2"); err != nil {
		t.Fatal(err)
	}
	implement, err := p.getNodeRuntime(ctx, id, "implement")
	if err != nil {
		t.Fatal(err)
	}
	if implement.TerminalID != "term-1" || implement.SessionID != "session-1" || implement.NodeVisitID != firstVisit {
		t.Fatalf("runtime changed across transition: %+v", implement)
	}

	// Revisiting updates only the visit ID, preserving reusable identities.
	secondVisit := run.NodeVisitID("visit-3")
	if err := p.updateNode(ctx, id, run.StateRunning, "implement", secondVisit); err != nil {
		t.Fatal(err)
	}
	implement, err = p.getNodeRuntime(ctx, id, "implement")
	if err != nil {
		t.Fatal(err)
	}
	if implement.TerminalID != "term-1" || implement.SessionID != "session-1" || implement.NodeVisitID != secondVisit {
		t.Fatalf("runtime not preserved on revisit: %+v", implement)
	}
	if implement.UpdatedAt.IsZero() {
		t.Fatal("runtime updated_at is zero")
	}

	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	db = openProjectionDB(t, path)
	defer db.Close()
	p = &RunProjection{DB: db}
	if err := p.migrate(); err != nil {
		t.Fatalf("migrate after restart: %v", err)
	}
	implement, err = p.getNodeRuntime(ctx, id, "implement")
	if err != nil {
		t.Fatalf("query runtime after restart: %v", err)
	}
	if implement.TerminalID != "term-1" || implement.SessionID != "session-1" || implement.NodeVisitID != secondVisit {
		t.Fatalf("runtime changed across restart: %+v", implement)
	}
}

func TestNodeRuntimeRemovedOnlyByRetention(t *testing.T) {
	ctx := context.Background()
	db := openProjectionDB(t, filepath.Join(t.TempDir(), "state.db"))
	defer db.Close()
	p := &RunProjection{DB: db}
	if err := p.migrate(); err != nil {
		t.Fatal(err)
	}

	id := run.ID("payments/basic/PAY-OLD")
	started := time.Now().UTC().Add(-32 * 24 * time.Hour)
	if err := p.insertStart(ctx, run.Start{
		ID: id, Repo: "payments", Workflow: workflow.Workflow{Name: "basic"},
		Ticket: task.TicketRef{ID: "old", Key: "PAY-OLD"},
	}, started); err != nil {
		t.Fatal(err)
	}
	if err := p.updateNode(ctx, id, run.StateRunning, "implement", "visit-old"); err != nil {
		t.Fatal(err)
	}
	if err := p.updateNodeRuntime(ctx, NodeRuntime{
		RunID: id, Node: "implement", TerminalID: "term-old", SessionID: "session-old", NodeVisitID: "visit-old",
	}); err != nil {
		t.Fatal(err)
	}
	finished := time.Now().UTC().Add(-31 * 24 * time.Hour)
	if err := p.updateState(ctx, id, run.StateCompleted, "", &finished); err != nil {
		t.Fatal(err)
	}
	if _, err := p.getNodeRuntime(ctx, id, "implement"); err != nil {
		t.Fatalf("completion removed runtime before retention: %v", err)
	}

	ids, err := p.sweepRetention(ctx, time.Now().UTC().Add(-30*24*time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if len(ids) != 1 || ids[0] != string(id) {
		t.Fatalf("retained ids = %v, want [%s]", ids, id)
	}
	if _, err := p.getNodeRuntime(ctx, id, "implement"); !errors.Is(err, errNodeRuntimeNotFound) {
		t.Fatalf("runtime after retention error = %v, want not found", err)
	}
}

func openProjectionDB(t *testing.T, path string) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	return db
}
