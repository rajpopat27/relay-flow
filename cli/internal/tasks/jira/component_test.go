package jira

import (
	"strings"
	"testing"
)

func TestJQLIncludesComponent(t *testing.T) {
	j, err := newJira(JiraConfig{Query: "project = GHCOS", IssueTypes: []string{"Task"}}, "wf", testNodes, "Raj Popat", "raj-test-repo", nil)
	if err != nil {
		t.Fatalf("%v", err)
	}
	if !strings.Contains(j.jql, `component = "raj-test-repo"`) {
		t.Errorf("JQL missing component filter: %q", j.jql)
	}
}
