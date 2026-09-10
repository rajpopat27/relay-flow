// Package repo owns registered repos: the in-memory Repo, the derived
// repo-to-workflow index, registration/removal, and per-repo pollers.
package repo

import (
	"fmt"
	"sort"
	"sync"

	"github.com/rajpopat27/relay-flow/internal/config"
	"github.com/rajpopat27/relay-flow/internal/task"
	"github.com/rajpopat27/relay-flow/internal/workflow"
)

// WorkflowBinding pairs a workflow with its compiled ticket matcher for one
// repo. Repo.Workflows is a derived in-memory index rebuilt at startup and
// after workflow submission/removal; Workflow.Repos is the source of truth.
type WorkflowBinding struct {
	Workflow *workflow.Workflow
	Match    func(task.Ticket) bool
}

type Info struct {
	Name       string           `json:"name"`
	Path       string           `json:"path"`
	TaskConfig config.RawValues `json:"taskConfig,omitempty"`
}

type Repo struct {
	Name       string
	Path       string
	TaskConfig config.RawValues
	TaskSystem task.System
	Workflows  []WorkflowBinding
	bindingsMu sync.RWMutex
}

func (r *Repo) Info() Info {
	return Info{Name: r.Name, Path: r.Path, TaskConfig: r.TaskConfig}
}

// Bindings returns a snapshot of the repo's currently published workflow
// bindings. WorkflowBinding values are immutable after publication.
func (r *Repo) Bindings() []WorkflowBinding {
	r.bindingsMu.RLock()
	bindings := make([]WorkflowBinding, len(r.Workflows))
	copy(bindings, r.Workflows)
	r.bindingsMu.RUnlock()
	return bindings
}

// Registry is the in-memory repo set.
type Registry struct {
	mu   sync.RWMutex
	byID map[string]*Repo
}

func NewRegistry() *Registry {
	return &Registry{byID: map[string]*Repo{}}
}

func (r *Registry) Get(name string) (*Repo, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	rp, ok := r.byID[name]
	return rp, ok
}

func (r *Registry) List() []*Repo {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]*Repo, 0, len(r.byID))
	for _, rp := range r.byID {
		out = append(out, rp)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

func (r *Registry) Replace(repo *Repo) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.byID == nil {
		r.byID = map[string]*Repo{}
	}
	r.byID[repo.Name] = repo
}

func (r *Registry) Remove(name string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.byID, name)
}

// BindWorkflows rebuilds the derived Repo.Workflows index from the given
// workflows. Each repo that a workflow lists gets a binding with the
// matcher compiled by that repo's task system. Repos not listed by a
// workflow keep no binding for it.
func (r *Registry) BindWorkflows(workflows []*workflow.Workflow) error {
	type binding struct {
		wf    *workflow.Workflow
		match func(task.Ticket) bool
	}
	byRepo := map[string][]binding{}
	for _, wf := range workflows {
		for _, repoName := range wf.Repos {
			rp, ok := r.Get(repoName)
			if !ok {
				return fmt.Errorf("workflow %q references unregistered repo %q", wf.Name, repoName)
			}
			match, err := rp.TaskSystem.CompileFilter(wf.TaskConfig)
			if err != nil {
				return fmt.Errorf("workflow %q repo %q: compile filter: %w", wf.Name, repoName, err)
			}
			byRepo[repoName] = append(byRepo[repoName], binding{wf: wf, match: match})
		}
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	for name, rp := range r.byID {
		binds := byRepo[name]
		next := make([]WorkflowBinding, 0, len(binds))
		for _, b := range binds {
			next = append(next, WorkflowBinding{Workflow: b.wf, Match: b.match})
		}
		sort.Slice(next, func(i, j int) bool { return next[i].Workflow.Name < next[j].Workflow.Name })
		rp.bindingsMu.Lock()
		rp.Workflows = next
		rp.bindingsMu.Unlock()
	}
	return nil
}
