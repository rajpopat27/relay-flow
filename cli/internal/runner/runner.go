// Package runner defines the execution-backend plug point. Orca ships
// built in (internal/runner/orca); tmux or others register the same way.
// A Runner knows how to spawn agent sessions, re-find them (bounce),
// nudge them, and tear them down — nothing about trackers.
package runner

import (
	"fmt"
	"sort"
	"sync"

	"github.com/rajpopat27/relayflow/cli/internal/tasks"
)

// Session is a live agent terminal/process handle.
type Session struct {
	ID    string
	Title string
}

// Runner is the execution-backend interface.
type Runner interface {
	// Spawn creates (or ensures) the ticket's worktree/session titled
	// "<key>:<agent>:<node>", injects env, and sends the initial prompt.
	Spawn(t tasks.Ticket, node, agent, prompt string, env map[string]string) error
	// Find locates the existing session for this ticket+node (bounce).
	Find(t tasks.Ticket, node string) (Session, bool, error)
	// Nudge sends a prompt into an existing session.
	Nudge(s Session, prompt string) error
	// Close tears down all of the ticket's sessions (terminal node).
	Close(t tasks.Ticket) error
}

// Factory builds a runner instance. runner.config is decoded by
// UnmarshalConfig (strict), then passed to New.
type Factory struct {
	UnmarshalConfig func(map[string]any) (any, error)
	New             func(cfg any) (Runner, error)
}

var (
	mu        sync.RWMutex
	factories = map[string]Factory{}
)

// Register makes a runner available under `type: <name>`. Panics on
// duplicate registration (programmer error).
func Register(name string, f Factory) {
	mu.Lock()
	defer mu.Unlock()
	if _, dup := factories[name]; dup {
		panic("runner: duplicate registration " + name)
	}
	factories[name] = f
}

// New resolves a runner by type name and builds an instance.
func New(typeName string, rawCfg map[string]any) (Runner, error) {
	mu.RLock()
	f, ok := factories[typeName]
	mu.RUnlock()
	if !ok {
		return nil, fmt.Errorf("unknown runner type %q (registered: %v)", typeName, registered())
	}
	cfg, err := f.UnmarshalConfig(rawCfg)
	if err != nil {
		return nil, fmt.Errorf("runner type %q config: %w", typeName, err)
	}
	return f.New(cfg)
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
