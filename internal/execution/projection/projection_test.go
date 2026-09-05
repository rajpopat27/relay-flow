package projection_test

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"sort"
	"testing"
	"time"

	_ "modernc.org/sqlite"

	"github.com/rajpopat27/relay-flow/internal/execution/projection"
	"github.com/rajpopat27/relay-flow/internal/run"
	"github.com/rajpopat27/relay-flow/internal/task"
	"github.com/rajpopat27/relay-flow/internal/workflow"
)

func openProjection(t *testing.T) (*projection.RunProjection, *sql.DB) {
	t.Helper()
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatalf("open projection database: %v", err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = db.Close() })
	p := &projection.RunProjection{DB: db}
	if err := p.Migrate(); err != nil {
		t.Fatalf("migrate projection: %v", err)
	}
	return p, db
}

func projectionStart(id run.ID, ticket string) run.Start {
	return run.Start{
		ID:       id,
		Repo:     "payments",
		RepoPath: "/srv/payments",
		Workflow: workflow.Workflow{
			Name:  "basicFlow",
			Repos: []string{"payments"},
			Nodes: map[string]workflow.Node{
				"start": {OnSuccess: []workflow.Route{{Target: "coding"}}},
				"coding": {
					Type: workflow.NodeAgent, Agent: "build", Description: "work",
					OnSuccess: []workflow.Route{{Target: "end"}},
					OnFailure: []workflow.Route{{Target: "coding"}},
				},
				"end": {},
			},
		},
		Ticket: task.TicketRef{ID: ticket, Key: ticket, Title: "parent"},
	}
}

func TestSharedProjectionSchemaAndQueries(t *testing.T) {
	ctx := context.Background()
	p, db := openProjection(t)

	rows, err := db.Query(`SELECT name FROM sqlite_master WHERE type = 'table' AND name LIKE 'relay_%'`)
	if err != nil {
		t.Fatalf("list relay tables: %v", err)
	}
	defer rows.Close()
	var tables []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			t.Fatalf("scan relay table: %v", err)
		}
		tables = append(tables, name)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate relay tables: %v", err)
	}
	sort.Strings(tables)
	wantTables := []string{
		"relay_executor_identity",
		"relay_node_runtime",
		"relay_node_sessions",
		"relay_processed_reports",
		"relay_runs",
	}
	if len(tables) != len(wantTables) {
		t.Fatalf("relay tables = %v, want exactly %v", tables, wantTables)
	}
	for i := range wantTables {
		if tables[i] != wantTables[i] {
			t.Fatalf("relay tables = %v, want exactly %v", tables, wantTables)
		}
	}

	start := projectionStart("payments/basicFlow/PAY-101", "PAY-101")
	startedAt := time.Unix(100, 0).UTC()
	if err := p.InsertStart(ctx, start, startedAt); err != nil {
		t.Fatalf("insert run: %v", err)
	}
	got, err := p.Get(ctx, start.ID)
	if err != nil {
		t.Fatalf("get run: %v", err)
	}
	if got.ID != start.ID || got.Ticket.Key != "PAY-101" || got.State != run.StateStarting {
		t.Fatalf("get run = %+v", got)
	}
	byTicket, err := p.FindByTicket(ctx, "PAY-101")
	if err != nil || byTicket.ID != start.ID {
		t.Fatalf("find by ticket = %+v, err %v", byTicket, err)
	}
	listed, err := p.List(ctx, run.Filter{Workflow: "basicFlow", Active: boolPtr(true)})
	if err != nil || len(listed) != 1 || listed[0].ID != start.ID {
		t.Fatalf("list active runs = %+v, err %v", listed, err)
	}
	activeWorkflow, err := p.HasActiveWorkflow(ctx, "basicFlow")
	if err != nil || !activeWorkflow {
		t.Fatalf("HasActiveWorkflow = %v, err %v", activeWorkflow, err)
	}
	activeRepo, err := p.HasActiveRepo(ctx, "payments")
	if err != nil || !activeRepo {
		t.Fatalf("HasActiveRepo = %v, err %v", activeRepo, err)
	}

	if err := p.UpdateNode(ctx, start.ID, run.StateWaiting, "coding", "visit-1"); err != nil {
		t.Fatalf("update node: %v", err)
	}
	runtime := projection.NodeRuntime{
		RunID: start.ID, Node: "coding", TerminalID: "term-1", SessionID: "session-1",
		NodeVisitID: "visit-1", UpdatedAt: startedAt,
	}
	if err := p.UpdateNodeRuntime(ctx, runtime); err != nil {
		t.Fatalf("update node runtime: %v", err)
	}
	gotRuntime, err := p.GetNodeRuntime(ctx, start.ID, "coding")
	if err != nil || gotRuntime.TerminalID != "term-1" || gotRuntime.SessionID != "session-1" || gotRuntime.NodeVisitID != "visit-1" {
		t.Fatalf("get node runtime = %+v, err %v", gotRuntime, err)
	}
	if err := p.RecordProcessedReport(ctx, start.ID, "visit-1", "report-1"); err != nil {
		t.Fatalf("record report: %v", err)
	}
	if err := p.RecordProcessedReport(ctx, start.ID, "visit-1", "report-1"); err != nil {
		t.Fatalf("idempotent record report: %v", err)
	}
	processed, err := p.HasProcessedReport(ctx, start.ID, "report-1")
	if err != nil || !processed {
		t.Fatalf("HasProcessedReport = %v, err %v", processed, err)
	}
	var reportCount int
	if err := db.QueryRow(`SELECT COUNT(*) FROM relay_processed_reports WHERE run_id = ?`, string(start.ID)).Scan(&reportCount); err != nil {
		t.Fatalf("count processed reports: %v", err)
	}
	if reportCount != 1 {
		t.Fatalf("processed report rows = %d, want one after repeated write", reportCount)
	}
}

func TestProjectionWritesAreDerivedAndDoNotSelectRoutes(t *testing.T) {
	ctx := context.Background()
	p, db := openProjection(t)
	start := projectionStart("run-derived", "PAY-201")
	if err := p.InsertStart(ctx, start, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	if err := p.UpdateNode(ctx, start.ID, run.StateWaiting, "coding", "visit-1"); err != nil {
		t.Fatal(err)
	}
	if err := p.UpdateNode(ctx, start.ID, run.StateWaiting, "coding", "visit-1"); err != nil {
		t.Fatal(err)
	}
	if err := p.RecordProcessedReport(ctx, start.ID, "visit-1", "report-1"); err != nil {
		t.Fatal(err)
	}
	if err := p.RecordProcessedReport(ctx, start.ID, "visit-1", "report-1"); err != nil {
		t.Fatal(err)
	}

	columns, err := db.Query(`SELECT name FROM pragma_table_info('relay_runs')`)
	if err != nil {
		t.Fatal(err)
	}
	defer columns.Close()
	for columns.Next() {
		var name string
		if err := columns.Scan(&name); err != nil {
			t.Fatal(err)
		}
		switch name {
		case "selected_route", "report", "report_id", "next_step":
			t.Fatalf("projection stores execution-authority column %q", name)
		}
	}
	var receipts int
	if err := db.QueryRow(`SELECT COUNT(*) FROM relay_processed_reports WHERE run_id = ?`, string(start.ID)).Scan(&receipts); err != nil {
		t.Fatal(err)
	}
	if receipts != 1 {
		t.Fatalf("idempotent projection receipt count = %d, want one", receipts)
	}
	got, err := p.Get(ctx, start.ID)
	if err != nil || got.CurrentNode != "coding" || got.CurrentNodeVisitID != "visit-1" {
		t.Fatalf("derived run after repeated writes = %+v, err %v", got, err)
	}
}

func TestProjectionImplementationIsSharedByExecutorModes(t *testing.T) {
	for _, mode := range []string{"goworkflows", "temporal"} {
		t.Run(mode, func(t *testing.T) {
			p, db := openProjection(t)
			var count int
			if err := db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name LIKE 'relay_%'`).Scan(&count); err != nil {
				t.Fatal(err)
			}
			if count != 5 {
				t.Fatalf("relay schema table count = %d, want five shared tables", count)
			}
			if err := p.InsertStart(context.Background(), projectionStart(run.ID("run-"+mode), "PAY-"+mode), time.Now().UTC()); err != nil {
				t.Fatalf("insert %s run through shared projection: %v", mode, err)
			}
		})
	}
}

func TestInitDatabaseWithIdentityWritesMarkerBeforeSuccess(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.db")
	identity := projection.ExecutorIdentity{
		ExecutorPlugin: "temporal", TemporalAddress: "localhost:7233", TemporalNamespace: "relay-flow-init",
	}
	if err := projection.InitDatabaseWithIdentity(path, identity); err != nil {
		t.Fatalf("initialize projection with identity: %v", err)
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	p := &projection.RunProjection{DB: db}
	got, ok, err := p.Identity(context.Background())
	if err != nil || !ok || got != identity {
		t.Fatalf("initialized identity = %#v/%v, err %v", got, ok, err)
	}
}

func TestExecutorIdentityIsSingletonAndImmutable(t *testing.T) {
	ctx := context.Background()
	p, db := openProjection(t)
	embedded := projection.ExecutorIdentity{ExecutorPlugin: "goworkflows"}
	if err := p.InitializeIdentity(ctx, embedded); err != nil {
		t.Fatalf("initialize embedded identity: %v", err)
	}
	if err := p.InitializeIdentity(ctx, embedded); err != nil {
		t.Fatalf("repeat identical identity initialization: %v", err)
	}
	got, ok, err := p.Identity(ctx)
	if err != nil || !ok || got != embedded {
		t.Fatalf("identity = %#v/%v, err %v; want %#v/present", got, ok, err, embedded)
	}
	if err := p.VerifyIdentity(ctx, embedded); err != nil {
		t.Fatalf("verify matching identity: %v", err)
	}

	for _, tc := range []struct {
		name      string
		expected  projection.ExecutorIdentity
		wantError error
	}{
		{
			name:      "different executor",
			expected:  projection.ExecutorIdentity{ExecutorPlugin: "temporal", TemporalAddress: "localhost:7233", TemporalNamespace: "relay-flow"},
			wantError: projection.ErrIdentityMismatch,
		},
		{
			name:      "Temporal fields in embedded config",
			expected:  projection.ExecutorIdentity{ExecutorPlugin: "goworkflows", TemporalAddress: "localhost:7233"},
			wantError: nil,
		},
	} {
		t.Run("reject "+tc.name, func(t *testing.T) {
			err := p.VerifyIdentity(ctx, tc.expected)
			if tc.wantError != nil {
				if !errors.Is(err, tc.wantError) {
					t.Fatalf("verify mismatch error = %v, want %v", err, tc.wantError)
				}
				return
			}
			if err == nil {
				t.Fatal("invalid embedded identity unexpectedly verified")
			}
		})
	}
	var identityRows int
	if err := db.QueryRow(`SELECT COUNT(*) FROM relay_executor_identity`).Scan(&identityRows); err != nil {
		t.Fatal(err)
	}
	if identityRows != 1 {
		t.Fatalf("identity rows = %d, want singleton", identityRows)
	}
}

func TestLegacyMissingIdentityIsGoworkflowsOnly(t *testing.T) {
	ctx := context.Background()
	p, _ := openProjection(t)
	legacy := projection.ExecutorIdentity{ExecutorPlugin: "goworkflows"}
	if err := p.VerifyIdentity(ctx, legacy); err != nil {
		t.Fatalf("legacy embedded identity should be adopted: %v", err)
	}
	got, ok, err := p.Identity(ctx)
	if err != nil || !ok || got != legacy {
		t.Fatalf("adopted legacy identity = %#v/%v, err %v", got, ok, err)
	}

	p2, _ := openProjection(t)
	temporal := projection.ExecutorIdentity{
		ExecutorPlugin: "temporal", TemporalAddress: "localhost:7233", TemporalNamespace: "relay-flow-test",
	}
	if err := p2.VerifyIdentity(ctx, temporal); !errors.Is(err, projection.ErrIdentityMissing) {
		t.Fatalf("missing Temporal identity error = %v, want ErrIdentityMissing", err)
	}
}

func TestIdentitySurvivesProjectionRebuild(t *testing.T) {
	ctx := context.Background()
	p, db := openProjection(t)
	temporal := projection.ExecutorIdentity{
		ExecutorPlugin: "temporal", TemporalAddress: "localhost:7233", TemporalNamespace: "relay-flow-rebuild",
	}
	if err := p.InitializeIdentity(ctx, temporal); err != nil {
		t.Fatalf("initialize Temporal identity: %v", err)
	}
	start := projectionStart("run-1", "PAY-101")
	if err := p.InsertStart(ctx, start, time.Now().UTC()); err != nil {
		t.Fatalf("insert run before rebuild: %v", err)
	}
	if _, err := db.Exec(`DELETE FROM relay_runs`); err != nil {
		t.Fatalf("clear derived projection: %v", err)
	}
	if err := p.Migrate(); err != nil {
		t.Fatalf("rebuild shared projection schema: %v", err)
	}
	if err := p.VerifyIdentity(ctx, temporal); err != nil {
		t.Fatalf("identity after projection rebuild: %v", err)
	}
	got, ok, err := p.Identity(ctx)
	if err != nil || !ok || got != temporal {
		t.Fatalf("identity after rebuild = %#v/%v, err %v", got, ok, err)
	}
}

func boolPtr(v bool) *bool { return &v }
