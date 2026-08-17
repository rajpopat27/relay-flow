package jira

import (
	"bytes"
	"fmt"

	"gopkg.in/yaml.v3"
)

// strictDecode re-marshals the opaque config map and decodes it into v
// with KnownFields(true), so unknown YAML keys in tasks.config are
// rejected just like top-level ones.
func strictDecode(m map[string]any, v any) error {
	if m == nil {
		m = map[string]any{}
	}
	b, err := yaml.Marshal(m)
	if err != nil {
		return fmt.Errorf("re-marshal config: %w", err)
	}
	dec := yaml.NewDecoder(bytes.NewReader(b))
	dec.KnownFields(true)
	return dec.Decode(v)
}
