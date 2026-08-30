// Package credentials stores integration secrets separately from machine
// configuration.
package credentials

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/rajpopat27/relay-flow/internal/config"
	"gopkg.in/yaml.v3"
)

type Jira struct {
	Email string `yaml:"email"`
	Token string `yaml:"token"`
}

type File struct {
	Jira Jira `yaml:"jira"`
}

func Path() (string, error) {
	if root := os.Getenv("RELAY_FLOW_HOME"); root != "" {
		return filepath.Join(root, "credentials.yaml"), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolve user home: %w", err)
	}
	return filepath.Join(home, ".relay-flow", "credentials.yaml"), nil
}

func Load(path string) (File, error) {
	info, err := os.Stat(path)
	if err != nil {
		return File{}, fmt.Errorf("stat credentials %s: %w", path, err)
	}
	if info.Mode().Perm()&0o077 != 0 {
		return File{}, fmt.Errorf("credentials %s must have mode 0600", path)
	}
	if info.Mode().Perm() != 0o600 {
		if err := os.Chmod(path, 0o600); err != nil {
			return File{}, fmt.Errorf("chmod credentials %s: %w", path, err)
		}
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return File{}, fmt.Errorf("read credentials %s: %w", path, err)
	}
	var out File
	if err := yaml.Unmarshal(raw, &out); err != nil {
		return File{}, fmt.Errorf("parse credentials %s: %w", path, err)
	}
	if out.Jira.Email == "" || out.Jira.Token == "" {
		return File{}, errors.New("Jira email and API token are required")
	}
	return out, nil
}

func LoadDefault() (File, error) {
	path, err := Path()
	if err != nil {
		return File{}, err
	}
	return Load(path)
}

func Save(path string, value File) error {
	if value.Jira.Email == "" || value.Jira.Token == "" {
		return errors.New("Jira email and API token are required")
	}
	raw, err := yaml.Marshal(value)
	if err != nil {
		return fmt.Errorf("marshal credentials: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("create credentials directory: %w", err)
	}
	if err := config.WriteAtomic(path, raw, 0o600); err != nil {
		return fmt.Errorf("write credentials: %w", err)
	}
	return nil
}
