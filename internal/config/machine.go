package config

import (
	"bytes"
	"fmt"
	"os"
	"strings"

	"gopkg.in/yaml.v3"
)

// Machine is the machine-wide configuration.
type Machine struct {
	PollIntervalSeconds       int             `yaml:"pollIntervalSeconds,omitempty"`
	CompletedRunRetentionDays int             `yaml:"completedRunRetentionDays,omitempty"`
	KeepTerminalsAlive        bool            `yaml:"keepTerminalsAlive"`
	KeepSessionsAlive         bool            `yaml:"keepSessionsAlive"`
	ExecutorPlugin            string          `yaml:"executorPlugin,omitempty"`
	TemporalAddress           string          `yaml:"temporalAddress,omitempty"`
	TemporalNamespace         string          `yaml:"temporalNamespace,omitempty"`
	TaskPlugin                string          `yaml:"taskPlugin"`
	TaskConfig                RawValues       `yaml:"taskConfig,omitempty"`
	RunnerPlugin              string          `yaml:"runnerPlugin"`
	RunnerConfig              RawValues       `yaml:"runnerConfig,omitempty"`
	HarnessPlugin             string          `yaml:"harnessPlugin"`
	HarnessConfig             RawValues       `yaml:"harnessConfig,omitempty"`
	Repos                     map[string]Repo `yaml:"repos,omitempty"`
}

// Repo is one registered repo entry.
type Repo struct {
	Path       string    `yaml:"path"`
	TaskConfig RawValues `yaml:"taskConfig,omitempty"`
}

const (
	defaultPollIntervalSeconds       = 15
	defaultCompletedRunRetentionDays = 30
	defaultExecutorPlugin            = "goworkflows"
	defaultTemporalAddress           = "localhost:7233"
)

// LoadMachine loads and validates the machine config at path, applying
// documented defaults for omitted global settings.
func LoadMachine(path string) (*Machine, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read machine config %s: %w", path, err)
	}
	var raw map[string]any
	if err := yaml.Unmarshal(data, &raw); err != nil {
		return nil, fmt.Errorf("decode machine config %s: %w", path, err)
	}
	var cfg Machine
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	if err := dec.Decode(&cfg); err != nil {
		return nil, fmt.Errorf("decode machine config %s: %w", path, err)
	}
	if v, ok := raw["pollIntervalSeconds"]; ok {
		n, isNum := v.(int)
		if !isNum || n <= 0 {
			return nil, fmt.Errorf("machine config %s: pollIntervalSeconds must be positive, got %v", path, v)
		}
	}
	if v, ok := raw["completedRunRetentionDays"]; ok {
		n, isNum := v.(int)
		if !isNum || n <= 0 {
			return nil, fmt.Errorf("machine config %s: completedRunRetentionDays must be positive, got %v", path, v)
		}
	}
	for _, key := range []string{"taskConfig", "runnerConfig", "harnessConfig", "executorPlugin", "temporalAddress", "temporalNamespace"} {
		if v, present := raw[key]; present && v == nil {
			return nil, fmt.Errorf("machine config %s: %s: explicit null is not allowed", path, key)
		}
	}
	for _, r := range []RawValues{cfg.TaskConfig, cfg.RunnerConfig, cfg.HarnessConfig} {
		if err := rejectNulls(r, ""); err != nil {
			return nil, fmt.Errorf("machine config %s: %w", path, err)
		}
	}
	rawRepos, _ := raw["repos"].(map[string]any)
	for name, repo := range cfg.Repos {
		if rv, ok := rawRepos[name].(map[string]any); ok {
			if v, present := rv["taskConfig"]; present && v == nil {
				return nil, fmt.Errorf("machine config %s repo %q: taskConfig: explicit null is not allowed", path, name)
			}
		}
		if err := rejectNulls(repo.TaskConfig, ""); err != nil {
			return nil, fmt.Errorf("machine config %s repo %q: %w", path, name, err)
		}
	}
	if cfg.PollIntervalSeconds == 0 {
		cfg.PollIntervalSeconds = defaultPollIntervalSeconds
	}
	if cfg.CompletedRunRetentionDays == 0 {
		cfg.CompletedRunRetentionDays = defaultCompletedRunRetentionDays
	}
	if cfg.ExecutorPlugin == "" {
		if _, present := raw["executorPlugin"]; present {
			return nil, fmt.Errorf("machine config %s: executorPlugin must not be empty", path)
		}
		cfg.ExecutorPlugin = defaultExecutorPlugin
	}
	cfg.TemporalAddress = strings.TrimSpace(cfg.TemporalAddress)
	cfg.TemporalNamespace = strings.TrimSpace(cfg.TemporalNamespace)
	switch cfg.ExecutorPlugin {
	case "goworkflows":
		if cfg.TemporalAddress != "" || cfg.TemporalNamespace != "" {
			return nil, fmt.Errorf("machine config %s: Temporal address/namespace are only valid with executorPlugin temporal", path)
		}
	case "temporal":
		if cfg.TemporalAddress == "" {
			cfg.TemporalAddress = defaultTemporalAddress
		}
		if cfg.TemporalNamespace == "" {
			return nil, fmt.Errorf("machine config %s: temporalNamespace is required for executorPlugin temporal", path)
		}
		if cfg.TemporalNamespace == "default" {
			return nil, fmt.Errorf("machine config %s: temporalNamespace must be a dedicated named namespace, not %q", path, cfg.TemporalNamespace)
		}
	default:
		return nil, fmt.Errorf("machine config %s: unknown executorPlugin %q (want goworkflows or temporal)", path, cfg.ExecutorPlugin)
	}
	if _, ok := raw["keepSessionsAlive"]; !ok {
		cfg.KeepSessionsAlive = true
	}
	if _, ok := raw["keepTerminalsAlive"]; !ok {
		cfg.KeepTerminalsAlive = true
	}
	if cfg.PollIntervalSeconds <= 0 {
		return nil, fmt.Errorf("machine config %s: pollIntervalSeconds must be positive, got %d", path, cfg.PollIntervalSeconds)
	}
	if cfg.CompletedRunRetentionDays <= 0 {
		return nil, fmt.Errorf("machine config %s: completedRunRetentionDays must be positive, got %d", path, cfg.CompletedRunRetentionDays)
	}
	if cfg.KeepTerminalsAlive && !cfg.KeepSessionsAlive {
		return nil, fmt.Errorf("machine config %s: keepTerminalsAlive requires keepSessionsAlive", path)
	}
	return &cfg, nil
}

// SaveMachine atomically writes the machine config at path with owner-only
// permissions (0600).
func SaveMachine(path string, cfg *Machine) error {
	data, err := yaml.Marshal(cfg)
	if err != nil {
		return fmt.Errorf("marshal machine config: %w", err)
	}
	if err := WriteAtomic(path, data, 0o600); err != nil {
		return fmt.Errorf("write machine config %s: %w", path, err)
	}
	return nil
}
