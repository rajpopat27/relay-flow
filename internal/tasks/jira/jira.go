// Package jira is the built-in Tasks adapter for Jira. It talks to Jira
// via the acli CLI and knows nothing about runners, terminals, or agents.
// States are Jira status names; claims are Jira labels (wf:<workflow>).
package jira

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/rajpopat27/relay-flow/internal/acli"
	"github.com/rajpopat27/relay-flow/internal/config"
	"github.com/rajpopat27/relay-flow/internal/tasks"
)

func init() {
	tasks.Register("jira", tasks.Factory{
		UnmarshalConfig: unmarshalConfig,
		New: func(cfg any, wfName string, nodes map[string]config.Node, assignee, repoName string) (tasks.Tasks, error) {
			c, ok := cfg.(JiraConfig)
			if !ok {
				return nil, fmt.Errorf("internal: jira factory received %T", cfg)
			}
			if repoName == "" {
				return nil, fmt.Errorf("jira adapter requires a repo name (used as the JQL component filter)")
			}
			return newJira(c, wfName, nodes, assignee, repoName, nil)
		},
	})
}

// JiraConfig is the strictly-unmarshalled tasks.config for type jira.
type JiraConfig struct {
	// Query is a JQL fragment (no issuetype / assignee / ORDER BY — those
	// are appended by the adapter or rejected).
	Query string `yaml:"query"`
	// IssueTypes restricts the workflow to these Jira issue types.
	IssueTypes []string `yaml:"issueTypes"`
	// AssigneeIsAgent marks centralized mode (org server owns the queue,
	// tickets assigned upstream to bot accounts): no assignee clause.
	AssigneeIsAgent bool `yaml:"assigneeIsAgent"`
}

var (
	issueTypeRe = regexp.MustCompile(`(?i)\bissuetype\b`)
	assigneeRe  = regexp.MustCompile(`(?i)\bassignee\b`)
)

func unmarshalConfig(m map[string]any) (any, error) {
	var c JiraConfig
	if err := strictDecode(m, &c); err != nil {
		return nil, err
	}
	if strings.TrimSpace(c.Query) == "" {
		return nil, fmt.Errorf("query must not be empty")
	}
	if strings.Contains(strings.ToUpper(c.Query), "ORDER BY") {
		return nil, fmt.Errorf("query must not contain ORDER BY (always ordered by updated)")
	}
	if issueTypeRe.MatchString(c.Query) {
		return nil, fmt.Errorf("query must not contain issuetype; use the issueTypes field")
	}
	if assigneeRe.MatchString(c.Query) {
		return nil, fmt.Errorf("query must not contain assignee; identity comes from machine config or assigneeIsAgent")
	}
	if len(c.IssueTypes) == 0 {
		return nil, fmt.Errorf("issueTypes must not be empty")
	}
	for _, it := range c.IssueTypes {
		if strings.TrimSpace(it) == "" {
			return nil, fmt.Errorf("issueTypes must not contain empty values")
		}
	}
	return c, nil
}

// aclier is the seam to Jira. *acli.Client satisfies it; tests fake it.
type aclier interface {
	Search(jql string) ([]acli.Ticket, error)
	View(key string) (acli.Ticket, error)
	AddLabel(key string, existing []string, label string) error
	Transition(key, status string) error
	Comment(key, body string) error
}

type jiraTasks struct {
	cfg        JiraConfig
	wfName     string
	nodes      map[string]config.Node
	assignee   string
	repoComponent string
	jql        string
	ac         aclier
}

// newJira builds the adapter. repoComponent is the Jira component name
// (the Orca repo displayName); empty in unit tests → clause omitted.
// ac nil → real acli client.
func newJira(cfg JiraConfig, wfName string, nodes map[string]config.Node, assignee, repoComponent string, ac aclier) (*jiraTasks, error) {
	if ac == nil {
		ac = acli.New()
	}
	j := &jiraTasks{cfg: cfg, wfName: wfName, nodes: nodes, assignee: assignee, repoComponent: repoComponent, ac: ac}
	j.jql = j.buildJQL()
	return j, nil
}

func (j *jiraTasks) buildJQL() string {
	quoted := make([]string, 0, len(j.cfg.IssueTypes))
	for _, it := range j.cfg.IssueTypes {
		quoted = append(quoted, fmt.Sprintf("%q", it))
	}
	q := fmt.Sprintf("(%s) AND issuetype IN (%s)", j.cfg.Query, strings.Join(quoted, ", "))
	if j.repoComponent != "" {
		q += fmt.Sprintf(" AND component = %q", j.repoComponent)
	}
	// Assignee comes from the machine config (per-person, uncommitted),
	// never the shared workflow YAML. Centralized mode skips the clause.
	if j.assignee != "" && !j.cfg.AssigneeIsAgent {
		q += fmt.Sprintf(" AND assignee = %q", j.assignee)
	}
	return q + " ORDER BY updated"
}

func claimLabel(wfName string) string { return "wf:" + wfName }

// List runs the workflow's one JQL query and maps each ticket's Jira
// status back to a node via the `when` values ("" = unmapped). ClaimedBy
// is read off the wf:* labels.
func (j *jiraTasks) List() ([]tasks.Ticket, error) {
	found, err := j.ac.Search(j.jql)
	if err != nil {
		return nil, err
	}
	out := make([]tasks.Ticket, 0, len(found))
	for _, t := range found {
		tk := tasks.Ticket{Key: t.Key, Summary: t.Summary}
		for name, n := range j.nodes {
			if strings.EqualFold(n.When, t.Status) {
				tk.Node = name
				break
			}
		}
		for _, l := range t.Labels {
			if strings.HasPrefix(l, "wf:") {
				tk.ClaimedBy = strings.TrimPrefix(l, "wf:")
				break
			}
		}
		out = append(out, tk)
	}
	return out, nil
}

// Claim attaches this workflow's claim label. Labels are never removed:
// they are the cross-restart mutex.
func (j *jiraTasks) Claim(t tasks.Ticket) error {
	cur, err := j.ac.View(t.Key)
	if err != nil {
		return fmt.Errorf("claim %s: view: %w", t.Key, err)
	}
	return j.ac.AddLabel(t.Key, cur.Labels, claimLabel(j.wfName))
}

// Report transitions the ticket to the target node's Jira status and
// posts the summary as a comment. Self-loop (target status == current
// status) → comment only (Jira has no self-transitions).
func (j *jiraTasks) Report(t tasks.Ticket, outcome, targetNode, summary string) error {
	target, ok := j.nodes[targetNode]
	if !ok {
		return fmt.Errorf("unknown node %q", targetNode)
	}
	cur, err := j.ac.View(t.Key)
	if err != nil {
		return fmt.Errorf("view %s: %w", t.Key, err)
	}
	agent := j.nodes[t.Node].Agent
	body := fmt.Sprintf("[%s] %s (agent: %s, node: %s) reported %s → %s\n\n%s", j.wfName, t.Key, agent, t.Node, outcome, targetNode, summary)
	if strings.EqualFold(cur.Status, target.When) {
		return j.ac.Comment(t.Key, body)
	}
	if err := j.ac.Transition(t.Key, target.When); err != nil {
		return fmt.Errorf("transition %s → %q: %w", t.Key, target.When, err)
	}
	return j.ac.Comment(t.Key, body)
}

// ProjectKeyFromQuery extracts the project key from a JQL fragment (used
// by submit-time status validation).
func ProjectKeyFromQuery(query string) (string, error) {
	re := regexp.MustCompile(`(?i)\bproject\s*=\s*("?[A-Za-z][A-Za-z0-9]*"?)`)
	m := re.FindStringSubmatch(query)
	if m == nil {
		return "", fmt.Errorf("could not find 'project = <KEY>' in query %q; it is required for status validation", query)
	}
	return strings.Trim(m[1], `"`), nil
}

// UnmarshalConfigForValidation exposes the strict config decode for
// submit-time validation (server needs the query/assigneeIsAgent fields).
func UnmarshalConfigForValidation(m map[string]any) (JiraConfig, error) {
	c, err := unmarshalConfig(m)
	if err != nil {
		return JiraConfig{}, err
	}
	return c.(JiraConfig), nil
}

// StatusValidator is the seam ValidateStates uses. *acli.Client fits.
type StatusValidator interface {
	ValidateStatus(projectKey, status string) error
}

// ValidateStates probes every node's `when` status against the Jira
// project; Jira's JQL parser rejects unknown statuses with a hard error,
// so a typo fails at submit instead of silently matching zero tickets.
func ValidateStates(v StatusValidator, nodes map[string]config.Node, projectKey string) ([]string, error) {
	seen := map[string]bool{}
	var bad []string
	for _, n := range nodes {
		key := strings.ToLower(strings.TrimSpace(n.When))
		if key == "" || seen[key] {
			continue
		}
		seen[key] = true
		if err := v.ValidateStatus(projectKey, n.When); err != nil {
			bad = append(bad, n.When)
		}
	}
	return bad, nil
}
