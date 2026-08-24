// Package jira is the Jira task-system adapter. It owns one typed Config
// spanning root, repo, workflow, and node scopes; core never imports it.
package jira

import (
	"context"
	"fmt"
	"log/slog"
	"sort"
	"strings"

	"github.com/rajpopat27/relay-flow/internal/config"
	"github.com/rajpopat27/relay-flow/internal/retry"
	"github.com/rajpopat27/relay-flow/internal/task"
	"github.com/rajpopat27/relay-flow/internal/task/jira/acli"
)

// Config is the adapter-owned typed config for every scope.
type Config struct {
	Assignee   string       `yaml:"assignee,omitempty"`
	Project    string       `yaml:"project,omitempty"`
	Component  string       `yaml:"component,omitempty"`
	Site       string       `yaml:"site,omitempty"`
	Filters    Filters      `yaml:"filters,omitempty"`
	Transition TransitionTo `yaml:"transitionTo,omitempty"`
}

// Filters are the structured, locally evaluable workflow ticket matchers.
type Filters struct {
	ParentStatuses []string `yaml:"parentStatuses,omitempty"`
	IssueTypes     []string `yaml:"issueTypes,omitempty"`
	Labels         []string `yaml:"labels,omitempty"`
	Assignees      []string `yaml:"assignees,omitempty"`
}

// TransitionTo carries parent/mailbox status transitions for a node.
type TransitionTo struct {
	ParentStatus string `yaml:"parentStatus,omitempty"`
	TaskStatus   string `yaml:"taskStatus,omitempty"`
}

// Deterministic transition defaults for omitted values.
const (
	defaultStartParentStatus = "In Progress"
	defaultWorkTaskStatus    = "In Progress"
	defaultEndParentStatus   = "Done"
)

// claimLabel is the permanent workflow claim label.
func claimLabel(workflow string) string { return "wf:" + workflow }

func init() {
	task.Register("jira", task.Factory{
		RequiredRepoKeys: func() []string { return []string{"project", "component"} },
		TaskScopeKey: func(rootConfig, repoConfig config.RawValues) (string, error) {
			var root, repoCfg Config
			if err := config.DecodeStrict(rootConfig, &root); err != nil {
				return "", fmt.Errorf("root task config: %w", err)
			}
			if err := config.DecodeStrict(repoConfig, &repoCfg); err != nil {
				return "", fmt.Errorf("repo task config: %w", err)
			}
			proj := repoCfg.Project
			comp := repoCfg.Component
			if proj == "" || comp == "" {
				return "", fmt.Errorf("jira task scope requires repo project and component")
			}
			return strings.Join([]string{root.Site, proj, comp}, "/"), nil
		},
		New: func(ctx context.Context, spec task.RepoSpec) (task.System, error) {
			return newSystem(acli.New(), spec)
		},
	})
}

// system is the repo-bound Jira task.System. It is safe for concurrent use;
// the ACLI client owns any subprocess serialization.
type system struct {
	cli       acli.Client
	repoName  string
	effective Config // root+repo merged
}

func newSystem(cli acli.Client, spec task.RepoSpec) (*system, error) {
	if spec.Name == "" {
		return nil, fmt.Errorf("jira: repo name is required")
	}
	merged := config.Merge(spec.RootConfig, spec.RepoConfig)
	var cfg Config
	if err := config.DecodeStrict(merged, &cfg); err != nil {
		return nil, fmt.Errorf("jira repo %q config: %w", spec.Name, err)
	}
	// project/component are required repo keys enforced at registration
	// (RequiredRepoKeys); construction validates the merged schema only.
	return &system{cli: cli, repoName: spec.Name, effective: cfg}, nil
}

// newSystemForCLI constructs a system around an explicit CLI seam (tests).
func newSystemForCLI(cli acli.Client) (task.System, error) {
	return newSystem(cli, task.RepoSpec{
		Name:       "payments",
		RootConfig: config.RawValues{},
		RepoConfig: config.RawValues{"project": "PAY", "component": "api"},
	})
}

// decodeConfig strictly decodes one operation's effective raw values.
func decodeConfig(raw config.RawValues) (Config, error) {
	var cfg Config
	if err := config.DecodeStrict(raw, &cfg); err != nil {
		return Config{}, err
	}
	return cfg, nil
}

// --- Poll / filters ---

// buildJQL scopes the parent poll by project/component and active statuses.
func (s *system) buildJQL() string {
	parts := []string{
		fmt.Sprintf("project = %s", s.effective.Project),
		fmt.Sprintf("component = %q", s.effective.Component),
		"issuetype != Subtask",
		"statusCategory != Done",
	}
	return strings.Join(parts, " AND ")
}

// Poll returns active parent tickets only; mailbox subtasks are never
// returned as candidates.
func (s *system) Poll(ctx context.Context) ([]task.Ticket, error) {
	if s.effective.Project == "" || s.effective.Component == "" {
		return nil, fmt.Errorf("jira repo %q: project and component are required to poll", s.repoName)
	}
	jql := s.buildJQL()
	slog.Debug("jira poll", "repo", s.repoName, "jql", jql)
	raw, err := s.cli.Search(ctx, jql)
	if err != nil {
		return nil, err
	}
	tickets, err := normalizeSearchResponse(raw)
	if err != nil {
		return nil, err
	}
	for _, t := range tickets {
		slog.Debug("jira ticket",
			"repo", s.repoName, "ticket", t.Key, "id", t.ID,
			"title", t.Title, "claims", strings.Join(t.WorkflowClaims, ","),
			"fields", fmt.Sprint(t.Fields))
	}
	return tickets, nil
}

// CompileFilter compiles workflow taskConfig.filters into an in-memory
// matcher over normalized ticket fields. Unknown filter fields are rejected.
func (s *system) CompileFilter(workflowTaskConfig config.RawValues) (func(task.Ticket) bool, error) {
	cfg, err := decodeConfig(workflowTaskConfig)
	if err != nil {
		return nil, err
	}
	f := cfg.Filters
	return func(t task.Ticket) bool {
		if len(f.ParentStatuses) > 0 && !contains(f.ParentStatuses, strField(t.Fields, "status")) {
			return false
		}
		if len(f.IssueTypes) > 0 && !contains(f.IssueTypes, strField(t.Fields, "issueType")) {
			return false
		}
		if len(f.Labels) > 0 {
			ticketLabels := strSliceField(t.Fields, "labels")
			for _, want := range f.Labels {
				if !contains(ticketLabels, want) {
					return false
				}
			}
		}
		if len(f.Assignees) > 0 && !contains(f.Assignees, strField(t.Fields, "assignee")) {
			return false
		}
		return true
	}, nil
}

// --- Claim ---

// Claim adds wf:<workflow> to the parent. It re-reads the parent's claims,
// treats the same claim as idempotent, and rejects a conflicting claim.
func (s *system) Claim(ctx context.Context, ticket task.TicketRef, workflow string) error {
	raw, err := s.cli.View(ctx, ticket.Key)
	if err != nil {
		return err
	}
	labels, err := labelsOf(raw)
	if err != nil {
		return err
	}
	want := claimLabel(workflow)
	for _, l := range labels {
		if l == want {
			return nil // idempotent
		}
		if strings.HasPrefix(l, "wf:") {
			return fmt.Errorf("ticket %s already claimed by %s", ticket.Key, l)
		}
	}
	return s.cli.EnsureLabel(ctx, ticket.Key, want)
}

// --- Config validation ---

// ValidateConfig strictly validates the workflow and every node task config
// against the adapter-owned schema for this repo. It never mutates the
// caller's maps.
func (s *system) ValidateConfig(_ context.Context, workflowTaskConfig config.RawValues, nodeTaskConfigs map[string]config.RawValues) error {
	if _, err := decodeConfig(workflowTaskConfig); err != nil {
		return fmt.Errorf("workflow taskConfig: %w", err)
	}
	nodes := make([]string, 0, len(nodeTaskConfigs))
	for n := range nodeTaskConfigs {
		nodes = append(nodes, n)
	}
	sort.Strings(nodes)
	for _, n := range nodes {
		if _, err := decodeConfig(nodeTaskConfigs[n]); err != nil {
			return fmt.Errorf("node %q taskConfig: %w", n, err)
		}
	}
	return nil
}

// LifecycleDefaults exposure: the deterministic Jira transition defaults per
// lifecycle point (spec: Jira transition defaults are deterministic). Run
// orchestration merges these raw values under the effective node config
// before ApplyTaskConfig; omitted values inherit the default, explicit
// values win (map merge with defaults as the lower layer).

// StartDefaults defaults the parent to In Progress.
func (s *system) StartDefaults() config.RawValues {
	return config.RawValues{"transitionTo": map[string]any{"parentStatus": defaultStartParentStatus}}
}

// WorkDefaults defaults the mailbox task status to In Progress; the parent
// is left unchanged when parentStatus is omitted.
func (s *system) WorkDefaults() config.RawValues {
	return config.RawValues{"transitionTo": map[string]any{"taskStatus": defaultWorkTaskStatus}}
}

// EndDefaults defaults the parent to Done.
func (s *system) EndDefaults() config.RawValues {
	return config.RawValues{"transitionTo": map[string]any{"parentStatus": defaultEndParentStatus}}
}

// endConfig applies the deterministic end default: omitted parentStatus
// becomes Done.
func endConfig(cfg config.RawValues) config.RawValues {
	return withTransitionDefault(cfg, "parentStatus", defaultEndParentStatus)
}

func withTransitionDefault(cfg config.RawValues, key, value string) config.RawValues {
	out := config.RawValues{}
	for k, v := range cfg {
		out[k] = v
	}
	merged := map[string]any{}
	if tr, ok := out["transitionTo"].(map[string]any); ok {
		for k, v := range tr {
			merged[k] = v
		}
	}
	if _, ok := merged[key]; !ok {
		merged[key] = value
	}
	out["transitionTo"] = merged
	return out
}

// --- Mailboxes ---

// EnsureMailboxes finds existing child mailboxes by parent and title
// (<ticket>:<node>), creates only missing ones with the workflow label, and
// returns the complete node-to-mailbox map.
func (s *system) EnsureMailboxes(ctx context.Context, parent task.TicketRef, workflow string, specs []task.MailboxSpec) (map[string]task.Mailbox, error) {
	raw, err := s.cli.View(ctx, parent.Key)
	if err != nil {
		return nil, err
	}
	existing, err := subtasksOf(raw)
	if err != nil {
		return nil, err
	}
	out := map[string]task.Mailbox{}
	for _, spec := range specs {
		if mb, ok := existing[spec.Title]; ok {
			// Found existing: reconcile the workflow label and description,
			// and return the complete node-identified value.
			if err := s.cli.EnsureLabel(ctx, mb.Key, claimLabel(workflow)); err != nil {
				return nil, fmt.Errorf("label mailbox %q: %w", mb.Key, err)
			}
			if err := s.cli.UpdateDescription(ctx, mb.Key, spec.Description); err != nil {
				return nil, fmt.Errorf("describe mailbox %q: %w", mb.Key, err)
			}
			mb.Node = spec.Node
			out[spec.Node] = mb
			continue
		}
		id, key, err := s.cli.CreateSubtask(ctx, parent.Key, spec.Title, spec.Description)
		if err != nil {
			return nil, fmt.Errorf("create mailbox %q: %w", spec.Title, err)
		}
		if err := s.cli.EnsureLabel(ctx, key, claimLabel(workflow)); err != nil {
			return nil, fmt.Errorf("label mailbox %q: %w", key, err)
		}
		out[spec.Node] = task.Mailbox{ID: id, Key: key, Node: spec.Node}
	}
	return out, nil
}

// --- Transitions ---

// ApplyTaskConfig applies the adapter-owned taskConfig to the parent and
// optional mailbox. Deterministic defaults: an omitted work-node taskStatus
// defaults the mailbox to In Progress and leaves the parent unchanged; an
// omitted parent-only parentStatus defaults to In Progress. Run
// orchestration merges EndDefaults into the end node's config before this
// call, so end processing transitions the parent to Done when omitted.
func (s *system) ApplyTaskConfig(ctx context.Context, target task.Target, taskConfig config.RawValues) error {
	cfg, err := decodeConfig(taskConfig)
	if err != nil {
		return err
	}
	tr := cfg.Transition
	if target.Mailbox != nil {
		status := tr.TaskStatus
		if status == "" {
			status = defaultWorkTaskStatus
		}
		if err := s.transition(ctx, target.Mailbox.Key, status); err != nil {
			return err
		}
		if tr.ParentStatus != "" {
			return s.transition(ctx, target.Parent.Key, tr.ParentStatus)
		}
		return nil
	}
	// Parent-only target (start/end lifecycle processing).
	status := tr.ParentStatus
	if status == "" {
		status = defaultStartParentStatus
	}
	return s.transition(ctx, target.Parent.Key, status)
}

// transition applies a Jira status transition, mapping a human-incompatible
// current state to a conflict.
func (s *system) transition(ctx context.Context, key, status string) error {
	err := s.cli.Transition(ctx, key, status)
	if err != nil && isConflict(err) {
		return retry.ConflictError(err)
	}
	return err
}

func isConflict(err error) bool {
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "transition") && (strings.Contains(msg, "not available") ||
		strings.Contains(msg, "cannot") || strings.Contains(msg, "invalid"))
}

// CompleteMailbox marks the mailbox Done using task-system semantics.
func (s *system) CompleteMailbox(ctx context.Context, mailbox task.Mailbox) error {
	return s.transition(ctx, mailbox.Key, "Done")
}

// --- Comments ---

// HasComment reports whether a comment carrying the stable relay-flow
// marker exists on the target.
func (s *system) HasComment(ctx context.Context, target task.Target, marker string) (bool, error) {
	key := target.Parent.Key
	if target.Mailbox != nil {
		key = target.Mailbox.Key
	}
	bodies, err := s.cli.ListComments(ctx, key)
	if err != nil {
		return false, err
	}
	for _, b := range bodies {
		if strings.Contains(b, marker) {
			return true, nil
		}
	}
	return false, nil
}

// Comment writes a human-readable comment carrying the stable marker. It is
// idempotent: when a comment with the marker already exists on the target it
// returns success without posting, so retried summary/feedback/cancellation
// comments are not duplicated.
func (s *system) Comment(ctx context.Context, target task.Target, body, marker string) error {
	key := target.Parent.Key
	if target.Mailbox != nil {
		key = target.Mailbox.Key
	}
	exists, err := s.HasComment(ctx, target, marker)
	if err != nil {
		return err
	}
	if exists {
		return nil
	}
	return s.cli.AddComment(ctx, key, body+"\n\n<!-- "+marker+" -->")
}

// --- Recovery ---

// ResetForRecovery resets mailbox subtasks to To Do while preserving
// comments, labels, and history. No parent rollback runs.
func (s *system) ResetForRecovery(ctx context.Context, _ task.TicketRef, mailboxes []task.Mailbox, _ config.RawValues) error {
	for _, mb := range mailboxes {
		if err := s.transition(ctx, mb.Key, "To Do"); err != nil {
			return fmt.Errorf("reset mailbox %s: %w", mb.Key, err)
		}
	}
	return nil
}

func contains(xs []string, want string) bool {
	for _, x := range xs {
		if x == want {
			return true
		}
	}
	return false
}

func strField(fields map[string]any, key string) string {
	s, _ := fields[key].(string)
	return s
}

func strSliceField(fields map[string]any, key string) []string {
	out, _ := fields[key].([]string)
	return out
}
