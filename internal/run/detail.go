package run

import (
	"time"

	"github.com/rajpopat27/relay-flow/internal/workflow"
)

// StepStatus is the display-only lifecycle of one recorded node visit. It is
// deliberately separate from State: State is the execution authority's
// current run state, while StepStatus describes one historical row in the
// relay-owned inspection projection.
type StepStatus string

const (
	StepPending   StepStatus = "pending"
	StepRunning   StepStatus = "running"
	StepWaiting   StepStatus = "waiting"
	StepSucceeded StepStatus = "succeeded"
	StepFailed    StepStatus = "failed"
	StepBlocked   StepStatus = "blocked"
	StepCanceled  StepStatus = "canceled"
)

// StepEntry is the small, engine-neutral read model used by run inspection.
// Sequence is the stable execution order within one attempt. A node visit is
// identified by NodeVisitID when the node is a work node; lifecycle rows such
// as start/end may not have one.
type StepEntry struct {
	RunID          ID            `json:"runId,omitempty"`
	Sequence       int64         `json:"sequence"`
	Node           string        `json:"node"`
	NodeVisitID    NodeVisitID   `json:"nodeVisitId,omitempty"`
	NodeType       string        `json:"nodeType,omitempty"`
	ParentSequence int64         `json:"parentSequence,omitempty"`
	Depth          int           `json:"depth,omitempty"`
	Status         StepStatus    `json:"status"`
	StartedAt      *time.Time    `json:"startedAt,omitempty"`
	FinishedAt     *time.Time    `json:"finishedAt,omitempty"`
	Duration       time.Duration `json:"duration,omitempty"`
	DurationKnown  bool          `json:"durationKnown,omitempty"`
	Message        string        `json:"message,omitempty"`
	Route          string        `json:"route,omitempty"`
	Runtime        string        `json:"runtime,omitempty"`
	Resource       string        `json:"resource,omitempty"`
}

// RunConditions are derived from the current run state and retry metadata.
// They are display data only and do not participate in execution decisions.
type RunConditions struct {
	NodeRunning    bool `json:"nodeRunning"`
	Waiting        bool `json:"waiting"`
	RetryScheduled bool `json:"retryScheduled"`
	Completed      bool `json:"completed"`
	Failed         bool `json:"failed"`
	Canceled       bool `json:"canceled"`
}

// RunProgress counts reached visits in the derived timeline. Pending rows are
// the denominator remainder for an active/waiting run.
type RunProgress struct {
	Completed int `json:"completed"`
	Total     int `json:"total"`
	Pending   int `json:"pending"`
}

// ResourceDuration is an optional display aggregation. Known is false when
// executors did not retain resource attribution; zero values with Known=true
// represent a real zero-duration resource interval.
type ResourceDuration struct {
	Known      bool          `json:"known"`
	Runner     time.Duration `json:"runner,omitempty"`
	TaskSystem time.Duration `json:"taskSystem,omitempty"`
	Harness    time.Duration `json:"harness,omitempty"`
	Other      time.Duration `json:"other,omitempty"`
}

// RunDetail embeds the original Run value so existing JSON clients that
// decode a run.Run continue to work when the server adds inspection fields.
// The remaining fields are read-only relay projection data.
type RunDetail struct {
	Run
	CreatedAt         *time.Time       `json:"createdAt,omitempty"`
	Conditions        RunConditions    `json:"conditions"`
	Progress          RunProgress      `json:"progress"`
	ResourcesDuration ResourceDuration `json:"resourcesDuration"`
	Steps             []StepEntry      `json:"steps"`
}

// NewRunDetail creates an empty detail response for a base run. Projection
// implementations fill Steps and derive the remaining fields.
func NewRunDetail(r Run) RunDetail {
	return RunDetail{Run: r, Steps: []StepEntry{}}
}

// AddPendingNodes adds only pending downstream rows that can be justified by
// the current run state and selected route. Completed runs intentionally show
// only actual visits; the static graph is not treated as execution history.
func (d *RunDetail) AddPendingNodes(wf workflow.Workflow) {
	if d.State == StateCompleted {
		return
	}
	actual := make(map[string]bool, len(d.Steps))
	maxSequence := int64(-1)
	current := d.CurrentNode
	var currentStep *StepEntry
	for i := range d.Steps {
		step := &d.Steps[i]
		if step.Status == StepPending {
			continue
		}
		actual[step.Node] = true
		if step.Sequence > maxSequence {
			maxSequence = step.Sequence
		}
		if currentStep == nil || step.Sequence > currentStep.Sequence {
			currentStep = step
		}
	}
	if current == "" && currentStep != nil {
		current = currentStep.Node
	}
	if current == "" {
		return
	}

	candidates := make([]string, 0, 2)
	addCandidate := func(target string) {
		if target == "" || actual[target] {
			return
		}
		for _, existing := range candidates {
			if existing == target {
				return
			}
		}
		candidates = append(candidates, target)
	}
	if currentStep != nil && currentStep.Node == current && currentStep.Route != "" {
		addCandidate(currentStep.Route)
	} else if node, ok := wf.Nodes[current]; ok {
		for _, route := range append(append([]workflow.Route{}, node.OnSuccess...), node.OnFailure...) {
			addCandidate(route.Target)
		}
	}

	added := map[string]bool{}
	var appendPending func(string, bool)
	appendPending = func(name string, followSingle bool) {
		if name == "" || actual[name] || added[name] {
			return
		}
		node, ok := wf.Nodes[name]
		if !ok {
			return
		}
		added[name] = true
		nodeType := string(node.Type)
		if name == workflow.StartNode || name == workflow.EndNode {
			nodeType = "lifecycle"
		}
		maxSequence++
		d.Steps = append(d.Steps, StepEntry{RunID: d.ID, Sequence: maxSequence,
			Node: name, NodeType: nodeType, Status: StepPending, Depth: 1})
		if !followSingle || name == workflow.EndNode {
			return
		}
		routes := append(append([]workflow.Route{}, node.OnSuccess...), node.OnFailure...)
		if len(routes) == 1 {
			appendPending(routes[0].Target, true)
		}
	}
	for _, candidate := range candidates {
		appendPending(candidate, currentStep != nil && currentStep.Node == current && currentStep.Route != "")
	}
}

// DeriveInspectionFields fills conditions, progress, and resource durations
// from the supplied run and step timeline. This helper intentionally reads no
// executor or task-system state.
func (d *RunDetail) DeriveInspectionFields(now time.Time) {
	if now.IsZero() {
		now = time.Now().UTC()
	}
	d.Conditions = RunConditions{
		NodeRunning:    d.State == StateRunning,
		Waiting:        d.State == StateWaiting,
		RetryScheduled: d.Retry != nil,
		Completed:      d.State == StateCompleted,
		Failed:         d.State == StateBlocked,
		Canceled:       d.State == StateCanceled || d.State == StateCanceling,
	}
	d.Progress = RunProgress{Total: len(d.Steps)}
	d.ResourcesDuration = ResourceDuration{}
	for i, step := range d.Steps {
		switch step.Status {
		case StepPending:
			d.Progress.Pending++
		default:
			// Progress counts reached visits, including the current waiting or
			// blocked visit; pending rows are the denominator remainder.
			d.Progress.Completed++
		}
		duration := step.Duration
		if duration == 0 && step.StartedAt != nil && step.FinishedAt != nil {
			duration = step.FinishedAt.Sub(*step.StartedAt)
			d.Steps[i].DurationKnown = true
		}
		if duration == 0 && step.StartedAt != nil && step.FinishedAt == nil &&
			(step.Status == StepRunning || step.Status == StepWaiting || step.Status == StepBlocked) {
			duration = now.Sub(*step.StartedAt)
		}
		if duration < 0 {
			duration = 0
		}
		if step.Resource != "" {
			d.ResourcesDuration.Known = true
		}
		switch step.Resource {
		case "runner":
			d.ResourcesDuration.Runner += duration
		case "task-system", "taskSystem":
			d.ResourcesDuration.TaskSystem += duration
		case "harness":
			d.ResourcesDuration.Harness += duration
		case "":
		default:
			d.ResourcesDuration.Other += duration
		}
	}
}

// DisplayStatus returns the stable human status vocabulary used by the CLI.
func (r Run) DisplayStatus() string {
	switch r.State {
	case StateCompleted:
		return "Succeeded"
	case StateRunning:
		return "Running"
	case StateWaiting:
		return "Waiting"
	case StateBlocked:
		return "Failed"
	case StateCanceled, StateCanceling:
		return "Canceled"
	case StateStarting:
		return "Running"
	default:
		return string(r.State)
	}
}

// DisplayStatus returns the stable human status vocabulary for a step.
func (s StepStatus) DisplayStatus() string {
	switch s {
	case StepSucceeded:
		return "Succeeded"
	case StepRunning:
		return "Running"
	case StepWaiting:
		return "Waiting"
	case StepFailed, StepBlocked:
		return "Failed"
	case StepCanceled:
		return "Canceled"
	case StepPending:
		return "Pending"
	default:
		return string(s)
	}
}

// IsTerminal reports whether a step has finished and should contribute to
// progress. It is intentionally local to the display model.
func (s StepStatus) IsTerminal() bool {
	return s == StepSucceeded || s == StepFailed || s == StepCanceled
}
