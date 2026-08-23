package jira

import (
	"encoding/json"
	"fmt"

	"github.com/rajpopat27/relay-flow/internal/task"
)

// rawSearchResponse matches the Jira search JSON envelope Poll consumes.
type rawSearchResponse struct {
	Issues []rawIssue `json:"issues"`
}

type rawIssue struct {
	ID     string `json:"id"`
	Key    string `json:"key"`
	Fields struct {
		Summary string `json:"summary"`
		Status  struct {
			Name string `json:"name"`
		} `json:"status"`
		IssueType struct {
			Name string `json:"name"`
		} `json:"issuetype"`
		Labels   []string `json:"labels"`
		Assignee *struct {
			DisplayName string `json:"displayName"`
		} `json:"assignee"`
		Subtasks []struct {
			ID     string `json:"id"`
			Key    string `json:"key"`
			Fields struct {
				Summary string `json:"summary"`
			} `json:"fields"`
		} `json:"subtasks"`
	} `json:"fields"`
}

// normalizeSearchResponse converts raw Jira search JSON into normalized
// parent tickets: status, issueType, labels, and assignee (display name)
// become plain Fields entries. Subtasks are never returned as parents.
func normalizeSearchResponse(raw []byte) ([]task.Ticket, error) {
	var resp rawSearchResponse
	if err := json.Unmarshal(raw, &resp); err != nil {
		return nil, fmt.Errorf("jira search: parse json: %w", err)
	}
	out := make([]task.Ticket, 0, len(resp.Issues))
	for _, issue := range resp.Issues {
		fields := map[string]any{
			"status":    issue.Fields.Status.Name,
			"issueType": issue.Fields.IssueType.Name,
			"labels":    append([]string{}, issue.Fields.Labels...),
		}
		if issue.Fields.Assignee != nil {
			fields["assignee"] = issue.Fields.Assignee.DisplayName
		}
		out = append(out, task.Ticket{
			ID:             issue.ID,
			Key:            issue.Key,
			Title:          issue.Fields.Summary,
			WorkflowClaims: claimLabels(issue.Fields.Labels),
			Fields:         fields,
		})
	}
	return out, nil
}

func claimLabels(labels []string) []string {
	var out []string
	for _, l := range labels {
		if len(l) > 3 && l[:3] == "wf:" {
			out = append(out, l)
		}
	}
	return out
}

// labelsOf extracts label strings from a raw Jira issue view.
func labelsOf(raw []byte) ([]string, error) {
	var issue rawIssue
	if err := json.Unmarshal(raw, &issue); err != nil {
		return nil, fmt.Errorf("jira view: parse json: %w", err)
	}
	return issue.Fields.Labels, nil
}

// subtasksOf maps existing subtask titles (<ticket>:<node>) to mailboxes.
func subtasksOf(raw []byte) (map[string]task.Mailbox, error) {
	var issue rawIssue
	if err := json.Unmarshal(raw, &issue); err != nil {
		return nil, fmt.Errorf("jira view: parse json: %w", err)
	}
	out := map[string]task.Mailbox{}
	for _, st := range issue.Fields.Subtasks {
		out[st.Fields.Summary] = task.Mailbox{ID: st.ID, Key: st.Key}
	}
	return out, nil
}
