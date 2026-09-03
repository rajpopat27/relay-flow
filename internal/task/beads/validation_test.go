package beads

import (
	"context"
	"strings"
	"testing"

	"github.com/rajpopat27/relay-flow/internal/config"
)

func validationSystem() *system {
	return &system{base: config.Merge(DefaultConfig())}
}

func TestValidateConfigMergesWorkflowAndNodeValuesLocally(t *testing.T) {
	err := validationSystem().ValidateConfig(context.Background(), config.RawValues{
		"filters": map[string]any{
			"parentStatuses": []any{"open"},
			"labels":         []any{"backend"},
		},
	}, map[string]config.RawValues{
		"implement": {"transitionTo": map[string]any{"taskStatus": "in_progress"}},
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestValidateConfigRejectsUnknownNodeFields(t *testing.T) {
	err := validationSystem().ValidateConfig(context.Background(), nil, map[string]config.RawValues{
		"implement": {"unsupported": true},
	})
	if err == nil || !strings.Contains(err.Error(), "unsupported") {
		t.Fatalf("unknown node field error = %v", err)
	}
}

func TestValidateConfigRejectsNullAndInvalidFilterValues(t *testing.T) {
	for _, tc := range []struct {
		name string
		raw  config.RawValues
		want string
	}{
		{
			name: "explicit null",
			raw: config.RawValues{"filters": map[string]any{
				"labels": nil,
			}},
			want: "explicit null",
		},
		{
			name: "wrong filter type",
			raw: config.RawValues{"filters": map[string]any{
				"labels": "backend",
			}},
			want: "cannot unmarshal",
		},
		{
			name: "empty filter value",
			raw: config.RawValues{"filters": map[string]any{
				"labels": []any{""},
			}},
			want: "must not be empty",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := validationSystem().ValidateConfig(context.Background(), tc.raw, nil)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("validation error = %v, want %q", err, tc.want)
			}
		})
	}
}

func TestValidateConfigRejectsUnsupportedStatusAndTemplate(t *testing.T) {
	for _, tc := range []struct {
		name string
		raw  config.RawValues
		want string
	}{
		{
			name: "legacy status",
			raw: config.RawValues{"status": map[string]any{
				"mailbox": "in_review",
			}},
			want: "field status not found",
		},
		{
			name: "summary placeholder",
			raw: config.RawValues{"templates": map[string]any{
				"summaryComment": "summary",
			}},
			want: "summaryComment must contain {{summaryReport}}",
		},
		{
			name: "unknown template variable",
			raw: config.RawValues{"templates": map[string]any{
				"mailboxDescription": "mailbox {{unknown}}",
			}},
			want: "unknown template variable",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := validationSystem().ValidateConfig(context.Background(), tc.raw, nil)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("validation error = %v, want %q", err, tc.want)
			}
		})
	}
}
