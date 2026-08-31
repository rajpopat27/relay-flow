// Package beads is the Beads task-system adapter. Beads owns its storage and
// workspace semantics; this package keeps those details behind the task.System
// boundary and talks to the bd CLI through bdcli.
package beads

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/rajpopat27/relay-flow/internal/config"
	"github.com/rajpopat27/relay-flow/internal/retry"
	"github.com/rajpopat27/relay-flow/internal/task"
	"github.com/rajpopat27/relay-flow/internal/task/beads/bdcli"
)

// Config is the Beads-owned task configuration shared by root, repository,
// workflow, and node scopes.
type Config struct {
	BeadsDir  string       `yaml:"beadsDir"`
	Filters   Filters      `yaml:"filters,omitempty"`
	Status    StatusConfig `yaml:"status,omitempty"`
	Templates Templates    `yaml:"templates,omitempty"`
}

// Filters are the structured, locally evaluated Beads ticket filters.
type Filters struct {
	ParentStatuses []string `yaml:"parentStatuses,omitempty"`
	IssueTypes     []string `yaml:"issueTypes,omitempty"`
	Labels         []string `yaml:"labels,omitempty"`
	Assignees      []string `yaml:"assignees,omitempty"`
}

// StatusConfig carries Beads parent and mailbox status values.
type StatusConfig struct {
	Parent  string `yaml:"parent,omitempty"`
	Mailbox string `yaml:"mailbox,omitempty"`
}

// Templates are Beads-owned mailbox and comment templates.
type Templates struct {
	MailboxDescription string `yaml:"mailboxDescription"`
	SummaryComment     string `yaml:"summaryComment"`
	FeedbackComment    string `yaml:"feedbackComment"`
}

const (
	defaultMailboxDescription = `Parent ticket: {{ticket}}
Workflow: {{workflow}}
Node: {{node}}
Node type: {{nodeType}}
Agent: {{agent}}
Mailbox: {{mailbox}}

Node work:
{{nodeDescription}}

Read this mailbox's comments for feedback from previous nodes.`
	defaultSummaryComment = `Summary for {{node}}

{{summaryReport}}`
	defaultFeedbackComment = `Feedback from {{sourceNode}} to {{targetNode}} mailbox {{mailbox}}

{{feedbackReport}}`
)

// DefaultConfig supplies only Beads task-system text defaults. A fresh map is
// returned for every call so callers can merge or modify it independently.
func DefaultConfig() config.RawValues {
	return config.RawValues{"templates": map[string]any{
		"mailboxDescription": defaultMailboxDescription,
		"summaryComment":     defaultSummaryComment,
		"feedbackComment":    defaultFeedbackComment,
	}}
}

func init() {
	task.Register("beads", task.Factory{
		RequiredRepoKeys:   func() []string { return []string{"beadsDir"} },
		TaskScopeKey:       beadsTaskScopeKey,
		Auth:               beadsAuth,
		DefaultConfig:      DefaultConfig,
		ValidateTextConfig: validateTextConfig,
		New:                newSystem,
	})
}

// system is one repo-bound Beads task system. Its CLI client serializes
// commands for the selected workspace, making the system safe for concurrent
// Repo Poller and durable-activity use.
type system struct {
	cli       bdcli.Client
	repoName  string
	repoPath  string
	beadsDir  string
	base      config.RawValues
	effective Config
}

// beadsTaskScopeKey returns the canonical physical Beads workspace. The
// workspace must be supplied by repoConfig; a root-level value never satisfies
// the required repo-scoped key.
func beadsTaskScopeKey(rootConfig, repoConfig config.RawValues) (string, error) {
	var root Config
	if err := config.DecodeStrict(rootConfig, &root); err != nil {
		return "", fmt.Errorf("root task config: %w", err)
	}
	var repo Config
	if err := config.DecodeStrict(repoConfig, &repo); err != nil {
		return "", fmt.Errorf("repo task config: %w", err)
	}
	if strings.TrimSpace(repo.BeadsDir) == "" {
		return "", errors.New("beads task scope requires repo beadsDir")
	}
	return canonicalBeadsDir(repo.BeadsDir)
}

func canonicalBeadsDir(value string) (string, error) {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return "", errors.New("beadsDir must not be empty")
	}
	abs, err := filepath.Abs(trimmed)
	if err != nil {
		return "", fmt.Errorf("resolve beadsDir %q: %w", trimmed, err)
	}
	info, err := os.Stat(abs)
	if err != nil {
		return "", fmt.Errorf("stat beadsDir %q: %w", trimmed, err)
	}
	if !info.IsDir() {
		return "", fmt.Errorf("beadsDir %q is not a directory", trimmed)
	}
	resolved, err := filepath.EvalSymlinks(abs)
	if err != nil {
		return "", fmt.Errorf("canonicalize beadsDir %q: %w", trimmed, err)
	}
	return filepath.Clean(resolved), nil
}

// newSystem constructs and probes a repo-bound Beads task system. It does not
// initialize a workspace or start any Beads/Dolt server.
func newSystem(ctx context.Context, spec task.RepoSpec) (task.System, error) {
	if strings.TrimSpace(spec.Name) == "" {
		return nil, errors.New("beads: repo name is required")
	}
	// Validate the repo-scoped key before merging with root values. This keeps a
	// root beadsDir from silently satisfying repository registration.
	beadsDir, err := beadsTaskScopeKey(spec.RootConfig, spec.RepoConfig)
	if err != nil {
		return nil, fmt.Errorf("beads repo %q: %w", spec.Name, err)
	}
	merged := config.Merge(DefaultConfig(), spec.RootConfig, spec.RepoConfig)
	cfg, err := decodeConfig(merged)
	if err != nil {
		return nil, fmt.Errorf("beads repo %q config: %w", spec.Name, err)
	}
	if err := validateTextConfig(merged); err != nil {
		return nil, fmt.Errorf("beads repo %q config: %w", spec.Name, err)
	}
	cli := bdcli.New(spec.Path, beadsDir)
	if err := cli.Probe(ctx); err != nil {
		return nil, fmt.Errorf("beads repo %q probe: %w", spec.Name, err)
	}
	return &system{
		cli:       cli,
		repoName:  spec.Name,
		repoPath:  spec.Path,
		beadsDir:  beadsDir,
		base:      merged,
		effective: cfg,
	}, nil
}

func decodeConfig(raw config.RawValues) (Config, error) {
	var cfg Config
	if err := config.DecodeStrict(raw, &cfg); err != nil {
		return Config{}, err
	}
	return cfg, nil
}

// validateTextConfig performs strict Beads config and template validation.
func validateTextConfig(raw config.RawValues) error {
	merged := config.Merge(DefaultConfig(), raw)
	cfg, err := decodeConfig(merged)
	if err != nil {
		return err
	}
	return validateTemplates(cfg.Templates)
}

var (
	textVarPattern = regexp.MustCompile(`\{\{([^{}]*)\}\}`)
	knownTextVars  = map[string]bool{
		"runID": true, "ticket": true, "workflow": true,
		"repo": true, "node": true, "nodeType": true, "agent": true,
		"nodeDescription": true, "nextSteps": true, "successRoutes": true,
		"failureRoutes": true, "mailbox": true, "sourceNode": true,
		"targetNode": true, "summaryReport": true, "feedbackReport": true,
	}
)

func validateTemplates(templates Templates) error {
	for name, tmpl := range map[string]string{
		"mailboxDescription": templates.MailboxDescription,
		"summaryComment":     templates.SummaryComment,
		"feedbackComment":    templates.FeedbackComment,
	} {
		for _, match := range textVarPattern.FindAllStringSubmatch(tmpl, -1) {
			if !knownTextVars[match[1]] {
				return fmt.Errorf("%s: unknown template variable {{%s}}", name, match[1])
			}
		}
	}
	if !strings.Contains(templates.SummaryComment, "{{summaryReport}}") {
		return errors.New("summaryComment must contain {{summaryReport}}")
	}
	if !strings.Contains(templates.FeedbackComment, "{{feedbackReport}}") {
		return errors.New("feedbackComment must contain {{feedbackReport}}")
	}
	return nil
}

// beadsAuth intentionally has no credential flow. Beads authentication and
// server credentials belong to the bd workspace; relay-flow must not create a
// credentials file. Non-empty arguments are rejected rather than ignored.
func beadsAuth(_ context.Context, args []string, _ io.Reader) error {
	if len(args) != 0 {
		return errors.New("beads task auth does not accept arguments")
	}
	return nil
}

// Poll reads ready and relay-owned active issues once each, merges overlapping
// results by issue ID, and returns only top-level issues. The CLI's
// --no-parent flag is an optimization rather than the correctness boundary:
// every returned issue is checked again before normalization.
func (s *system) Poll(ctx context.Context) ([]task.Ticket, error) {
	ready, err := s.cli.ListReady(ctx)
	if err != nil {
		return nil, err
	}
	claimed, err := s.cli.ListClaimed(ctx)
	if err != nil {
		return nil, err
	}

	issues := make(map[string]bdcli.Issue, len(ready)+len(claimed))
	order := make([]string, 0, len(ready)+len(claimed))
	for _, issue := range ready {
		if _, exists := issues[issue.ID]; !exists {
			order = append(order, issue.ID)
		}
		issues[issue.ID] = issue
	}
	for _, issue := range claimed {
		if _, exists := issues[issue.ID]; !exists {
			order = append(order, issue.ID)
		}
		// Claimed results are read after ready results and therefore replace an
		// overlapping ready copy with its current labels/status.
		issues[issue.ID] = issue
	}

	tickets := make([]task.Ticket, 0, len(order))
	for _, issueID := range order {
		issue := issues[issueID]
		if strings.TrimSpace(issue.Parent) != "" {
			continue
		}
		tickets = append(tickets, issueToTicket(issue))
	}
	return tickets, nil
}

// issueToTicket converts the small Beads issue shape into the core ticket
// contract. Beads issue IDs are stable identities for both Ticket.ID and
// Ticket.Key; workflow labels are retained separately for routing and in the
// normalized fields for adapter-owned filter matching.
func issueToTicket(issue bdcli.Issue) task.Ticket {
	return task.Ticket{
		ID:             issue.ID,
		Key:            issue.ID,
		Title:          issue.Title,
		WorkflowClaims: extractWorkflowClaims(issue.Labels),
		Fields:         normalizeFields(issue),
	}
}

func normalizeFields(issue bdcli.Issue) map[string]any {
	return map[string]any{
		"status":      issue.Status,
		"issueType":   issue.IssueType,
		"assignee":    issue.Assignee,
		"priority":    issue.Priority,
		"description": issue.Description,
		"labels":      append([]string(nil), issue.Labels...),
	}
}

func extractWorkflowClaims(labels []string) []string {
	claims := make([]string, 0)
	for _, label := range labels {
		if strings.HasPrefix(label, "wf:") && len(label) > len("wf:") {
			claims = append(claims, label)
		}
	}
	return claims
}

func claimLabel(workflow string) string {
	return "wf:" + workflow
}

// CompileFilter compiles Beads-owned structured filters into a local matcher.
// No Beads query language is accepted or sent to the CLI.
func (s *system) CompileFilter(workflowTaskConfig config.RawValues) (func(task.Ticket) bool, error) {
	merged := config.Merge(s.base, workflowTaskConfig)
	cfg, err := decodeConfig(merged)
	if err != nil {
		return nil, err
	}
	f := cfg.Filters
	return func(ticket task.Ticket) bool {
		if len(f.ParentStatuses) > 0 && !containsExact(f.ParentStatuses, stringField(ticket.Fields, "status")) {
			return false
		}
		if len(f.IssueTypes) > 0 && !containsExact(f.IssueTypes, stringField(ticket.Fields, "issueType")) {
			return false
		}
		if len(f.Labels) > 0 {
			labels := stringSliceField(ticket.Fields, "labels")
			for _, required := range f.Labels {
				if !containsExact(labels, required) {
					return false
				}
			}
		}
		if len(f.Assignees) > 0 && !containsFold(f.Assignees, stringField(ticket.Fields, "assignee")) {
			return false
		}
		return true
	}, nil
}

func containsExact(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

func containsFold(values []string, want string) bool {
	for _, value := range values {
		if strings.EqualFold(value, want) {
			return true
		}
	}
	return false
}

func stringField(fields map[string]any, key string) string {
	value, _ := fields[key].(string)
	return value
}

func stringSliceField(fields map[string]any, key string) []string {
	value, _ := fields[key].([]string)
	return value
}

// Claim adds the permanent workflow label to the parent issue. Routing has
// already resolved the workflow before this method runs, so no Beads ready or
// claim command is used and status/assignee are left untouched.
func (s *system) Claim(ctx context.Context, ticket task.TicketRef, workflow string) error {
	if strings.TrimSpace(ticket.Key) == "" {
		return errors.New("beads: ticket key is required to claim")
	}
	if strings.TrimSpace(workflow) == "" {
		return errors.New("beads: workflow is required to claim")
	}
	return s.cli.Update(ctx, ticket.Key, bdcli.UpdateInput{
		AddLabels: []string{"wf:" + workflow},
	})
}

func (*system) ValidateConfig(context.Context, config.RawValues, map[string]config.RawValues) error {
	return errors.New("beads: ValidateConfig is not implemented")
}

// RenderText expands the adapter-owned task-system templates using the same
// simple replacement rules as the other task adapters.
func (s *system) RenderText(kind task.TextKind, data task.TextData) (string, error) {
	var template string
	switch kind {
	case task.TextMailboxDescription:
		template = s.effective.Templates.MailboxDescription
	case task.TextSummaryComment:
		template = s.effective.Templates.SummaryComment
	case task.TextFeedbackComment:
		template = s.effective.Templates.FeedbackComment
	default:
		return "", fmt.Errorf("beads: unknown task text kind %q", kind)
	}
	values := map[string]string{
		"runID": data.RunID, "ticket": data.Ticket, "workflow": data.Workflow,
		"repo": data.Repo, "node": data.Node, "nodeType": data.NodeType,
		"agent": data.Agent, "nodeDescription": data.NodeDescription,
		"nextSteps": data.NextSteps, "successRoutes": data.SuccessRoutes,
		"failureRoutes": data.FailureRoutes, "mailbox": data.Mailbox,
		"sourceNode": data.SourceNode, "targetNode": data.TargetNode,
		"summaryReport": data.SummaryReport, "feedbackReport": data.FeedbackReport,
	}
	return textVarPattern.ReplaceAllStringFunc(template, func(match string) string {
		parts := textVarPattern.FindStringSubmatch(match)
		if len(parts) != 2 {
			return match
		}
		return values[parts[1]]
	}), nil
}

// EnsureMailboxes finds reusable child issues by their stable title, updates
// existing descriptions/labels, creates only missing children, and returns a
// complete node-to-mailbox map.
func (s *system) EnsureMailboxes(ctx context.Context, parent task.TicketRef, workflow string, specs []task.MailboxSpec) (map[string]task.Mailbox, error) {
	parentID := parent.Key
	if strings.TrimSpace(parentID) == "" {
		parentID = parent.ID
	}
	if strings.TrimSpace(parentID) == "" {
		return nil, errors.New("beads: parent key is required to ensure mailboxes")
	}
	if strings.TrimSpace(workflow) == "" {
		return nil, errors.New("beads: workflow is required to ensure mailboxes")
	}
	children, err := s.cli.ListChildren(ctx, parentID)
	if err != nil {
		return nil, fmt.Errorf("list children of %q: %w", parentID, err)
	}

	seenSpecs := make(map[string]struct{}, len(specs))
	for _, spec := range specs {
		if _, exists := seenSpecs[spec.Node]; exists {
			return nil, fmt.Errorf("duplicate mailbox node %q", spec.Node)
		}
		seenSpecs[spec.Node] = struct{}{}
	}

	type requestedMailbox struct {
		spec  task.MailboxSpec
		title string
		issue bdcli.Issue
	}
	requested := make([]requestedMailbox, 0, len(specs))
	for _, spec := range specs {
		title := mailboxTitle(parentID, spec.Node)
		issue, err := findMailbox(children, title)
		if err == nil {
			requested = append(requested, requestedMailbox{spec: spec, title: title, issue: issue})
			continue
		}
		if !errors.Is(err, errMailboxNotFound) {
			return nil, err
		}
		requested = append(requested, requestedMailbox{spec: spec, title: title})
	}

	out := make(map[string]task.Mailbox, len(specs))
	missing := make([]task.MailboxSpec, 0, len(specs))
	for _, mailbox := range requested {
		if mailbox.issue.ID != "" {
			description := mailbox.spec.Description
			if err := s.cli.Update(ctx, mailbox.issue.ID, bdcli.UpdateInput{
				Description: &description,
				AddLabels:   []string{claimLabel(workflow)},
			}); err != nil {
				return nil, fmt.Errorf("reconcile mailbox %q: %w", mailbox.issue.ID, err)
			}
			out[mailbox.spec.Node] = issueToMailbox(mailbox.issue, mailbox.spec.Node)
			continue
		}
		missing = append(missing, task.MailboxSpec{
			Node:        mailbox.spec.Node,
			Title:       mailbox.title,
			Description: mailbox.spec.Description,
			TaskConfig:  mailbox.spec.TaskConfig,
			TextData:    mailbox.spec.TextData,
		})
	}

	for _, spec := range missing {
		issue, err := s.cli.CreateChild(ctx, parentID, spec.Title, spec.Description, claimLabel(workflow))
		if err != nil {
			return nil, fmt.Errorf("create mailbox %q: %w", spec.Title, err)
		}
		if issue.ID == "" {
			return nil, fmt.Errorf("create mailbox %q returned no issue ID", spec.Title)
		}
		out[spec.Node] = issueToMailbox(issue, spec.Node)
	}
	return out, nil
}

func mailboxTitle(parentID, node string) string {
	return parentID + ":" + node
}

var errMailboxNotFound = errors.New("mailbox not found")

func findMailbox(children []bdcli.Issue, title string) (bdcli.Issue, error) {
	var found bdcli.Issue
	foundMatch := false
	for _, child := range children {
		if child.Title != title {
			continue
		}
		if foundMatch {
			return bdcli.Issue{}, fmt.Errorf("duplicate mailbox title %q", title)
		}
		found = child
		foundMatch = true
	}
	if !foundMatch {
		return bdcli.Issue{}, fmt.Errorf("%w: %q", errMailboxNotFound, title)
	}
	if found.ID == "" {
		return bdcli.Issue{}, fmt.Errorf("mailbox %q has no issue ID", title)
	}
	return found, nil
}

func issueToMailbox(issue bdcli.Issue, node string) task.Mailbox {
	return task.Mailbox{ID: issue.ID, Key: issue.ID, Node: node}
}

// ApplyTaskConfig applies only the configured Beads status fields. Text and
// lifecycle behavior are layered onto this operation by the later adapter
// implementation tasks; status writes themselves always use reconciliation.
func (s *system) ApplyTaskConfig(ctx context.Context, target task.Target, taskConfig config.RawValues) error {
	cfg, err := decodeConfig(taskConfig)
	if err != nil {
		return err
	}
	if target.Mailbox != nil {
		if cfg.Status.Mailbox != "" {
			if err := s.reconcileStatus(ctx, target.Mailbox.Key, expectedMailboxSource(cfg.Status.Mailbox), cfg.Status.Mailbox); err != nil {
				return err
			}
		}
		if cfg.Status.Parent != "" {
			return s.reconcileStatus(ctx, target.Parent.Key, expectedParentSource(cfg.Status.Parent), cfg.Status.Parent)
		}
		return nil
	}
	if cfg.Status.Parent == "" {
		return nil
	}
	return s.reconcileStatus(ctx, target.Parent.Key, expectedParentSource(cfg.Status.Parent), cfg.Status.Parent)
}

func expectedMailboxSource(target string) string {
	if target == "closed" {
		return "in_progress"
	}
	return "open"
}

func expectedParentSource(string) string {
	// Parent issues remain open while work is in progress. End and other
	// configured parent transitions therefore start from the open lifecycle
	// state; an already-target issue is handled idempotently by reconcileStatus.
	return "open"
}

func (s *system) reconcileStatus(ctx context.Context, issueID, expected, target string) error {
	if strings.TrimSpace(issueID) == "" {
		return errors.New("beads: issue key is required for status reconciliation")
	}
	if strings.TrimSpace(target) == "" {
		return errors.New("beads: target status is required for status reconciliation")
	}
	issue, err := s.cli.Show(ctx, issueID)
	if err != nil {
		return err
	}
	if issue.Status == target {
		return nil
	}
	if issue.Status != expected {
		return retry.ConflictError(fmt.Errorf("issue %q has status %q; expected %q before changing to %q", issueID, issue.Status, expected, target))
	}
	// Beads intentionally receives an unconditional update. The small race
	// between this read and write is an accepted last-writer-wins behavior for
	// this adapter; do not add --if-status or a fallback path.
	return s.cli.Update(ctx, issueID, bdcli.UpdateInput{Status: target})
}

// CompleteMailbox closes only the supplied mailbox. It performs the same
// read-before-write reconciliation as other Beads status operations: a closed
// mailbox is an idempotent success, an in-progress mailbox is unconditionally
// updated to closed, and an incompatible state is a retryable conflict.
func (s *system) CompleteMailbox(ctx context.Context, mailbox task.Mailbox) error {
	if strings.TrimSpace(mailbox.Key) == "" {
		return errors.New("beads: mailbox key is required to complete")
	}
	return s.reconcileStatus(ctx, mailbox.Key, "in_progress", "closed")
}

func targetIssueID(target task.Target) string {
	if target.Mailbox != nil && strings.TrimSpace(target.Mailbox.Key) != "" {
		return target.Mailbox.Key
	}
	return target.Parent.Key
}

// HasComment checks the selected issue's comments for a stable marker.
func (s *system) HasComment(ctx context.Context, target task.Target, marker string) (bool, error) {
	issueID := targetIssueID(target)
	if strings.TrimSpace(issueID) == "" {
		return false, errors.New("beads: comment target key is required")
	}
	comments, err := s.cli.ListComments(ctx, issueID)
	if err != nil {
		return false, err
	}
	for _, comment := range comments {
		if strings.Contains(comment.Text, marker) {
			return true, nil
		}
	}
	return false, nil
}

// Comment checks for an existing marker before writing the marked body
// through bdcli's stdin-safe comment operation.
func (s *system) Comment(ctx context.Context, target task.Target, body, marker string) error {
	issueID := targetIssueID(target)
	if strings.TrimSpace(issueID) == "" {
		return errors.New("beads: comment target key is required")
	}
	exists, err := s.HasComment(ctx, target, marker)
	if err != nil {
		return err
	}
	if exists {
		return nil
	}
	return s.cli.AddComment(ctx, issueID, body+"\n\n<!-- "+marker+" -->")
}

// StartDefaults leaves the parent status unchanged when entering a workflow.
func (*system) StartDefaults() config.RawValues { return config.RawValues{} }

// WorkDefaults starts a work mailbox in progress while leaving the parent
// open and visible to the claimed-parent poll query.
func (*system) WorkDefaults() config.RawValues {
	return config.RawValues{"status": map[string]any{"mailbox": "in_progress"}}
}

// EndDefaults closes the parent after workflow completion.
func (*system) EndDefaults() config.RawValues {
	return config.RawValues{"status": map[string]any{"parent": "closed"}}
}

// ResetForRecovery reopens the parent and every known mailbox, clearing any
// deferred state while preserving comments, labels, descriptions, history,
// and issues themselves.
func (s *system) ResetForRecovery(ctx context.Context, parent task.TicketRef, mailboxes []task.Mailbox, _ config.RawValues) error {
	parentID := parent.Key
	if strings.TrimSpace(parentID) == "" {
		parentID = parent.ID
	}
	if strings.TrimSpace(parentID) == "" {
		return errors.New("beads: parent key is required for recovery reset")
	}
	if err := s.cli.Update(ctx, parentID, bdcli.UpdateInput{Status: "open", ClearDefer: true}); err != nil {
		return fmt.Errorf("reset parent %q: %w", parentID, err)
	}
	for _, mailbox := range mailboxes {
		if strings.TrimSpace(mailbox.Key) == "" {
			return errors.New("beads: mailbox key is required for recovery reset")
		}
		if err := s.cli.Update(ctx, mailbox.Key, bdcli.UpdateInput{Status: "open", ClearDefer: true}); err != nil {
			return fmt.Errorf("reset mailbox %q: %w", mailbox.Key, err)
		}
	}
	return nil
}

var _ task.System = (*system)(nil)
