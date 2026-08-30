// Package harness defines the harness contract: agent validation, session
// discovery, resume syntax, and launch command construction. The runner
// executes the returned command; the harness never manipulates runner
// state.
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
	NextSteps   []workflow.Route
	ResumeID    string
}

type Harness interface {
	ValidateAgent(ctx context.Context, repoPath, agent string) error
	FindSession(ctx context.Context, repoPath, title string) (Session, bool, error)
	BuildCommand(spec LaunchSpec) (runner.Command, error)
}
