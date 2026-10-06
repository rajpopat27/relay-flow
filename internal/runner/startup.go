package runner

import (
	"context"
	"fmt"
	"time"
)

// StartupProber is the machine-level readiness boundary. Selected runners
// must implement it; repo validation and discovery remain separate operations.
// A probe performs one read-only runtime request, with no retries or writes.
type StartupProber interface {
	ProbeStartup(context.Context) error
}

// ProbeStartup checks the selected runner once, within the caller's aggregate
// startup budget and a five-second per-probe ceiling. Missing probe support is
// an error, never permission to skip a selected dependency.
func ProbeStartup(ctx context.Context, selected Runner) error {
	probe, ok := selected.(StartupProber)
	if !ok {
		return fmt.Errorf("selected runner does not implement startup probing")
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	return probe.ProbeStartup(ctx)
}
