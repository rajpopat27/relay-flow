// Package bdcli is the narrow subprocess client for the Beads bd CLI.
//
// Beads owns its storage and workspace selection. This package only executes
// bd commands with JSON output and translates the small portion of that JSON
// contract needed by the task-system adapter.
package bdcli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"sync"
)

// CLI executes bd in one repository and one configured Beads workspace.
// Commands for a workspace are serialized because a single bd workspace may
// be backed by an embedded database.
type CLI struct {
	repoPath string
	beadsDir string
	mu       sync.Mutex
}

// New constructs a CLI client for the registered code repository and Beads
// workspace. The paths are passed to every child process; this function does
// not initialize or otherwise inspect the workspace.
func New(repoPath, beadsDir string) *CLI {
	return &CLI{repoPath: repoPath, beadsDir: beadsDir}
}

// Issue contains only the Beads issue fields relay-flow needs.
type Issue struct {
	ID          string   `json:"id"`
	Title       string   `json:"title"`
	Description string   `json:"description,omitempty"`
	Status      string   `json:"status"`
	IssueType   string   `json:"issue_type"`
	Priority    int      `json:"priority"`
	Assignee    string   `json:"assignee,omitempty"`
	Labels      []string `json:"labels,omitempty"`
	Parent      string   `json:"parent,omitempty"`
}

// Comment is a Beads issue comment.
type Comment struct {
	ID   string `json:"id"`
	Text string `json:"text"`
}

// Client is the narrow command seam consumed by the Beads task adapter.
type Client interface {
	Probe(ctx context.Context) error

	ListReady(ctx context.Context) ([]Issue, error)
	ListClaimed(ctx context.Context) ([]Issue, error)

	ListChildren(ctx context.Context, parentID string) ([]Issue, error)
	Show(ctx context.Context, issueID string) (Issue, error)

	ListComments(ctx context.Context, issueID string) ([]Comment, error)

	CreateChild(
		ctx context.Context,
		parentID string,
		title string,
		description string,
		label string,
	) (Issue, error)

	Update(ctx context.Context, issueID string, input UpdateInput) error
	AddComment(ctx context.Context, issueID, body string) error
}

// UpdateInput describes the supported bd update flags. Description is sent
// through stdin when non-nil so multiline mailbox descriptions are preserved.
type UpdateInput struct {
	Description *string
	Status      string
	Assignee    string
	AddLabels   []string
	ClearDefer  bool
}

// CommandError reports a bd process that exited unsuccessfully. Stdout and
// stderr are kept separate because bd may emit useful diagnostics on either
// stream.
type CommandError struct {
	Args     []string
	ExitCode int
	Stderr   string
	Stdout   string

	cause error
}

func (e *CommandError) Error() string {
	if e == nil {
		return "bd command failed"
	}
	message := fmt.Sprintf("bd %v: exit status %d", e.Args, e.ExitCode)
	if stderr := strings.TrimSpace(e.Stderr); stderr != "" {
		message += ": " + stderr
	}
	return message
}

func (e *CommandError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.cause
}

// Probe performs a harmless bounded read to ensure that the selected bd
// binary can use the configured workspace.
func (c *CLI) Probe(ctx context.Context) error {
	var issues []Issue
	return c.runJSON(ctx, []string{"list", "--ready", "--limit", "1", "--no-parent", "--json"}, nil, &issues)
}

// ListReady lists ready top-level issues.
func (c *CLI) ListReady(ctx context.Context) ([]Issue, error) {
	var issues []Issue
	if err := c.runJSON(ctx, []string{"list", "--ready", "--no-parent", "--limit", "0", "--json"}, nil, &issues); err != nil {
		return nil, err
	}
	return issues, nil
}

// ListClaimed lists active top-level issues carrying a relay-flow workflow
// claim. The deferred status is intentional: it is part of the selected bd
// command contract for this adapter.
func (c *CLI) ListClaimed(ctx context.Context) ([]Issue, error) {
	var issues []Issue
	if err := c.runJSON(ctx, []string{
		"list", "--no-parent", "--status", "open,in_progress,blocked,deferred",
		"--label-pattern", "wf:*", "--limit", "0", "--json",
	}, nil, &issues); err != nil {
		return nil, err
	}
	return issues, nil
}

// ListChildren lists every child issue of parentID, including closed issues.
func (c *CLI) ListChildren(ctx context.Context, parentID string) ([]Issue, error) {
	var issues []Issue
	if err := c.runJSON(ctx, []string{
		"list", "--parent", parentID, "--all", "--limit", "0", "--json",
	}, nil, &issues); err != nil {
		return nil, err
	}
	return issues, nil
}

// Show reads one issue. The selected bd CLI returns a one-element JSON array
// for show, so an empty result is treated as an unusable response.
func (c *CLI) Show(ctx context.Context, issueID string) (Issue, error) {
	var issues []Issue
	if err := c.runJSON(ctx, []string{"show", issueID, "--json"}, nil, &issues); err != nil {
		return Issue{}, err
	}
	if len(issues) == 0 {
		return Issue{}, fmt.Errorf("bd show %q returned no issues", issueID)
	}
	return issues[0], nil
}

// ListComments lists comments for one issue.
func (c *CLI) ListComments(ctx context.Context, issueID string) ([]Comment, error) {
	var comments []Comment
	if err := c.runJSON(ctx, []string{"comments", issueID, "--json"}, nil, &comments); err != nil {
		return nil, err
	}
	return comments, nil
}

// CreateChild creates a task child under parentID. The description is sent on
// stdin to preserve multiline text exactly.
func (c *CLI) CreateChild(ctx context.Context, parentID, title, description, label string) (Issue, error) {
	var issue Issue
	if err := c.runJSON(ctx, []string{
		"create", title,
		"--type", "task",
		"--parent", parentID,
		"--no-inherit-labels",
		"--labels", label,
		"--stdin", "--json",
	}, strings.NewReader(description), &issue); err != nil {
		return Issue{}, err
	}
	return issue, nil
}

// Update applies the requested bd update flags. The method intentionally does
// not add conditional status flags; status reconciliation belongs to the
// Beads adapter, which reads the issue before calling Update.
func (c *CLI) Update(ctx context.Context, issueID string, input UpdateInput) error {
	args := []string{"update", issueID}
	var stdin io.Reader
	if input.Description != nil {
		args = append(args, "--description=-")
		stdin = strings.NewReader(*input.Description)
	}
	if input.Status != "" {
		args = append(args, "--status", input.Status)
	}
	if input.Assignee != "" {
		args = append(args, "--assignee", input.Assignee)
	}
	for _, label := range input.AddLabels {
		args = append(args, "--add-label", label)
	}
	if input.ClearDefer {
		args = append(args, "--defer", "")
	}
	if len(args) == 2 {
		return errors.New("bd update requires at least one update field")
	}
	args = append(args, "--json")
	var response any
	return c.runJSON(ctx, args, stdin, &response)
}

// AddComment writes a comment through stdin so multiline content is not
// interpreted as shell or command-line text.
func (c *CLI) AddComment(ctx context.Context, issueID, body string) error {
	var response any
	return c.runJSON(ctx, []string{"comment", issueID, "--stdin", "--json"}, strings.NewReader(body), &response)
}

// run executes one serialized bd command and captures stdout and stderr
// independently.
func (c *CLI) run(ctx context.Context, args []string, stdin io.Reader) ([]byte, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	cmd := exec.CommandContext(ctx, "bd", args...)
	cmd.Dir = c.repoPath
	cmd.Env = commandEnvironment(c.repoPath, c.beadsDir)
	cmd.Stdin = stdin

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		exitCode := -1
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) && exitErr.ProcessState != nil {
			exitCode = exitErr.ExitCode()
		}
		return stdout.Bytes(), &CommandError{
			Args:     append([]string(nil), args...),
			ExitCode: exitCode,
			Stderr:   stderr.String(),
			Stdout:   stdout.String(),
			cause:    err,
		}
	}
	return stdout.Bytes(), nil
}

// runJSON runs bd and decodes its stdout. Informational text printed before
// the JSON value is tolerated; stderr remains available only on CommandError
// and is never treated as JSON input.
func (c *CLI) runJSON(ctx context.Context, args []string, stdin io.Reader, destination any) error {
	data, err := c.run(ctx, args, stdin)
	if err != nil {
		return err
	}
	return parseJSON(data, destination)
}

// commandEnvironment preserves unrelated process environment while removing
// ambient Beads selectors that could redirect a child command. The configured
// workspace is always the sole BEADS_DIR entry.
func commandEnvironment(_ string, beadsDir string) []string {
	ambient := os.Environ()
	env := make([]string, 0, len(ambient)+1)
	for _, entry := range ambient {
		key, _, ok := strings.Cut(entry, "=")
		if ok && (key == "BEADS_DIR" || key == "BEADS_DB" || key == "BD_DB") {
			continue
		}
		env = append(env, entry)
	}
	env = append(env, "BEADS_DIR="+beadsDir)
	return env
}

// parseJSON decodes the first valid JSON value in data, allowing informational
// lines before it. This keeps the parser tolerant of bd's human-facing notices
// without combining stderr with stdout.
func parseJSON(data []byte, destination any) error {
	var lastErr error
	for offset, value := range data {
		if value != '{' && value != '[' {
			continue
		}
		decoder := json.NewDecoder(bytes.NewReader(data[offset:]))
		var raw json.RawMessage
		if err := decoder.Decode(&raw); err != nil {
			lastErr = err
			continue
		}
		if err := json.Unmarshal(raw, destination); err != nil {
			lastErr = err
			continue
		}
		return nil
	}
	if lastErr == nil {
		lastErr = errors.New("no JSON value found")
	}
	return fmt.Errorf("parse bd JSON: %w", lastErr)
}

var _ Client = (*CLI)(nil)
