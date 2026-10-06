package bdcli

import (
	"errors"
	"fmt"
	"os/exec"
)

// CheckExecutable is a local prerequisite check, valid without a configured
// workspace. It never invokes bd or lets an ambient workspace act as a probe.
func CheckExecutable() error {
	_, err := exec.LookPath("bd")
	if errors.Is(err, exec.ErrNotFound) {
		return fmt.Errorf("bd startup: missing executable \"bd\": %w", err)
	}
	if err != nil {
		return fmt.Errorf("bd startup: resolve executable \"bd\": %w", err)
	}
	return nil
}
