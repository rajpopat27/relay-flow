package temporal

import (
	"context"
	"fmt"

	"github.com/rajpopat27/relay-flow/internal/execution/projection"
	"github.com/rajpopat27/relay-flow/internal/run"
	"github.com/rajpopat27/relay-flow/internal/runner"
)

// rediscoverMissingTerminals repairs only a missing derived terminal handle.
// Runner adapters that support title discovery perform a read-only lookup;
// recovery never creates a terminal or closes a healthy one.
func (e *Engine) rediscoverMissingTerminals(ctx context.Context, start run.Start, state RunStateSnapshot) error {
	discoverer, ok := e.deps.Runner.(runner.TerminalDiscoverer)
	if !ok {
		return nil
	}
	spec := runner.RunSpec{RunID: start.ID, RepoName: start.Repo, RepoPath: start.RepoPath, TicketKey: start.Ticket.Key}
	for _, binding := range state.RuntimeBindings {
		if binding.Node == "" || binding.Node != state.Run.CurrentNode || binding.NodeVisitID == "" || binding.TerminalID != "" {
			continue
		}
		title := start.Ticket.Key + ":" + binding.Node
		terminal, found, err := discoverer.DiscoverTerminal(ctx, spec, title)
		if err != nil {
			return fmt.Errorf("discover terminal %q during Temporal recovery: %w", title, err)
		}
		if !found || terminal.ID == "" {
			continue
		}
		if err := e.runs.UpdateNodeRuntime(ctx, projection.NodeRuntime{
			RunID: start.ID, Node: binding.Node, TerminalID: terminal.ID,
			SessionID: binding.SessionID, NodeVisitID: binding.NodeVisitID,
		}); err != nil {
			return fmt.Errorf("restore terminal binding %q: %w", title, err)
		}
	}
	return nil
}
