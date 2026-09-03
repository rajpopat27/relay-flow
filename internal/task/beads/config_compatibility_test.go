package beads

import (
	"testing"

	"github.com/rajpopat27/relay-flow/internal/config"
	"github.com/rajpopat27/relay-flow/internal/task"
)

func TestBeadsConfigAcceptsSharedTransitionAndAssignee(t *testing.T) {
	err := task.ValidateTextConfig("beads", config.RawValues{
		"assignee": "owner@example.com",
		"filters": map[string]any{
			"parentStatuses": []any{"open"},
			"issueTypes":     []any{"epic"},
			"labels":         []any{"relay-ready"},
			"assignees":      []any{"owner@example.com"},
		},
		"transitionTo": map[string]any{
			"parentStatus": "in_progress",
			"taskStatus":   "closed",
		},
	})
	if err != nil {
		t.Fatalf("shared transitionTo and assignee configuration was rejected: %v", err)
	}
}

func TestBeadsConfigRejectsLegacyStatusVocabulary(t *testing.T) {
	for _, field := range []string{"parent", "mailbox"} {
		t.Run(field, func(t *testing.T) {
			err := task.ValidateTextConfig("beads", config.RawValues{
				"status": map[string]any{field: "open"},
			})
			if err == nil {
				t.Fatalf("legacy status.%s configuration was accepted", field)
			}
		})
	}
}

func TestBeadsConfigRejectsJiraOnlyFields(t *testing.T) {
	for _, field := range []string{"project", "component"} {
		t.Run(field, func(t *testing.T) {
			err := task.ValidateTextConfig("beads", config.RawValues{field: "not-supported"})
			if err == nil {
				t.Fatalf("Jira-only field %q was accepted", field)
			}
		})
	}
}

func TestBeadsCompileFilterUsesInheritedTopLevelAssignee(t *testing.T) {
	sys := &system{base: config.Merge(DefaultConfig(), config.RawValues{
		"assignee": "Repo.Bot@Example.com",
	})}
	match, err := sys.CompileFilter(nil)
	if err != nil {
		t.Fatalf("CompileFilter failed: %v", err)
	}
	if !match(task.Ticket{Fields: map[string]any{"assignee": "repo.bot@example.COM"}}) {
		t.Fatal("top-level assignee did not become the default filter")
	}
	if match(task.Ticket{Fields: map[string]any{"assignee": "other@example.com"}}) {
		t.Fatal("ticket assigned to another user matched the default assignee filter")
	}
}

func TestBeadsExplicitAssigneeFilterOverridesInheritedDefault(t *testing.T) {
	sys := &system{base: config.Merge(DefaultConfig(), config.RawValues{
		"assignee": "repo@example.com",
	})}
	match, err := sys.CompileFilter(config.RawValues{
		"assignee": "workflow@example.com",
		"filters": map[string]any{
			"assignees": []any{"selected@example.com"},
		},
	})
	if err != nil {
		t.Fatalf("CompileFilter failed: %v", err)
	}
	if !match(task.Ticket{Fields: map[string]any{"assignee": "SELECTED@example.com"}}) {
		t.Fatal("explicit assignee filter rejected the selected assignee")
	}
	for _, assignee := range []string{"repo@example.com", "workflow@example.com"} {
		if match(task.Ticket{Fields: map[string]any{"assignee": assignee}}) {
			t.Fatalf("inherited assignee %q overrode explicit assignee filter", assignee)
		}
	}
}

func TestBeadsConfigPreservesRootRepoWorkflowNodePrecedence(t *testing.T) {
	root := config.RawValues{
		"assignee": "root@example.com",
		"filters": map[string]any{
			"parentStatuses": []any{"root"},
			"labels":         []any{"root-label"},
		},
		"transitionTo": map[string]any{
			"parentStatus": "open",
			"taskStatus":   "open",
		},
	}
	repo := config.RawValues{
		"assignee": "repo@example.com",
		"filters": map[string]any{
			"parentStatuses": []any{"repo"},
		},
		"transitionTo": map[string]any{
			"parentStatus": "in_progress",
		},
	}
	workflow := config.RawValues{
		"assignee": "workflow@example.com",
		"filters": map[string]any{
			"issueTypes": []any{"workflow-type"},
		},
		"transitionTo": map[string]any{
			"taskStatus": "in_progress",
		},
	}
	node := config.RawValues{
		"assignee": "node@example.com",
		"filters": map[string]any{
			"labels": []any{"node-label"},
		},
		"transitionTo": map[string]any{
			"taskStatus": "closed",
		},
	}

	merged := config.Merge(root, repo, workflow, node)
	if merged["assignee"] != "node@example.com" {
		t.Fatalf("merged assignee = %v, want node value", merged["assignee"])
	}
	transition, ok := merged["transitionTo"].(map[string]any)
	if !ok {
		t.Fatalf("merged transitionTo = %#v, want map", merged["transitionTo"])
	}
	if transition["parentStatus"] != "in_progress" {
		t.Fatalf("merged parent status = %v, want repo value", transition["parentStatus"])
	}
	if transition["taskStatus"] != "closed" {
		t.Fatalf("merged task status = %v, want node value", transition["taskStatus"])
	}
	filters, ok := merged["filters"].(map[string]any)
	if !ok {
		t.Fatalf("merged filters = %#v, want map", merged["filters"])
	}
	if got := filters["parentStatuses"].([]any); len(got) != 1 || got[0] != "repo" {
		t.Fatalf("merged parent statuses = %#v, want repo value", filters["parentStatuses"])
	}
	if got := filters["issueTypes"].([]any); len(got) != 1 || got[0] != "workflow-type" {
		t.Fatalf("merged issue types = %#v, want workflow value", filters["issueTypes"])
	}
	if got := filters["labels"].([]any); len(got) != 1 || got[0] != "node-label" {
		t.Fatalf("merged labels = %#v, want node value", filters["labels"])
	}

	if err := task.ValidateTextConfig("beads", merged); err != nil {
		t.Fatalf("effective root/repo/workflow/node configuration was rejected: %v", err)
	}
}
