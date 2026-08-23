package runner

import (
	"fmt"
	"sort"
	"strings"
	"sync"

	"github.com/rajpopat27/relay-flow/internal/config"
)

// Factory constructs a Runner from root runner config. No repo-level runner
// config exists; the runner resolves internal workspace IDs from the
// registered repo name and path.
type Factory func(config.RawValues) (Runner, error)

var (
	registryMu sync.RWMutex
	registry   = map[string]Factory{}
)

// Register adds a runner factory by name. Duplicate registration panics.
func Register(name string, factory Factory) {
	registryMu.Lock()
	defer registryMu.Unlock()
	if _, exists := registry[name]; exists {
		panic(fmt.Sprintf("runner: duplicate registration of %q", name))
	}
	registry[name] = factory
}

// New constructs the named runner.
func New(name string, cfg config.RawValues) (Runner, error) {
	registryMu.RLock()
	f, ok := registry[name]
	registryMu.RUnlock()
	if !ok {
		return nil, fmt.Errorf("runner: unknown plugin %q (registered: %s)", name, strings.Join(Names(), ", "))
	}
	return f(cfg)
}

// Names returns the registered plugin names sorted.
func Names() []string {
	registryMu.RLock()
	defer registryMu.RUnlock()
	out := make([]string, 0, len(registry))
	for name := range registry {
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}
