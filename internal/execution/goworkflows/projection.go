package goworkflows

import (
	"context"
	"database/sql"
	"sync"
	"time"

	"github.com/rajpopat27/relay-flow/internal/execution/projection"
	"github.com/rajpopat27/relay-flow/internal/run"
)

// RunProjection is retained as the embedded-engine facade for compatibility
// with the existing go-workflows activities and tests. The relay schema and
// SQL implementation live in internal/execution/projection so Temporal and
// goworkflows share exactly one projection implementation.
type RunProjection struct {
	DB        *sql.DB
	inner     *projection.RunProjection
	runtimeMu sync.Mutex
}

// NodeRuntime is the durable runtime identity for one node in a run.
type NodeRuntime = projection.NodeRuntime

var errRunNotFound = projection.ErrRunNotFound
var errNodeRuntimeNotFound = projection.ErrNodeRuntimeNotFound

// IsNotFound reports a missing projection row.
func IsNotFound(err error) bool { return projection.IsNotFound(err) }

func (p *RunProjection) shared() *projection.RunProjection {
	if p.inner == nil {
		p.inner = &projection.RunProjection{DB: p.DB}
	}
	return p.inner
}

func (p *RunProjection) migrate() error { return p.shared().Migrate() }

func (p *RunProjection) insertStart(ctx context.Context, start run.Start, now time.Time) error {
	return p.shared().InsertStart(ctx, start, now)
}

func (p *RunProjection) updateState(ctx context.Context, id run.ID, state run.State, lastErr string, finished *time.Time) error {
	return p.shared().UpdateState(ctx, id, state, lastErr, finished)
}

func (p *RunProjection) updateRetry(ctx context.Context, id run.ID, status *run.RetryStatus) error {
	return p.shared().UpdateRetry(ctx, id, status)
}

func (p *RunProjection) updateNode(ctx context.Context, id run.ID, state run.State, node string, visit run.NodeVisitID) error {
	return p.shared().UpdateNode(ctx, id, state, node, visit)
}

func (p *RunProjection) getNodeRuntime(ctx context.Context, id run.ID, node string) (NodeRuntime, error) {
	return p.shared().GetNodeRuntime(ctx, id, node)
}

func (p *RunProjection) updateNodeRuntime(ctx context.Context, runtime NodeRuntime) error {
	return p.shared().UpdateNodeRuntime(ctx, runtime)
}

func (p *RunProjection) loadNodeRuntime(ctx context.Context, id run.ID, node string) (NodeRuntime, error) {
	return p.shared().LoadNodeRuntime(ctx, id, node)
}

func (p *RunProjection) nodeRuntimeVisitIsCurrent(ctx context.Context, id run.ID, node string, visit run.NodeVisitID) (bool, error) {
	return p.shared().NodeRuntimeVisitIsCurrent(ctx, id, node, visit)
}

func (p *RunProjection) replaceNodeRuntime(ctx context.Context, id run.ID, node string, visit run.NodeVisitID, terminalID, previousSessionID, sessionID string) error {
	return p.shared().ReplaceNodeRuntime(ctx, id, node, visit, terminalID, previousSessionID, sessionID)
}

func (p *RunProjection) updateNodeRuntimeVisit(ctx context.Context, id run.ID, node string, visit run.NodeVisitID) error {
	return p.shared().UpdateNodeRuntimeVisit(ctx, id, node, visit)
}

func (p *RunProjection) registerNodeSession(ctx context.Context, registration run.NodeRuntimeRegistration) (bool, error) {
	return p.shared().RegisterNodeSession(ctx, registration)
}

func (p *RunProjection) hasProcessedReport(ctx context.Context, id run.ID, reportID string) (bool, error) {
	return p.shared().HasProcessedReport(ctx, id, reportID)
}

func (p *RunProjection) recordProcessedReport(ctx context.Context, id run.ID, visit run.NodeVisitID, reportID string) error {
	return p.shared().RecordProcessedReport(ctx, id, visit, reportID)
}

func (p *RunProjection) clearNodeRuntime(ctx context.Context, id run.ID, node string, clearTerminal, clearSession bool) error {
	return p.shared().ClearNodeRuntime(ctx, id, node, clearTerminal, clearSession)
}

func (p *RunProjection) listNodeRuntimes(ctx context.Context, id run.ID) ([]NodeRuntime, error) {
	return p.shared().ListNodeRuntimes(ctx, id)
}

func (p *RunProjection) get(ctx context.Context, id run.ID) (run.Run, error) {
	return p.shared().Get(ctx, id)
}

func (p *RunProjection) findByTicket(ctx context.Context, ticket string) (run.Run, error) {
	return p.shared().FindByTicket(ctx, ticket)
}

func (p *RunProjection) findByLogicalID(ctx context.Context, logicalID run.ID) (run.Run, error) {
	return p.shared().FindByLogicalID(ctx, logicalID)
}

func (p *RunProjection) list(ctx context.Context, filter run.Filter) ([]run.Run, error) {
	return p.shared().List(ctx, filter)
}

func (p *RunProjection) hasActive(ctx context.Context, column, value string) (bool, error) {
	// Keep this narrow compatibility method for the existing engine; the
	// shared implementation exposes typed workflow/repo query methods.
	if column == "workflow" {
		return p.shared().HasActiveWorkflow(ctx, value)
	}
	return p.shared().HasActiveRepo(ctx, value)
}

func (p *RunProjection) sweepRetention(ctx context.Context, olderThan time.Time) ([]string, error) {
	return p.shared().SweepRetention(ctx, olderThan)
}
