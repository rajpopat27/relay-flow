package beads

import (
	"context"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"time"

	"github.com/rajpopat27/relay-flow/internal/config"
	"github.com/rajpopat27/relay-flow/internal/task/beads/bdcli"
)

// probeStartup checks only explicitly configured connections, not repository
// ownership, filters, tickets or history. A shared workspace is probed once.
func probeStartup(ctx context.Context, root config.RawValues, repos map[string]config.Repo) error {
	if err := bdcli.CheckExecutable(); err != nil {
		return err
	}
	rootCfg, err := decodeConfig(root)
	if err != nil {
		return fmt.Errorf("beads startup root config: %w", err)
	}
	workspaces := map[string]string{} // canonical workspace -> code cwd
	names := make([]string, 0, len(repos))
	for name := range repos {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		registered := repos[name]
		dir, err := configuredBeadsDir(root, registered.TaskConfig)
		if err != nil {
			return fmt.Errorf("beads startup repo %q: %w", name, err)
		}
		dir, err = canonicalBeadsDir(dir)
		if err != nil {
			return fmt.Errorf("beads startup repo %q workspace: %w", name, err)
		}
		if _, exists := workspaces[dir]; !exists {
			workspaces[dir] = registered.Path
		}
	}
	// A root connection, when explicitly supplied, must not be silently skipped
	// even without repos. Prefer an existing code cwd for a shared connection;
	// otherwise use the explicitly configured workspace, never an ambient cwd.
	if strings.TrimSpace(rootCfg.BeadsDir) != "" {
		dir, err := canonicalBeadsDir(rootCfg.BeadsDir)
		if err != nil {
			return fmt.Errorf("beads startup root workspace: %w", err)
		}
		if _, exists := workspaces[dir]; !exists {
			workspaces[dir] = dir
		}
	}
	if len(workspaces) == 0 {
		slog.Info("beads startup connection verification deferred to registration", "reason", "no configured workspace")
		return nil
	}
	dirs := make([]string, 0, len(workspaces))
	for dir := range workspaces {
		dirs = append(dirs, dir)
	}
	sort.Strings(dirs)
	for _, dir := range dirs {
		probeCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
		err := bdcli.New(workspaces[dir], dir).Probe(probeCtx)
		cancel()
		if err != nil {
			return fmt.Errorf("beads startup workspace probe: %w", err)
		}
	}
	return nil
}
