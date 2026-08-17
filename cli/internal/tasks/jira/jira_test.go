package jira

import (
	"strings"
	"testing"

	"github.com/rajpopat27/relayflow/cli/internal/acli"
	"github.com/rajpopat27/relayflow/cli/internal/config"
	"github.com/rajpopat27/relayflow/cli/internal/tasks"
)

var testNodes = map[string]config.Node{
	"coding":    {Agent: "build", When: "In Progress", OnSuccess: "reviewing", OnFailure: "coding"},
	"reviewing": {Agent: "build", When: "In Review", OnSuccess: "done", OnFailure: "coding"},
	"done":      {When: "Done"},
}

func TestClaimLabel(t *testing.T) {
	if got := claimLabel("xyzTaskFlow"); got != "wf:xyzTaskFlow" {
		t.Errorf("claimLabel = %q", got)
	}
}

func TestBuildJQL(t *testing.T) {
	cfg := JiraConfig{Query: "project = xyz", IssueTypes: []string{"Task"}}
	j, err := newJira(cfg, "wf", testNodes, "Jane Doe", "repo:xyz", nil)
	if err != nil {
		t.Fatalf("%v", err)
	}
	want := `(project = xyz) AND issuetype IN ("Task") AND component = "repo:xyz" AND assignee = "Jane Doe" ORDER BY updated`
	if j.jql != want {
		t.Errorf("jql =\n  %q\nwant\n  %q", j.jql, want)
	}
}

func TestBuildJQLCentralizedSkipsAssignee(t *testing.T) {
	cfg := JiraConfig{Query: "project = xyz", IssueTypes: []string{"Task", "Story"}, AssigneeIsAgent: true}
	j, err := newJira(cfg, "wf", testNodes, "", "repo:xyz", nil)
	if err != nil {
		t.Fatalf("%v", err)
	}
	if strings.Contains(j.jql, "assignee") {
		t.Errorf("assigneeIsAgent must skip assignee clause: %q", j.jql)
	}
	if !strings.Contains(j.jql, `"Task", "Story"`) {
		t.Errorf("issueTypes list: %q", j.jql)
	}
}

func TestConfigValidation(t *testing.T) {
	base := map[string]any{"query": "project = xyz", "issueTypes": []any{"Task"}}
	goodAny, err := unmarshalConfig(base)
	if err != nil {
		t.Fatalf("valid config rejected: %v", err)
	}
	good := goodAny.(JiraConfig)
	if good.Query != "project = xyz" || len(good.IssueTypes) != 1 {
		t.Errorf("%+v", good)
	}

	bad := []struct {
		name string
		mut  func(map[string]any)
		want string
	}{
		{"empty query", func(m map[string]any) { m["query"] = "" }, "query"},
		{"no issueTypes", func(m map[string]any) { delete(m, "issueTypes") }, "issueTypes"},
		{"ORDER BY in query", func(m map[string]any) { m["query"] = "project = xyz ORDER BY rank" }, "ORDER BY"},
		{"issuetype in query", func(m map[string]any) { m["query"] = "project = xyz AND issuetype = Bug" }, "issuetype"},
		{"assignee in query", func(m map[string]any) { m["query"] = "project = xyz AND assignee = bob" }, "assignee"},
		{"unknown field", func(m map[string]any) { m["bogus"] = 1 }, "bogus"},
	}
	for _, tc := range bad {
		t.Run(tc.name, func(t *testing.T) {
			m := map[string]any{"query": "project = xyz", "issueTypes": []any{"Task"}}
			tc.mut(m)
			if _, err := unmarshalConfig(m); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Errorf("err = %v, want mention %q", err, tc.want)
			}
		})
	}
}

type fakeAcli struct {
	tickets       []acli.Ticket
	labelsAdded   []string
	transitions   []string
	comments      []string
	views         map[string]acli.Ticket
	transitionErr error
}

func (f *fakeAcli) Search(jql string) ([]acli.Ticket, error) { return f.tickets, nil }
func (f *fakeAcli) AddLabel(key string, existing []string, label string) error {
	f.labelsAdded = append(f.labelsAdded, key+":"+label)
	return nil
}
func (f *fakeAcli) Transition(key, status string) error {
	f.transitions = append(f.transitions, key+":"+status)
	return f.transitionErr
}
func (f *fakeAcli) Comment(key, body string) error {
	f.comments = append(f.comments, key+":"+body)
	return nil
}
func (f *fakeAcli) View(key string) (acli.Ticket, error) {
	t, ok := f.views[key]
	if !ok {
		t = acli.Ticket{Key: key}
	}
	return t, nil
}

func mustJira(t *testing.T, ac aclier) *jiraTasks {
	t.Helper()
	j, err := newJira(JiraConfig{Query: "project = xyz", IssueTypes: []string{"Task"}}, "wf", testNodes, "", "repo:xyz", ac)
	if err != nil {
		t.Fatalf("%v", err)
	}
	return j
}

func TestListMapsStateToNode(t *testing.T) {
	fa := &fakeAcli{tickets: []acli.Ticket{
		{Key: "XYZ-1", Summary: "s", Status: "In Progress"},
		{Key: "XYZ-2", Summary: "s", Status: "In Review", Labels: []string{"wf:otherFlow"}},
		{Key: "XYZ-3", Summary: "s", Status: "Backlog"},
	}}
	j := mustJira(t, fa)
	got, err := j.List()
	if err != nil {
		t.Fatalf("%v", err)
	}
	if len(got) != 3 {
		t.Fatalf("len = %d", len(got))
	}
	if got[0].Node != "coding" || got[0].ClaimedBy != "" {
		t.Errorf("t1 = %+v", got[0])
	}
	if got[1].Node != "reviewing" || got[1].ClaimedBy != "otherFlow" {
		t.Errorf("t2 = %+v", got[1])
	}
	if got[2].Node != "" {
		t.Errorf("unmapped state must yield empty Node: %+v", got[2])
	}
}

func TestClaim(t *testing.T) {
	fa := &fakeAcli{views: map[string]acli.Ticket{"XYZ-1": {Key: "XYZ-1", Labels: []string{"existing"}}}}
	j := mustJira(t, fa)
	err := j.Claim(tasksTicket("XYZ-1"))
	if err != nil {
		t.Fatalf("%v", err)
	}
	if len(fa.labelsAdded) != 1 || fa.labelsAdded[0] != "XYZ-1:wf:wf" {
		t.Errorf("labelsAdded = %v", fa.labelsAdded)
	}
}

func TestReportTransitions(t *testing.T) {
	fa := &fakeAcli{views: map[string]acli.Ticket{
		"XYZ-1": {Key: "XYZ-1", Status: "In Progress"},
	}}
	j := mustJira(t, fa)
	err := j.Report(tasksTicket("XYZ-1"), "success", "reviewing", "did the thing")
	if err != nil {
		t.Fatalf("%v", err)
	}
	if len(fa.transitions) != 1 || fa.transitions[0] != "XYZ-1:In Review" {
		t.Errorf("transitions = %v", fa.transitions)
	}
	if len(fa.comments) != 1 || !strings.Contains(fa.comments[0], "did the thing") {
		t.Errorf("comments = %v", fa.comments)
	}
}

func TestReportSelfLoopCommentsOnly(t *testing.T) {
	fa := &fakeAcli{views: map[string]acli.Ticket{
		"XYZ-1": {Key: "XYZ-1", Status: "In Progress"},
	}}
	j := mustJira(t, fa)
	// coding onFailure = coding: target state == current state.
	err := j.Report(tasksTicket("XYZ-1"), "failure", "coding", "still broken")
	if err != nil {
		t.Fatalf("%v", err)
	}
	if len(fa.transitions) != 0 {
		t.Errorf("self-loop must not transition: %v", fa.transitions)
	}
	if len(fa.comments) != 1 {
		t.Errorf("self-loop must comment: %v", fa.comments)
	}
}

func TestReportUnknownTargetNode(t *testing.T) {
	fa := &fakeAcli{views: map[string]acli.Ticket{"XYZ-1": {Key: "XYZ-1", Status: "In Progress"}}}
	j := mustJira(t, fa)
	if err := j.Report(tasksTicket("XYZ-1"), "success", "nowhere", "x"); err == nil ||
		!strings.Contains(err.Error(), "unknown node") {
		t.Errorf("err = %v", err)
	}
}

func TestProjectKeyFromQuery(t *testing.T) {
	got, err := ProjectKeyFromQuery("project = xyz AND foo = bar")
	if err != nil || got != "xyz" {
		t.Errorf("got %q err %v", got, err)
	}
	got, err = ProjectKeyFromQuery(`PROJECT = "ABC"`)
	if err != nil || got != "ABC" {
		t.Errorf("got %q err %v", got, err)
	}
	if _, err = ProjectKeyFromQuery("foo = bar"); err == nil {
		t.Error("expected error for missing project")
	}
}

func TestValidateStates(t *testing.T) {
	fa := &fakeValidator{good: map[string]bool{"In Progress": true, "In Review": true}}
	bad, err := ValidateStates(fa, testNodes, "xyz")
	if err != nil {
		t.Fatalf("%v", err)
	}
	if len(bad) != 1 || bad[0] != "Done" {
		t.Errorf("bad = %v", bad)
	}
}

type fakeValidator struct{ good map[string]bool }

func (f *fakeValidator) ValidateStatus(projectKey, status string) error {
	if f.good[status] {
		return nil
	}
	return errInvalidStatus
}

var errInvalidStatus = errorString("invalid status")

type errorString string

func (e errorString) Error() string { return string(e) }

func tasksTicket(key string) tasks.Ticket { return tasks.Ticket{Key: key} }

func TestReportTransitionFailurePropagates(t *testing.T) {
	fa := &fakeAcli{
		views:         map[string]acli.Ticket{"XYZ-1": {Key: "XYZ-1", Status: "In Progress"}},
		transitionErr: errorString("No allowed transitions found for given status"),
	}
	j := mustJira(t, fa)
	err := j.Report(tasks.Ticket{Key: "XYZ-1"}, "success", "reviewing", "x")
	if err == nil || !strings.Contains(err.Error(), "No allowed transitions") {
		t.Errorf("transition failure must propagate, got %v", err)
	}
	if len(fa.comments) != 0 {
		t.Errorf("no comment on failed transition: %v", fa.comments)
	}
}
