// Package tasks defines the ticket-system plug point. Jira ships built in
// (internal/tasks/jira); beads/linear/github adapters register the same
// way via init() — the database/sql driver pattern: import to register,
// then resolve by type name from the workflow YAML's `tasks.type`.
package tasks

import (
	"fmt"
	"sort"
	"sync"

	"relayflow/internal/config"
)

// Ticket is one unit of work as seen by the daemon. Adapters fill Node by
// reverse-mapping the ticket's tracker state through the workflow's node
// `when` values ("" = unmapped state), and ClaimedBy from claim labels
// ("" = unclaimed).
type Ticket struct {
	Key       string
	Summary   string
	Node      string
	ClaimedBy string
}

// Tasks is the tracker adapter interface: list candidate tickets, claim
// one, report an outcome. One List call per poll cycle per workflow.
type Tasks interface {
	List() ([]Ticket, error)
	Claim(t Ticket) error
	// Report records the agent outcome: transitions the ticket to the
	// target node's tracker state (adapter resolves it) and posts the
	// summary. Self-loop (target state == current) → comment only.
	Report(t Ticket, outcome, targetNode, summary string) error
}

// Factory builds an adapter instance for one submitted workflow.
type Factory struct {
	// UnmarshalConfig strictly decodes the YAML's tasks.config map into
	// the adapter's own config struct.
	UnmarshalConfig func(map[string]any) (any, error)
	// New builds the adapter. wfName is the workflow identity (claim
	// labels); nodes carry the `when` state map; assignee is the machine
	// user's tracker identity ("" when the adapter doesn't need it);
	// repoName is the repo's display name (tracker-side component/label
	// scoping; "" when the adapter doesn't scope by repo).
	New func(cfg any, wfName string, nodes map[string]config.Node, assignee, repoName string) (Tasks, error)
}

var (
	mu        sync.RWMutex
	factories = map[string]Factory{}
)

// Register makes an adapter available under `type: <name>`. Called from
// adapter init(); panics on duplicate names (programmer error).
func Register(name string, f Factory) {
	mu.Lock()
	defer mu.Unlock()
	if _, dup := factories[name]; dup {
		panic("tasks: duplicate adapter registration " + name)
	}
	factories[name] = f
}

// New resolves a tasks adapter by type name and builds an instance.
func New(typeName string, rawCfg map[string]any, wfName string, nodes map[string]config.Node, assignee, repoName string) (Tasks, error) {
	mu.RLock()
	f, ok := factories[typeName]
	mu.RUnlock()
	if !ok {
		return nil, fmt.Errorf("unknown tasks type %q (registered: %v)", typeName, registered())
	}
	cfg, err := f.UnmarshalConfig(rawCfg)
	if err != nil {
		return nil, fmt.Errorf("tasks type %q config: %w", typeName, err)
	}
	return f.New(cfg, wfName, nodes, assignee, repoName)
}

func registered() []string {
	mu.RLock()
	defer mu.RUnlock()
	names := make([]string, 0, len(factories))
	for n := range factories {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}
