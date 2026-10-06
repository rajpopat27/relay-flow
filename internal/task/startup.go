package task

import (
	"context"
	"fmt"

	"github.com/rajpopat27/relay-flow/internal/config"
)

// ProbeStartup dispatches only the selected plugin's machine prerequisite
// checks, even without registered repos. The caller supplies the aggregate
// deadline; the adapter owns connection deduplication and per-probe limits.
func ProbeStartup(ctx context.Context, name string, root config.RawValues, repos map[string]config.Repo) error {
	f, err := lookup(name)
	if err != nil {
		return err
	}
	if f.ProbeStartup == nil {
		return fmt.Errorf("task plugin %q has no startup probe", name)
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	return f.ProbeStartup(ctx, config.Merge(defaultConfig(f), root), repos)
}
