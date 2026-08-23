// Package config stores and decodes machine/workflow configuration values.
// It does not own task/runner/harness plugin registries; it only holds the
// raw serializable values they decode from.
package config

import (
	"bytes"
	"fmt"

	"gopkg.in/yaml.v3"
)

// RawValues holds raw, serializable plugin configuration values.
type RawValues map[string]any

// Merge applies values in order (root, repo, workflow, node precedence):
// maps merge recursively, a later scalar or list replaces the earlier value,
// and omitted keys inherit. Inputs are never mutated.
func Merge(values ...RawValues) RawValues {
	out := RawValues{}
	for _, v := range values {
		out = mergeInto(out, v)
	}
	return out
}

func mergeInto(dst, src map[string]any) map[string]any {
	for k, sv := range src {
		if sm, ok := sv.(map[string]any); ok {
			fresh := map[string]any{}
			if dm, ok := dst[k].(map[string]any); ok {
				mergeInto(fresh, dm)
			}
			dst[k] = mergeInto(fresh, sm)
			continue
		}
		dst[k] = sv
	}
	return dst
}

// DecodeStrict decodes raw values into dst, rejecting unknown fields and
// explicit null values.
func DecodeStrict(values RawValues, dst any) error {
	if err := rejectNulls(values, ""); err != nil {
		return err
	}
	data, err := yaml.Marshal(map[string]any(values))
	if err != nil {
		return fmt.Errorf("marshal raw values: %w", err)
	}
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	if err := dec.Decode(dst); err != nil {
		return fmt.Errorf("strict decode: %w", err)
	}
	return nil
}

func rejectNulls(values map[string]any, path string) error {
	for k, v := range values {
		key := k
		if path != "" {
			key = path + "." + k
		}
		if err := rejectNullValue(v, key); err != nil {
			return err
		}
	}
	return nil
}

func rejectNullValue(v any, key string) error {
	if v == nil {
		return fmt.Errorf("config key %q: explicit null is not allowed", key)
	}
	switch t := v.(type) {
	case map[string]any:
		return rejectNulls(t, key)
	case []any:
		for i, elem := range t {
			if err := rejectNullValue(elem, fmt.Sprintf("%s[%d]", key, i)); err != nil {
				return err
			}
		}
	}
	return nil
}
