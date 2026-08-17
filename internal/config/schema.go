// Package config loads and validates relay-flow workflow YAML files.
package config

import (
	"bytes"
	"fmt"
	"os"
	"regexp"
	"strings"

	"gopkg.in/yaml.v3"
)

// StringList accepts either a scalar or a list of strings in YAML and
// normalizes both forms to []string, so a single value can be written as
// `closeOn: done` instead of `closeOn: [done]`.
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

// Has reports membership (case-insensitive).
func (s StringList) Has(name string) bool {
	for _, value := range s {
		if strings.EqualFold(value, name) {
			return true
		}
	}
	return false
}

// AdapterSpec selects a pluggable adapter (tasks or runner) by type name.
// Config is opaque to core: the adapter's factory unmarshals it strictly.
type AdapterSpec struct {
	Type   string         `yaml:"type"`
	Config map[string]any `yaml:"config"`
}

// Node is one square on the board: a tracker state (When), the agent that
// works tickets in that state, and the outcome edges to other nodes.
type Node struct {
	// Agent is the OpenCode agent serving this node. Empty = terminal node
	// (no agent runs; must be listed in closeOn).
	Agent string `yaml:"agent"`
	// When is the tracker state string that routes tickets to this node
	// (poll-time condition). Unique across the file, case-insensitive.
	When string `yaml:"when"`
	// OnSuccess / OnFailure are the outcome edges: node names the ticket
	// moves to when its agent reports success/failure. Self-loops allowed.
	OnSuccess string `yaml:"onSuccess"`
	OnFailure string `yaml:"onFailure"`
	// NudgePrompt is sent into the ticket's existing terminal when it
	// lands back on this node (bounce). Supports {{ticket}} and {{node}}
	// placeholders; defaults to DefaultNudgePrompt.
	NudgePrompt string `yaml:"nudgePrompt"`
}

// DefaultNudgePrompt is used when a node declares no nudgePrompt.
const DefaultNudgePrompt = "Ticket {{ticket}} is at node '{{node}}' again. Read the tracker's latest feedback, continue your work, and end your reply with the STATUS/SUMMARY block as before."

// Config is one workflow file: name, adapters, nodes, closeOn.
type Config struct {
	// Name is the workflow's identity: server registry key, claim-label
	// component (`wf:<name>`), CLI argument. CamelCase, unique per server.
	Name                string `yaml:"name"`
	PollIntervalSeconds int    `yaml:"pollIntervalSeconds"`
	// Tasks selects the ticket-system adapter (jira, beads, ...).
	Tasks AdapterSpec `yaml:"tasks"`
	// Runner selects the execution backend (orca, tmux, ...).
	Runner AdapterSpec `yaml:"runner"`
	// CloseOn lists terminal nodes: tickets reaching them get their
	// terminals closed. Nodes here must have no agent.
	CloseOn StringList `yaml:"closeOn"`
	// Nodes maps node names to their definitions. The graph: each agent
	// node's edges point at other nodes; terminal nodes have no agent.
	Nodes map[string]Node `yaml:"nodes"`
}

var namePattern = regexp.MustCompile(`^[a-z][A-Za-z0-9]*$`)

// NodeForState returns the node whose When matches tracker state
// (case-insensitive), or "" if none.
func (c *Config) NodeForState(state string) string {
	for name, n := range c.Nodes {
		if strings.EqualFold(n.When, state) {
			return name
		}
	}
	return ""
}

// Validate cross-checks the graph so broken configs fail at submit, before
// any goroutine starts.
func (c *Config) Validate() error {
	if !namePattern.MatchString(c.Name) {
		return fmt.Errorf("name %q must be camelCase with no spaces", c.Name)
	}
	if strings.TrimSpace(c.Tasks.Type) == "" {
		return fmt.Errorf("tasks.type must not be empty")
	}
	if strings.TrimSpace(c.Runner.Type) == "" {
		return fmt.Errorf("runner.type must not be empty")
	}
	if len(c.Nodes) == 0 {
		return fmt.Errorf("nodes must not be empty")
	}
	if len(c.CloseOn) == 0 {
		return fmt.Errorf("closeOn must not be empty (terminal nodes that close ticket terminals)")
	}
	for _, co := range c.CloseOn {
		n, ok := c.Nodes[co]
		if !ok {
			return fmt.Errorf("closeOn: unknown node %q", co)
		}
		if n.Agent != "" {
			return fmt.Errorf("closeOn: node %q has an agent; closeOn nodes must be terminal (no agent)", co)
		}
	}
	seenWhen := map[string]string{}
	for name, n := range c.Nodes {
		if strings.TrimSpace(n.When) == "" {
			return fmt.Errorf("nodes[%s].when must not be empty", name)
		}
		key := strings.ToLower(strings.TrimSpace(n.When))
		if other, dup := seenWhen[key]; dup {
			return fmt.Errorf("nodes[%s].when %q duplicates nodes[%s].when", name, n.When, other)
		}
		seenWhen[key] = name
		if n.Agent == "" {
			// Agentless = human gate / pause node: no automation, no
			// edges required. Whether it also closes terminals is
			// controlled solely by closeOn.
			continue
		}
		if n.OnSuccess == "" {
			return fmt.Errorf("nodes[%s].onSuccess must not be empty (agent nodes need both outcome edges)", name)
		}
		if n.OnFailure == "" {
			return fmt.Errorf("nodes[%s].onFailure must not be empty (agent nodes need both outcome edges)", name)
		}
		if _, ok := c.Nodes[n.OnSuccess]; !ok {
			return fmt.Errorf("nodes[%s].onSuccess: unknown node %q", name, n.OnSuccess)
		}
		if _, ok := c.Nodes[n.OnFailure]; !ok {
			return fmt.Errorf("nodes[%s].onFailure: unknown node %q", name, n.OnFailure)
		}
		if n.NudgePrompt == "" {
			n.NudgePrompt = DefaultNudgePrompt
			c.Nodes[name] = n
		}
	}
	return nil
}

// Parse decodes and validates workflow YAML bytes. name is only used in
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

// Load reads and parses a workflow YAML file.
func Load(path string) (*Config, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read config %s: %w", path, err)
	}
	return Parse(path, b)
}
