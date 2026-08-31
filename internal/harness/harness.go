// Package harness defines the harness contract: repository setup, prompt
// rendering, agent validation, session discovery, resume syntax, and launch
// command construction. The runner executes the returned command; the harness
// never manipulates runner state.
package harness

import (
	"context"

	"github.com/rajpopat27/relay-flow/internal/identity"
	"github.com/rajpopat27/relay-flow/internal/runner"
	"github.com/rajpopat27/relay-flow/internal/workflow"
)

type Session struct {
	ID    string
	Title string
}

type PromptKind string

const (
	PromptInitial  PromptKind = "initial"
	PromptFeedback PromptKind = "feedback"
)

// PromptData is the task-system-neutral data core supplies to the selected
// harness. Harness templates, including HITL instructions, are rendered only
// by the harness.
type PromptData struct {
	TaskSystem      string
	Ticket          string
	Workflow        string
	Repo            string
	Node            string
	NodeType        workflow.NodeType
	Agent           string
	NodeDescription string
	NextSteps       string
	Mailbox         string
}

type LaunchSpec struct {
	RunID       identity.RunID
	NodeVisitID identity.NodeVisitID
	RepoName    string
	RepoPath    string
	Workflow    string
	Ticket      string
	Node        string
	NodeType    workflow.NodeType
	Agent       string
	Title       string
	Prompt      string
	NudgePrompt string
	PromptData  PromptData
	NextSteps   []workflow.Route
	ResumeID    string
}

type Harness interface {
	SetupRepo(ctx context.Context, repoPath string) error
	ValidateAgent(ctx context.Context, repoPath, agent string) error
	FindSession(ctx context.Context, repoPath, title string) (Session, bool, error)
	RenderPrompt(kind PromptKind, data PromptData, nudgeTemplate string) (string, error)
	BuildCommand(spec LaunchSpec) (runner.Command, error)
}
