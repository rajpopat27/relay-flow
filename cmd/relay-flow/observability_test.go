package main

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
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
		{"repo", "--help"}, {"repo", "register", "--help"}, {"repo", "remove", "--help"},
		{"repo", "get", "--help"}, {"repo", "list", "--help"}, {"run", "--help"},
		{"run", "get", "--help"}, {"task", "--help"}, {"task", "auth", "--help"},
		{"init", "--help"}, {"serve", "--help"}, {"runtime-register", "--help"}, {"version", "--help"},
	} {
		var out bytes.Buffer
		if code := printScopedHelp(args, &out); code != exitOK {
			t.Fatalf("help %v exit = %d", args, code)
		}
		if out.Len() == 0 {
			t.Fatalf("help %v was empty", args)
		}
		if !strings.Contains(out.String(), "Example") {
			t.Fatalf("help %v missing example: %q", args, out.String())
		}
	}
	root := scopedHelp(t, "--help")
	if !strings.Contains(root, "Use relay-flow <command> --help for command-specific details.") {
		t.Fatalf("root help missing scoped-help guidance:\n%s", root)
	}
}

func TestInitHelpDocumentsTemporalFlags(t *testing.T) {
	text := scopedHelp(t, "init", "--help")
	for _, want := range []string{
		"--executor-plugin <name>",
		"--temporal-address <host:port>",
		"--temporal-namespace <name>",
		"applies only to --executor-plugin temporal",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("init help missing %q:\n%s", want, text)
		}
	}
}

func TestRepositoryHelpDocumentsGroupAndEverySubcommand(t *testing.T) {
	group := scopedHelp(t, "repo", "--help")
	for _, want := range []string{
		"Manage registered runner repositories and task-system configuration.",
		"register", "remove", "list", "get",
		"Usage: relay-flow repo register", "Usage: relay-flow repo remove --name <name>",
		"Usage: relay-flow repo list", "Usage: relay-flow repo get --name <name>",
		"Example (Beads):", "beadsDir=/work/payments/.beads",
		"Use relay-flow repo <subcommand> --help for flag details.",
	} {
		if !strings.Contains(group, want) {
			t.Fatalf("repo group help missing %q:\n%s", want, group)
		}
	}

	register := scopedHelp(t, "repo", "register", "--help")
	for _, want := range []string{
		"Register a runner repository and task-system configuration.",
		"--name <name>", "--path <path>", "--set key=value",
		"Optional for interactive registration", "required in flagged/non-interactive mode",
		"Interactive mode: omit --name, --path, and --set", "Repeat --set",
		"duplicate keys are rejected", "derived keys cannot be overridden",
		"Example (Beads):", "beadsDir=/work/payments/.beads",
	} {
		if !strings.Contains(register, want) {
			t.Fatalf("repo register help missing %q:\n%s", want, register)
		}
	}
	if strings.Contains(register, "component=backend") {
		t.Fatalf("repo register help advertises a derived Jira component override:\n%s", register)
	}

	for _, tc := range []struct {
		name string
		want []string
	}{
		{name: "remove", want: []string{"Remove a registered repository.", "--name <name>", "required", "No other flags are supported.", "Example:"}},
		{name: "list", want: []string{"List registered repositories.", "Flags: none.", "Example:"}},
		{name: "get", want: []string{"Show one registered repository", "--name <name>", "required", "No other flags are supported.", "Example:"}},
	} {
		text := scopedHelp(t, "repo", tc.name, "--help")
		for _, want := range tc.want {
			if !strings.Contains(text, want) {
				t.Fatalf("repo %s help missing %q:\n%s", tc.name, want, text)
			}
		}
	}
}

func TestWorkflowRunAndTaskGroupsDescribeTheirSubcommands(t *testing.T) {
	for _, tc := range []struct {
		args []string
		want []string
	}{
		{args: []string{"workflow", "--help"}, want: []string{"Manage validated workflow definitions", "workflow submit --file <path>", "workflow remove --name <name>", "workflow list [--json]", "workflow get --name <name>", "<subcommand> --help"}},
		{args: []string{"run", "--help"}, want: []string{"Inspect and control durable ticket runs.", "run list", "run get", "run restart", "run cancel", "--reason <text>", "<subcommand> --help"}},
		{args: []string{"task", "--help"}, want: []string{"Run task-system commands", "auth", "task <subcommand> --help"}},
		{args: []string{"workflow", "submit", "--help"}, want: []string{"--file <path>", "required", "Example:"}},
		{args: []string{"run", "list", "--help"}, want: []string{"--repo <name>", "--workflow <name>", "--ticket <key>", "--active", "--json", "--no-color", "Example:"}},
		{args: []string{"task", "auth", "--help"}, want: []string{"task-plugin options are passed through unchanged", "no flags", "Example:"}},
	} {
		text := scopedHelp(t, tc.args...)
		for _, want := range tc.want {
			if !strings.Contains(text, want) {
				t.Fatalf("help %v missing %q:\n%s", tc.args, want, text)
			}
		}
	}
}

func TestScopedHelpDoesNotRequireRelayFlowHomeOrServer(t *testing.T) {
	home := filepath.Join(t.TempDir(), "missing-home")
	t.Setenv("RELAY_FLOW_HOME", home)
	code, output := captureStdout(t, func() int {
		return run([]string{"repo", "register", "--help"}, strings.NewReader(""))
	})
	if code != exitOK {
		t.Fatalf("help exit = %d, want %d", code, exitOK)
	}
	if !strings.Contains(output, "Usage: relay-flow repo register") {
		t.Fatalf("help output = %q", output)
	}
	if _, err := os.Stat(home); !os.IsNotExist(err) {
		t.Fatalf("help touched relay-flow home: stat error = %v", err)
	}
}

func scopedHelp(t *testing.T, args ...string) string {
	t.Helper()
	var out bytes.Buffer
	if code := printScopedHelp(args, &out); code != exitOK {
		t.Fatalf("help %v exit = %d", args, code)
	}
	return out.String()
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
