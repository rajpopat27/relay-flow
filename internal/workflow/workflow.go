// Package workflow defines workflow values, strict parsing, validation,
// structured reports, storage, and the workflow service.
package workflow

import (
	"bytes"
	"fmt"
	"regexp"
	"sort"

	"gopkg.in/yaml.v3"

	"github.com/rajpopat27/relay-flow/internal/config"
)

type NodeType string

const (
	NodeAgent NodeType = "agent"
	NodeHITL  NodeType = "hitl"
)

type Outcome string

const (
	OutcomeSuccess Outcome = "success"
	OutcomeFailure Outcome = "failure"
)

// Reserved lifecycle node names. The word "terminal" is runner-only.
const (
	StartNode = "start"
	EndNode   = "end"
)

type Workflow struct {
	Name               string           `yaml:"name" json:"name"`
	Repos              []string         `yaml:"repos" json:"repos"`
	CleanupRunnerOnEnd bool             `yaml:"cleanupRunnerOnEnd" json:"cleanupRunnerOnEnd"`
	TaskConfig         config.RawValues `yaml:"taskConfig,omitempty" json:"taskConfig,omitempty"`
	Nodes              map[string]Node  `yaml:"nodes" json:"nodes"`
}

type Node struct {
	Type        NodeType         `yaml:"type,omitempty" json:"type,omitempty"`
	Agent       string           `yaml:"agent,omitempty" json:"agent,omitempty"`
	Description string           `yaml:"description,omitempty" json:"description,omitempty"`
	NudgePrompt string           `yaml:"nudgePrompt,omitempty" json:"nudgePrompt,omitempty"`
	TaskConfig  config.RawValues `yaml:"taskConfig,omitempty" json:"taskConfig,omitempty"`
	OnSuccess   []Route          `yaml:"onSuccess,omitempty" json:"onSuccess,omitempty"`
	OnFailure   []Route          `yaml:"onFailure,omitempty" json:"onFailure,omitempty"`
}

type Route struct {
	Target string `yaml:"target" json:"target"`
	When   string `yaml:"when,omitempty" json:"when,omitempty"`
}

type NudgeTemplateData struct {
	Ticket    string
	Workflow  string
	Repo      string
	Node      string
	NextSteps string
}

var namePattern = regexp.MustCompile(`^[a-z][a-zA-Z0-9]*$`)

var nudgeVarPattern = regexp.MustCompile(`\{\{([^{}]*)\}\}`)

var knownNudgeVars = map[string]bool{
	"ticket": true, "workflow": true, "repo": true, "node": true, "nextSteps": true,
}

// Parse strictly decodes a workflow YAML document. Unknown fields and
// explicit null values inside any taskConfig are rejected. The returned
// workflow is not yet validated.
func Parse(name string, yamlBytes []byte) (*Workflow, error) {
	var raw map[string]any
	if err := yaml.Unmarshal(yamlBytes, &raw); err != nil {
		return nil, fmt.Errorf("parse workflow %q: %w", name, err)
	}
	var wf Workflow
	dec := yaml.NewDecoder(bytes.NewReader(yamlBytes))
	dec.KnownFields(true)
	if err := dec.Decode(&wf); err != nil {
		return nil, fmt.Errorf("parse workflow %q: %w", name, err)
	}
	if wf.Nodes == nil {
		wf.Nodes = map[string]Node{}
	}
	if v, present := raw["taskConfig"]; present && v == nil {
		return nil, fmt.Errorf("parse workflow %q: taskConfig: explicit null is not allowed", name)
	}
	if err := rejectNullValues(wf.TaskConfig, "taskConfig"); err != nil {
		return nil, fmt.Errorf("parse workflow %q: %w", name, err)
	}
	rawNodes, _ := raw["nodes"].(map[string]any)
	for nodeName, n := range wf.Nodes {
		if rn, ok := rawNodes[nodeName].(map[string]any); ok {
			if v, present := rn["taskConfig"]; present && v == nil {
				return nil, fmt.Errorf("parse workflow %q node %q: taskConfig: explicit null is not allowed", name, nodeName)
			}
		}
		if err := rejectNullValues(n.TaskConfig, "nodes."+nodeName+".taskConfig"); err != nil {
			return nil, fmt.Errorf("parse workflow %q: %w", name, err)
		}
	}
	return &wf, nil
}

// rejectNullValues recursively rejects explicit YAML nulls in raw task
// config values.
func rejectNullValues(values map[string]any, path string) error {
	for k, v := range values {
		key := path + "." + k
		if err := rejectNullValue(v, key); err != nil {
			return err
		}
	}
	return nil
}

func rejectNullValue(v any, key string) error {
	if v == nil {
		return fmt.Errorf("config key %q: explicit null is not allowed", key)
	}
	switch t := v.(type) {
	case map[string]any:
		return rejectNullValues(t, key)
	case []any:
		for i, elem := range t {
			if err := rejectNullValue(elem, fmt.Sprintf("%s[%d]", key, i)); err != nil {
				return err
			}
		}
	}
	return nil
}

// Validate checks the workflow graph, identity, repos, nodes, routes, and
// nudge templates against the workflow-definition spec.
func (w *Workflow) Validate() error {
	if !namePattern.MatchString(w.Name) {
		return fmt.Errorf("workflow name %q must be lower-camel alphanumeric beginning with a lowercase letter", w.Name)
	}
	if len(w.Repos) == 0 {
		return fmt.Errorf("workflow %q must list at least one repo", w.Name)
	}
	seen := map[string]bool{}
	for _, r := range w.Repos {
		if seen[r] {
			return fmt.Errorf("workflow %q lists duplicate repo %q", w.Name, r)
		}
		seen[r] = true
	}

	start, ok := w.Nodes[StartNode]
	if !ok {
		return fmt.Errorf("workflow %q must contain a %q node", w.Name, StartNode)
	}
	if err := validateStart(start); err != nil {
		return fmt.Errorf("workflow %q start: %w", w.Name, err)
	}
	end, ok := w.Nodes[EndNode]
	if !ok {
		return fmt.Errorf("workflow %q must contain an %q node", w.Name, EndNode)
	}
	if err := validateEnd(end); err != nil {
		return fmt.Errorf("workflow %q end: %w", w.Name, err)
	}

	for name, n := range w.Nodes {
		if name == StartNode || name == EndNode {
			continue
		}
		if err := validateWorkNode(w.Name, name, n); err != nil {
			return err
		}
	}
	for name, n := range w.Nodes {
		for _, r := range allRoutes(n) {
			if r.Target == StartNode {
				return fmt.Errorf("workflow %q node %q: routes cannot target reserved %q", w.Name, name, StartNode)
			}
			if _, ok := w.Nodes[r.Target]; !ok {
				return fmt.Errorf("workflow %q node %q: route target %q does not exist", w.Name, name, r.Target)
			}
		}
	}
	if err := w.validateReachability(); err != nil {
		return err
	}
	return nil
}

func validateStart(n Node) error {
	if n.Type != "" || n.Agent != "" || n.Description != "" || n.NudgePrompt != "" {
		return fmt.Errorf("start must not declare type, agent, description, or nudgePrompt")
	}
	if len(n.OnFailure) != 0 {
		return fmt.Errorf("start must not declare failure routes")
	}
	if len(n.OnSuccess) != 1 {
		return fmt.Errorf("start must declare exactly one success target, got %d", len(n.OnSuccess))
	}
	return nil
}

func validateEnd(n Node) error {
	if n.Type != "" || n.Agent != "" || n.Description != "" || n.NudgePrompt != "" {
		return fmt.Errorf("end must not declare type, agent, description, or nudgePrompt")
	}
	if len(n.OnSuccess) != 0 || len(n.OnFailure) != 0 {
		return fmt.Errorf("end must not declare routes")
	}
	return nil
}

func validateWorkNode(wfName, name string, n Node) error {
	if n.Type != NodeAgent && n.Type != NodeHITL {
		return fmt.Errorf("workflow %q node %q: type must be %q or %q", wfName, name, NodeAgent, NodeHITL)
	}
	if n.Agent == "" {
		return fmt.Errorf("workflow %q node %q: agent is required", wfName, name)
	}
	if n.Description == "" {
		return fmt.Errorf("workflow %q node %q: description is required", wfName, name)
	}
	if len(n.OnSuccess) == 0 {
		return fmt.Errorf("workflow %q node %q: at least one success route is required", wfName, name)
	}
	if len(n.OnFailure) == 0 {
		return fmt.Errorf("workflow %q node %q: at least one failure route is required", wfName, name)
	}
	for _, m := range nudgeVarPattern.FindAllStringSubmatch(n.NudgePrompt, -1) {
		if !knownNudgeVars[m[1]] {
			return fmt.Errorf("workflow %q node %q: unknown nudge template variable {{%s}}", wfName, name, m[1])
		}
	}
	return nil
}

func allRoutes(n Node) []Route {
	out := make([]Route, 0, len(n.OnSuccess)+len(n.OnFailure))
	out = append(out, n.OnSuccess...)
	out = append(out, n.OnFailure...)
	return out
}

// validateReachability ensures every work node is reachable from start and
// that end is reachable. Backward edges and self-loops are allowed.
func (w *Workflow) validateReachability() error {
	adj := map[string][]string{}
	for name, n := range w.Nodes {
		for _, r := range allRoutes(n) {
			adj[name] = append(adj[name], r.Target)
		}
	}
	visited := map[string]bool{}
	queue := []string{StartNode}
	for len(queue) > 0 {
		cur := queue[0]
		queue = queue[1:]
		if visited[cur] {
			continue
		}
		visited[cur] = true
		queue = append(queue, adj[cur]...)
	}
	for name := range w.Nodes {
		if !visited[name] {
			return fmt.Errorf("workflow %q node %q is not reachable from %q", w.Name, name, StartNode)
		}
	}
	return nil
}

// StartTarget returns the single success target of the start node.
func (w *Workflow) StartTarget() (string, error) {
	start, ok := w.Nodes[StartNode]
	if !ok {
		return "", fmt.Errorf("workflow %q has no %q node", w.Name, StartNode)
	}
	if len(start.OnSuccess) != 1 {
		return "", fmt.Errorf("workflow %q start must have exactly one success target, got %d", w.Name, len(start.OnSuccess))
	}
	return start.OnSuccess[0].Target, nil
}

// Routes returns the configured routes for a node and outcome.
func (w *Workflow) Routes(node string, outcome Outcome) ([]Route, error) {
	n, ok := w.Nodes[node]
	if !ok {
		return nil, fmt.Errorf("workflow %q has no node %q", w.Name, node)
	}
	switch outcome {
	case OutcomeSuccess:
		return n.OnSuccess, nil
	case OutcomeFailure:
		return n.OnFailure, nil
	default:
		return nil, fmt.Errorf("workflow %q node %q: unknown outcome %q", w.Name, node, outcome)
	}
}

// RenderNudge renders the node's nudge template with the supported
// variables. Nodes without a nudge template use the default nudge.
func (w *Workflow) RenderNudge(node string, data NudgeTemplateData) (string, error) {
	n, ok := w.Nodes[node]
	if !ok {
		return "", fmt.Errorf("workflow %q has no node %q", w.Name, node)
	}
	tmpl := n.NudgePrompt
	if tmpl == "" {
		tmpl = defaultNudgePrompt
	}
	out := nudgeVarPattern.ReplaceAllStringFunc(tmpl, func(m string) string {
		varName := nudgeVarPattern.FindStringSubmatch(m)[1]
		switch varName {
		case "ticket":
			return data.Ticket
		case "workflow":
			return data.Workflow
		case "repo":
			return data.Repo
		case "node":
			return data.Node
		case "nextSteps":
			return data.NextSteps
		}
		return m
	})
	return out, nil
}

const defaultNudgePrompt = "Your last message did not contain a complete, valid report. " +
	"Reply with the full report contract for node {{node}} (ticket {{ticket}}). " +
	"Valid next steps: {{nextSteps}}."

// sortedTargets is a helper for deterministic error messages.
func sortedTargets(routes []Route) []string {
	out := make([]string, 0, len(routes))
	for _, r := range routes {
		out = append(out, r.Target)
	}
	sort.Strings(out)
	return out
}
