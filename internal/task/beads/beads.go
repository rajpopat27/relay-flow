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

func (*system) RenderText(task.TextKind, task.TextData) (string, error) {
	return "", errors.New("beads: RenderText is not implemented")
}

func (*system) EnsureMailboxes(context.Context, task.TicketRef, string, []task.MailboxSpec) (map[string]task.Mailbox, error) {
	return nil, errors.New("beads: EnsureMailboxes is not implemented")
}

func (*system) ApplyTaskConfig(context.Context, task.Target, config.RawValues) error {
	return errors.New("beads: ApplyTaskConfig is not implemented")
}

func (*system) CompleteMailbox(context.Context, task.Mailbox) error {
	return errors.New("beads: CompleteMailbox is not implemented")
}

func (*system) HasComment(context.Context, task.Target, string) (bool, error) {
	return false, errors.New("beads: HasComment is not implemented")
}

func (*system) Comment(context.Context, task.Target, string, string) error {
	return errors.New("beads: Comment is not implemented")
}

func (*system) ResetForRecovery(context.Context, task.TicketRef, []task.Mailbox, config.RawValues) error {
	return errors.New("beads: ResetForRecovery is not implemented")
}

var _ task.System = (*system)(nil)
