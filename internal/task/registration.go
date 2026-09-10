package task

import (
	"context"
	"fmt"

	"github.com/rajpopat27/relay-flow/internal/config"
)

// RegistrationField describes one task-plugin-owned repository-registration
// value. The CLI only renders this metadata; it does not interpret provider
// names or status vocabulary. A field with Options is rendered as a select,
// otherwise it is rendered as free text. Derived fields are filled from the
// runner candidate name and are never prompted.
type RegistrationField struct {
	Key     string   `json:"key"`
	Title   string   `json:"title"`
	Options []string `json:"options,omitempty"`
	Default string   `json:"default,omitempty"`
	Derived bool     `json:"derived,omitempty"`
}

// Registration describes the fields needed by a task plugin for one repo
// registration.
type Registration struct {
	Fields []RegistrationField `json:"fields"`
}

// RegistrationFields returns task-plugin-owned registration metadata for the
// supplied values. Values use the plugin's registration keys (including
// dotted keys such as statusDefaults.start) and are intentionally opaque to
// core.
func RegistrationFields(ctx context.Context, name string, values config.RawValues) ([]RegistrationField, error) {
	f, err := lookup(name)
	if err != nil {
		return nil, err
	}
	if f.RegistrationFields == nil {
		return nil, fmt.Errorf("task plugin %q does not provide repository registration fields", name)
	}
	return f.RegistrationFields(ctx, values)
}

// ValidateRegistration lets a task plugin validate registration values,
// including remote project/workflow metadata, before the repository is
// persisted. Plugins that do not need this phase remain unchanged.
func ValidateRegistration(ctx context.Context, name string, spec RepoRegistrationSpec) error {
	f, err := lookup(name)
	if err != nil {
		return err
	}
	if f.ValidateRegistration == nil {
		return nil
	}
	if err := f.ValidateRegistration(ctx, spec); err != nil {
		return fmt.Errorf("task registration: %w", err)
	}
	return nil
}
