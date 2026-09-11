package runner

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
)

// CheckCleanCheckout verifies the ticket-scoped checkout before runner cleanup.
// A checkout that has already disappeared is an idempotent cleanup success;
// callers preserve their existing roll-forward behavior in that case.
func CheckCleanCheckout(ctx context.Context, ticket, checkout string) error {
	if strings.TrimSpace(checkout) == "" {
		return fmt.Errorf("cannot check Git cleanliness for ticket %q: checkout path is empty", ticket)
	}
	if _, err := os.Stat(checkout); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return fmt.Errorf("cannot check Git cleanliness for ticket %q at %q: %w", ticket, checkout, err)
	}

	out, err := exec.CommandContext(ctx, "git", "-C", checkout, "status", "--porcelain=v1", "--untracked-files=all").CombinedOutput()
	if err != nil {
		detail := strings.TrimSpace(string(out))
		if detail != "" {
			return fmt.Errorf("cannot check Git cleanliness for ticket %q at %q: %w: %s", ticket, checkout, err, detail)
		}
		return fmt.Errorf("cannot check Git cleanliness for ticket %q at %q: %w", ticket, checkout, err)
	}
	if len(out) != 0 {
		return fmt.Errorf("ticket %q checkout is dirty; commit required before runner cleanup", ticket)
	}
	return nil
}
