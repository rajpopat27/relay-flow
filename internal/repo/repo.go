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
	// TaskSystemError is set when local startup construction could not create
	// a usable adapter. The repo remains visible for diagnostics and repair,
	// while only workflows referencing it are isolated.
	TaskSystemError error
	Workflows       []WorkflowBinding
	bindingsMu      sync.RWMutex
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
// workflow keep no binding for it. The strict form is used by submission,
// where a binding error must reject the candidate before it is stored.
func (r *Registry) BindWorkflows(workflows []*workflow.Workflow) error {
	_, err := r.bindWorkflows(workflows, false)
	return err
}

// BindingIssue identifies a workflow that could not be safely published.
// Startup uses the isolated form so one invalid workflow does not prevent
// unrelated workflows from being bound.
type BindingIssue struct {
	Workflow *workflow.Workflow
	Error    error
}

// BindWorkflowsIsolated rebuilds all bindings while isolating workflows whose
// referenced repo or matcher cannot be built. The returned issues are
// diagnostics only; valid workflows are still published atomically.
func (r *Registry) BindWorkflowsIsolated(workflows []*workflow.Workflow) []BindingIssue {
	issues, _ := r.bindWorkflows(workflows, true)
	return issues
}

func (r *Registry) bindWorkflows(workflows []*workflow.Workflow, isolate bool) ([]BindingIssue, error) {
	type binding struct {
		wf    *workflow.Workflow
		match func(task.Ticket) bool
	}
	byRepo := map[string][]binding{}
	issues := []BindingIssue{}
	for _, wf := range workflows {
		if wf == nil || !wf.IsRoutable() {
			continue
		}
		failed := false
		for _, repoName := range wf.Repos {
			rp, ok := r.Get(repoName)
			if !ok {
				err := fmt.Errorf("workflow %q references unregistered repo %q", wf.Name, repoName)
				if !isolate {
					return nil, err
				}
				wf.MarkBlocked(err.Error(), wf.RepairCommand)
				issues = append(issues, BindingIssue{Workflow: wf, Error: err})
				failed = true
				break
			}
			if rp.TaskSystem == nil {
				reason := rp.TaskSystemError
				if reason == nil {
					reason = fmt.Errorf("repo %q task system is unavailable", repoName)
				}
				err := fmt.Errorf("workflow %q repo %q: task system unavailable: %w", wf.Name, repoName, reason)
				if !isolate {
					return nil, err
				}
				wf.MarkBlocked(err.Error(), wf.RepairCommand)
				issues = append(issues, BindingIssue{Workflow: wf, Error: err})
				failed = true
				break
			}
			match, err := rp.TaskSystem.CompileFilter(wf.TaskConfig)
			if err != nil {
				err = fmt.Errorf("workflow %q repo %q: compile filter: %w", wf.Name, repoName, err)
				if !isolate {
					return nil, err
				}
				wf.MarkBlocked(err.Error(), wf.RepairCommand)
				issues = append(issues, BindingIssue{Workflow: wf, Error: err})
				failed = true
				break
			}
			byRepo[repoName] = append(byRepo[repoName], binding{wf: wf, match: match})
		}
		if failed {
			// A workflow is all-or-nothing across its referenced repositories;
			// never leave a partial route for it.
			for repoName, binds := range byRepo {
				filtered := binds[:0]
				for _, b := range binds {
					if b.wf != wf {
						filtered = append(filtered, b)
					}
				}
				if len(filtered) == 0 {
					delete(byRepo, repoName)
				} else {
					byRepo[repoName] = filtered
				}
			}
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
	return issues, nil
}
