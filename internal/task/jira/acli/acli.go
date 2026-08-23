// Package acli wraps the Atlassian `acli` CLI behind a small fakeable
// interface. Every call is real; there is no dry-run mode.
package acli

import (
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"strings"
)

// Client is the fakeable Jira CLI seam used by the Jira task adapter.
type Client interface {
	// Search runs a JQL query and returns the raw Jira search JSON
	// ({"issues":[...]}).
	Search(ctx context.Context, jql string) ([]byte, error)
	// View returns the raw Jira issue JSON for one key.
	View(ctx context.Context, key string) ([]byte, error)
	// CreateSubtask creates a child work item and returns its id and key.
	CreateSubtask(ctx context.Context, parentKey, title, description string) (id, key string, err error)
	// Transition moves a work item to a status.
	Transition(ctx context.Context, key, status string) error
	// EnsureLabel adds the label when absent.
	EnsureLabel(ctx context.Context, key, label string) error
	// UpdateDescription replaces a work item's description.
	UpdateDescription(ctx context.Context, key, description string) error
	// ListComments returns the work item's comment bodies.
	ListComments(ctx context.Context, key string) ([]string, error)
	// AddComment posts a comment.
	AddComment(ctx context.Context, key, body string) error
}

// CLI is the production Client backed by the acli binary.
type CLI struct{}

func New() *CLI { return &CLI{} }

func (CLI) Search(ctx context.Context, jql string) ([]byte, error) {
	return runOut(ctx, "jira", "workitem", "search", "--jql", jql, "--json")
}

func (CLI) View(ctx context.Context, key string) ([]byte, error) {
	return runOut(ctx, "jira", "workitem", "view", key,
		"--fields", "summary,description,status,components,issuetype,parent,labels,comment,assignee,subtasks",
		"--json")
}

func (CLI) CreateSubtask(ctx context.Context, parentKey, title, description string) (string, string, error) {
	out, err := runOut(ctx, "jira", "workitem", "create",
		"--type", "Subtask", "--parent", parentKey,
		"--summary", title, "--body", description, "--json")
	if err != nil {
		return "", "", err
	}
	var env struct {
		ID  string `json:"id"`
		Key string `json:"key"`
	}
	if err := json.Unmarshal(out, &env); err != nil {
		return "", "", fmt.Errorf("acli create subtask: parse json: %w", err)
	}
	return env.ID, env.Key, nil
}

func (CLI) Transition(ctx context.Context, key, status string) error {
	return runChecked(ctx, "jira", "workitem", "transition", "--key", key, "--status", status, "--yes", "--json")
}

func (c CLI) EnsureLabel(ctx context.Context, key, label string) error {
	raw, err := c.View(ctx, key)
	if err != nil {
		return err
	}
	var v struct {
		Fields struct {
			Labels []string `json:"labels"`
		} `json:"fields"`
	}
	if err := json.Unmarshal(raw, &v); err != nil {
		return fmt.Errorf("acli view %s: parse labels: %w", key, err)
	}
	for _, l := range v.Fields.Labels {
		if l == label {
			return nil
		}
	}
	all := append(append([]string{}, v.Fields.Labels...), label)
	return runChecked(ctx, "jira", "workitem", "edit", "--key", key, "--labels", strings.Join(all, ","), "--yes", "--json")
}

func (CLI) UpdateDescription(ctx context.Context, key, description string) error {
	return runChecked(ctx, "jira", "workitem", "edit", "--key", key, "--body", description, "--yes", "--json")
}

func (CLI) ListComments(ctx context.Context, key string) ([]string, error) {
	raw, err := runOut(ctx, "jira", "workitem", "view", key, "--fields", "comment", "--json")
	if err != nil {
		return nil, err
	}
	var v struct {
		Fields struct {
			Comment struct {
				Comments []struct {
					Body string `json:"body"`
				} `json:"comments"`
			} `json:"comment"`
		} `json:"fields"`
	}
	if err := json.Unmarshal(raw, &v); err != nil {
		return nil, fmt.Errorf("acli comments %s: parse json: %w", key, err)
	}
	var out []string
	for _, c := range v.Fields.Comment.Comments {
		out = append(out, c.Body)
	}
	return out, nil
}

func (CLI) AddComment(ctx context.Context, key, body string) error {
	return runChecked(ctx, "jira", "workitem", "comment", "create", "--key", key, "--body", body, "--json")
}

func runOut(ctx context.Context, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, "acli", args...)
	var stderr strings.Builder
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("acli %v: %w: %s", args, err, strings.TrimSpace(stderr.String()))
	}
	return out, nil
}

// runChecked runs a mutating acli command. acli exits 0 even when the
// operation fails server-side, so the JSON envelope is inspected.
func runChecked(ctx context.Context, args ...string) error {
	out, err := runOut(ctx, args...)
	if err != nil {
		return err
	}
	var env struct {
		Results []struct {
			Status  string `json:"status"`
			Message string `json:"message"`
			ID      string `json:"id"`
		} `json:"results"`
	}
	if jsonErr := json.Unmarshal(out, &env); jsonErr == nil && env.Results != nil {
		for _, r := range env.Results {
			if !strings.EqualFold(r.Status, "SUCCESS") {
				return fmt.Errorf("acli %v: %s: %s", args, r.ID, r.Message)
			}
		}
	}
	return nil
}
