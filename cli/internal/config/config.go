// Package config loads .workflow/<workflow-name>.yaml.
package config

import (
	"fmt"
	"os"
	"strings"

	"gopkg.in/yaml.v3"
)

// StatusDef is one internal status an agent may report, with the
// human-readable description injected into the agent's prompt so it knows
// when to use it. Kept as an ordered list (not a map) so prompt order is
// deterministic and deliberate.
type StatusDef struct {
	Name        string `yaml:"name"`
	Description string `yaml:"description"`
}

type AgentConfig struct {
	Statuses     []StatusDef       `yaml:"statuses"`
	JiraStatusOn map[string]string `yaml:"jira_status_on"`
	// NudgePrompt is the message sent into the agent's existing terminal
	// when its ticket lands back on a status mapped to this agent (instead
	// of spawning a fresh session). Supports {{ticket}} and {{status}}
	// placeholders. Defaults to DefaultNudgePrompt when empty.
	NudgePrompt string `yaml:"nudge_prompt"`
}

// DefaultNudgePrompt is used when an agent declares no nudge_prompt.
const DefaultNudgePrompt = "Ticket {{ticket}} is assigned to you again (Jira status: {{status}}). Run `acli jira workitem view {{ticket}} --fields summary,description,comment --json` to read the latest feedback, then continue working on it. End your reply with the STATUS/SUMMARY block as before."

// StatusNames returns the agent's allowed internal status names, in
// declared order.
func (a AgentConfig) StatusNames() []string {
	names := make([]string, len(a.Statuses))
	for i, s := range a.Statuses {
		names[i] = s.Name
	}
	return names
}

// HasStatus reports whether name is one of this agent's declared statuses.
func (a AgentConfig) HasStatus(name string) bool {
	for _, s := range a.Statuses {
		if s.Name == name {
			return true
		}
	}
	return false
}

// IssueTypeWorkflow is one issue type's full workflow: which agent handles
// each Jira status, and that issue type's own agent definitions. Agents
// are scoped per issue type, not shared globally — two issue types can
// each define an agent named "plan" with entirely different
// jira_status_on mappings, since the same real `opencode --agent plan`
// executable is just invoked with different status-transition rules
// depending on which issue type's workflow dispatched it.
type IssueTypeWorkflow struct {
	Statuses map[string]string      `yaml:"statuses"`
	Agents   map[string]AgentConfig `yaml:"agents"`
	// CloseOnStatuses lists Jira statuses at which the daemon closes all
	// of the ticket's agent terminals (e.g. ["Done"]). Statuses NOT
	// listed here and not mapped to an agent leave terminals untouched —
	// so a ticket sitting in "In Review" keeps its sessions alive and a
	// bounce back to a mapped status nudges the same session instead of
	// respawning.
	CloseOnStatuses []string `yaml:"close_on_statuses"`
}

// ShouldCloseTerminals reports whether jiraStatus is one of the issue
// type's close_on_statuses (case-insensitive).
func (c *Config) ShouldCloseTerminals(issueType, jiraStatus string) bool {
	for _, s := range c.Workflows[issueType].CloseOnStatuses {
		if strings.EqualFold(s, jiraStatus) {
			return true
		}
	}
	return false
}

type Config struct {
	// JQL is the base query; the component filter (derived from the
	// current repo's displayName) and the workflow-claim label exclusion
	// are appended automatically at runtime — never written by hand.
	JQL                 string `yaml:"jql"`
	PollIntervalSeconds int    `yaml:"poll_interval_seconds"`
	// Workflows maps issueType -> its statuses->agent map plus its own
	// agent definitions. This is the sole basis for deciding which agent
	// handles a ticket and what it may transition to.
	Workflows map[string]IssueTypeWorkflow `yaml:"workflows"`
}

// AgentForStatus returns which agent should be invoked for a ticket's
// issue type + current Jira status (matched case-insensitively), per the
// explicit workflows mapping.
func (c *Config) AgentForStatus(issueType, jiraStatus string) (string, bool) {
	for status, agent := range c.Workflows[issueType].Statuses {
		if strings.EqualFold(status, jiraStatus) {
			return agent, true
		}
	}
	return "", false
}

// AgentConfigFor returns the named agent's config, scoped to issueType.
func (c *Config) AgentConfigFor(issueType, agentName string) (AgentConfig, bool) {
	agentCfg, ok := c.Workflows[issueType].Agents[agentName]
	return agentCfg, ok
}

// Validate cross-checks every reference in Workflows against that issue
// type's own Agents and every declared status against jira_status_on, so
// a broken config fails fast at load time instead of silently dropping
// reports or transitioning a ticket to an empty Jira status at runtime.
func (c *Config) Validate() error {
	if strings.Contains(strings.ToUpper(c.JQL), "ORDER BY") {
		return fmt.Errorf("jql must not contain ORDER BY; it is always ordered by updated automatically")
	}
	for issueType, wf := range c.Workflows {
		for agentName := range wf.Agents {
			a := wf.Agents[agentName]
			if a.NudgePrompt == "" {
				a.NudgePrompt = DefaultNudgePrompt
				wf.Agents[agentName] = a
			}
		}
		for jiraStatus, agentName := range wf.Statuses {
			agentCfg, ok := wf.Agents[agentName]
			if !ok {
				return fmt.Errorf("workflows[%s].statuses[%s]: agent %q not defined in workflows[%s].agents", issueType, jiraStatus, agentName, issueType)
			}
			if len(agentCfg.Statuses) == 0 {
				return fmt.Errorf("workflows[%s].agents[%s]: statuses must not be empty", issueType, agentName)
			}
			for _, status := range agentCfg.Statuses {
				jiraTarget, ok := agentCfg.JiraStatusOn[status.Name]
				if !ok || jiraTarget == "" {
					return fmt.Errorf("workflows[%s].agents[%s]: status %q has no non-empty jira_status_on mapping", issueType, agentName, status.Name)
				}
			}
		}
	}
	return nil
}

func Load(path string) (*Config, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read config %s: %w", path, err)
	}
	var c Config
	if err := yaml.Unmarshal(b, &c); err != nil {
		return nil, fmt.Errorf("parse config %s: %w", path, err)
	}
	if c.JQL == "" {
		return nil, fmt.Errorf("config %s: jql must not be empty", path)
	}
	if c.PollIntervalSeconds <= 0 {
		c.PollIntervalSeconds = 15
	}
	if err := c.Validate(); err != nil {
		return nil, fmt.Errorf("config %s: %w", path, err)
	}
	return &c, nil
}
