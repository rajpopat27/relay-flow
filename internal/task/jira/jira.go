// Package jira is the Jira task-system adapter. It owns one typed Config
// spanning root, repo, workflow, and node scopes; core never imports it.
package jira

import (
	"context"
	"fmt"
	"log/slog"
	"regexp"
	"sort"
	"strings"
	"sync"

	"github.com/rajpopat27/relay-flow/internal/config"
	"github.com/rajpopat27/relay-flow/internal/retry"
	"github.com/rajpopat27/relay-flow/internal/task"
	jirarest "github.com/rajpopat27/relay-flow/internal/task/jira/rest"
)

// Config is the adapter-owned typed config for every scope.
type Config struct {
	Assignee   string       `yaml:"assignee,omitempty"`
	Project    string       `yaml:"project,omitempty"`
	Component  string       `yaml:"component,omitempty"`
	Filters    Filters      `yaml:"filters,omitempty"`
	Transition TransitionTo `yaml:"transitionTo,omitempty"`
	Templates  Templates    `yaml:"templates,omitempty"`
}

// Templates are Jira-owned task-system descriptions and comments.
type Templates struct {
	MailboxDescription string `yaml:"mailboxDescription"`
	SummaryComment     string `yaml:"summaryComment"`
	FeedbackComment    string `yaml:"feedbackComment"`
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
	defaultStartParentStatus  = "In Progress"
	defaultWorkTaskStatus     = "In Progress"
	defaultEndParentStatus    = "Done"
	currentUserAssigneeFilter = "currentUser()"
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

var textVarPattern = regexp.MustCompile(`\{\{([^{}]*)\}\}`)

var knownTextVars = map[string]bool{
	"runID": true, "ticket": true, "workflow": true,
	"repo": true, "node": true, "nodeType": true, "agent": true,
	"nodeDescription": true, "nextSteps": true, "successRoutes": true,
	"failureRoutes": true, "mailbox": true, "sourceNode": true,
	"targetNode": true, "summaryReport": true, "feedbackReport": true,
}

// DefaultConfig supplies Jira task-system text defaults for relay-flow init.
func DefaultConfig() config.RawValues {
	return config.RawValues{"templates": map[string]any{
		"mailboxDescription": defaultMailboxDescription,
		"summaryComment":     defaultSummaryComment,
		"feedbackComment":    defaultFeedbackComment,
	}}
}

var (
	clientsMu sync.Mutex
	clients   = map[string]struct {
		token  string
		client *jirarest.HTTPClient
	}{}
)

// claimLabel is the permanent workflow claim label.
func claimLabel(workflow string) string { return "wf:" + workflow }

func init() {
	task.Register("jira", task.Factory{
		RequiredRepoKeys: func() []string { return []string{"project", "component"} },
		DefaultConfig:    DefaultConfig,
		ValidateTextConfig: func(raw config.RawValues) error {
			var cfg Config
			if err := config.DecodeStrict(raw, &cfg); err != nil {
				return err
			}
			return validateTemplates(cfg.Templates)
		},
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
			creds, err := loadCredentialsDefault()
			if err != nil {
				return "", fmt.Errorf("jira credentials: %w", err)
			}
			return strings.Join([]string{creds.Site, proj, comp}, "/"), nil
		},
		Auth: auth,
		New: func(ctx context.Context, spec task.RepoSpec) (task.System, error) {
			merged := config.Merge(spec.RootConfig, spec.RepoConfig)
			var cfg Config
			if err := config.DecodeStrict(merged, &cfg); err != nil {
				return nil, fmt.Errorf("jira repo %q config: %w", spec.Name, err)
			}
			creds, err := loadCredentialsDefault()
			if err != nil {
				return nil, fmt.Errorf("jira credentials: %w", err)
			}
			client, err := sharedClient(creds.Site, creds.Email, creds.Token)
			if err != nil {
				return nil, err
			}
			sys, err := newSystem(ctx, client, spec)
			if err != nil {
				return nil, err
			}
			sys.currentUser = strings.TrimSpace(creds.Email)
			return sys, nil
		},
	})
}

func sharedClient(site, email, token string) (*jirarest.HTTPClient, error) {
	key := strings.TrimRight(site, "/") + "\x00" + email
	clientsMu.Lock()
	defer clientsMu.Unlock()
	if cached := clients[key]; cached.client != nil && cached.token == token {
		return cached.client, nil
	}
	client, err := jirarest.New(site, email, token)
	if err != nil {
		return nil, err
	}
	clients[key] = struct {
		token  string
		client *jirarest.HTTPClient
	}{token: token, client: client}
	return client, nil
}

// system is the repo-bound Jira task.System. It is safe for concurrent use;
// the REST client owns connection reuse, caches, and request limiting.
type system struct {
	cli         jirarest.Client
	repoName    string
	currentUser string
	base        config.RawValues
	effective   Config // root+repo merged
}

func newSystem(ctx context.Context, cli jirarest.Client, spec task.RepoSpec) (*system, error) {
	if spec.Name == "" {
		return nil, fmt.Errorf("jira: repo name is required")
	}
	merged := config.Merge(DefaultConfig(), spec.RootConfig, spec.RepoConfig)
	var cfg Config
	if err := config.DecodeStrict(merged, &cfg); err != nil {
		return nil, fmt.Errorf("jira repo %q config: %w", spec.Name, err)
	}
	if err := validateTemplates(cfg.Templates); err != nil {
		return nil, fmt.Errorf("jira repo %q taskConfig.templates: %w", spec.Name, err)
	}
	if cfg.Assignee != "" {
		if err := cli.ValidateAssignee(ctx, cfg.Project, cfg.Assignee); err != nil {
			return nil, fmt.Errorf("jira repo %q assignee %q: %w", spec.Name, cfg.Assignee, err)
		}
	}
	s := &system{cli: cli, repoName: spec.Name, base: merged, effective: cfg}
	if err := s.validateTransition(ctx, "repo config", cfg.Project, cfg.Transition); err != nil {
		return nil, fmt.Errorf("jira repo %q: %w", spec.Name, err)
	}
	for _, status := range []string{defaultStartParentStatus, defaultEndParentStatus, "To Do"} {
		if err := cli.ValidateStatus(ctx, cfg.Project, status); err != nil {
			return nil, fmt.Errorf("jira repo %q default status %q: %w", spec.Name, status, err)
		}
	}
	// project/component are required repo keys enforced at registration
	// (RequiredRepoKeys); construction also probes external Jira names.
	return s, nil
}

// newSystemForCLI constructs a system around an explicit Jira client seam.
func newSystemForCLI(cli jirarest.Client) (task.System, error) {
	return newSystem(context.Background(), cli, task.RepoSpec{
		Name:       "payments",
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
		return fmt.Errorf("summaryComment must contain {{summaryReport}}")
	}
	if !strings.Contains(templates.FeedbackComment, "{{feedbackReport}}") {
		return fmt.Errorf("feedbackComment must contain {{feedbackReport}}")
	}
	return nil
}

// RenderText renders Jira-owned mailbox descriptions and comments.
func (s *system) RenderText(kind task.TextKind, data task.TextData) (string, error) {
	var tmpl string
	switch kind {
	case task.TextMailboxDescription:
		tmpl = s.effective.Templates.MailboxDescription
	case task.TextSummaryComment:
		tmpl = s.effective.Templates.SummaryComment
	case task.TextFeedbackComment:
		tmpl = s.effective.Templates.FeedbackComment
	default:
		return "", fmt.Errorf("jira: unknown task text kind %q", kind)
	}
	values := map[string]string{
		"runID": data.RunID, "ticket": data.Ticket,
		"workflow": data.Workflow, "repo": data.Repo, "node": data.Node,
		"nodeType": data.NodeType, "agent": data.Agent,
		"nodeDescription": data.NodeDescription, "nextSteps": data.NextSteps,
		"successRoutes": data.SuccessRoutes, "failureRoutes": data.FailureRoutes,
		"mailbox": data.Mailbox, "sourceNode": data.SourceNode,
		"targetNode": data.TargetNode, "summaryReport": data.SummaryReport,
		"feedbackReport": data.FeedbackReport,
	}
	return textVarPattern.ReplaceAllStringFunc(tmpl, func(match string) string {
		return values[textVarPattern.FindStringSubmatch(match)[1]]
	}), nil
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
	merged := config.Merge(s.base, workflowTaskConfig)
	cfg, err := decodeConfig(merged)
	if err != nil {
		return nil, err
	}
	f := cfg.Filters
	if !hasAssigneeFilter(merged) && cfg.Assignee != "" {
		f.Assignees = []string{cfg.Assignee}
	}
	resolvedAssignees, err := s.resolveAssigneeFilters(f.Assignees)
	if err != nil {
		return nil, err
	}
	f.Assignees = resolvedAssignees
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
		if len(f.Assignees) > 0 && !containsFold(f.Assignees, strField(t.Fields, "assignee")) {
			return false
		}
		return true
	}, nil
}

func (s *system) resolveAssigneeFilters(values []string) ([]string, error) {
	resolved := make([]string, 0, len(values))
	for _, value := range values {
		if value == currentUserAssigneeFilter {
			if s.currentUser == "" {
				return nil, fmt.Errorf("jira: filters.assignees value %q requires authenticated Jira credentials", currentUserAssigneeFilter)
			}
			value = s.currentUser
		}
		resolved = append(resolved, value)
	}
	return resolved, nil
}

func hasAssigneeFilter(raw config.RawValues) bool {
	switch filters := raw["filters"].(type) {
	case map[string]any:
		_, ok := filters["assignees"]
		return ok
	case config.RawValues:
		_, ok := filters["assignees"]
		return ok
	default:
		return false
	}
}

// --- Claim ---

// Claim adds wf:<workflow> using the claims already inspected by routing.
// Jira's label-add operation is idempotent.
func (s *system) Claim(ctx context.Context, ticket task.TicketRef, workflow string) error {
	return s.cli.EnsureLabel(ctx, ticket.Key, claimLabel(workflow))
}

// --- Config validation ---

// ValidateConfig strictly validates the workflow and every node task config
// against the adapter-owned schema for this repo. It never mutates the
// caller's maps.
func (s *system) ValidateConfig(ctx context.Context, workflowTaskConfig config.RawValues, nodeTaskConfigs map[string]config.RawValues) error {
	workflowCfg, err := decodeConfig(config.Merge(s.base, workflowTaskConfig))
	if err != nil {
		return fmt.Errorf("workflow taskConfig: %w", err)
	}
	if err := validateTemplates(workflowCfg.Templates); err != nil {
		return fmt.Errorf("workflow taskConfig.templates: %w", err)
	}
	if err := s.validateAssignee(ctx, "workflow", workflowCfg.Project, workflowCfg.Assignee); err != nil {
		return err
	}
	if err := s.validateTransition(ctx, "workflow", workflowCfg.Project, workflowCfg.Transition); err != nil {
		return err
	}
	nodes := make([]string, 0, len(nodeTaskConfigs))
	for n := range nodeTaskConfigs {
		nodes = append(nodes, n)
	}
	sort.Strings(nodes)
	for _, n := range nodes {
		cfg, err := decodeConfig(config.Merge(s.base, workflowTaskConfig, nodeTaskConfigs[n]))
		if err != nil {
			return fmt.Errorf("node %q taskConfig: %w", n, err)
		}
		if err := validateTemplates(cfg.Templates); err != nil {
			return fmt.Errorf("node %q taskConfig.templates: %w", n, err)
		}
		if err := s.validateAssignee(ctx, fmt.Sprintf("node %q", n), cfg.Project, cfg.Assignee); err != nil {
			return err
		}
		if err := s.validateTransition(ctx, fmt.Sprintf("node %q", n), cfg.Project, cfg.Transition); err != nil {
			return err
		}
	}
	return nil
}

func (s *system) validateAssignee(ctx context.Context, scope, project, assignee string) error {
	if assignee == "" {
		return nil
	}
	if err := s.cli.ValidateAssignee(ctx, project, assignee); err != nil {
		return fmt.Errorf("%s assignee %q: %w", scope, assignee, err)
	}
	return nil
}

func (s *system) validateTransition(ctx context.Context, scope, project string, transition TransitionTo) error {
	for _, candidate := range []struct {
		field  string
		status string
	}{
		{field: "parentStatus", status: transition.ParentStatus},
		{field: "taskStatus", status: transition.TaskStatus},
	} {
		field, status := candidate.field, candidate.status
		if status == "" {
			continue
		}
		if err := s.cli.ValidateStatus(ctx, project, status); err != nil {
			return fmt.Errorf("%s %s %q: %w", scope, field, status, err)
		}
	}
	return nil
}

// LifecycleDefaults exposure: the deterministic Jira transition defaults per
// lifecycle point (spec: Jira transition defaults are deterministic). Run
// orchestration merges these raw values under the effective node config
// before ApplyTaskConfig, so the effective precedence is
// built-in default < root < repo < workflow < node.
//
// The methods also carry the inherited root/repository transitionTo and
// assignee values. ApplyTaskConfig receives only the workflow/node config the
// caller assembles, so returning inherited values here is what makes a
// repo-scoped value take effect at runtime instead of being validated and then
// silently discarded. internal/task/beads implements the same rule; keep the
// two adapters aligned.

// StartDefaults defaults the parent to In Progress.
func (s *system) StartDefaults() config.RawValues {
	return s.lifecycleDefaults(transitionDefault("parentStatus", defaultStartParentStatus))
}

// WorkDefaults defaults the mailbox task status to In Progress; the parent
// is left unchanged when parentStatus is omitted.
func (s *system) WorkDefaults() config.RawValues {
	return s.lifecycleDefaults(transitionDefault("taskStatus", defaultWorkTaskStatus))
}

// EndDefaults defaults the parent to Done.
func (s *system) EndDefaults() config.RawValues {
	return s.lifecycleDefaults(transitionDefault("parentStatus", defaultEndParentStatus))
}

func transitionDefault(key, value string) config.RawValues {
	return config.RawValues{"transitionTo": map[string]any{key: value}}
}

// lifecycleDefaults layers the inherited root/repository operation values over
// the built-in lifecycle default. Only the keys ApplyTaskConfig consumes are
// carried, so unrelated configuration never enters durable activity inputs.
func (s *system) lifecycleDefaults(builtin config.RawValues) config.RawValues {
	inherited := config.RawValues{}
	for _, key := range []string{"transitionTo", "assignee"} {
		if value, ok := s.base[key]; ok {
			inherited[key] = value
		}
	}
	return config.Merge(builtin, inherited)
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
	missing := make([]task.MailboxSpec, 0)
	for _, spec := range specs {
		if mb, ok := existing[spec.Title]; ok {
			if err := s.cli.UpdateMailbox(ctx, mb.Key, spec.Description, claimLabel(workflow)); err != nil {
				return nil, fmt.Errorf("reconcile mailbox %q: %w", mb.Key, err)
			}
			mb.Node = spec.Node
			out[spec.Node] = mb
			continue
		}
		missing = append(missing, spec)
	}
	if len(missing) == 0 {
		return out, nil
	}
	createSpecs := make([]jirarest.SubtaskSpec, 0, len(missing))
	for _, spec := range missing {
		createSpecs = append(createSpecs, jirarest.SubtaskSpec{Title: spec.Title, Description: spec.Description})
	}
	created, err := s.cli.CreateSubtasks(ctx, parent.Key, s.effective.Project, claimLabel(workflow), createSpecs)
	if err != nil {
		return nil, fmt.Errorf("create mailboxes: %w", err)
	}
	if len(created) != len(missing) {
		return nil, fmt.Errorf("create mailboxes: created %d of %d", len(created), len(missing))
	}
	for i, mailbox := range created {
		spec := missing[i]
		if mailbox.ID == "" || mailbox.Key == "" {
			return nil, fmt.Errorf("create mailbox %q: Jira returned no id/key", spec.Title)
		}
		out[spec.Node] = task.Mailbox{ID: mailbox.ID, Key: mailbox.Key, Node: spec.Node}
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
		if err := s.transition(ctx, target.Mailbox.Key, status, cfg.Assignee); err != nil {
			return err
		}
		if tr.ParentStatus != "" {
			return s.transition(ctx, target.Parent.Key, tr.ParentStatus, "")
		}
		return nil
	}
	// Parent-only target (start/end lifecycle processing).
	status := tr.ParentStatus
	if status == "" {
		status = defaultStartParentStatus
	}
	return s.transition(ctx, target.Parent.Key, status, "")
}

// transition applies a Jira status transition, mapping a human-incompatible
// current state to a conflict.
func (s *system) transition(ctx context.Context, key, status, assignee string) error {
	err := s.cli.Transition(ctx, key, status, assignee)
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
	return s.transition(ctx, mailbox.Key, "Done", "")
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

// PrepareRestart reopens existing relay-owned mailboxes before a fresh
// explicit attempt. It does not change the parent ticket: the normal start
// ApplyTaskConfig operation owns the parent status check, so a human-owned
// parent status can put the new attempt in blocked state without being
// overwritten. Jira transition failures are classified as conflicts by
// transition and therefore retry without blind status writes.
func (s *system) PrepareRestart(ctx context.Context, _ task.TicketRef, mailboxes []task.Mailbox) error {
	for _, mailbox := range mailboxes {
		if err := s.transition(ctx, mailbox.Key, "To Do", ""); err != nil {
			return fmt.Errorf("reopen mailbox %s for restart: %w", mailbox.Key, err)
		}
	}
	return nil
}

// --- Recovery ---

// ResetForRecovery resets mailbox subtasks to To Do while preserving
// comments, labels, and history. No parent rollback runs.
func (s *system) ResetForRecovery(ctx context.Context, _ task.TicketRef, mailboxes []task.Mailbox, _ config.RawValues) error {
	for _, mb := range mailboxes {
		if err := s.transition(ctx, mb.Key, "To Do", ""); err != nil {
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

func containsFold(xs []string, want string) bool {
	for _, x := range xs {
		if strings.EqualFold(x, want) {
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

var (
	_ task.System            = (*system)(nil)
	_ task.LifecycleDefaults = (*system)(nil)
	_ task.RestartPreparer   = (*system)(nil)
)
