package jira

import (
	"strings"
	"testing"
)

func TestDistributedJQLHasAssignee(t *testing.T) {
	j, err := newJira(JiraConfig{Query: "project = GHCOS", IssueTypes: []string{"Task"}}, "wf", testNodes, "Raj Popat", "raj-test-repo", nil)
	if err != nil {
		t.Fatalf("%v", err)
	}
	if !strings.Contains(j.jql, `assignee = "Raj Popat"`) {
		t.Errorf("distributed JQL missing assignee: %q", j.jql)
	}
}
