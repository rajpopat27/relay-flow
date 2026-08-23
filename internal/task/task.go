// Package task defines task-system values and the System contract. Run IDs,
// node visits, reports, and orchestration inputs belong to run, not here.
package task

import (
	"context"

	"github.com/rajpopat27/relay-flow/internal/config"
)

type Ticket struct {
	ID             string
	Key            string
	Title          string
	WorkflowClaims []string
	Fields         map[string]any
}

type TicketRef struct {
	ID    string `json:"id"`
	Key   string `json:"key"`
	Title string `json:"title"`
}

func (t Ticket) Ref() TicketRef {
	return TicketRef{ID: t.ID, Key: t.Key, Title: t.Title}
}

type Mailbox struct {
	ID   string `json:"id"`
	Key  string `json:"key"`
	Node string `json:"node"`
}

type MailboxSpec struct {
	Node        string           `json:"node"`
	Title       string           `json:"title"`
	Description string           `json:"description"`
	TaskConfig  config.RawValues `json:"taskConfig,omitempty"`
}

type Target struct {
	Parent  TicketRef
	Mailbox *Mailbox
}

// System is the task-system contract. One instance is created per
// registered repo and is safe for concurrent use.
//
// Poll returns active parent tickets only; mailbox subtasks are never
// routed as workflow runs. EnsureMailboxes is the sole mailbox discovery
// operation: it finds existing child mailboxes, creates only missing ones,
// and returns the complete node-to-mailbox map. Each method is idempotent
// where the task system permits; adapters own reconciliation when one
// method requires multiple remote calls.
type System interface {
	Poll(ctx context.Context) ([]Ticket, error)
	CompileFilter(workflowTaskConfig config.RawValues) (func(Ticket) bool, error)
	Claim(ctx context.Context, ticket TicketRef, workflow string) error

	ValidateConfig(ctx context.Context, workflowTaskConfig config.RawValues, nodeTaskConfigs map[string]config.RawValues) error
	EnsureMailboxes(ctx context.Context, parent TicketRef, workflow string, specs []MailboxSpec) (map[string]Mailbox, error)
	ApplyTaskConfig(ctx context.Context, target Target, taskConfig config.RawValues) error
	CompleteMailbox(ctx context.Context, mailbox Mailbox) error
	HasComment(ctx context.Context, target Target, marker string) (bool, error)
	Comment(ctx context.Context, target Target, body, marker string) error
	ResetForRecovery(ctx context.Context, parent TicketRef, mailboxes []Mailbox, taskConfig config.RawValues) error
}
