package workflow

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"github.com/rajpopat27/relay-flow/internal/config"
)

// Store persists workflow definitions as YAML files under Dir. The workflow
// file is the durable definition.
type Store struct {
	Dir string
}

func (s *Store) path(name string) string {
	return filepath.Join(s.Dir, name+".yaml")
}

// LoadAll reads and parses every stored workflow file.
func (s *Store) LoadAll() ([]*Workflow, error) {
	entries, err := os.ReadDir(s.Dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("read workflow dir %s: %w", s.Dir, err)
	}
	var out []*Workflow
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".yaml") {
			continue
		}
		name := strings.TrimSuffix(e.Name(), ".yaml")
		wf, err := s.Get(name)
		if err != nil {
			return nil, err
		}
		out = append(out, wf)
	}
	return out, nil
}

// Get reads and parses one stored workflow by name.
func (s *Store) Get(name string) (*Workflow, error) {
	raw, err := os.ReadFile(s.path(name))
	if err != nil {
		return nil, fmt.Errorf("read workflow %q: %w", name, err)
	}
	return Parse(name, raw)
}

// Put atomically writes the workflow file with mode 0644.
func (s *Store) Put(name string, yamlBytes []byte) error {
	if err := os.MkdirAll(s.Dir, 0o755); err != nil {
		return fmt.Errorf("create workflow dir %s: %w", s.Dir, err)
	}
	if err := config.WriteAtomic(s.path(name), yamlBytes, 0o644); err != nil {
		return fmt.Errorf("store workflow %q: %w", name, err)
	}
	return nil
}

// Remove deletes the workflow file.
func (s *Store) Remove(name string) error {
	if err := os.Remove(s.path(name)); err != nil {
		return fmt.Errorf("remove workflow %q: %w", name, err)
	}
	return nil
}

// Registry is the in-memory workflow set. The registry has no repo index;
// repo bindings are the only derived repo-to-workflow index.
type Registry struct {
	mu   sync.RWMutex
	byID map[string]*Workflow
}

func (r *Registry) get(name string) (*Workflow, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	wf, ok := r.byID[name]
	return wf, ok
}

// Get returns the workflow by name.
func (r *Registry) Get(name string) (*Workflow, bool) {
	return r.get(name)
}

// List returns all registered workflows sorted by name.
func (r *Registry) List() []*Workflow {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]*Workflow, 0, len(r.byID))
	for _, wf := range r.byID {
		out = append(out, wf)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// ReferencesRepo reports whether any registered workflow lists the repo.
func (r *Registry) ReferencesRepo(repo string) bool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	for _, wf := range r.byID {
		for _, rr := range wf.Repos {
			if rr == repo {
				return true
			}
		}
	}
	return false
}

// Replace creates or replaces the workflow in the registry.
func (r *Registry) Replace(wf *Workflow) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.byID == nil {
		r.byID = map[string]*Workflow{}
	}
	r.byID[wf.Name] = wf
}

// Remove deletes the workflow from the registry.
func (r *Registry) Remove(name string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.byID, name)
}
