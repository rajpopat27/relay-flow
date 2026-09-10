package main

import (
	"bytes"
	"io"
	"os"
	"strings"
	"testing"
	"time"

	runsvc "github.com/rajpopat27/relay-flow/internal/run"
	"github.com/rajpopat27/relay-flow/internal/server"
	"github.com/rajpopat27/relay-flow/internal/task"
	"github.com/rajpopat27/relay-flow/internal/workflow"
)

func TestRunDetailRendererKeepsPendingAndNestedVisitOrder(t *testing.T) {
	now := time.Date(2026, 9, 10, 12, 0, 0, 0, time.UTC)
	started := now.Add(-2 * time.Minute)
	finished := now.Add(-time.Minute)
	detail := runsvc.RunDetail{Run: runsvc.Run{
		ID: "repo/workflow/T-1", Repo: "repo", Workflow: "workflow", State: runsvc.StateWaiting,
		Ticket: runsvcTicket("T-1"), StartedAt: started,
	}, Steps: []runsvc.StepEntry{
		{Sequence: 0, Node: "start", Status: runsvc.StepSucceeded, Depth: 1, StartedAt: &started, FinishedAt: &finished},
		{Sequence: 1, Node: "coding", Status: runsvc.StepWaiting, Depth: 1, StartedAt: &started},
		{Sequence: 2, Node: "coding", Status: runsvc.StepFailed, Depth: 2, StartedAt: &started, FinishedAt: &finished, Message: "setup failed", ParentSequence: 1},
		{Sequence: 3, Node: "end", Status: runsvc.StepPending, Depth: 1},
	}}
	var out bytes.Buffer
	renderRunDetail(&out, detail, renderOptions{ASCII: true, Now: now})
	text := out.String()
	for _, want := range []string{"Status:           Waiting", "Conditions:", "Progress:         3/4", "[ ] end", "setup failed", "    `--"} {
		if !strings.Contains(text, want) {
			t.Fatalf("rendered detail missing %q:\n%s", want, text)
		}
	}
	if strings.Index(text, "start") > strings.Index(text, "coding") || strings.Index(text, "coding") > strings.Index(text, "end") {
		t.Fatalf("step order was not preserved:\n%s", text)
	}
}

func TestScopedHelpCoversAllCommandLevels(t *testing.T) {
	for _, args := range [][]string{
		{"--help"}, {"workflow", "--help"}, {"workflow", "list", "--help"},
		{"repo", "--help"}, {"repo", "register", "--help"}, {"run", "--help"},
		{"run", "get", "--help"}, {"task", "--help"}, {"task", "auth", "--help"},
		{"serve", "--help"}, {"runtime-register", "--help"}, {"version", "--help"},
	} {
		var out bytes.Buffer
		if code := printScopedHelp(args, &out); code != exitOK {
			t.Fatalf("help %v exit = %d", args, code)
		}
		if out.Len() == 0 {
			t.Fatalf("help %v was empty", args)
		}
		if !strings.Contains(out.String(), "Example:") {
			t.Fatalf("help %v missing example: %q", args, out.String())
		}
	}
	var example bytes.Buffer
	if code := printScopedHelp([]string{"run", "get", "--help"}, &example); code != exitOK || !strings.Contains(example.String(), "Example:") {
		t.Fatalf("run get help missing example: %q", example.String())
	}
}

func TestFilteredEmptyRunListShowsEffectiveFilters(t *testing.T) {
	var out bytes.Buffer
	active := true
	renderRunsFiltered(&out, nil, renderOptions{ASCII: true}, runsvc.Filter{Repo: "payments", Workflow: "basicFlow", Active: &active})
	text := out.String()
	if !strings.Contains(text, "repo=payments") || !strings.Contains(text, "workflow=basicFlow") || !strings.Contains(text, "active=true") || !strings.Contains(text, "0 runs | 0 active | 0 failed") {
		t.Fatalf("empty filtered output = %q", text)
	}
	var workflows bytes.Buffer
	renderWorkflowSummaries(&workflows, nil, renderOptions{ASCII: true, Width: 120})
	if !strings.Contains(workflows.String(), "0 workflows | 0 active") {
		t.Fatalf("empty workflow output = %q", workflows.String())
	}
}

func TestRunDetailRendererShowsRetryErrorWhenRunErrorIsEmpty(t *testing.T) {
	detail := runsvc.RunDetail{Run: runsvc.Run{ID: "repo/workflow/T-RETRY", State: runsvc.StateWaiting, StartedAt: time.Now().UTC()}}
	detail.Retry = &runsvc.RetryStatus{Attempt: 2, LastError: "runner temporarily unavailable", NextRetryAt: time.Now().UTC().Add(time.Minute)}
	var out bytes.Buffer
	renderRunDetail(&out, detail, renderOptions{ASCII: true, Width: 120, Now: time.Now().UTC()})
	if !strings.Contains(out.String(), "runner temporarily unavailable") || !strings.Contains(out.String(), "RetryScheduled:   True") {
		t.Fatalf("retry error output = %q", out.String())
	}
}

func TestTerminalStepWithUnknownDurationRendersPlaceholder(t *testing.T) {
	started := time.Now().UTC().Add(-time.Minute)
	detail := runsvc.RunDetail{Run: runsvc.Run{ID: "repo/workflow/T-UNKNOWN", State: runsvc.StateCompleted, StartedAt: started}, Steps: []runsvc.StepEntry{{Node: "coding", Status: runsvc.StepSucceeded, Depth: 1, StartedAt: &started}}}
	var out bytes.Buffer
	renderRunDetail(&out, detail, renderOptions{ASCII: true, Width: 120, Now: time.Now().UTC()})
	found := false
	for _, line := range strings.Split(out.String(), "\n") {
		if strings.Contains(line, "coding") && strings.Contains(line, " - ") {
			found = true
		}
	}
	if !found {
		t.Fatalf("unknown terminal duration output = %q", out.String())
	}
}

func TestRunDetailRendererPreservesKnownZeroTimingAndResourceValues(t *testing.T) {
	created := time.Date(2026, 9, 10, 11, 0, 0, 0, time.UTC)
	started := created.Add(time.Minute)
	detail := runsvc.RunDetail{Run: runsvc.Run{ID: "repo/workflow/T-2", State: runsvc.StateCompleted, StartedAt: started, FinishedAt: &started}, CreatedAt: &created, Steps: []runsvc.StepEntry{{Node: "end", Status: runsvc.StepSucceeded, Depth: 1, StartedAt: &started, FinishedAt: &started, Resource: "harness"}}}
	var out bytes.Buffer
	renderRunDetail(&out, detail, renderOptions{ASCII: true, Now: started})
	text := out.String()
	for _, want := range []string{"Created:          2026-09-10 11:00:00 UTC", "Started:          2026-09-10 11:01:00 UTC", "Duration:         0s", "ResourcesDuration: runner 0s, task-system 0s, harness 0s"} {
		if !strings.Contains(text, want) {
			t.Fatalf("timing/resource output missing %q:\n%s", want, text)
		}
	}
}

func TestLoadingIndicatorIsSilentForPipedOutput(t *testing.T) {
	oldStdout, oldStderr := os.Stdout, os.Stderr
	stdoutR, stdoutW, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	stderrR, stderrW, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stdout, os.Stderr = stdoutW, stderrW
	finish := beginLoading("Loading runs", false)
	finish(nil)
	_ = stdoutW.Close()
	_ = stderrW.Close()
	os.Stdout, os.Stderr = oldStdout, oldStderr
	stdout, _ := io.ReadAll(stdoutR)
	stderr, _ := io.ReadAll(stderrR)
	_ = stdoutR.Close()
	_ = stderrR.Close()
	if len(stdout) != 0 || len(stderr) != 0 {
		t.Fatalf("piped loading output stdout=%q stderr=%q", stdout, stderr)
	}
}

func TestNarrowRunDetailOutputDoesNotExceedTerminalWidth(t *testing.T) {
	detail := runsvc.RunDetail{Run: runsvc.Run{ID: "repo/workflow/T-1", State: runsvc.StateWaiting, StartedAt: time.Now().UTC()}, Steps: []runsvc.StepEntry{{Node: "a-very-long-node-name", Status: runsvc.StepWaiting, Depth: 1, Message: "a very long message"}}}
	var out bytes.Buffer
	renderRunDetail(&out, detail, renderOptions{ASCII: true, Width: 40, Now: time.Now().UTC()})
	for _, line := range strings.Split(strings.TrimSuffix(out.String(), "\n"), "\n") {
		if len([]rune(line)) > 40 {
			t.Fatalf("narrow line length=%d: %q", len([]rune(line)), line)
		}
	}
}

func TestBlockedActiveRunCountsAsActiveAndFailed(t *testing.T) {
	var out bytes.Buffer
	active := true
	renderRunsFiltered(&out, []runsvc.Run{{State: runsvc.StateBlocked, Ticket: runsvcTicket("T-BLOCKED"), Workflow: "workflow"}}, renderOptions{ASCII: true, Width: 120}, runsvc.Filter{Active: &active})
	if !strings.Contains(out.String(), "1 active") || !strings.Contains(out.String(), "1 failed") {
		t.Fatalf("blocked aggregate = %q", out.String())
	}
}

func TestNarrowWorkflowDetailStaysWithinTerminalWidth(t *testing.T) {
	wf := &workflow.Workflow{Name: "a-very-long-workflow-name", Repos: []string{"a-very-long-repository-name"}, Nodes: map[string]workflow.Node{workflow.StartNode: {}, workflow.EndNode: {}}}
	var out bytes.Buffer
	renderWorkflowDetail(&out, server.WorkflowDetail{Workflow: wf}, renderOptions{ASCII: true, Width: 40})
	for _, line := range strings.Split(strings.TrimSuffix(out.String(), "\n"), "\n") {
		if len([]rune(line)) > 40 {
			t.Fatalf("narrow workflow detail line length=%d: %q", len([]rune(line)), line)
		}
	}
}

func TestNarrowWorkflowAndRunListsStayWithinTerminalWidth(t *testing.T) {
	options := renderOptions{ASCII: true, Width: 40, Now: time.Now().UTC()}
	var workflows bytes.Buffer
	renderWorkflowSummaries(&workflows, []server.WorkflowSummary{{Workflow: &workflow.Workflow{Name: "a-very-long-workflow-name", Repos: []string{"a-very-long-repository-name"}}, NodeCount: 7, ActiveRuns: 1}}, options)
	var runs bytes.Buffer
	renderRunsFiltered(&runs, []runsvc.Run{{ID: "repo/workflow/T-3", Repo: "a-very-long-repository-name", Workflow: "a-very-long-workflow-name", Ticket: runsvcTicket("T-3"), CurrentNode: "a-very-long-node-name", State: runsvc.StateBlocked, UpdatedAt: options.Now}}, options, runsvc.Filter{Repo: "repo"})
	for _, text := range []string{workflows.String(), runs.String()} {
		for _, line := range strings.Split(strings.TrimSuffix(text, "\n"), "\n") {
			if len([]rune(line)) > options.Width {
				t.Fatalf("narrow list line length=%d: %q", len([]rune(line)), line)
			}
		}
	}
}

func TestVersionOutputIsServerIndependent(t *testing.T) {
	var short, long bytes.Buffer
	printVersion(&short, false)
	printVersion(&long, true)
	if !strings.HasPrefix(short.String(), "relay-flow ") || !strings.Contains(long.String(), "Go:") || !strings.Contains(long.String(), "Platform:") {
		t.Fatalf("version output short=%q long=%q", short.String(), long.String())
	}
}

func runsvcTicket(key string) task.TicketRef {
	return task.TicketRef{Key: key}
}
