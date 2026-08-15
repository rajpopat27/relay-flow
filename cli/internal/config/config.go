// Package config loads .workflow/<config-name>.yaml.
package config

import (
	"bytes"
	"fmt"
	"os"
	"regexp"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

// StringList accepts either a scalar or a list of strings in YAML and
// normalizes both forms to []string, so a single value can be written as
// `closeOn: Done` instead of `closeOn: [Done]`.
type StringList []string

func (s *StringList) UnmarshalYAML(value *yaml.Node) error {
	if value.Kind == yaml.ScalarNode {
		if value.Tag != "!!str" {
			return fmt.Errorf("expected string or list of strings")
		}
		*s = []string{value.Value}
		return nil
	}
	var values []string
	if err := value.Decode(&values); err != nil {
		return err
	}
	*s = values
	return nil
}

func (s StringList) Has(name string) bool {
	for _, value := range s {
		if strings.EqualFold(value, name) {
			return true
		}
	}
	return false
}

func (s StringList) First() string {
	if len(s) == 0 {
		return ""
	}
	return s[0]
}

// HandleSpec is one Jira status an agent handles, with the outcome map
// that applies while the ticket is in that status. Per-status outcomes
// let one agent serve multiple statuses with different targets (e.g.
// plan's "done" means In Progress from To Do, but Done from In Review).
type HandleSpec struct {
	Status string `yaml:"status"`
	// Outcomes maps each agent-reported status to the next Jira status.
	// A target equal to Status is a self-loop: Report comments but skips
	// the Jira transition (Jira has no self-transitions).
	Outcomes map[string]string `yaml:"outcomes"`
}

type AgentConfig struct {
	// Handles is the list of Jira statuses that activate this agent,
	// each with its own outcome map.
	Handles []HandleSpec `yaml:"handles"`
	// NudgePrompt is the message sent into the agent's existing terminal
	// when its ticket lands back on a status handled by this agent (instead
	// of spawning a fresh session). Supports {{ticket}} and {{status}}
	// placeholders. Defaults to DefaultNudgePrompt when empty.
	NudgePrompt string `yaml:"nudgePrompt"`
}

// DefaultNudgePrompt is used when an agent declares no nudgePrompt.
const DefaultNudgePrompt = "Ticket {{ticket}} is assigned to you again (Jira status: {{status}}). Run `acli jira workitem view {{ticket}} --fields summary,description,comment --json` to read the latest feedback, then continue working on it. End your reply with the STATUS/SUMMARY block as before."

// HandlesStatus reports whether jiraStatus activates this agent
// (case-insensitive).
func (a AgentConfig) HandlesStatus(jiraStatus string) bool {
	for _, h := range a.Handles {
		if strings.EqualFold(h.Status, jiraStatus) {
			return true
		}
	}
	return false
}

// OutcomesFor returns the outcome map for jiraStatus (case-insensitive),
// or nil if this agent does not handle that status.
func (a AgentConfig) OutcomesFor(jiraStatus string) map[string]string {
	for _, h := range a.Handles {
		if strings.EqualFold(h.Status, jiraStatus) {
			return h.Outcomes
		}
	}
	return nil
}

// StatusNamesFor returns the allowed internal status names for jiraStatus,
// sorted so prompt and validation messages are deterministic.
func (a AgentConfig) StatusNamesFor(jiraStatus string) []string {
	outcomes := a.OutcomesFor(jiraStatus)
	names := make([]string, 0, len(outcomes))
	for name := range outcomes {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// HasStatus reports whether name is one of this agent's allowed outcomes
// for the given Jira status.
func (a AgentConfig) HasStatus(jiraStatus, name string) bool {
	_, ok := a.OutcomesFor(jiraStatus)[name]
	return ok
}

// AllJiraStatuses returns every Jira status name referenced by this agent
// (handle statuses + outcome targets), for status validation.
func (a AgentConfig) AllJiraStatuses() []string {
	seen := map[string]bool{}
	var names []string
	add := func(s string) {
		if k := strings.ToLower(strings.TrimSpace(s)); k != "" && !seen[k] {
			seen[k] = true
			names = append(names, s)
		}
	}
	for _, h := range a.Handles {
		add(h.Status)
		for _, target := range h.Outcomes {
			add(target)
		}
	}
	return names
}

// Workflow is one JQL-selected ticket workflow: which agents handle each
// Jira status and which statuses end terminal lifetime.
type Workflow struct {
	// JQL is the workflow's base query; the component filter (derived from
	// the current repo's displayName), the issueTypes filter, and a fixed
	// ordering are appended automatically at runtime — never written by
	// hand. JQL must therefore not contain issuetype or ORDER BY clauses.
	JQL string `yaml:"jql"`
	// IssueTypes lists the Jira issue types this workflow applies to
	// (e.g. [Task, Story]). Required: workflows map to issue types, so a
	// story never accidentally enters a task's status flow. Appended to
	// the JQL as `AND issuetype IN (...)`. Accepts scalar or list.
	IssueTypes StringList `yaml:"issueTypes"`
	// CloseOn lists Jira statuses at which the daemon closes all of the
	// ticket's agent terminals (e.g. ["Done"]). Statuses NOT listed here
	// and not handled by an agent leave terminals untouched, so a review
	// bounce can reuse the same session.
	CloseOn StringList `yaml:"closeOn"`
	// Agents maps workflow-local agent names to the statuses they handle
	// and their allowed outcome transitions.
	Agents map[string]AgentConfig `yaml:"agents"`
}

type Config struct {
	// Name is the config's identity: server registry key, claim-label
	// component, and CLI argument. Must be camelCase, unique per server.
	Name                string `yaml:"name"`
	PollIntervalSeconds int    `yaml:"pollIntervalSeconds"`
	// Assignee, when set, is appended to every workflow's JQL as
	// `AND assignee = "<value>"` (distributed mode: each dev's server
	// sees only their own tickets). Accepts a Jira display name or
	// accountId; validated against Jira at submit time.
	Assignee string `yaml:"assignee"`
	// AssigneeIsAgent marks centralized mode: a single org server owns
	// the queue and tickets are assigned to bot/agent accounts upstream,
	// so no assignee clause is added to the JQL.
	// Exactly one of Assignee / AssigneeIsAgent must be set.
	AssigneeIsAgent bool `yaml:"assigneeIsAgent"`
	// Workflows maps workflow IDs to their full workflow definition. IDs
	// must be camelCase and contain no spaces because they are used in CLI
	// arguments, env vars, labels, and log/pid paths.
	Workflows map[string]Workflow `yaml:"workflows"`
}

// AgentForStatus returns which agent should be invoked for a ticket's
// current Jira status in the named workflow.
func (c *Config) AgentForStatus(workflowName, jiraStatus string) (string, bool) {
	for agentName, agent := range c.Workflows[workflowName].Agents {
		if agent.HandlesStatus(jiraStatus) {
			return agentName, true
		}
	}
	return "", false
}

// AgentConfigFor returns the named agent's config, scoped to workflow.
func (c *Config) AgentConfigFor(workflowName, agentName string) (AgentConfig, bool) {
	agentCfg, ok := c.Workflows[workflowName].Agents[agentName]
	return agentCfg, ok
}

// ShouldCloseTerminals reports whether jiraStatus is one of the workflow's
// closeOn statuses (case-insensitive).
func (c *Config) ShouldCloseTerminals(workflowName, jiraStatus string) bool {
	return c.Workflows[workflowName].CloseOn.Has(jiraStatus)
}

var workflowIDPattern = regexp.MustCompile(`^[a-z][A-Za-z0-9]*$`)

// issueTypeRe detects an issuetype clause inside a hand-written JQL.
var issueTypeRe = regexp.MustCompile(`(?i)\bissuetype\b`)

// assigneeRe detects an assignee clause inside a hand-written JQL.
var assigneeRe = regexp.MustCompile(`(?i)\bassignee\b`)

// Validate cross-checks every status reference and detects duplicate
// handles within each workflow, so broken configs fail at load time.
func (c *Config) Validate() error {
	if c.Name == "" {
		return fmt.Errorf("name must not be empty; it identifies this config (server registry, claim labels)")
	}
	if !workflowIDPattern.MatchString(c.Name) {
		return fmt.Errorf("name %q must be camelCase with no spaces", c.Name)
	}
	if c.Assignee != "" && c.AssigneeIsAgent {
		return fmt.Errorf("assignee and assigneeIsAgent are mutually exclusive: assignee = distributed mode, assigneeIsAgent = central org server")
	}
	if c.Assignee == "" && !c.AssigneeIsAgent {
		return fmt.Errorf("one of assignee or assigneeIsAgent must be set: assignee = \"<jira user>\" for a per-developer server, assigneeIsAgent: true for a central org server")
	}
	if len(c.Workflows) == 0 {
		return fmt.Errorf("workflows must not be empty")
	}
	for workflowName, wf := range c.Workflows {
		if !workflowIDPattern.MatchString(workflowName) {
			return fmt.Errorf("workflows[%s]: workflow ID must be camelCase with no spaces", workflowName)
		}
		if wf.JQL == "" {
			return fmt.Errorf("workflows[%s].jql must not be empty", workflowName)
		}
		if strings.Contains(strings.ToUpper(wf.JQL), "ORDER BY") {
			return fmt.Errorf("workflows[%s].jql must not contain ORDER BY; it is always ordered by updated automatically", workflowName)
		}
		if issueTypeRe.MatchString(wf.JQL) {
			return fmt.Errorf("workflows[%s].jql must not contain issuetype; use the issueTypes field instead", workflowName)
		}
		if assigneeRe.MatchString(wf.JQL) {
			return fmt.Errorf("workflows[%s].jql must not contain assignee; use the top-level assignee field instead", workflowName)
		}
		if len(wf.IssueTypes) == 0 {
			return fmt.Errorf("workflows[%s].issueTypes must not be empty; workflows map to issue types", workflowName)
		}
		for _, it := range wf.IssueTypes {
			if strings.TrimSpace(it) == "" {
				return fmt.Errorf("workflows[%s].issueTypes must not contain empty values", workflowName)
			}
		}
		seenHandles := map[string]string{}
		for agentName := range wf.Agents {
			a := wf.Agents[agentName]
			if a.NudgePrompt == "" {
				a.NudgePrompt = DefaultNudgePrompt
				wf.Agents[agentName] = a
			}
			if len(a.Handles) == 0 {
				return fmt.Errorf("workflows[%s].agents[%s].handles must not be empty", workflowName, agentName)
			}
			for _, h := range a.Handles {
				key := strings.ToLower(strings.TrimSpace(h.Status))
				if key == "" {
					return fmt.Errorf("workflows[%s].agents[%s].handles must not contain empty statuses", workflowName, agentName)
				}
				if other, ok := seenHandles[key]; ok {
					return fmt.Errorf("workflows[%s].agents[%s].handles[%s] duplicates workflows[%s].agents[%s]", workflowName, agentName, h.Status, workflowName, other)
				}
				seenHandles[key] = agentName
				if len(h.Outcomes) == 0 {
					return fmt.Errorf("workflows[%s].agents[%s].handles[%s].outcomes must not be empty", workflowName, agentName, h.Status)
				}
				for statusName, target := range h.Outcomes {
					if strings.TrimSpace(statusName) == "" || strings.TrimSpace(target) == "" {
						return fmt.Errorf("workflows[%s].agents[%s].handles[%s].outcomes must not contain empty statuses or targets", workflowName, agentName, h.Status)
					}
				}
			}
		}
	}
	return nil
}

// Parse decodes and validates config YAML bytes. name is only used in
// error messages.
func Parse(name string, b []byte) (*Config, error) {
	var c Config
	dec := yaml.NewDecoder(bytes.NewReader(b))
	dec.KnownFields(true)
	if err := dec.Decode(&c); err != nil {
		return nil, fmt.Errorf("parse config %s: %w", name, err)
	}
	if c.PollIntervalSeconds <= 0 {
		c.PollIntervalSeconds = 15
	}
	if err := c.Validate(); err != nil {
		return nil, fmt.Errorf("config %s: %w", name, err)
	}
	return &c, nil
}

func Load(path string) (*Config, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read config %s: %w", path, err)
	}
	return Parse(path, b)
}
