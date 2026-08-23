// Package paths computes the fixed ~/.relay-flow layout and ensures the root
// directory exists with owner-only permissions.
package paths

import (
	"fmt"
	"os"
	"path/filepath"
)

// Paths is the fixed ~/.relay-flow layout. All process artifacts stay under Root.
type Paths struct {
	Root      string
	Config    string
	Workflows string
	Database  string
	Socket    string
	Lock      string
	ServerLog string
	PluginLog string
}

// ForUserHome returns the Paths rooted at the current user's home directory.
func ForUserHome() (Paths, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return Paths{}, fmt.Errorf("resolve user home: %w", err)
	}
	root := filepath.Join(home, ".relay-flow")
	return Paths{
		Root:      root,
		Config:    filepath.Join(root, "config.yaml"),
		Workflows: filepath.Join(root, "workflows"),
		Database:  filepath.Join(root, "state.db"),
		Socket:    filepath.Join(root, "server.sock"),
		Lock:      filepath.Join(root, "server.lock"),
		ServerLog: filepath.Join(root, "server.log"),
		PluginLog: filepath.Join(root, "plugin.log"),
	}, nil
}

// Ensure creates Root (0700) and the Workflows subdirectory if missing.
func Ensure(p Paths) error {
	if err := os.MkdirAll(p.Root, 0o700); err != nil {
		return fmt.Errorf("create root %s: %w", p.Root, err)
	}
	if err := os.Chmod(p.Root, 0o700); err != nil {
		return fmt.Errorf("chmod root %s: %w", p.Root, err)
	}
	if err := os.MkdirAll(p.Workflows, 0o700); err != nil {
		return fmt.Errorf("create workflows %s: %w", p.Workflows, err)
	}
	for _, logPath := range []string{p.ServerLog, p.PluginLog} {
		f, err := os.OpenFile(logPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
		if err != nil {
			return fmt.Errorf("create log %s: %w", logPath, err)
		}
		if err := f.Chmod(0o600); err != nil {
			f.Close()
			return fmt.Errorf("chmod log %s: %w", logPath, err)
		}
		if err := f.Close(); err != nil {
			return fmt.Errorf("close log %s: %w", logPath, err)
		}
	}
	return nil
}
