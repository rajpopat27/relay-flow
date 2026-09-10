package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"sort"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/mattn/go-isatty"
	runsvc "github.com/rajpopat27/relay-flow/internal/run"
	"github.com/rajpopat27/relay-flow/internal/server"
	"github.com/rajpopat27/relay-flow/internal/workflow"
)

// renderOptions controls terminal-sensitive decoration. The renderer itself
// never writes ANSI escapes; marks remain meaningful when color is disabled or
// output is piped.
type renderOptions struct {
	ASCII bool
	Width int
	Now   time.Time
}

func cliRenderOptions() renderOptions {
	ascii := true
	if isatty.IsTerminal(os.Stdout.Fd()) && os.Getenv("TERM") != "dumb" {
		ascii = false
	}
	width := 120
	if value, err := strconv.Atoi(os.Getenv("COLUMNS")); err == nil && value > 0 {
		width = value
	}
	return renderOptions{ASCII: ascii, Width: width, Now: time.Now().UTC()}
}

func encodeJSON(value any) int {
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	if err := enc.Encode(value); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return exitFail
	}
	return exitOK
}

func beginLoading(label string, jsonOutput bool) func(error) {
	if jsonOutput || !isatty.IsTerminal(os.Stdout.Fd()) {
		return func(error) {}
	}
	fmt.Fprintf(os.Stderr, "⠋ %s...\n", label)
	return func(err error) {
		if err != nil {
			fmt.Fprintf(os.Stderr, "✗ %s failed\n", label)
			return
		}
		fmt.Fprintf(os.Stderr, "✓ %s loaded\n", label)
	}
}

func (o renderOptions) now() time.Time {
	if o.Now.IsZero() {
		return time.Now().UTC()
	}
	return o.Now
}

func markForRun(r runsvc.Run, ascii bool) string {
	switch r.State {
	case runsvc.StateCompleted:
		return statusMark("completed", ascii)
	case runsvc.StateCanceled, runsvc.StateCanceling:
		return statusMark("canceled", ascii)
	case runsvc.StateBlocked:
		return statusMark("failed", ascii)
	case runsvc.StateStarting, runsvc.StateRunning, runsvc.StateWaiting:
		return statusMark("running", ascii)
	default:
		return statusMark("pending", ascii)
	}
}

func markForStep(status runsvc.StepStatus, ascii bool) string {
	switch status {
	case runsvc.StepSucceeded:
		return statusMark("completed", ascii)
	case runsvc.StepFailed, runsvc.StepBlocked:
		return statusMark("failed", ascii)
	case runsvc.StepCanceled:
		return statusMark("canceled", ascii)
	case runsvc.StepRunning, runsvc.StepWaiting:
		return statusMark("running", ascii)
	default:
		return statusMark("pending", ascii)
	}
}

func statusMark(status string, ascii bool) string {
	if ascii {
		switch status {
		case "completed", "valid":
			return "[x]"
		case "running", "waiting", "active":
			return "[>]"
		case "failed", "blocked":
			return "[!]"
		case "canceled":
			return "[-]"
		default:
			return "[ ]"
		}
	}
	switch status {
	case "completed", "valid":
		return "✓"
	case "running", "waiting", "active":
		return "⟳"
	case "failed", "blocked":
		return "✗"
	case "canceled":
		return "−"
	default:
		return "○"
	}
}

func formatTimestamp(t time.Time) string {
	if t.IsZero() {
		return "-"
	}
	return t.UTC().Format("2006-01-02 15:04:05 UTC")
}

func formatDuration(duration time.Duration) string {
	if duration <= 0 {
		return "-"
	}
	seconds := int64(duration / time.Second)
	if seconds == 0 {
		return "<1s"
	}
	days := seconds / 86400
	seconds %= 86400
	hours := seconds / 3600
	seconds %= 3600
	minutes := seconds / 60
	seconds %= 60
	var parts []string
	if days > 0 {
		parts = append(parts, fmt.Sprintf("%dd", days))
	}
	if hours > 0 || days > 0 {
		parts = append(parts, fmt.Sprintf("%dh", hours))
	}
	if minutes > 0 || hours > 0 || days > 0 {
		parts = append(parts, fmt.Sprintf("%dm", minutes))
	}
	if seconds > 0 || len(parts) == 0 {
		parts = append(parts, fmt.Sprintf("%ds", seconds))
	}
	return strings.Join(parts, " ")
}

func runDuration(r runsvc.Run, now time.Time) time.Duration {
	if r.StartedAt.IsZero() {
		return 0
	}
	end := now
	if r.FinishedAt != nil {
		end = *r.FinishedAt
	}
	if end.Before(r.StartedAt) {
		return 0
	}
	return end.Sub(r.StartedAt)
}

func stepDuration(step runsvc.StepEntry, now time.Time) time.Duration {
	if step.Duration > 0 {
		return step.Duration
	}
	if step.StartedAt == nil || (step.FinishedAt == nil && step.Status.IsTerminal() && !step.DurationKnown) {
		return 0
	}
	end := now
	if step.FinishedAt != nil {
		end = *step.FinishedAt
	}
	if end.Before(*step.StartedAt) {
		return 0
	}
	return end.Sub(*step.StartedAt)
}

func formatKnownDuration(duration time.Duration, known bool) string {
	if !known {
		return "-"
	}
	if duration == 0 {
		return "0s"
	}
	return formatDuration(duration)
}

func stepDurationKnown(step runsvc.StepEntry) bool {
	if step.DurationKnown || step.Duration != 0 || step.FinishedAt != nil {
		return true
	}
	return !step.Status.IsTerminal() && step.StartedAt != nil
}

func renderRunDetail(w io.Writer, detail runsvc.RunDetail, options renderOptions) {
	var body strings.Builder
	renderRunDetailBody(&body, detail, options)
	text := body.String()
	if options.Width <= 0 {
		_, _ = io.WriteString(w, text)
		return
	}
	for _, line := range strings.Split(strings.TrimSuffix(text, "\n"), "\n") {
		fmt.Fprintln(w, truncate(line, options.Width))
	}
}

func renderRunDetailBody(w io.Writer, detail runsvc.RunDetail, options renderOptions) {
	now := options.now()
	detail.DeriveInspectionFields(now)
	fmt.Fprintf(w, "Name:             %s\n", valueOrDash(string(detail.ID)))
	fmt.Fprintf(w, "Workflow:         %s\n", valueOrDash(detail.Workflow))
	fmt.Fprintf(w, "Repository:       %s\n", valueOrDash(detail.Repo))
	fmt.Fprintf(w, "Ticket:           %s\n", valueOrDash(detail.Ticket.Key))
	fmt.Fprintf(w, "Status:           %s\n\n", detail.DisplayStatus())

	fmt.Fprintln(w, "Conditions:")
	fmt.Fprintf(w, "  NodeRunning:      %s\n", boolText(detail.Conditions.NodeRunning))
	fmt.Fprintf(w, "  Waiting:          %s\n", boolText(detail.Conditions.Waiting))
	fmt.Fprintf(w, "  RetryScheduled:   %s\n", boolText(detail.Conditions.RetryScheduled))
	fmt.Fprintf(w, "  Completed:        %s\n", boolText(detail.Conditions.Completed))
	if detail.Conditions.Failed {
		fmt.Fprintf(w, "  Failed:           True\n")
	}
	if detail.Conditions.Canceled {
		fmt.Fprintf(w, "  Canceled:         True\n")
	}
	if detail.Retry != nil && detail.Retry.LastError != "" {
		fmt.Fprintf(w, "  RetryError:        %s\n", detail.Retry.LastError)
	}
	fmt.Fprintln(w)

	var createdAt time.Time
	if detail.CreatedAt != nil {
		createdAt = *detail.CreatedAt
	}
	fmt.Fprintf(w, "Created:          %s\n", formatTimestamp(createdAt))
	fmt.Fprintf(w, "Started:          %s\n", formatTimestamp(detail.StartedAt))
	if detail.FinishedAt == nil {
		fmt.Fprintln(w, "Finished:         -")
	} else {
		fmt.Fprintf(w, "Finished:         %s\n", formatTimestamp(*detail.FinishedAt))
	}
	fmt.Fprintf(w, "Duration:         %s\n", formatKnownDuration(runDuration(detail.Run, now), !detail.StartedAt.IsZero()))
	fmt.Fprintf(w, "Progress:         %d/%d\n", detail.Progress.Completed, detail.Progress.Total)
	fmt.Fprintf(w, "ResourcesDuration: runner %s, task-system %s, harness %s\n\n",
		formatKnownDuration(detail.ResourcesDuration.Runner, detail.ResourcesDuration.Known),
		formatKnownDuration(detail.ResourcesDuration.TaskSystem, detail.ResourcesDuration.Known),
		formatKnownDuration(detail.ResourcesDuration.Harness, detail.ResourcesDuration.Known))

	nameWidth, messageWidth := 40, 24
	header := "STEP                                      DURATION   MESSAGE                  RUNTIME"
	if options.Width > 0 && options.Width < 100 {
		nameWidth = options.Width / 2
		if nameWidth < 16 {
			nameWidth = 16
		}
		messageWidth = options.Width - nameWidth - 28
		if messageWidth < 8 {
			messageWidth = 8
		}
		header = "STEP DURATION MESSAGE RUNTIME"
	}
	fmt.Fprintln(w, truncate(header, options.Width))
	rootMessage := strings.ToLower(detail.DisplayStatus())
	if detail.LastError != "" {
		rootMessage = detail.LastError
	} else if detail.Retry != nil && detail.Retry.LastError != "" {
		rootMessage = detail.Retry.LastError
	}
	rootLine := fmt.Sprintf("%s %-*s %-10s %-*s %s", markForRun(detail.Run, options.ASCII),
		nameWidth, truncate(string(detail.ID), nameWidth), formatKnownDuration(runDuration(detail.Run, now), !detail.StartedAt.IsZero()), messageWidth, truncate(rootMessage, messageWidth), "-")
	fmt.Fprintln(w, truncate(rootLine, options.Width))
	for i, step := range detail.Steps {
		message := step.Message
		if message == "" {
			message = strings.ToLower(step.Status.DisplayStatus())
		}
		prefix := stepTreePrefix(detail.Steps, i, options.ASCII)
		line := fmt.Sprintf("%s %s %-*s %-10s %-*s %s", prefix,
			markForStep(step.Status, options.ASCII), nameWidth-4, truncate(step.Node, nameWidth-4),
			formatKnownDuration(stepDuration(step, now), stepDurationKnown(step)), messageWidth, truncate(message, messageWidth), valueOrDash(step.Runtime))
		fmt.Fprintln(w, truncate(line, options.Width))
	}
}

func stepIndexBySequence(steps []runsvc.StepEntry, sequence int64) (int, bool) {
	for i := range steps {
		if steps[i].Sequence == sequence {
			return i, true
		}
	}
	return 0, false
}

func hasLaterSibling(steps []runsvc.StepEntry, index int, parent int64) bool {
	for i := index + 1; i < len(steps); i++ {
		if steps[i].ParentSequence == parent {
			return true
		}
	}
	return false
}

func stepTreePrefix(steps []runsvc.StepEntry, index int, ascii bool) string {
	// Walk persisted parent links rather than assuming that sequence order is
	// a flat list. This keeps revisit rows under their actual parent visit.
	ancestors := make([]int, 0, steps[index].Depth)
	parent := steps[index].ParentSequence
	seen := map[int64]bool{}
	for parent != 0 && !seen[parent] {
		seen[parent] = true
		parentIndex, ok := stepIndexBySequence(steps, parent)
		if !ok {
			break
		}
		ancestors = append(ancestors, parentIndex)
		parent = steps[parentIndex].ParentSequence
	}
	var b strings.Builder
	for i := len(ancestors) - 1; i >= 0; i-- {
		ancestor := ancestors[i]
		if hasLaterSibling(steps, index, steps[ancestor].Sequence) {
			if ascii {
				b.WriteString("|   ")
			} else {
				b.WriteString("│   ")
			}
		} else {
			b.WriteString("    ")
		}
	}
	if hasLaterSibling(steps, index, steps[index].ParentSequence) {
		if ascii {
			b.WriteString("|--")
		} else {
			b.WriteString("├──")
		}
	} else if ascii {
		b.WriteString("`--")
	} else {
		b.WriteString("└──")
	}
	return b.String()
}

func renderWorkflowSummaries(w io.Writer, summaries []server.WorkflowSummary, options renderOptions) {
	var body strings.Builder
	renderWorkflowSummariesBody(&body, summaries, options)
	writeTerminalWidth(w, body.String(), options.Width)
}

func renderWorkflowSummariesBody(w io.Writer, summaries []server.WorkflowSummary, options renderOptions) {
	if len(summaries) == 0 {
		fmt.Fprintf(w, "%s No workflows configured. Submit one with: relay-flow workflow submit --file <path>\n", statusMark("pending", options.ASCII))
		fmt.Fprintln(w, "0 workflows | 0 active run(s)")
		return
	}
	fmt.Fprintln(w, "WORKFLOWS")
	fmt.Fprintln(w, strings.Repeat("=", 80))
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "STATE\tNAME\tREPOS\tNODES\tACTIVE\tLAST RUN")
	fmt.Fprintln(tw, "------\t----\t-----\t-----\t------\t--------")
	active := 0
	for _, summary := range summaries {
		name := "-"
		repos := strings.Join(summary.Repositories, ",")
		if summary.Workflow != nil {
			name = summary.Name
			if repos == "" {
				repos = strings.Join(summary.Repos, ",")
			}
		}
		last := "no runs"
		state := statusMark("pending", options.ASCII)
		if summary.LatestRun != nil {
			last = strings.ToLower(summary.LatestRun.DisplayStatus())
			if summary.LatestRun.CurrentNode != "" {
				last += " / " + summary.LatestRun.CurrentNode
			}
			state = markForRun(*summary.LatestRun, options.ASCII)
		}
		active += summary.ActiveRuns
		fmt.Fprintf(tw, "%s\t%s\t%s\t%d\t%d\t%s\n", state, name, valueOrDash(repos), summary.NodeCount, summary.ActiveRuns, last)
	}
	_ = tw.Flush()
	fmt.Fprintln(w, strings.Repeat("=", 80))
	fmt.Fprintf(w, "%d workflows | %d active run(s)\n\n", len(summaries), active)
	fmt.Fprintf(w, "Legend: %s healthy/complete  %s active  %s failed  %s no runs\n",
		statusMark("completed", options.ASCII), statusMark("active", options.ASCII),
		statusMark("failed", options.ASCII), statusMark("pending", options.ASCII))
}

func renderRuns(w io.Writer, runs []runsvc.Run, options renderOptions) {
	renderRunsFiltered(w, runs, options, runsvc.Filter{})
}

func renderRunsFiltered(w io.Writer, runs []runsvc.Run, options renderOptions, filter runsvc.Filter) {
	var body strings.Builder
	renderRunsFilteredBody(&body, runs, options, filter)
	writeTerminalWidth(w, body.String(), options.Width)
}

func renderRunsFilteredBody(w io.Writer, runs []runsvc.Run, options renderOptions, filter runsvc.Filter) {
	if len(runs) == 0 {
		fmt.Fprintf(w, "%s No runs match filters (%s). Try: relay-flow run list --active\n", statusMark("pending", options.ASCII), formatRunFilter(filter))
		fmt.Fprintln(w, "0 runs | 0 active | 0 failed | 0 completed | 0 canceled")
		return
	}
	fmt.Fprintln(w, "RUNS")
	fmt.Fprintln(w, strings.Repeat("=", 80))
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "STATE\tTICKET\tREPOSITORY\tWORKFLOW\tNODE\tAGE\tRETRY/ERROR")
	fmt.Fprintln(tw, "-----\t------\t----------\t--------\t----\t---\t-----------")
	now := options.now()
	active, failed, completed, canceled := 0, 0, 0, 0
	for _, current := range runs {
		state := markForRun(current, options.ASCII)
		switch current.State {
		case runsvc.StateCompleted:
			completed++
		case runsvc.StateCanceled:
			canceled++
		case runsvc.StateBlocked:
			failed++
			active++
		default:
			active++
		}
		extra := "-"
		if current.Retry != nil {
			extra = fmt.Sprintf("retry %d: %s", current.Retry.Attempt, current.Retry.LastError)
		} else if current.LastError != "" {
			extra = current.LastError
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\t%s\n", state,
			valueOrDash(current.Ticket.Key), valueOrDash(current.Repo), valueOrDash(current.Workflow),
			valueOrDash(current.CurrentNode), formatAge(current.UpdatedAt, now), truncate(extra, 40))
	}
	_ = tw.Flush()
	fmt.Fprintln(w, strings.Repeat("=", 80))
	fmt.Fprintf(w, "%d runs | %d active | %d failed | %d completed | %d canceled\n", len(runs), active, failed, completed, canceled)
}

func renderWorkflowDetail(w io.Writer, detail server.WorkflowDetail, options renderOptions) {
	var body strings.Builder
	renderWorkflowDetailBody(&body, detail, options)
	writeTerminalWidth(w, body.String(), options.Width)
}

func renderWorkflowDetailBody(w io.Writer, detail server.WorkflowDetail, options renderOptions) {
	wf := detail.Workflow
	if wf == nil {
		fmt.Fprintf(w, "%s Workflow unavailable\n", statusMark("failed", options.ASCII))
		return
	}
	fmt.Fprintf(w, "WORKFLOW  %s\n", wf.Name)
	fmt.Fprintln(w, strings.Repeat("=", 80))
	validMark := statusMark("valid", options.ASCII)
	fmt.Fprintf(w, "STATUS       %s VALID\n", validMark)
	fmt.Fprintf(w, "REPOSITORIES %s\n", valueOrDash(strings.Join(wf.Repos, ", ")))
	fmt.Fprintf(w, "NODES        %d\n", len(wf.Nodes))
	cleanup := "disabled"
	if wf.CleanupRunnerOnEnd {
		cleanup = "enabled"
	}
	fmt.Fprintf(w, "CLEANUP      %s\n", cleanup)
	fmt.Fprintf(w, "ACTIVE RUNS  %d\n", detail.ActiveRuns)
	fmt.Fprintln(w, strings.Repeat("=", 80))
	fmt.Fprintln(w, "\nGRAPH")
	renderGraph(w, wf, workflow.StartNode, "  ", map[string]bool{}, options.ASCII)
	fmt.Fprintln(w, "\nRECENT RUNS")
	if len(detail.RecentRuns) == 0 {
		fmt.Fprintf(w, "  %s no runs\n", statusMark("pending", options.ASCII))
		return
	}
	for _, current := range detail.RecentRuns {
		fmt.Fprintf(w, "  %s %-24s %-10s %s\n", markForRun(current, options.ASCII), current.ID,
			strings.ToLower(current.DisplayStatus()), valueOrDash(current.CurrentNode))
	}
}

func renderGraph(w io.Writer, wf *workflow.Workflow, node, indent string, visiting map[string]bool, ascii bool) {
	if visiting[node] {
		fmt.Fprintf(w, "%s%s %s (cycle)\n", indent, statusMark("valid", ascii), node)
		return
	}
	n, ok := wf.Nodes[node]
	if !ok {
		fmt.Fprintf(w, "%s%s %s (missing)\n", indent, statusMark("failed", ascii), node)
		return
	}
	fmt.Fprintf(w, "%s%s %s\n", indent, statusMark("valid", ascii), node)
	visiting[node] = true
	type graphRoute struct {
		route   workflow.Route
		outcome string
	}
	routes := make([]graphRoute, 0, len(n.OnSuccess)+len(n.OnFailure))
	for _, route := range n.OnSuccess {
		routes = append(routes, graphRoute{route: route, outcome: "success"})
	}
	for _, route := range n.OnFailure {
		routes = append(routes, graphRoute{route: route, outcome: "failure"})
	}
	for i, edge := range routes {
		route := edge.route
		connector := "+-->"
		if i == len(routes)-1 {
			connector = "`-->"
		}
		if ascii {
			connector = "+-->"
			if i == len(routes)-1 {
				connector = "`-->"
			}
		}
		when := route.When
		if when != "" {
			when = " — " + when
		}
		fmt.Fprintf(w, "%s%s %s %s -> %s%s\n", indent, connector, statusMark("valid", ascii), edge.outcome, route.Target, when)
		if !visiting[route.Target] {
			renderGraph(w, wf, route.Target, indent+"     ", visiting, ascii)
		}
	}
	delete(visiting, node)
}

func writeTerminalWidth(w io.Writer, text string, width int) {
	if width <= 0 {
		_, _ = io.WriteString(w, text)
		return
	}
	for _, line := range strings.Split(strings.TrimSuffix(text, "\n"), "\n") {
		fmt.Fprintln(w, truncate(line, width))
	}
}

func formatRunFilter(filter runsvc.Filter) string {
	parts := make([]string, 0, 4)
	if filter.Repo != "" {
		parts = append(parts, "repo="+filter.Repo)
	}
	if filter.Workflow != "" {
		parts = append(parts, "workflow="+filter.Workflow)
	}
	if filter.Ticket != "" {
		parts = append(parts, "ticket="+filter.Ticket)
	}
	if filter.Active != nil {
		parts = append(parts, fmt.Sprintf("active=%t", *filter.Active))
	}
	if len(parts) == 0 {
		return "none"
	}
	return strings.Join(parts, ", ")
}

func formatAge(updated, now time.Time) string {
	if updated.IsZero() {
		return "-"
	}
	age := now.Sub(updated)
	if age < 0 {
		age = 0
	}
	return formatDuration(age)
}

func boolText(value bool) string {
	if value {
		return "True"
	}
	return "False"
}

func valueOrDash(value string) string {
	if strings.TrimSpace(value) == "" {
		return "-"
	}
	return value
}

func truncate(value string, width int) string {
	value = strings.ReplaceAll(strings.ReplaceAll(value, "\n", " "), "\r", " ")
	if width <= 0 || len([]rune(value)) <= width {
		return value
	}
	runes := []rune(value)
	if width <= 3 {
		return string(runes[:width])
	}
	return string(runes[:width-3]) + "..."
}

// sortedRuns is useful for deterministic callers that assemble detail data
// outside the server.
func sortedRuns(runs []runsvc.Run) []runsvc.Run {
	out := append([]runsvc.Run(nil), runs...)
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].UpdatedAt.Equal(out[j].UpdatedAt) {
			return out[i].ID < out[j].ID
		}
		return out[i].UpdatedAt.After(out[j].UpdatedAt)
	})
	return out
}
