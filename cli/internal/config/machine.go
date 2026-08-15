package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)

// MachineConfig is the per-machine, uncommitted config at
// ~/.orca-jira-loop/config.yaml. It holds settings that are personal to
// whoever runs the server on this machine — never committed to a repo,
// unlike workflow YAMLs which the whole team shares.
type MachineConfig struct {
	// Assignee is this machine user's Jira identity (display name or
	// accountId). In distributed mode (workflow yaml without
	// assigneeIsAgent), every workflow JQL gets `AND assignee = "<this>"`,
	// so a teammate's server never touches your tickets and vice versa.
	// Probe-validated against Jira at submit time.
	Assignee string `yaml:"assignee"`
}

// MachineConfigPath returns ~/.orca-jira-loop/config.yaml.
func MachineConfigPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".orca-jira-loop", "config.yaml"), nil
}

// Save writes the machine config, creating the dir. Assignee is required.
func (m *MachineConfig) Save() error {
	if strings.TrimSpace(m.Assignee) == "" {
		return fmt.Errorf("assignee must not be empty")
	}
	p, err := MachineConfigPath()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return err
	}
	b, err := yaml.Marshal(m)
	if err != nil {
		return err
	}
	return os.WriteFile(p, b, 0o600) // personal identity: owner-only
}

// LoadMachineConfig reads ~/.orca-jira-loop/config.yaml.
func LoadMachineConfig() (*MachineConfig, error) {
	p, err := MachineConfigPath()
	if err != nil {
		return nil, err
	}
	b, err := os.ReadFile(p)
	if err != nil {
		return nil, fmt.Errorf("machine config %s not found — run `orca-jira-loop init --assignee \"<your Jira name>\"` first", p)
	}
	var m MachineConfig
	if err := yaml.Unmarshal(b, &m); err != nil {
		return nil, fmt.Errorf("parse machine config %s: %w", p, err)
	}
	if strings.TrimSpace(m.Assignee) == "" {
		return nil, fmt.Errorf("machine config %s: assignee must not be empty", p)
	}
	return &m, nil
}
