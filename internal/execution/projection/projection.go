package projection

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"time"

	_ "modernc.org/sqlite"

	"github.com/rajpopat27/relay-flow/internal/run"
)

// RunProjection is the derived relay_runs read model in the same SQLite
// database as the engine backend. Durable workflow history is authoritative;
// this table serves application-level queries only. Updates are idempotent
// durable activities, so replay repairs interrupted updates.
type RunProjection struct {
	DB        *sql.DB
	runtimeMu sync.Mutex
}

func openProjectionDatabase(path string) (*sql.DB, error) {
	db, err := sql.Open("sqlite", fmt.Sprintf("file:%s?_txlock=immediate", path))
	if err != nil {
		return nil, fmt.Errorf("open %s: %w", path, err)
	}
	if _, err := db.Exec(`PRAGMA schema_version`); err != nil {
		db.Close()
		return nil, fmt.Errorf("open %s: %w", path, err)
	}
	if _, err := db.Exec("PRAGMA journal_mode=WAL;"); err != nil {
		db.Close()
		return nil, fmt.Errorf("open %s: %w", path, err)
	}
	if err := os.Chmod(path, 0o600); err != nil && !os.IsNotExist(err) {
		db.Close()
		return nil, fmt.Errorf("chmod %s: %w", path, err)
	}
	if err := (&RunProjection{DB: db}).Migrate(); err != nil {
		db.Close()
		return nil, fmt.Errorf("migrate relay projection: %w", err)
	}
	return db, nil
}

// InitDatabase creates a relay projection database at path. It is shared by
// every durable executor; engine-specific history tables are owned by the
// selected executor and are not created here.
func InitDatabase(path string) error {
	db, err := openProjectionDatabase(path)
	if err != nil {
		return err
	}
	return db.Close()
}

// InitDatabaseWithIdentity initializes the shared relay projection and writes
// its immutable executor marker before reporting initialization success. The
// marker write itself is transactional and uses the same open database that
// created the projection.
func InitDatabaseWithIdentity(path string, identity ExecutorIdentity) error {
	if err := validateExecutorIdentity(identity); err != nil {
		return err
	}
	db, err := openProjectionDatabase(path)
	if err != nil {
		return err
	}
	defer db.Close()
	return (&RunProjection{DB: db}).InitializeIdentity(context.Background(), identity)
}

// HasNonterminalRuns checks a valid existing projection without migrating it.
func HasNonterminalRuns(path string) (bool, error) {
	db, err := sql.Open("sqlite", fmt.Sprintf("file:%s?mode=ro", path))
	if err != nil {
		return false, fmt.Errorf("open %s: %w", path, err)
	}
	defer db.Close()
	var active bool
	if err := db.QueryRow(`SELECT EXISTS(
		SELECT 1 FROM relay_runs WHERE state NOT IN ('completed', 'canceled')
	)`).Scan(&active); err != nil {
		return false, fmt.Errorf("inspect %s: %w", path, err)
	}
	return active, nil
}

// NodeRuntime is the durable runtime identity for one node in a run. Unlike
// relay_runs' current-node fields, one row is retained for every visited node.
type NodeRuntime struct {
	RunID       run.ID
	Node        string
	TerminalID  string
	SessionID   string
	NodeVisitID run.NodeVisitID
	UpdatedAt   time.Time
}

const relayRunsSchema = `
CREATE TABLE IF NOT EXISTS relay_executor_identity (
    singleton INTEGER PRIMARY KEY CHECK (singleton = 1),
    executor_plugin TEXT NOT NULL,
    temporal_address TEXT,
    temporal_namespace TEXT
);
CREATE TABLE IF NOT EXISTS relay_runs (
    id TEXT PRIMARY KEY,
    logical_run_id TEXT,
    attempt_id INTEGER,
    repo TEXT NOT NULL,
    workflow TEXT NOT NULL,
    ticket_id TEXT NOT NULL,
    ticket_key TEXT NOT NULL,
    state TEXT NOT NULL,
    current_node TEXT,
    current_node_visit_id TEXT,
    last_error TEXT,
	retry_error TEXT,
	retry_attempt INTEGER,
	next_retry_at DATETIME,
    started_at DATETIME NOT NULL,
    updated_at DATETIME NOT NULL,
    finished_at DATETIME
);
CREATE INDEX IF NOT EXISTS relay_runs_ticket_key ON relay_runs (ticket_key);
CREATE INDEX IF NOT EXISTS relay_runs_workflow_state ON relay_runs (workflow, state);
CREATE INDEX IF NOT EXISTS relay_runs_repo_state ON relay_runs (repo, state);
CREATE TABLE IF NOT EXISTS relay_node_runtime (
    run_id TEXT NOT NULL,
    node TEXT NOT NULL,
    terminal_id TEXT,
    session_id TEXT,
    node_visit_id TEXT NOT NULL,
    updated_at DATETIME NOT NULL,
    PRIMARY KEY (run_id, node),
    FOREIGN KEY (run_id) REFERENCES relay_runs(id) ON DELETE CASCADE
);
CREATE TABLE IF NOT EXISTS relay_processed_reports (
    run_id TEXT NOT NULL,
    report_id TEXT NOT NULL,
    node_visit_id TEXT NOT NULL,
    created_at DATETIME NOT NULL,
    PRIMARY KEY (run_id, report_id),
    FOREIGN KEY (run_id) REFERENCES relay_runs(id) ON DELETE CASCADE
);
CREATE TABLE IF NOT EXISTS relay_node_sessions (
    run_id TEXT NOT NULL,
    node TEXT NOT NULL,
    session_id TEXT NOT NULL,
    node_visit_id TEXT NOT NULL,
    created_at DATETIME NOT NULL,
    PRIMARY KEY (run_id, node, session_id),
    FOREIGN KEY (run_id) REFERENCES relay_runs(id) ON DELETE CASCADE
);
`

func (p *RunProjection) migrate() error {
	if _, err := p.DB.Exec(relayRunsSchema); err != nil {
		return err
	}
	for name, definition := range map[string]string{
		"logical_run_id": "TEXT",
		"attempt_id":     "INTEGER",
		"retry_error":    "TEXT",
		"retry_attempt":  "INTEGER",
		"next_retry_at":  "DATETIME",
	} {
		var count int
		if err := p.DB.QueryRow(`SELECT COUNT(1) FROM pragma_table_info('relay_runs') WHERE name = ?`, name).Scan(&count); err != nil {
			return err
		}
		if count == 0 {
			if _, err := p.DB.Exec(`ALTER TABLE relay_runs ADD COLUMN ` + name + ` ` + definition); err != nil {
				return err
			}
		}
	}
	// Rows created before attempt identities were introduced represent the
	// original attempt. Backfill the stable logical ID and attempt number so
	// restart allocation remains numeric and never reuses attempt 1.
	if _, err := p.DB.Exec(`UPDATE relay_runs SET logical_run_id = id WHERE COALESCE(logical_run_id, '') = ''`); err != nil {
		return err
	}
	if _, err := p.DB.Exec(`UPDATE relay_runs SET attempt_id = 1 WHERE attempt_id IS NULL OR attempt_id = 0`); err != nil {
		return err
	}
	return nil
}

var errRunNotFound = errors.New("run not found")
var errNodeRuntimeNotFound = errors.New("node runtime not found")

// IsNotFound reports a missing projection row.
func IsNotFound(err error) bool { return errors.Is(err, errRunNotFound) }

func (p *RunProjection) insertStart(ctx context.Context, s run.Start, now time.Time) error {
	logicalID := s.LogicalID
	if logicalID == "" {
		logicalID = s.ID
	}
	attemptID := s.AttemptID
	if attemptID == 0 {
		attemptID = 1
	}
	_, err := p.DB.ExecContext(ctx, `
		INSERT INTO relay_runs (id, logical_run_id, attempt_id, repo, workflow, ticket_id, ticket_key, state, started_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(id) DO NOTHING`,
		string(s.ID), string(logicalID), int64(attemptID), s.Repo, s.Workflow.Name, s.Ticket.ID, s.Ticket.Key,
		string(run.StateStarting), now, now)
	return err
}

func (p *RunProjection) updateState(ctx context.Context, id run.ID, state run.State, lastErr string, finished *time.Time) error {
	terminal := state == run.StateCompleted || state == run.StateCanceled
	_, err := p.DB.ExecContext(ctx, `
		UPDATE relay_runs SET state = ?, last_error = ?, updated_at = ?, finished_at = COALESCE(?, finished_at),
			retry_error = CASE WHEN ? THEN NULL ELSE retry_error END,
			retry_attempt = CASE WHEN ? THEN NULL ELSE retry_attempt END,
			next_retry_at = CASE WHEN ? THEN NULL ELSE next_retry_at END
		WHERE id = ?`,
		string(state), lastErr, time.Now().UTC(), finished, terminal, terminal, terminal, string(id))
	return err
}

func (p *RunProjection) updateRetry(ctx context.Context, id run.ID, status *run.RetryStatus) error {
	if status == nil {
		_, err := p.DB.ExecContext(ctx, `
			UPDATE relay_runs SET retry_error = NULL, retry_attempt = NULL, next_retry_at = NULL, updated_at = ?
			WHERE id = ?`, time.Now().UTC(), string(id))
		return err
	}
	_, err := p.DB.ExecContext(ctx, `
		UPDATE relay_runs SET retry_error = ?, retry_attempt = ?, next_retry_at = ?, updated_at = ?
		WHERE id = ?`, status.LastError, status.Attempt, status.NextRetryAt, time.Now().UTC(), string(id))
	return err
}

func (p *RunProjection) updateNode(ctx context.Context, id run.ID, state run.State, node string, visit run.NodeVisitID) error {
	p.runtimeMu.Lock()
	defer p.runtimeMu.Unlock()
	tx, err := p.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	now := time.Now().UTC()
	if _, err := tx.ExecContext(ctx, `
		UPDATE relay_runs SET state = ?, current_node = ?, current_node_visit_id = ?, updated_at = ?
		WHERE id = ?`,
		string(state), node, string(visit), now, string(id)); err != nil {
		return err
	}
	// A revisit changes only the latest visit ID. Reusable terminal/session
	// identities remain attached to this run/node row.
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO relay_node_runtime (run_id, node, node_visit_id, updated_at)
		VALUES (?, ?, ?, ?)
		ON CONFLICT(run_id, node) DO UPDATE SET
			node_visit_id = excluded.node_visit_id,
			updated_at = excluded.updated_at`,
		string(id), node, string(visit), now); err != nil {
		return err
	}
	return tx.Commit()
}

func (p *RunProjection) getNodeRuntime(ctx context.Context, id run.ID, node string) (NodeRuntime, error) {
	var rt NodeRuntime
	var terminalID, sessionID sql.NullString
	err := p.DB.QueryRowContext(ctx, `
		SELECT run_id, node, terminal_id, session_id, node_visit_id, updated_at
		FROM relay_node_runtime WHERE run_id = ? AND node = ?`, string(id), node).
		Scan(&rt.RunID, &rt.Node, &terminalID, &sessionID, &rt.NodeVisitID, &rt.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return NodeRuntime{}, errNodeRuntimeNotFound
	}
	if err != nil {
		return NodeRuntime{}, err
	}
	rt.TerminalID = terminalID.String
	rt.SessionID = sessionID.String
	return rt, nil
}

func (p *RunProjection) updateNodeRuntime(ctx context.Context, rt NodeRuntime) error {
	_, err := p.DB.ExecContext(ctx, `
		INSERT INTO relay_node_runtime (run_id, node, terminal_id, session_id, node_visit_id, updated_at)
		VALUES (?, ?, ?, ?, ?, ?)
		ON CONFLICT(run_id, node) DO UPDATE SET
			terminal_id = excluded.terminal_id,
			session_id = excluded.session_id,
			node_visit_id = excluded.node_visit_id,
			updated_at = excluded.updated_at`,
		string(rt.RunID), rt.Node, nullableString(rt.TerminalID), nullableString(rt.SessionID),
		string(rt.NodeVisitID), time.Now().UTC())
	return err
}

func (p *RunProjection) loadNodeRuntime(ctx context.Context, id run.ID, node string) (NodeRuntime, error) {
	rt, err := p.getNodeRuntime(ctx, id, node)
	if errors.Is(err, errNodeRuntimeNotFound) {
		return NodeRuntime{RunID: id, Node: node}, nil
	}
	return rt, err
}

func (p *RunProjection) nodeRuntimeVisitIsCurrent(ctx context.Context, id run.ID, node string, visit run.NodeVisitID) (bool, error) {
	var count int
	err := p.DB.QueryRowContext(ctx, `
		SELECT COUNT(1) FROM relay_node_runtime
		WHERE run_id = ? AND node = ? AND node_visit_id = ?`,
		string(id), node, string(visit)).Scan(&count)
	return count == 1, err
}

func (p *RunProjection) replaceNodeRuntime(ctx context.Context, id run.ID, node string, visit run.NodeVisitID, terminalID, previousSessionID, sessionID string) error {
	result, err := p.DB.ExecContext(ctx, `
		UPDATE relay_node_runtime SET terminal_id = ?,
			session_id = CASE WHEN COALESCE(session_id, '') = ? THEN ? ELSE session_id END,
			updated_at = ?
		WHERE run_id = ? AND node = ? AND node_visit_id = ?`,
		nullableString(terminalID), previousSessionID, nullableString(sessionID), time.Now().UTC(),
		string(id), node, string(visit))
	if err != nil {
		return err
	}
	updated, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if updated != 1 {
		return fmt.Errorf("node runtime %s/%s visit %s is not current", id, node, visit)
	}
	return nil
}

func (p *RunProjection) updateNodeRuntimeVisit(ctx context.Context, id run.ID, node string, visit run.NodeVisitID) error {
	p.runtimeMu.Lock()
	defer p.runtimeMu.Unlock()
	_, err := p.DB.ExecContext(ctx, `
		INSERT INTO relay_node_runtime (run_id, node, node_visit_id, updated_at)
		VALUES (?, ?, ?, ?)
		ON CONFLICT(run_id, node) DO UPDATE SET
			node_visit_id = excluded.node_visit_id,
			updated_at = excluded.updated_at`,
		string(id), node, string(visit), time.Now().UTC())
	return err
}

func (p *RunProjection) registerNodeSession(ctx context.Context, registration run.NodeRuntimeRegistration) (bool, error) {
	p.runtimeMu.Lock()
	defer p.runtimeMu.Unlock()
	tx, err := p.DB.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer tx.Rollback()
	var current run.NodeVisitID
	if err := tx.QueryRowContext(ctx, `
		SELECT node_visit_id FROM relay_node_runtime WHERE run_id = ? AND node = ?`,
		string(registration.RunID), registration.Node).Scan(&current); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return false, nil
		}
		return false, err
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO relay_node_sessions (run_id, node, session_id, node_visit_id, created_at)
		VALUES (?, ?, ?, ?, ?) ON CONFLICT(run_id, node, session_id) DO NOTHING`,
		string(registration.RunID), registration.Node, registration.SessionID, string(current), time.Now().UTC()); err != nil {
		return false, err
	}
	var bound run.NodeVisitID
	if err := tx.QueryRowContext(ctx, `
		SELECT node_visit_id FROM relay_node_sessions
		WHERE run_id = ? AND node = ? AND session_id = ?`,
		string(registration.RunID), registration.Node, registration.SessionID).Scan(&bound); err != nil {
		return false, err
	}
	if bound != current {
		return false, tx.Commit()
	}
	result, err := tx.ExecContext(ctx, `
		UPDATE relay_node_runtime SET session_id = ?, updated_at = ?
		WHERE run_id = ? AND node = ? AND node_visit_id = ?`,
		registration.SessionID, time.Now().UTC(), string(registration.RunID), registration.Node, string(bound))
	if err != nil {
		return false, err
	}
	updated, err := result.RowsAffected()
	if err != nil {
		return false, err
	}
	return updated == 1, tx.Commit()
}

func (p *RunProjection) hasProcessedReport(ctx context.Context, id run.ID, reportID string) (bool, error) {
	var count int
	err := p.DB.QueryRowContext(ctx, `
		SELECT COUNT(1) FROM relay_processed_reports WHERE run_id = ? AND report_id = ?`,
		string(id), reportID).Scan(&count)
	return count == 1, err
}

func (p *RunProjection) recordProcessedReport(ctx context.Context, id run.ID, visit run.NodeVisitID, reportID string) error {
	_, err := p.DB.ExecContext(ctx, `
		INSERT INTO relay_processed_reports (run_id, report_id, node_visit_id, created_at)
		VALUES (?, ?, ?, ?) ON CONFLICT(run_id, report_id) DO NOTHING`,
		string(id), reportID, string(visit), time.Now().UTC())
	return err
}

func (p *RunProjection) clearNodeRuntime(ctx context.Context, id run.ID, node string, clearTerminal, clearSession bool) error {
	_, err := p.DB.ExecContext(ctx, `
		UPDATE relay_node_runtime SET
			terminal_id = CASE WHEN ? THEN NULL ELSE terminal_id END,
			session_id = CASE WHEN ? THEN NULL ELSE session_id END,
			updated_at = ?
		WHERE run_id = ? AND node = ?`, clearTerminal, clearSession,
		time.Now().UTC(), string(id), node)
	return err
}

func (p *RunProjection) listNodeRuntimes(ctx context.Context, id run.ID) ([]NodeRuntime, error) {
	rows, err := p.DB.QueryContext(ctx, `
		SELECT run_id, node, terminal_id, session_id, node_visit_id, updated_at
		FROM relay_node_runtime WHERE run_id = ?`, string(id))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []NodeRuntime
	for rows.Next() {
		var rt NodeRuntime
		var terminalID, sessionID sql.NullString
		if err := rows.Scan(&rt.RunID, &rt.Node, &terminalID, &sessionID, &rt.NodeVisitID, &rt.UpdatedAt); err != nil {
			return nil, err
		}
		rt.TerminalID, rt.SessionID = terminalID.String, sessionID.String
		out = append(out, rt)
	}
	return out, rows.Err()
}

func nullableString(value string) any {
	if value == "" {
		return nil
	}
	return value
}

func (p *RunProjection) get(ctx context.Context, id run.ID) (run.Run, error) {
	row := p.DB.QueryRowContext(ctx, `
		SELECT id, logical_run_id, attempt_id, repo, workflow, ticket_id, ticket_key, state, current_node, current_node_visit_id, last_error,
			retry_error, retry_attempt, next_retry_at, started_at, updated_at, finished_at
		FROM relay_runs WHERE id = ?`, string(id))
	return scanRun(row)
}

func (p *RunProjection) findByTicket(ctx context.Context, ticket string) (run.Run, error) {
	row := p.DB.QueryRowContext(ctx, `
		SELECT id, logical_run_id, attempt_id, repo, workflow, ticket_id, ticket_key, state, current_node, current_node_visit_id, last_error,
			retry_error, retry_attempt, next_retry_at, started_at, updated_at, finished_at
		FROM relay_runs WHERE ticket_key = ? ORDER BY started_at DESC, attempt_id DESC LIMIT 1`, ticket)
	return scanRun(row)
}

func (p *RunProjection) findByLogicalID(ctx context.Context, logicalID run.ID) (run.Run, error) {
	row := p.DB.QueryRowContext(ctx, `
		SELECT id, logical_run_id, attempt_id, repo, workflow, ticket_id, ticket_key, state, current_node, current_node_visit_id, last_error,
			retry_error, retry_attempt, next_retry_at, started_at, updated_at, finished_at
		FROM relay_runs WHERE logical_run_id = ? ORDER BY started_at DESC, attempt_id DESC LIMIT 1`, string(logicalID))
	return scanRun(row)
}

type rowScanner interface {
	Scan(dest ...any) error
}

func scanRun(row rowScanner) (run.Run, error) {
	var r run.Run
	var logicalID sql.NullString
	var attemptNumber sql.NullInt64
	var node, visit, lastErr, retryErr sql.NullString
	var retryAttempt sql.NullInt64
	var nextRetry, finished sql.NullTime
	var started, updated time.Time
	err := row.Scan(&r.ID, &logicalID, &attemptNumber, &r.Repo, &r.Workflow, &r.Ticket.ID, &r.Ticket.Key, &r.State,
		&node, &visit, &lastErr, &retryErr, &retryAttempt, &nextRetry, &started, &updated, &finished)
	if errors.Is(err, sql.ErrNoRows) {
		return run.Run{}, errRunNotFound
	}
	if err != nil {
		return run.Run{}, err
	}
	if logicalID.Valid && logicalID.String != "" {
		r.LogicalID = run.ID(logicalID.String)
	} else {
		r.LogicalID = r.ID
	}
	if attemptNumber.Valid && attemptNumber.Int64 > 0 {
		r.AttemptID = run.AttemptID(attemptNumber.Int64)
	} else {
		r.AttemptID = 1
	}
	r.CurrentNode = node.String
	r.CurrentNodeVisitID = run.NodeVisitID(visit.String)
	r.LastError = lastErr.String
	if retryErr.Valid && retryAttempt.Valid && nextRetry.Valid {
		r.Retry = &run.RetryStatus{
			Attempt: int(retryAttempt.Int64), LastError: retryErr.String, NextRetryAt: nextRetry.Time,
		}
	}
	r.StartedAt = started
	r.UpdatedAt = updated
	if finished.Valid {
		t := finished.Time
		r.FinishedAt = &t
	}
	return r, nil
}

func (p *RunProjection) list(ctx context.Context, f run.Filter) ([]run.Run, error) {
	q := `SELECT id, logical_run_id, attempt_id, repo, workflow, ticket_id, ticket_key, state, current_node, current_node_visit_id, last_error, retry_error, retry_attempt, next_retry_at, started_at, updated_at, finished_at FROM relay_runs WHERE 1=1`
	var args []any
	if f.Repo != "" {
		q += ` AND repo = ?`
		args = append(args, f.Repo)
	}
	if f.Workflow != "" {
		q += ` AND workflow = ?`
		args = append(args, f.Workflow)
	}
	if f.Ticket != "" {
		q += ` AND ticket_key = ?`
		args = append(args, f.Ticket)
	}
	if f.Active != nil {
		if *f.Active {
			q += ` AND state NOT IN ('completed', 'canceled')`
		} else {
			q += ` AND state IN ('completed', 'canceled')`
		}
	}
	q += ` ORDER BY started_at`
	rows, err := p.DB.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []run.Run
	for rows.Next() {
		r, err := scanRun(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

func (p *RunProjection) hasActive(ctx context.Context, column, value string) (bool, error) {
	var n int
	err := p.DB.QueryRowContext(ctx,
		fmt.Sprintf(`SELECT COUNT(1) FROM relay_runs WHERE %s = ? AND state NOT IN ('completed', 'canceled')`, column),
		value).Scan(&n)
	return n > 0, err
}

// sweepRetention removes terminal projection rows whose finished_at is older
// than the retention window. Nonterminal runs are never removed.
func (p *RunProjection) sweepRetention(ctx context.Context, olderThan time.Time) ([]string, error) {
	rows, err := p.DB.QueryContext(ctx, `
		SELECT id FROM relay_runs
		WHERE state IN ('completed', 'canceled') AND finished_at IS NOT NULL AND finished_at < ?`, olderThan)
	if err != nil {
		return nil, err
	}
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return nil, err
		}
		ids = append(ids, id)
	}
	rows.Close()
	for _, id := range ids {
		tx, err := p.DB.BeginTx(ctx, nil)
		if err != nil {
			return ids, err
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM relay_processed_reports WHERE run_id = ?`, id); err != nil {
			tx.Rollback()
			return ids, err
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM relay_node_sessions WHERE run_id = ?`, id); err != nil {
			tx.Rollback()
			return ids, err
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM relay_node_runtime WHERE run_id = ?`, id); err != nil {
			tx.Rollback()
			return ids, err
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM relay_runs WHERE id = ?`, id); err != nil {
			tx.Rollback()
			return ids, err
		}
		if err := tx.Commit(); err != nil {
			return ids, err
		}
	}
	return ids, nil
}

// The exported methods below are the engine-neutral projection surface. The
// small unexported methods above retain the original implementation shape so
// the goworkflows facade can preserve its package-local call sites without
// copying the schema or SQL.

// Migrate creates or upgrades all relay-owned projection tables.
func (p *RunProjection) Migrate() error { return p.migrate() }

// InsertStart records a starting run idempotently.
func (p *RunProjection) InsertStart(ctx context.Context, start run.Start, now time.Time) error {
	return p.insertStart(ctx, start, now)
}

// UpdateState updates the lifecycle state and terminal metadata.
func (p *RunProjection) UpdateState(ctx context.Context, id run.ID, state run.State, lastErr string, finished *time.Time) error {
	return p.updateState(ctx, id, state, lastErr, finished)
}

// UpdateRetry updates or clears active retry metadata.
func (p *RunProjection) UpdateRetry(ctx context.Context, id run.ID, status *run.RetryStatus) error {
	return p.updateRetry(ctx, id, status)
}

// UpdateNode updates the current node and its latest visit identity.
func (p *RunProjection) UpdateNode(ctx context.Context, id run.ID, state run.State, node string, visit run.NodeVisitID) error {
	return p.updateNode(ctx, id, state, node, visit)
}

// GetNodeRuntime returns one node's persisted runtime binding.
func (p *RunProjection) GetNodeRuntime(ctx context.Context, id run.ID, node string) (NodeRuntime, error) {
	return p.getNodeRuntime(ctx, id, node)
}

// UpdateNodeRuntime upserts a node's persisted runtime binding.
func (p *RunProjection) UpdateNodeRuntime(ctx context.Context, runtime NodeRuntime) error {
	return p.updateNodeRuntime(ctx, runtime)
}

// LoadNodeRuntime loads a runtime binding, returning an empty binding when the
// node has not been seen yet.
func (p *RunProjection) LoadNodeRuntime(ctx context.Context, id run.ID, node string) (NodeRuntime, error) {
	return p.loadNodeRuntime(ctx, id, node)
}

// NodeRuntimeVisitIsCurrent reports whether visit is the current node visit.
func (p *RunProjection) NodeRuntimeVisitIsCurrent(ctx context.Context, id run.ID, node string, visit run.NodeVisitID) (bool, error) {
	return p.nodeRuntimeVisitIsCurrent(ctx, id, node, visit)
}

// ReplaceNodeRuntime replaces runtime handles for the current visit.
func (p *RunProjection) ReplaceNodeRuntime(ctx context.Context, id run.ID, node string, visit run.NodeVisitID, terminalID, previousSessionID, sessionID string) error {
	return p.replaceNodeRuntime(ctx, id, node, visit, terminalID, previousSessionID, sessionID)
}

// UpdateNodeRuntimeVisit updates only the current visit while preserving
// reusable terminal and session handles.
func (p *RunProjection) UpdateNodeRuntimeVisit(ctx context.Context, id run.ID, node string, visit run.NodeVisitID) error {
	return p.updateNodeRuntimeVisit(ctx, id, node, visit)
}

// RegisterNodeSession records a runtime session and binds it only when its
// visit is still current.
func (p *RunProjection) RegisterNodeSession(ctx context.Context, registration run.NodeRuntimeRegistration) (bool, error) {
	return p.registerNodeSession(ctx, registration)
}

// HasProcessedReport checks the derived report receipt fast path.
func (p *RunProjection) HasProcessedReport(ctx context.Context, id run.ID, reportID string) (bool, error) {
	return p.hasProcessedReport(ctx, id, reportID)
}

// RecordProcessedReport records a report receipt idempotently.
func (p *RunProjection) RecordProcessedReport(ctx context.Context, id run.ID, visit run.NodeVisitID, reportID string) error {
	return p.recordProcessedReport(ctx, id, visit, reportID)
}

// ClearNodeRuntime clears selected reusable runtime handles.
func (p *RunProjection) ClearNodeRuntime(ctx context.Context, id run.ID, node string, clearTerminal, clearSession bool) error {
	return p.clearNodeRuntime(ctx, id, node, clearTerminal, clearSession)
}

// ListNodeRuntimes returns all persisted bindings for one run.
func (p *RunProjection) ListNodeRuntimes(ctx context.Context, id run.ID) ([]NodeRuntime, error) {
	return p.listNodeRuntimes(ctx, id)
}

// Get returns one derived run row.
func (p *RunProjection) Get(ctx context.Context, id run.ID) (run.Run, error) {
	return p.get(ctx, id)
}

// FindByTicket returns the latest derived run for a ticket.
func (p *RunProjection) FindByTicket(ctx context.Context, ticket string) (run.Run, error) {
	return p.findByTicket(ctx, ticket)
}

// FindByLogicalID returns the latest attempt for a logical run.
func (p *RunProjection) FindByLogicalID(ctx context.Context, logicalID run.ID) (run.Run, error) {
	return p.findByLogicalID(ctx, logicalID)
}

// List returns derived runs matching filter.
func (p *RunProjection) List(ctx context.Context, filter run.Filter) ([]run.Run, error) {
	return p.list(ctx, filter)
}

// HasActiveWorkflow reports whether a nonterminal run exists for workflow.
func (p *RunProjection) HasActiveWorkflow(ctx context.Context, workflow string) (bool, error) {
	return p.hasActive(ctx, "workflow", workflow)
}

// HasActiveRepo reports whether a nonterminal run exists for repo.
func (p *RunProjection) HasActiveRepo(ctx context.Context, repoName string) (bool, error) {
	return p.hasActive(ctx, "repo", repoName)
}

// SweepRetention deletes terminal rows older than olderThan and their derived
// child rows. It never removes active lifecycle states.
func (p *RunProjection) SweepRetention(ctx context.Context, olderThan time.Time) ([]string, error) {
	return p.sweepRetention(ctx, olderThan)
}

// ErrRunNotFound identifies a missing derived run row.
var ErrRunNotFound = errRunNotFound

// ErrNodeRuntimeNotFound identifies a missing node runtime row.
var ErrNodeRuntimeNotFound = errNodeRuntimeNotFound

// ExecutorIdentity is the immutable durable-execution identity of one
// initialized relay-flow home.
type ExecutorIdentity struct {
	ExecutorPlugin    string
	TemporalAddress   string
	TemporalNamespace string
}

// ErrIdentityMissing means a database has no installation identity marker.
var ErrIdentityMissing = errors.New("executor identity missing")

// ErrIdentityMismatch means configured execution identity differs from the
// initialized installation marker.
var ErrIdentityMismatch = errors.New("executor identity mismatch")

func validateExecutorIdentity(identity ExecutorIdentity) error {
	switch identity.ExecutorPlugin {
	case "goworkflows":
		if identity.TemporalAddress != "" || identity.TemporalNamespace != "" {
			return fmt.Errorf("goworkflows identity must not contain Temporal address or namespace")
		}
	case "temporal":
		if strings.TrimSpace(identity.TemporalAddress) == "" || strings.TrimSpace(identity.TemporalNamespace) == "" {
			return fmt.Errorf("temporal identity requires address and namespace")
		}
	default:
		return fmt.Errorf("unknown executor plugin %q", identity.ExecutorPlugin)
	}
	return nil
}

// InitializeIdentity atomically creates the singleton marker or verifies that
// an existing marker has exactly the same durable identity. It never updates a
// marker in place, so a changed executor/address/namespace fails closed.
func (p *RunProjection) InitializeIdentity(ctx context.Context, expected ExecutorIdentity) error {
	if err := validateExecutorIdentity(expected); err != nil {
		return err
	}
	tx, err := p.DB.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin executor identity transaction: %w", err)
	}
	defer tx.Rollback()

	var actual ExecutorIdentity
	var address, namespace sql.NullString
	err = tx.QueryRowContext(ctx, `
		SELECT executor_plugin, temporal_address, temporal_namespace
		FROM relay_executor_identity WHERE singleton = 1`).Scan(
		&actual.ExecutorPlugin, &address, &namespace)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		_, err = tx.ExecContext(ctx, `
			INSERT INTO relay_executor_identity
				(singleton, executor_plugin, temporal_address, temporal_namespace)
			VALUES (1, ?, ?, ?)`, expected.ExecutorPlugin, nullableString(expected.TemporalAddress), nullableString(expected.TemporalNamespace))
		if err != nil {
			return fmt.Errorf("insert executor identity: %w", err)
		}
	case err != nil:
		return fmt.Errorf("read executor identity: %w", err)
	default:
		actual.TemporalAddress = address.String
		actual.TemporalNamespace = namespace.String
		if actual != expected {
			return fmt.Errorf("%w: configured=%+v persisted=%+v", ErrIdentityMismatch, expected, actual)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit executor identity: %w", err)
	}
	return nil
}

// VerifyIdentity checks the singleton marker before worker startup. A legacy
// marker-less database may be adopted only by the embedded executor; this
// first successful verification records that legacy identity.
func (p *RunProjection) VerifyIdentity(ctx context.Context, expected ExecutorIdentity) error {
	if err := validateExecutorIdentity(expected); err != nil {
		return err
	}
	actual, present, err := p.Identity(ctx)
	if err != nil {
		return err
	}
	if !present {
		if expected.ExecutorPlugin != "goworkflows" {
			return fmt.Errorf("%w: Temporal installation requires an initialized marker", ErrIdentityMissing)
		}
		return p.InitializeIdentity(ctx, expected)
	}
	if actual != expected {
		return fmt.Errorf("%w: configured=%+v persisted=%+v", ErrIdentityMismatch, expected, actual)
	}
	return nil
}

// Identity reads the singleton marker. The boolean is false when the marker is
// absent; absence is distinct from a malformed/missing table error.
func (p *RunProjection) Identity(ctx context.Context) (ExecutorIdentity, bool, error) {
	var identity ExecutorIdentity
	var address, namespace sql.NullString
	err := p.DB.QueryRowContext(ctx, `
		SELECT executor_plugin, temporal_address, temporal_namespace
		FROM relay_executor_identity WHERE singleton = 1`).Scan(
		&identity.ExecutorPlugin, &address, &namespace)
	if errors.Is(err, sql.ErrNoRows) {
		return ExecutorIdentity{}, false, nil
	}
	if err != nil {
		return ExecutorIdentity{}, false, fmt.Errorf("read executor identity: %w", err)
	}
	identity.TemporalAddress = address.String
	identity.TemporalNamespace = namespace.String
	return identity, true, nil
}
