// Package config loads .workflow/<config-name>.yaml.
package config

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
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

type AgentConfig struct {
	// Handles is the list of Jira statuses that activate this agent.
	Handles StringList `yaml:"handles"`
	// Outcomes maps each agent-reported status to the next Jira status.
	// Status declaration order follows the workflow author's outcome order.
	Outcomes map[string]string `yaml:"outcomes"`
	// NudgePrompt is the message sent into the agent's existing terminal
	// when its ticket lands back on a status handled by this agent (instead
	// of spawning a fresh session). Supports {{ticket}} and {{status}}
	// placeholders. Defaults to DefaultNudgePrompt when empty.
	NudgePrompt string `yaml:"nudgePrompt"`
}

// DefaultNudgePrompt is used when an agent declares no nudgePrompt.
const DefaultNudgePrompt = "Ticket {{ticket}} is assigned to you again (Jira status: {{status}}). Run `acli jira workitem view {{ticket}} --fields summary,description,comment --json` to read the latest feedback, then continue working on it. End your reply with the STATUS/SUMMARY block as before."

// StatusNames returns the agent's allowed internal status names, sorted so
// prompt and validation messages are deterministic.
func (a AgentConfig) StatusNames() []string {
	names := make([]string, 0, len(a.Outcomes))
	for name := range a.Outcomes {
		names = append(names, name)
	}
	return names
}

// HasStatus reports whether name is one of this agent's allowed outcomes.
func (a AgentConfig) HasStatus(name string) bool {
	_, ok := a.Outcomes[name]
	return ok
}

// Workflow is one JQL-selected ticket workflow: which agents handle each
// Jira status and which statuses end terminal lifetime.
type Workflow struct {
	// JQL is the workflow's base query; the component filter (derived from
	// the current repo's displayName) and a fixed ordering are appended
	// automatically at runtime — never written by hand.
	JQL string `yaml:"jql"`
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
	PollIntervalSeconds int `yaml:"pollIntervalSeconds"`
	// Workflows maps workflow IDs to their full workflow definition. IDs
	// must be camelCase and contain no spaces because they are used in CLI
	// arguments, env vars, labels, and log/pid paths.
	Workflows map[string]Workflow `yaml:"workflows"`
}

// AgentForStatus returns which agent should be invoked for a ticket's
// current Jira status in the named workflow.
func (c *Config) AgentForStatus(workflowName, jiraStatus string) (string, bool) {
	for agentName, agent := range c.Workflows[workflowName].Agents {
		if agent.Handles.Has(jiraStatus) {
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

// Validate cross-checks every status reference and detects duplicate
// handles within each workflow, so broken configs fail at load time.
func (c *Config) Validate() error {
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
			if len(a.Outcomes) == 0 {
				return fmt.Errorf("workflows[%s].agents[%s].outcomes must not be empty", workflowName, agentName)
			}
			for _, status := range a.Handles {
				key := strings.ToLower(strings.TrimSpace(status))
				if key == "" {
					return fmt.Errorf("workflows[%s].agents[%s].handles must not contain empty statuses", workflowName, agentName)
				}
				if other, ok := seenHandles[key]; ok && other != agentName {
					return fmt.Errorf("workflows[%s].agents[%s].handles[%s] duplicates workflows[%s].agents[%s]", workflowName, agentName, status, workflowName, other)
				}
				seenHandles[key] = agentName
			}
			for statusName, target := range a.Outcomes {
				if strings.TrimSpace(statusName) == "" || strings.TrimSpace(target) == "" {
					return fmt.Errorf("workflows[%s].agents[%s].outcomes must not contain empty statuses or targets", workflowName, agentName)
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

// SavedPath returns the server's saved copy location for a config:
// ~/.orca-jira-loop/configs/<name>.yaml.
func SavedPath(configName string) (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	dir := filepath.Join(home, ".orca-jira-loop", "configs")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	return filepath.Join(dir, configName+".yaml"), nil
}

// LoadWithFallback loads .workflow/<name>.yaml from cwd; if missing, falls
// back to the server's saved copy at SavedPath(name). Used by `report`,
// whose cwd is a ticket worktree that may not carry the YAML.
func LoadWithFallback(configName string) (*Config, error) {
	cwdPath := filepath.Join(".workflow", configName+".yaml")
	if _, err := os.Stat(cwdPath); err == nil {
		return Load(cwdPath)
	}
	saved, err := SavedPath(configName)
	if err != nil {
		return nil, err
	}
	if _, err := os.Stat(saved); err != nil {
		return nil, fmt.Errorf("config %q not found at %s or %s", configName, cwdPath, saved)
	}
	return Load(saved)
}
