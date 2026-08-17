package jira

import (
	"strings"
	"testing"
)

func TestDistributedJQLHasAssignee(t *testing.T) {
	j, err := newJira(JiraConfig{Query: "project = XYZ", IssueTypes: []string{"Task"}}, "wf", testNodes, "Jane Doe", "xyz-repo", nil)
	if err != nil {
		t.Fatalf("%v", err)
	}
	if !strings.Contains(j.jql, `assignee = "Jane Doe"`) {
		t.Errorf("distributed JQL missing assignee: %q", j.jql)
	}
}
