package jira

import (
	"strings"
	"testing"
)

func TestJQLIncludesComponent(t *testing.T) {
	j, err := newJira(JiraConfig{Query: "project = XYZ", IssueTypes: []string{"Task"}}, "wf", testNodes, "Jane Doe", "xyz-repo", nil)
	if err != nil {
		t.Fatalf("%v", err)
	}
	if !strings.Contains(j.jql, `component = "xyz-repo"`) {
		t.Errorf("JQL missing component filter: %q", j.jql)
	}
}
