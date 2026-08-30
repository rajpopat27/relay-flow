package jira

import (
	"context"
	"os"
	"strings"
	"testing"

	"github.com/rajpopat27/relay-flow/internal/config"
	"github.com/rajpopat27/relay-flow/internal/task"
)

// 3.13: filter tests per specs/repo-workflow-routing "Workflow filters are
// structured and locally evaluable": the adapter compiles workflow
// taskConfig.filters into matchers over normalized ticket fields; matching
// is in-memory against one repo poll batch (no per-workflow re-query).
// Package-local so it can use the adapter's test seams without exporting
// production API.

// pollCountingSystem wraps the adapter's Poll to prove matchers never
// trigger another fetch.
type pollCountingSystem struct {
	task.System
	polls int
}

func (p *pollCountingSystem) Poll(ctx context.Context) ([]task.Ticket, error) {
	p.polls++
	return p.System.Poll(ctx)
}

func TestCompileFilterMatchesNormalizedFields(t *testing.T) {
	sys := newSystemWithFake(t, &fakeJira{})

	match, err := sys.CompileFilter(config.RawValues{
		"filters": map[string]any{
			"parentStatuses": []any{"To Do"},
			"issueTypes":     []any{"Task"},
			"labels":         []any{"coding"},
		},
	})
	if err != nil {
		t.Fatalf("CompileFilter failed: %v", err)
	}

	ticket := task.Ticket{
		ID: "1", Key: "PAY-101",
		Fields: map[string]any{
			"status":    "To Do",
			"issueType": "Task",
			"labels":    []string{"coding", "backend"},
		},
	}
	if !match(ticket) {
		t.Fatal("matching ticket rejected by compiled filter")
	}
}

func TestCompileFilterRejectsNonMatching(t *testing.T) {
	sys := newSystemWithFake(t, &fakeJira{})
	match, err := sys.CompileFilter(config.RawValues{
		"filters": map[string]any{"parentStatuses": []any{"To Do"}},
	})
	if err != nil {
		t.Fatalf("CompileFilter failed: %v", err)
	}
	for _, tc := range []struct {
		name   string
		fields map[string]any
	}{
		{"wrong status", map[string]any{"status": "Done", "issueType": "Task"}},
		{"missing status", map[string]any{"issueType": "Task"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if match(task.Ticket{Key: "PAY-1", Fields: tc.fields}) {
				t.Fatal("non-matching ticket accepted")
			}
		})
	}
}

func TestCompileFilterRejectsUnknownField(t *testing.T) {
	sys := newSystemWithFake(t, &fakeJira{})
	if _, err := sys.CompileFilter(config.RawValues{
		"filters": map[string]any{"jql": "project = PAY"},
	}); err == nil {
		t.Fatal("arbitrary query/filter field accepted; workflow validation must reject it")
	}
}

func TestMatchingIsInMemoryNoRequery(t *testing.T) {
	// One repo poll fetches the batch; the compiled matcher then evaluates
	// every ticket in memory with no per-workflow re-query.
	fake := &fakeJira{searchJSON: []byte(`[{"id":"1","key":"PAY-1","fields":{"summary":"a","status":{"name":"To Do"},"issuetype":{"name":"Task"},"labels":[]}},{"id":"2","key":"PAY-2","fields":{"summary":"b","status":{"name":"Done"},"issuetype":{"name":"Task"},"labels":[]}},{"id":"3","key":"PAY-3","fields":{"summary":"c","status":{"name":"To Do"},"issuetype":{"name":"Task"},"labels":[]}}]`)}
	base := newSystemWithFake(t, fake)
	counting := &pollCountingSystem{System: base}

	match, err := counting.CompileFilter(config.RawValues{
		"filters": map[string]any{"parentStatuses": []any{"To Do"}},
	})
	if err != nil {
		t.Fatal(err)
	}

	// The single repo poll.
	batch, err := counting.Poll(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if counting.polls != 1 {
		t.Fatalf("polls = %d, want exactly 1 repo poll", counting.polls)
	}
	if len(batch) == 0 {
		t.Fatal("poll returned no batch to match against")
	}

	// Matching the batch must not trigger another poll.
	var matched int
	for _, tk := range batch {
		if match(tk) {
			matched++
		}
	}
	if counting.polls != 1 {
		t.Fatalf("matching triggered %d extra task-system polls; matching must be in-memory", counting.polls-1)
	}
}

func TestJiraJSONNormalization(t *testing.T) {
	// The adapter normalizes Jira search JSON (status, issue type, labels,
	// assignee) into task.Ticket.Fields. Package-local so it can call the
	// adapter's unexported normalization directly.
	// The REST boundary supplies a normalized array of issue objects, and the
	// normalized assignee is the user's email address — the stable identity
	// workflow filters match on, not the human-readable display name.
	raw := []byte(`[{"id":"1","key":"PAY-101","fields":{"summary":"parent","status":{"name":"To Do"},"issuetype":{"name":"Task"},"labels":["coding"],"assignee":{"displayName":"Relay Bot","emailAddress":"relay@bot"}}}]`)

	tickets, err := normalizeSearchResponse(raw)
	if err != nil {
		t.Fatalf("normalization failed: %v", err)
	}
	if len(tickets) != 1 {
		t.Fatalf("tickets = %d, want 1", len(tickets))
	}
	tk := tickets[0]
	if tk.Fields["status"] != "To Do" {
		t.Fatalf("status = %v", tk.Fields["status"])
	}
	if tk.Fields["issueType"] != "Task" {
		t.Fatalf("issueType = %v", tk.Fields["issueType"])
	}
	labels, ok := tk.Fields["labels"].([]string)
	if !ok || len(labels) != 1 || labels[0] != "coding" {
		t.Fatalf("labels = %v", tk.Fields["labels"])
	}
	// Raw Jira assignee object is normalized to its email address.
	if tk.Fields["assignee"] != "relay@bot" {
		t.Fatalf("assignee = %v, want normalized email address", tk.Fields["assignee"])
	}
}

func TestJiraJSONNormalizationFiltersOnlyOpenInwardBlockers(t *testing.T) {
	raw := []byte(`[
		{"id":"1","key":"PAY-1","fields":{"summary":"open blocker","status":{"name":"To Do"},"issuetype":{"name":"Task"},"labels":[],"issuelinks":[
			{"type":{"name":"Blocks"},"inwardIssue":{"key":"OTHER-1","fields":{"status":{"statusCategory":{"key":"done"}}}}},
			{"type":{"name":"Blocks"},"inwardIssue":{"key":"OTHER-2","fields":{"status":{"statusCategory":{"key":"new"}}}}}
		]}},
		{"id":"2","key":"PAY-2","fields":{"summary":"closed blockers","status":{"name":"To Do"},"issuetype":{"name":"Task"},"labels":[],"issuelinks":[
			{"type":{"name":"Blocks"},"inwardIssue":{"key":"OTHER-3","fields":{"status":{"statusCategory":{"key":"done"}}}}}
		]}},
		{"id":"3","key":"PAY-3","fields":{"summary":"blocks another","status":{"name":"To Do"},"issuetype":{"name":"Task"},"labels":[],"issuelinks":[
			{"type":{"name":"Blocks"},"outwardIssue":{"key":"OTHER-4","fields":{"status":{"statusCategory":{"key":"new"}}}}}
		]}}
	]`)
	tickets, err := normalizeSearchResponse(raw)
	if err != nil {
		t.Fatal(err)
	}
	if len(tickets) != 2 || tickets[0].Key != "PAY-2" || tickets[1].Key != "PAY-3" {
		t.Fatalf("eligible tickets = %+v, want PAY-2 and PAY-3", tickets)
	}
}

func TestJiraJSONNormalizationParsesCapturedIssueShape(t *testing.T) {
	// Preserve the captured issue-field fixture while the REST client contract
	// itself is exercised through strict HTTP tests.
	raw, err := os.ReadFile("testdata/jira_search_issues.json")
	if err != nil {
		t.Fatalf("read Jira fixture: %v", err)
	}
	tickets, err := normalizeSearchResponse(raw)
	if err != nil {
		t.Fatalf("normalization of captured Jira output failed: %v", err)
	}
	if len(tickets) == 0 {
		t.Fatal("captured Jira fixture yielded no tickets")
	}
	for _, tk := range tickets {
		if tk.Key == "" || tk.ID == "" {
			t.Fatalf("ticket missing id/key: %+v", tk)
		}
		if tk.Fields["status"] == "" || tk.Fields["issueType"] == "" {
			t.Fatalf("ticket %s missing status/issueType: %+v", tk.Key, tk.Fields)
		}
		if _, ok := tk.Fields["labels"].([]string); !ok {
			t.Fatalf("ticket %s labels not normalized to []string: %+v", tk.Key, tk.Fields)
		}
		// The fixture is captured with assignee=currentUser(), so every
		// entry carries an email-form normalized assignee.
		a, _ := tk.Fields["assignee"].(string)
		if a == "" || !strings.Contains(a, "@") {
			t.Fatalf("ticket %s assignee = %q, want an email address", tk.Key, a)
		}
	}
}

func TestCompileFilterAssigneeMatchesEmail(t *testing.T) {
	// 9.9: workflow assignee filters match the normalized EMAIL identity,
	// not the display name. e2e workflow.yaml filters on the user's email.
	sys := newSystemWithFake(t, &fakeJira{})
	match, err := sys.CompileFilter(config.RawValues{
		"filters": map[string]any{"assignees": []any{"raj.popat@example.com"}},
	})
	if err != nil {
		t.Fatalf("CompileFilter failed: %v", err)
	}
	if !match(task.Ticket{Key: "PAY-1", Fields: map[string]any{"assignee": "raj.popat@example.com"}}) {
		t.Fatal("email assignee rejected")
	}
	if match(task.Ticket{Key: "PAY-1", Fields: map[string]any{"assignee": "Raj Popat"}}) {
		t.Fatal("display name accepted; filters must match the normalized email")
	}
}

func TestCompileFilterAssigneeMatchAndMismatch(t *testing.T) {
	// Assignee is a supported structured filter dimension
	// (repo-workflow-routing: "parent statuses, issue types, labels, and
	// assignee"); it matches the normalized ticket's assignee field.
	// NOTE: the filter key `assignees` follows the same naming as
	// parentStatuses/issueTypes/labels (plural); the docs do not pin the key.
	sys := newSystemWithFake(t, &fakeJira{})
	match, err := sys.CompileFilter(config.RawValues{
		"filters": map[string]any{"assignees": []any{"relay-bot@example.com"}},
	})
	if err != nil {
		t.Fatalf("CompileFilter failed: %v", err)
	}
	if !match(task.Ticket{Key: "PAY-1", Fields: map[string]any{"assignee": "relay-bot@example.com"}}) {
		t.Fatal("matching assignee rejected")
	}
	if match(task.Ticket{Key: "PAY-1", Fields: map[string]any{"assignee": "someone-else@example.com"}}) {
		t.Fatal("non-matching assignee accepted")
	}
}
