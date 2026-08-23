// Package runner defines the runner contract: workspaces/environments,
// terminals, liveness, and process handles. The runner does not know
// task-system fields, workflow routes, report contents, or agent command
// syntax.
package runner

import (
	"context"

	"github.com/rajpopat27/relay-flow/internal/identity"
)

type RepoCandidate struct {
	Name string `json:"name"`
	Path string `json:"path"`
}

type Environment struct {
	ID   string `json:"id"`
	Path string `json:"path"`
}

type Terminal struct {
	ID    string `json:"id"`
	Title string `json:"title"`
}

type Command struct {
	Executable string            `json:"executable"`
	Args       []string          `json:"args,omitempty"`
	Env        map[string]string `json:"env,omitempty"`
}

type RunSpec struct {
	RunID     identity.RunID
	RepoName  string
	RepoPath  string
	TicketKey string
}

// Runner owns ticket-scoped environments, terminals, and liveness.
// FindTerminal returns only a live usable terminal. CloseTerminals closes
// agent terminals while preserving the environment/workspace; CleanupRun
// removes all runner-owned run resources at end. Both reconstruct identity
// from RunSpec without SQLite IDs.
//
// Terminal titles are stable and minimal: only "<ticket>:<node>" — never
// nodeVisitID, workflow name, agent name, or other changing metadata.
type Runner interface {
	DiscoverRepos(ctx context.Context) ([]RepoCandidate, error)
	ValidateRepo(ctx context.Context, name, path string) error
	EnsureEnvironment(ctx context.Context, spec RunSpec) (Environment, error)
	FindTerminal(ctx context.Context, env Environment, title string) (Terminal, bool, error)
	CloseTerminal(ctx context.Context, terminal Terminal) error
	EnsureTerminal(ctx context.Context, env Environment, title string, command Command) (Terminal, error)
	CloseTerminals(ctx context.Context, spec RunSpec) error
	CleanupRun(ctx context.Context, spec RunSpec) error
}
