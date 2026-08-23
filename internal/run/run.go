// Package run defines run IDs/state, orchestration values, the durable
// Executor boundary, run queries, and the Run Manager.
package run

import (
	"context"
	"time"

	"github.com/rajpopat27/relay-flow/internal/config"
	"github.com/rajpopat27/relay-flow/internal/identity"
	"github.com/rajpopat27/relay-flow/internal/task"
	"github.com/rajpopat27/relay-flow/internal/workflow"
)

type ID = identity.RunID
type NodeVisitID = identity.NodeVisitID

type State string

const (
	StateStarting  State = "starting"
	StateRunning   State = "running"
	StateWaiting   State = "waiting"
	StateBlocked   State = "blocked"
	StateCompleted State = "completed"
	StateCanceling State = "canceling"
	StateCanceled  State = "canceled"
)

// Start carries the immutable value snapshot of the accepted workflow for
// deterministic replay. The interpreter consumes only this snapshot.
type Start struct {
	ID       ID                `json:"id"`
	Repo     string            `json:"repo"`
	RepoPath string            `json:"repoPath"`
	Workflow workflow.Workflow `json:"workflow"`
	Ticket   task.TicketRef    `json:"ticket"`
}

type Work struct {
	RunID              ID
	Repo               string
	Workflow           string
	Parent             task.TicketRef
	WorkflowTaskConfig config.RawValues
}

type NodeWork struct {
	Work
	Node           string
	NodeVisitID    NodeVisitID
	Mailbox        task.Mailbox
	NodeTaskConfig config.RawValues
}

type CommentWork struct {
	Item   task.Target
	Body   string
	Marker string
}

type Run struct {
	ID                 ID             `json:"id"`
	Repo               string         `json:"repo"`
	Workflow           string         `json:"workflow"`
	Ticket             task.TicketRef `json:"ticket"`
	State              State          `json:"state"`
	CurrentNode        string         `json:"currentNode,omitempty"`
	CurrentNodeVisitID NodeVisitID    `json:"currentNodeVisitId,omitempty"`
	LastError          string         `json:"lastError,omitempty"`
	StartedAt          time.Time      `json:"startedAt"`
	UpdatedAt          time.Time      `json:"updatedAt"`
	FinishedAt         *time.Time     `json:"finishedAt,omitempty"`
}

type ReportRequest struct {
	RunID       ID              `json:"runId"`
	NodeVisitID NodeVisitID     `json:"nodeVisitId"`
	Report      workflow.Report `json:"report"`
}

type ReportAck struct {
	Accepted  bool `json:"accepted"`
	Duplicate bool `json:"duplicate"`
}

type Filter struct {
	Repo     string
	Workflow string
	Ticket   string
	Active   *bool
}

// Executor is the replacement boundary for go-workflows, Temporal, or
// another durable engine. No go-workflows context, instance, backend,
// queue, or error crosses this interface.
type Executor interface {
	EnsureRun(ctx context.Context, start Start) (created bool, err error)
	SubmitReport(ctx context.Context, report ReportRequest) (ReportAck, error)
	CancelRun(ctx context.Context, id ID, reason string) error
}

type RunQueries interface {
	GetRun(ctx context.Context, id ID) (Run, error)
	FindRunByTicket(ctx context.Context, ticket string) (Run, error)
	ListRuns(ctx context.Context, filter Filter) ([]Run, error)
	HasActiveWorkflow(ctx context.Context, workflow string) (bool, error)
	HasActiveRepo(ctx context.Context, repo string) (bool, error)
}
