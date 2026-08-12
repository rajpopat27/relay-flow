// Package acli wraps the Atlassian `acli` CLI. Every call is real — no
// dry-run mode: acli/Jira calls always execute (only orca CLI calls
// support dry-run).
package acli

import (
	"encoding/json"
	"fmt"
	"log"
	"os/exec"
	"strings"
)

type Client struct{}

func New() *Client {
	return &Client{}
}

// rawSearchResult matches acli search's minimal JSON shape: only `key`.
type rawSearchResult struct {
	Key string `json:"key"`
}

// rawTicket matches acli view's JSON shape: `key` is top-level, other
// fields live under `fields`.
type rawTicket struct {
	Key    string `json:"key"`
	Fields struct {
		Summary string `json:"summary"`
		Status  struct {
			Name string `json:"name"`
		} `json:"status"`
		Components []struct {
			Name string `json:"name"`
		} `json:"components"`
		IssueType struct {
			Name string `json:"name"`
		} `json:"issuetype"`
		Description json.RawMessage `json:"description"`
		Parent      struct {
			Key string `json:"key"`
		} `json:"parent"`
		Labels []string `json:"labels"`
	} `json:"fields"`
}

type Ticket struct {
	Key         string
	Summary     string
	Description string
	Status      string
	IssueType   string
	// Component resolves to an Orca repo by matching repo displayName.
	Component string
	// ParentKey is the Jira parent ticket's key (native subtask parent
	// link), if any. Subtasks reuse their parent's worktree/base branch
	// instead of branching from main.
	ParentKey string
	// Labels are the ticket's Jira labels, e.g. "baseBranch:foo" or the
	// "orca-workflow:<name>" claim label.
	Labels []string
}

// LabelValue returns the value portion of a "prefix:value" label on the
// ticket, if present, e.g. LabelValue("baseBranch") -> "foo" for label
// "baseBranch:foo".
func (t Ticket) LabelValue(prefix string) (string, bool) {
	for _, l := range t.Labels {
		if strings.HasPrefix(l, prefix+":") {
			return strings.TrimPrefix(l, prefix+":"), true
		}
	}
	return "", false
}

// adfText extracts plain text from a Jira ADF (Atlassian Document Format)
// description by concatenating every "text" node.
func adfText(raw json.RawMessage) string {
	var node struct {
		Text    string            `json:"text"`
		Content []json.RawMessage `json:"content"`
	}
	if err := json.Unmarshal(raw, &node); err != nil {
		return ""
	}
	var parts []string
	if node.Text != "" {
		parts = append(parts, node.Text)
	}
	for _, c := range node.Content {
		if t := adfText(c); t != "" {
			parts = append(parts, t)
		}
	}
	return strings.Join(parts, " ")
}

// Search runs `acli jira workitem search --jql <jql> --fields key --json`
// to get matching ticket keys only, then fetches full details for each via
// `view`. In dry-run mode, no subprocess is executed; caller should treat
// result as empty.
func (c *Client) Search(jql string) ([]Ticket, error) {
	// No --fields flag: default output always includes top-level "key",
	// which is all we need here. Full details are fetched per-ticket via view.
	out, err := exec.Command("acli", "jira", "workitem", "search",
		"--jql", jql,
		"--json").Output()
	if err != nil {
		return nil, fmt.Errorf("acli search: %w", err)
	}
	var raw []rawSearchResult
	if err := json.Unmarshal(out, &raw); err != nil {
		return nil, fmt.Errorf("acli search: parse json: %w", err)
	}
	tickets := make([]Ticket, 0, len(raw))
	for _, r := range raw {
		t, err := c.View(r.Key)
		if err != nil {
			log.Printf("acli search: could not fetch details for %s: %v", r.Key, err)
			continue
		}
		tickets = append(tickets, t)
	}
	return tickets, nil
}

// ValidateStatus checks that status is a real Jira status in the given
// project by running `status = "<status>"` as a JQL fragment — Jira's JQL
// parser rejects unknown status values with a hard error, so a typo in
// the workflow YAML ("DO Done") fails here instead of silently never
// matching at runtime. Returns nil if valid, error otherwise.
func (c *Client) ValidateStatus(projectKey, status string) error {
	jql := fmt.Sprintf(`project = %s AND status = %q`, projectKey, status)
	out, err := exec.Command("acli", "jira", "workitem", "search",
		"--jql", jql, "--json").CombinedOutput()
	if err != nil {
		return fmt.Errorf("status %q is not valid in project %s: %s", status, projectKey, strings.TrimSpace(string(out)))
	}
	return nil
}

// view fetches full ticket details via
// `acli jira workitem view <key> --fields status,assignee,reporter,components --json`.
func (c *Client) View(key string) (Ticket, error) {
	out, err := exec.Command("acli", "jira", "workitem", "view", key,
		"--fields", "summary,description,status,components,issuetype,parent,labels",
		"--json").Output()
	if err != nil {
		return Ticket{}, fmt.Errorf("acli view %s: %w", key, err)
	}
	var r rawTicket
	if err := json.Unmarshal(out, &r); err != nil {
		return Ticket{}, fmt.Errorf("acli view %s: parse json: %w", key, err)
	}
	var t Ticket
	t.Key = r.Key
	t.Summary = r.Fields.Summary
	t.Description = adfText(r.Fields.Description)
	t.Status = r.Fields.Status.Name
	t.IssueType = r.Fields.IssueType.Name
	if len(r.Fields.Components) > 0 {
		t.Component = r.Fields.Components[0].Name
	}
	t.ParentKey = r.Fields.Parent.Key
	t.Labels = r.Fields.Labels
	return t, nil
}

// AddLabel adds label to the ticket's existing labels (acli's --labels
// flag replaces the set, so the caller/View result must be merged in).
func (c *Client) AddLabel(key string, existing []string, label string) error {
	for _, l := range existing {
		if l == label {
			return nil // already present
		}
	}
	all := append(append([]string{}, existing...), label)
	return runAcli("jira", "workitem", "edit", "--key", key, "--labels", strings.Join(all, ","), "--yes", "--json")
}

func (c *Client) Transition(key, status string) error {
	return runAcli("jira", "workitem", "transition", "--key", key, "--status", status, "--yes", "--json")
}

func (c *Client) Comment(key, body string) error {
	return runAcli("jira", "workitem", "comment", "create", "--key", key, "--body", body, "--json")
}

func runAcli(args ...string) error {
	out, err := exec.Command("acli", args...).CombinedOutput()
	if err != nil {
		return fmt.Errorf("acli %v: %w: %s", args, err, string(out))
	}
	return nil
}
