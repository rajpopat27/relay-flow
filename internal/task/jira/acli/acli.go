// Package acli wraps the Atlassian `acli` CLI behind a small fakeable
// interface. Every call is real; there is no dry-run mode.
//
// 9.5 external-call logging: each CLI method emits one debug line BEFORE
// the call (operation, key, transition target / jql as applicable) and one
// info line AFTER with only the outcome (ok/error), never the payload.
package acli

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os/exec"
	"strings"
)

// Client is the fakeable Jira CLI seam used by the Jira task adapter.
type Client interface {
	// Search runs a JQL query and returns the raw acli search JSON — a
	// BARE ARRAY of issue objects (acli's wire shape, not the Jira REST
	// {"issues":[...]} envelope).
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
	slog.Debug("jira call", "op", "search", "jql", jql)
	// acli's default search fields omit labels; request every field the
	// normalizer consumes (subtasks are not searchable via --fields).
	out, err := runOut(ctx, "jira", "workitem", "search",
		"--jql", jql,
		"--fields", "key,summary,status,issuetype,labels,assignee",
		"--json")
	logOutcome("search", "", err)
	return out, err
}

func (CLI) View(ctx context.Context, key string) ([]byte, error) {
	slog.Debug("jira call", "op", "view", "key", key)
	out, err := runOut(ctx, "jira", "workitem", "view", key,
		"--fields", "summary,description,status,components,issuetype,parent,labels,comment,assignee,subtasks",
		"--json")
	logOutcome("view", key, err)
	return out, err
}

func (CLI) CreateSubtask(ctx context.Context, parentKey, title, description string) (string, string, error) {
	slog.Debug("jira call", "op", "create-subtask", "key", parentKey, "title", title)
	out, err := runOut(ctx, "jira", "workitem", "create",
		"--type", "Subtask", "--parent", parentKey,
		"--summary", title, "--body", description, "--json")
	if err != nil {
		logOutcome("create-subtask", parentKey, err)
		return "", "", err
	}
	var env struct {
		ID  string `json:"id"`
		Key string `json:"key"`
	}
	if err := json.Unmarshal(out, &env); err != nil {
		logOutcome("create-subtask", parentKey, err)
		return "", "", fmt.Errorf("acli create subtask: parse json: %w", err)
	}
	slog.Info("jira outcome", "op", "create-subtask", "key", parentKey, "child", env.Key)
	return env.ID, env.Key, nil
}

func (CLI) Transition(ctx context.Context, key, status string) error {
	slog.Debug("jira call", "op", "transition", "key", key, "target", status)
	err := runChecked(ctx, "jira", "workitem", "transition", "--key", key, "--status", status, "--yes", "--json")
	logOutcome("transition", key, err, "target", status)
	return err
}

func (c CLI) EnsureLabel(ctx context.Context, key, label string) error {
	slog.Debug("jira call", "op", "ensure-label", "key", key, "label", label)
	raw, err := c.View(ctx, key)
	if err != nil {
		logOutcome("ensure-label", key, err, "label", label)
		return err
	}
	var v struct {
		Fields struct {
			Labels []string `json:"labels"`
		} `json:"fields"`
	}
	if err := json.Unmarshal(raw, &v); err != nil {
		logOutcome("ensure-label", key, err, "label", label)
		return fmt.Errorf("acli view %s: parse labels: %w", key, err)
	}
	for _, l := range v.Fields.Labels {
		if l == label {
			slog.Info("jira outcome", "op", "ensure-label", "key", key, "label", label, "result", "already-present")
			return nil
		}
	}
	all := append(append([]string{}, v.Fields.Labels...), label)
	err = runChecked(ctx, "jira", "workitem", "edit", "--key", key, "--labels", strings.Join(all, ","), "--yes", "--json")
	logOutcome("ensure-label", key, err, "label", label)
	return err
}

func (CLI) UpdateDescription(ctx context.Context, key, description string) error {
	slog.Debug("jira call", "op", "update-description", "key", key)
	err := runChecked(ctx, "jira", "workitem", "edit", "--key", key, "--body", description, "--yes", "--json")
	logOutcome("update-description", key, err)
	return err
}

func (CLI) ListComments(ctx context.Context, key string) ([]string, error) {
	slog.Debug("jira call", "op", "list-comments", "key", key)
	raw, err := runOut(ctx, "jira", "workitem", "view", key, "--fields", "comment", "--json")
	if err != nil {
		logOutcome("list-comments", key, err)
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
		logOutcome("list-comments", key, err)
		return nil, fmt.Errorf("acli comments %s: parse json: %w", key, err)
	}
	var out []string
	for _, c := range v.Fields.Comment.Comments {
		out = append(out, c.Body)
	}
	slog.Info("jira outcome", "op", "list-comments", "key", key, "count", len(out))
	return out, nil
}

func (CLI) AddComment(ctx context.Context, key, body string) error {
	slog.Debug("jira call", "op", "add-comment", "key", key)
	err := runChecked(ctx, "jira", "workitem", "comment", "create", "--key", key, "--body", body, "--json")
	logOutcome("add-comment", key, err)
	return err
}

// logOutcome writes the one-line info record for a jira call's outcome.
// Payloads are never logged: the raw error embeds the full acli argv
// (including comment bodies, descriptions, JQL), so only the trailing
// stderr/stdout fragment after the last colon is kept. extra carries
// structured extras (e.g. the transition target).
func logOutcome(op, key string, err error, extra ...any) {
	attrs := []any{"op", op}
	if key != "" {
		attrs = append(attrs, "key", key)
	}
	attrs = append(attrs, extra...)
	if err != nil {
		attrs = append(attrs, "result", "error", "error", sanitizeErr(err))
	} else {
		attrs = append(attrs, "result", "ok")
	}
	slog.Info("jira outcome", attrs...)
}

// sanitizeErr strips the leading "acli [args...]:" prefix from acli error
// strings so info-level outcome lines never leak argv payloads (comment
// bodies, descriptions, JQL). Keeps the wrapped exit error + trailing
// stderr fragment, which carry the actual failure reason. Handles both
// "acli [args]: ..." (runOut/runChecked) and "acli <op>: ..." (local
// wrapping) shapes.
func sanitizeErr(err error) string {
	if err == nil {
		return ""
	}
	s := err.Error()
	// Strip "acli [args...]: " (runOut shape: "acli %v: %w: %s").
	if strings.HasPrefix(s, "acli [") {
		if i := strings.Index(s, "]: "); i >= 0 {
			return s[i+3:]
		}
	}
	// Strip "acli <op>: " (parse-error wraps).
	if strings.HasPrefix(s, "acli ") {
		if i := strings.Index(s, ": "); i >= 0 {
			return s[i+2:]
		}
	}
	return s
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
