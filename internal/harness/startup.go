package harness

import (
	"context"
	"fmt"
	"time"
)

// StartupProber is the local selected-executable prerequisite boundary. It
// must not enumerate agents, inspect repositories or launch a session.
type StartupProber interface {
	ProbeStartup(context.Context) error
}

// ProbeStartup checks the constructed selected harness. Missing support is
// an error, not permission to skip its executable prerequisite.
func ProbeStartup(ctx context.Context, selected Harness) error {
	probe, ok := selected.(StartupProber)
	if !ok {
		return fmt.Errorf("selected harness does not implement startup probing")
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	return probe.ProbeStartup(ctx)
}
