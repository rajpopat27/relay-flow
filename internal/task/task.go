// Package task defines task-system values and the System contract. Run IDs,
// node visits, reports, and orchestration inputs belong to run, not here.
package task

import (
	"context"
	"errors"
	"fmt"

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
	TextData    TextData         `json:"textData"`
}

type TextKind string

const (
	TextMailboxDescription TextKind = "mailboxDescription"
	TextSummaryComment     TextKind = "summaryComment"
	TextFeedbackComment    TextKind = "feedbackComment"
)

// TextData contains existing run, mailbox, and node values supplied to a
// task-system text template. The task system owns rendering; harness prompt
// templates are a separate contract.
type TextData struct {
	RunID           string
	Ticket          string
	Workflow        string
	Repo            string
	Node            string
	NodeType        string
	Agent           string
	NodeDescription string
	NextSteps       string
	SuccessRoutes   string
	FailureRoutes   string
	Mailbox         string
	SourceNode      string
	TargetNode      string
	SummaryReport   string
	FeedbackReport  string
	Report          string
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
	RenderText(kind TextKind, data TextData) (string, error)
	EnsureMailboxes(ctx context.Context, parent TicketRef, workflow string, specs []MailboxSpec) (map[string]Mailbox, error)
	ApplyTaskConfig(ctx context.Context, target Target, taskConfig config.RawValues) error
	CompleteMailbox(ctx context.Context, mailbox Mailbox) error
	HasComment(ctx context.Context, target Target, marker string) (bool, error)
	Comment(ctx context.Context, target Target, body, marker string) error
	ResetForRecovery(ctx context.Context, parent TicketRef, mailboxes []Mailbox, taskConfig config.RawValues) error
}

// ErrOwnershipMismatch identifies a task-system ownership predicate that did
// not accept the current ticket. Adapters return OwnershipMismatchError from
// their last-mile claim check and from claimed-run revalidation; core can
// classify the race without learning provider-specific owner fields.
var ErrOwnershipMismatch = errors.New("ticket owner does not match workflow")

// OwnershipMismatchError is the provider-neutral shape of an ownership
// rejection. It intentionally omits the current owner so diagnostics do not
// expose unnecessary personal data.
type OwnershipMismatchError struct {
	Ticket   string
	Workflow string
}

func (e *OwnershipMismatchError) Error() string {
	return fmt.Sprintf("ticket %s is not owned by workflow %s", e.Ticket, e.Workflow)
}

func (e *OwnershipMismatchError) Unwrap() error { return ErrOwnershipMismatch }

// OwnershipFilterCompiler is one part of the required automatic-routing
// ownership capability set. The returned matcher contains only the
// ownership/assignee predicate from the effective workflow config; it must not
// repeat lifecycle filters such as status, issue type, or ordinary labels on a
// claimed-ticket route.
type OwnershipFilterCompiler interface {
	CompileOwnershipFilter(workflowTaskConfig config.RawValues) (func(Ticket) bool, error)
}

// OwnershipValidator is one part of the required automatic-routing
// ownership capability set. It is used at the last-mile boundary and before
// claimed-run recovery, re-reading the provider-owned ticket and evaluating
// the effective ownership predicate.
type OwnershipValidator interface {
	ValidateOwnership(ctx context.Context, ticket TicketRef, workflow string, workflowTaskConfig config.RawValues) error
}

// ConditionalClaimer is one part of the required automatic-routing
// ownership capability set. It replaces Claim for an unassigned poll result;
// the adapter performs its final ownership read immediately before the
// idempotent workflow-label write, closing the poll-to-claim reassignment race.
type ConditionalClaimer interface {
	ClaimIfOwned(ctx context.Context, ticket TicketRef, workflow string, workflowTaskConfig config.RawValues) error
}

// OwnershipCapabilities is the complete capability set required by an
// adapter that participates in automatic workflow routing. Keeping this as a
// separate boundary leaves the core System contract focused on task
// primitives while allowing registration to fail closed when an adapter would
// otherwise silently fall back to unrestricted Claim behavior.
type OwnershipCapabilities interface {
	OwnershipFilterCompiler
	OwnershipValidator
	ConditionalClaimer
}

// ClaimOwnerBackfiller is an explicit provider-side repair capability for a
// legacy wf: claim that predates ownership provenance. Normal routing never
// invokes it; an operator/provider workflow may add the durable marker after
// verifying the live assignee, without removing the existing claim or history.
type ClaimOwnerBackfiller interface {
	BackfillClaimOwner(ctx context.Context, ticket TicketRef, workflow string, workflowTaskConfig config.RawValues) error
}

// AgentEnvironment is an optional adapter capability. It returns the
// task-system workspace environment an agent process must receive so agent
// task commands address the same workspace as relay-flow. Values are
// workspace selectors only, never credentials; an empty value explicitly
// neutralizes an ambient selector. Core forwards the map through the harness
// launch spec without learning adapter vocabulary.
type AgentEnvironment interface {
	AgentEnv() map[string]string
}

// RestartPreparer is an optional adapter capability used only by explicit
// run restarts. It reopens relay-owned mailbox state while preserving all
// comments, labels, and descriptions. A human-owned incompatible state must
// be returned as retry.ConflictError; core never names provider statuses.
type RestartPreparer interface {
	PrepareRestart(ctx context.Context, parent TicketRef, mailboxes []Mailbox) error
}

// LifecycleDefaults is an optional adapter capability: a System whose
// taskConfig carries lifecycle-dependent defaults (e.g. Jira's deterministic
// transitionTo defaults) exposes them here. The lifecycle-aware caller
// (run orchestration) merges the returned raw values into the effective node
// config before ApplyTaskConfig based on whether the node is start, a work
// node, or end. Core never learns adapter vocabulary; the adapter never
// learns lifecycle steps. The System interface is unchanged.
type LifecycleDefaults interface {
	// StartDefaults are merged under the start node's taskConfig.
	StartDefaults() config.RawValues
	// WorkDefaults are merged under every work node's taskConfig.
	WorkDefaults() config.RawValues
	// EndDefaults are merged under the end node's taskConfig.
	EndDefaults() config.RawValues
}
