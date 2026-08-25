package goworkflows

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/rajpopat27/relay-flow/internal/run"
)

// RunProjection is the derived relay_runs read model in the same SQLite
// database as the engine backend. Durable workflow history is authoritative;
// this table serves application-level queries only. Updates are idempotent
// durable activities, so replay repairs interrupted updates.
type RunProjection struct {
	DB *sql.DB
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
	_, err := p.DB.ExecContext(ctx, `
		UPDATE relay_runs SET state = ?, current_node = ?, current_node_visit_id = ?, updated_at = ?
		WHERE id = ?`,
		string(state), node, string(visit), time.Now().UTC(), string(id))
	return err
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
		if _, err := p.DB.ExecContext(ctx, `DELETE FROM relay_runs WHERE id = ?`, id); err != nil {
			return ids, err
		}
	}
	return ids, nil
}
