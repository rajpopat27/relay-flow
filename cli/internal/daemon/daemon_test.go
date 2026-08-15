package daemon

import (
	"fmt"
	"sort"
	"strings"
	"testing"

	"orca-jira-loop/internal/acli"
	"orca-jira-loop/internal/config"
)

// fakeAcli implements StatusValidator for tests.
type fakeAcli struct {
	valid map[string]bool // "PROJECT/Status" -> valid
	calls []string
}

func (f *fakeAcli) ValidateStatus(projectKey, status string) error {
	f.calls = append(f.calls, projectKey+"/"+status)
	if f.valid[projectKey+"/"+status] {
		return nil
	}
	return fmt.Errorf("invalid status %q", status)
}

func testConfig(t *testing.T) *config.Config {
	t.Helper()
	c, err := config.Parse("test", []byte(`
pollIntervalSeconds: 30
workflows:
  taskDevelopment:
    jql: project = FOO
    issueTypes: [Task]
    closeOn: Done
    agents:
      dev:
        handles:
          - status: To Do
            outcomes:
              done: In Review
      reviewer:
        handles:
          - status: In Review
            outcomes:
              approved: Done
  incidentResponse:
    jql: project = BAR
    issueTypes: [Task]
    closeOn: [Resolved]
    agents:
      responder:
        handles:
          - status: Open
            outcomes:
              fixed: Resolved
`))
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func TestValidateConfigStatuses_Valid(t *testing.T) {
	fake := &fakeAcli{valid: map[string]bool{
		"FOO/Done": true, "FOO/To Do": true, "FOO/In Review": true,
		"BAR/Resolved": true, "BAR/Open": true,
	}}
	bad, err := ValidateConfigStatuses(testConfig(t), fake)
	if err != nil {
		t.Fatalf("ValidateConfigStatuses: %v", err)
	}
	if len(bad) != 0 {
		t.Fatalf("unexpected bad statuses: %v", bad)
	}
}

func TestValidateConfigStatuses_BadStatus(t *testing.T) {
	fake := &fakeAcli{valid: map[string]bool{
		"FOO/To Do": true, "FOO/In Review": true,
		// "Done" missing -> invalid in FOO
		"BAR/Resolved": true, "BAR/Open": true,
	}}
	bad, err := ValidateConfigStatuses(testConfig(t), fake)
	if err != nil {
		t.Fatalf("ValidateConfigStatuses: %v", err)
	}
	if len(bad) != 1 || bad[0] != "taskDevelopment: Done" {
		t.Fatalf("bad=%v, want [taskDevelopment: Done]", bad)
	}
}

func TestValidateConfigStatuses_DedupesRepeatedNames(t *testing.T) {
	fake := &fakeAcli{valid: map[string]bool{
		"FOO/Done": true, "FOO/To Do": true, "FOO/In Review": true,
		"BAR/Resolved": true, "BAR/Open": true,
	}}
	if _, err := ValidateConfigStatuses(testConfig(t), fake); err != nil {
		t.Fatal(err)
	}
	// "Done" appears as outcome + closeOn in taskDevelopment; validate once per workflow.
	count := 0
	for _, c := range fake.calls {
		if c == "FOO/Done" {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("FOO/Done validated %d times, want 1 (calls=%v)", count, fake.calls)
	}
}

func TestValidateConfigStatuses_JQLMissingProject(t *testing.T) {
	c, err := config.Parse("test", []byte(`
workflows:
  broken:
    jql: assignee = currentUser()
    issueTypes: [Task]
    closeOn: Done
    agents:
      dev:
        handles:
          - status: To Do
            outcomes:
              done: Done
`))
	if err != nil {
		t.Fatal(err)
	}
	fake := &fakeAcli{valid: map[string]bool{}}
	if _, err := ValidateConfigStatuses(c, fake); err == nil {
		t.Fatal("expected error for JQL without project key")
	}
}

func TestBuildJQL_AppendsIssueTypes(t *testing.T) {
	d := New("test", testConfig(t), "repo-id", "my-repo", false)
	got := d.buildJQL("project = FOO", config.StringList{"Task", "Story"})
	want := `(project = FOO) AND issuetype IN ("Task", "Story") AND component = "my-repo" ORDER BY updated`
	if got != want {
		t.Fatalf("buildJQL=%q, want %q", got, want)
	}
	got = d.buildJQL("project = FOO", config.StringList{"Task"})
	want = `(project = FOO) AND issuetype IN ("Task") AND component = "my-repo" ORDER BY updated`
	if got != want {
		t.Fatalf("buildJQL single=%q, want %q", got, want)
	}
}

// fakeReportAcli implements ReportAcli for Report tests.
type fakeReportAcli struct {
	status        string // ticket's current Jira status returned by View
	comments      []string
	transitions   []string
	viewErr       error
	commentErr    error
	transitionErr error
}

func (f *fakeReportAcli) View(key string) (acli.Ticket, error) {
	if f.viewErr != nil {
		return acli.Ticket{}, f.viewErr
	}
	return acli.Ticket{Key: key, Status: f.status}, nil
}

func (f *fakeReportAcli) Comment(key, body string) error {
	if f.commentErr != nil {
		return f.commentErr
	}
	f.comments = append(f.comments, body)
	return nil
}

func (f *fakeReportAcli) Transition(key, status string) error {
	if f.transitionErr != nil {
		return f.transitionErr
	}
	f.transitions = append(f.transitions, status)
	return nil
}

// reportConfig: one agent ("multi") handles two statuses with DIFFERENT
// outcomes per status — the v3 case.
func reportConfig(t *testing.T) *config.Config {
	t.Helper()
	c, err := config.Parse("test", []byte(`
workflows:
  taskDevelopment:
    jql: project = FOO
    issueTypes: [Task]
    closeOn: Done
    agents:
      multi:
        handles:
          - status: To Do
            outcomes:
              done: In Progress
              blocked: To Do
          - status: In Review
            outcomes:
              done: Done
              blocked: To Do
`))
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func TestReport_PerStatusOutcome(t *testing.T) {
	cfg := reportConfig(t)

	// Same agent, same reported status "done" — target depends on the
	// ticket's CURRENT Jira status.
	fake := &fakeReportAcli{status: "To Do"}
	res, err := Report(cfg, fake, "taskDevelopment", "FOO-1", "multi", "done", "did work")
	if err != nil {
		t.Fatalf("Report: %v", err)
	}
	if res.Action != "transitioned" || res.Detail != "In Progress" {
		t.Fatalf("from To Do: got %+v, want transitioned->In Progress", res)
	}

	fake2 := &fakeReportAcli{status: "In Review"}
	res, err = Report(cfg, fake2, "taskDevelopment", "FOO-1", "multi", "done", "did work")
	if err != nil {
		t.Fatalf("Report: %v", err)
	}
	if res.Action != "transitioned" || res.Detail != "Done" {
		t.Fatalf("from In Review: got %+v, want transitioned->Done", res)
	}
}

func TestReport_SelfLoopSkipsTransition(t *testing.T) {
	cfg := reportConfig(t)
	fake := &fakeReportAcli{status: "To Do"}
	res, err := Report(cfg, fake, "taskDevelopment", "FOO-1", "multi", "blocked", "stuck")
	if err != nil {
		t.Fatalf("Report: %v", err)
	}
	if res.Action != "transitioned" || res.Detail != "To Do" {
		t.Fatalf("got %+v, want transitioned->To Do (self-loop)", res)
	}
	if len(fake.transitions) != 0 {
		t.Fatalf("self-loop must not call Transition, got %v", fake.transitions)
	}
	if len(fake.comments) != 1 {
		t.Fatalf("self-loop must still comment, got %v", fake.comments)
	}
}

func TestReport_InvalidStatusNudges(t *testing.T) {
	cfg := reportConfig(t)
	fake := &fakeReportAcli{status: "To Do"}
	res, err := Report(cfg, fake, "taskDevelopment", "FOO-1", "multi", "bogus", "x")
	if err != nil {
		t.Fatalf("Report: %v", err)
	}
	if res.Action != "nudged" {
		t.Fatalf("got %+v, want nudged", res)
	}
	if !strings.Contains(res.Detail, "done") || !strings.Contains(res.Detail, "blocked") {
		t.Fatalf("nudge must list valid statuses, got %q", res.Detail)
	}
	if len(fake.transitions) != 0 || len(fake.comments) != 0 {
		t.Fatal("invalid status must not comment or transition")
	}
}

func TestReport_TransitionError(t *testing.T) {
	cfg := reportConfig(t)
	fake := &fakeReportAcli{status: "To Do", transitionErr: fmt.Errorf("no such transition")}
	res, err := Report(cfg, fake, "taskDevelopment", "FOO-1", "multi", "done", "x")
	if err == nil || res.Action != "error" {
		t.Fatalf("got %+v err=%v, want error action", res, err)
	}
	if len(fake.comments) != 1 {
		t.Fatal("comment must land before transition is attempted")
	}
}

func TestValidateConfigStatuses_SortedOutput(t *testing.T) {
	fake := &fakeAcli{valid: map[string]bool{}} // everything invalid
	bad, err := ValidateConfigStatuses(testConfig(t), fake)
	if err != nil {
		t.Fatal(err)
	}
	if !sort.StringsAreSorted(bad) {
		t.Fatalf("bad list not sorted: %v", bad)
	}
	for _, b := range bad {
		if !strings.Contains(b, ": ") {
			t.Fatalf("bad entry %q missing workflow prefix", b)
		}
	}
}
