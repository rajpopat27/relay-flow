package server

import (
	"context"
	"sort"

	"github.com/rajpopat27/relay-flow/internal/run"
	"github.com/rajpopat27/relay-flow/internal/workflow"
)

// WorkflowSummary is a read-only consumer DTO for compact workflow listing.
// The embedded definition keeps the existing JSON fields available while the
// additive fields provide the one-request execution summary used by the CLI.
type WorkflowSummary struct {
	*workflow.Workflow
	Repositories []string `json:"repositories,omitempty"`
	NodeCount    int      `json:"nodeCount"`
	ActiveRuns   int      `json:"activeRuns"`
	LatestRun    *run.Run `json:"latestRun,omitempty"`
}

// WorkflowDetail is a read-only query DTO. It deliberately contains domain
// values only; no executor, database, runner, or task-system implementation
// type crosses the Unix-socket boundary.
type WorkflowDetail struct {
	*workflow.Workflow
	Valid      bool      `json:"valid"`
	ActiveRuns int       `json:"activeRuns"`
	RecentRuns []run.Run `json:"recentRuns"`
}

// WorkflowSummaryQueries is an optional server capability. Keeping it
// separate from Deps preserves the small existing service contract for test
// fakes and older composition roots.
type WorkflowSummaryQueries interface {
	ListWorkflowSummaries(context.Context) ([]WorkflowSummary, error)
}

type WorkflowDetailQueries interface {
	GetWorkflowDetail(context.Context, string) (WorkflowDetail, error)
}

type RunDetailQueries interface {
	GetRunDetail(context.Context, string) (run.RunDetail, error)
}

// BuildWorkflowSummaries computes all summaries from one workflow list and one
// run list. The helper is used by the composition root and is intentionally
// independent of a durable executor implementation.
func BuildWorkflowSummaries(workflows []*workflow.Workflow, runs []run.Run) []WorkflowSummary {
	latest := map[string]run.Run{}
	active := map[string]int{}
	for _, candidate := range runs {
		if current, ok := latest[candidate.Workflow]; !ok || newerRun(candidate, current) {
			latest[candidate.Workflow] = candidate
		}
		if candidate.State != run.StateCompleted && candidate.State != run.StateCanceled {
			active[candidate.Workflow]++
		}
	}
	out := make([]WorkflowSummary, 0, len(workflows))
	for _, wf := range workflows {
		if wf == nil {
			continue
		}
		repos := append([]string(nil), wf.Repos...)
		sort.Strings(repos)
		var latestRun *run.Run
		if candidate, ok := latest[wf.Name]; ok {
			copy := candidate
			latestRun = &copy
		}
		out = append(out, WorkflowSummary{
			Workflow: wf, Repositories: repos, NodeCount: len(wf.Nodes),
			ActiveRuns: active[wf.Name], LatestRun: latestRun,
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

func newerRun(candidate, current run.Run) bool {
	if candidate.StartedAt.After(current.StartedAt) {
		return true
	}
	return candidate.StartedAt.Equal(current.StartedAt) && candidate.UpdatedAt.After(current.UpdatedAt)
}

// BuildWorkflowDetail builds the static-definition plus recent execution
// summary response without changing workflow validation or execution.
func BuildWorkflowDetail(wf *workflow.Workflow, runs []run.Run) WorkflowDetail {
	recent := append([]run.Run(nil), runs...)
	sort.SliceStable(recent, func(i, j int) bool {
		if recent[i].UpdatedAt.Equal(recent[j].UpdatedAt) {
			return recent[i].StartedAt.After(recent[j].StartedAt)
		}
		return recent[i].UpdatedAt.After(recent[j].UpdatedAt)
	})
	if len(recent) > 5 {
		recent = recent[:5]
	}
	active := 0
	for _, candidate := range runs {
		if candidate.State != run.StateCompleted && candidate.State != run.StateCanceled {
			active++
		}
	}
	return WorkflowDetail{Workflow: wf, Valid: wf != nil, ActiveRuns: active, RecentRuns: recent}
}
