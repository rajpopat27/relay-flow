package jira

import (
	"encoding/json"
	"fmt"

	"github.com/rajpopat27/relay-flow/internal/task"
)

// rawIssue matches one entry in acli's search output. acli emits a BARE
// ARRAY of these (no {"issues":[...]} REST envelope) — the adapter owns
// the acli wire contract, not the REST API's.
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
			DisplayName  string `json:"displayName"`
			EmailAddress string `json:"emailAddress"`
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

// normalizeSearchResponse converts raw acli search JSON (a bare array of
// issue objects) into normalized parent tickets: status, issueType, labels,
// and assignee become plain Fields entries. Assignee is normalized to the
// user's email address — the stable, machine-comparable identity workflow
// filters match against (displayName is human-readable, not an identifier).
// Subtasks are never returned as parents.
func normalizeSearchResponse(raw []byte) ([]task.Ticket, error) {
	var issues []rawIssue
	if err := json.Unmarshal(raw, &issues); err != nil {
		return nil, fmt.Errorf("jira search: parse json: %w", err)
	}
	out := make([]task.Ticket, 0, len(issues))
	for _, issue := range issues {
		fields := map[string]any{
			"status":    issue.Fields.Status.Name,
			"issueType": issue.Fields.IssueType.Name,
			"labels":    append([]string{}, issue.Fields.Labels...),
		}
		if issue.Fields.Assignee != nil {
			fields["assignee"] = issue.Fields.Assignee.EmailAddress
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
