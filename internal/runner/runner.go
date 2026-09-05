// Package runner defines the runner contract: workspaces/environments,
// terminals, liveness, and process handles. The runner does not know
// task-system fields, workflow routes, report contents, or agent command
// syntax.
package runner

import (
	"context"
	"errors"

	"github.com/rajpopat27/relay-flow/internal/identity"
)

var ErrSessionUnavailable = errors.New("stored session unavailable")

const (
	WorkspaceStatusInProgress = "in-progress"
	WorkspaceStatusInReview   = "in-review"
	WorkspaceStatusCompleted  = "completed"
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
// FindTerminal checks a persisted terminal handle against the external runner
// and returns only a live usable terminal. CloseTerminals closes agent terminals
// while preserving the environment/workspace; CleanupRun removes all
// runner-owned run resources at end. Both reconstruct identity from RunSpec
// without SQLite IDs.
//
// Terminal titles are stable and minimal: only "<ticket>:<node>" — never
// nodeVisitID, workflow name, agent name, or other changing metadata.
// TerminalDiscoverer is an optional recovery capability. It discovers a
// currently live run-owned terminal by its stable title without creating a
// terminal or changing runner state. The core Runner contract remains
// unchanged for adapters that do not support title discovery.
type TerminalDiscoverer interface {
	DiscoverTerminal(ctx context.Context, spec RunSpec, title string) (Terminal, bool, error)
}

type Runner interface {
	DiscoverRepos(ctx context.Context) ([]RepoCandidate, error)
	ValidateRepo(ctx context.Context, name, path string) error
	EnsureEnvironment(ctx context.Context, spec RunSpec) (Environment, error)
	SetEnvironmentStatus(ctx context.Context, env Environment, status string) error
	FindTerminal(ctx context.Context, terminal Terminal) (Terminal, bool, error)
	CreateTerminal(ctx context.Context, env Environment, title string, command Command) (Terminal, error)
	EnsureTerminal(ctx context.Context, env Environment, terminal Terminal, title string, command Command) (Terminal, error)
	SendTerminal(ctx context.Context, terminal Terminal, text string) error
	CloseTerminal(ctx context.Context, terminal Terminal) error
	CloseTerminals(ctx context.Context, spec RunSpec) error
	CleanupRun(ctx context.Context, spec RunSpec) error
}
