// Package opencode wraps the `opencode` CLI to validate agent names.
package opencode

import (
	"fmt"
	"os/exec"
	"strings"
)

// Exists reports whether name is a known opencode agent, via
// `opencode agent list` (agent names are the unindented lines).
func Exists(name string) (bool, error) {
	out, err := exec.Command("opencode", "agent", "list").Output()
	if err != nil {
		return false, fmt.Errorf("opencode agent list: %w", err)
	}
	for _, line := range strings.Split(string(out), "\n") {
		if line == "" || line[0] == ' ' || line[0] == '[' || line[0] == '{' {
			continue
		}
		if strings.Fields(line)[0] == name {
			return true, nil
		}
	}
	return false, nil
}
