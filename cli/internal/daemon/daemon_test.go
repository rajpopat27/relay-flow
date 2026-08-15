package daemon

import (
	"fmt"
	"sort"
	"strings"
	"testing"

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
    closeOn: Done
    agents:
      dev:
        handles: [To Do]
        outcomes:
          done: In Review
      reviewer:
        handles: [In Review]
        outcomes:
          approved: Done
  incidentResponse:
    jql: project = BAR
    closeOn: [Resolved]
    agents:
      responder:
        handles: [Open]
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
    closeOn: Done
    agents:
      dev:
        handles: [To Do]
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
