package goworkflows

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sync"
	"time"

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
CREATE TABLE IF NOT EXISTS relay_runs (
    id TEXT PRIMARY KEY,
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
		"retry_error":   "TEXT",
		"retry_attempt": "INTEGER",
		"next_retry_at": "DATETIME",
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
	return nil
}

var errRunNotFound = errors.New("run not found")
var errNodeRuntimeNotFound = errors.New("node runtime not found")

// IsNotFound reports a missing projection row.
func IsNotFound(err error) bool { return errors.Is(err, errRunNotFound) }

func (p *RunProjection) insertStart(ctx context.Context, s run.Start, now time.Time) error {
	_, err := p.DB.ExecContext(ctx, `
		INSERT INTO relay_runs (id, repo, workflow, ticket_id, ticket_key, state, started_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(id) DO NOTHING`,
		string(s.ID), s.Repo, s.Workflow.Name, s.Ticket.ID, s.Ticket.Key,
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
		SELECT id, repo, workflow, ticket_id, ticket_key, state, current_node, current_node_visit_id, last_error,
			retry_error, retry_attempt, next_retry_at, started_at, updated_at, finished_at
		FROM relay_runs WHERE id = ?`, string(id))
	return scanRun(row)
}

func (p *RunProjection) findByTicket(ctx context.Context, ticket string) (run.Run, error) {
	row := p.DB.QueryRowContext(ctx, `
		SELECT id, repo, workflow, ticket_id, ticket_key, state, current_node, current_node_visit_id, last_error,
			retry_error, retry_attempt, next_retry_at, started_at, updated_at, finished_at
		FROM relay_runs WHERE ticket_key = ? ORDER BY started_at DESC LIMIT 1`, ticket)
	return scanRun(row)
}

type rowScanner interface {
	Scan(dest ...any) error
}

func scanRun(row rowScanner) (run.Run, error) {
	var r run.Run
	var node, visit, lastErr, retryErr sql.NullString
	var retryAttempt sql.NullInt64
	var nextRetry, finished sql.NullTime
	var started, updated time.Time
	err := row.Scan(&r.ID, &r.Repo, &r.Workflow, &r.Ticket.ID, &r.Ticket.Key, &r.State,
		&node, &visit, &lastErr, &retryErr, &retryAttempt, &nextRetry, &started, &updated, &finished)
	if errors.Is(err, sql.ErrNoRows) {
		return run.Run{}, errRunNotFound
	}
	if err != nil {
		return run.Run{}, err
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
	q := `SELECT id, repo, workflow, ticket_id, ticket_key, state, current_node, current_node_visit_id, last_error, retry_error, retry_attempt, next_retry_at, started_at, updated_at, finished_at FROM relay_runs WHERE 1=1`
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
