package jira

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/rajpopat27/relay-flow/internal/config"
	"github.com/rajpopat27/relay-flow/internal/task"
)

type validationClient struct {
	fakeClient
	assignees []string
	statuses  []string
}

func (c *validationClient) ValidateAssignee(_ context.Context, _ string, assignee string) error {
	c.assignees = append(c.assignees, assignee)
	if assignee == "missing" {
		return errors.New("invalid assignee")
	}
	return nil
}

func (c *validationClient) ValidateStatus(_ context.Context, project, status string) error {
	c.statuses = append(c.statuses, project+":"+status)
	if status == "DO Done" {
		return errors.New("invalid status")
	}
	return nil
}

func TestNewSystemRejectsInvalidAssignee(t *testing.T) {
	client := &validationClient{}
	_, err := newSystem(context.Background(), client, taskSpec(config.RawValues{"assignee": "missing"}, nil))
	if err == nil || !strings.Contains(err.Error(), `assignee "missing"`) {
		t.Fatalf("newSystem error = %v", err)
	}
	if len(client.assignees) != 1 || client.assignees[0] != "missing" {
		t.Fatalf("validated assignees = %v", client.assignees)
	}
}

func TestValidateConfigProbesMergedTransitionStatusesWithoutMutation(t *testing.T) {
	client := &validationClient{}
	sys, err := newSystem(context.Background(), client, taskSpec(config.RawValues{"assignee": "valid"}, nil))
	if err != nil {
		t.Fatal(err)
	}
	wf := config.RawValues{"transitionTo": map[string]any{"parentStatus": "In Review"}}
	node := config.RawValues{"transitionTo": map[string]any{"taskStatus": "DO Done"}}
	err = sys.ValidateConfig(context.Background(), wf, map[string]config.RawValues{"review": node})
	if err == nil || !strings.Contains(err.Error(), `node "review" taskStatus "DO Done"`) {
		t.Fatalf("ValidateConfig error = %v", err)
	}
	if got := wf["transitionTo"].(map[string]any); len(got) != 1 || got["parentStatus"] != "In Review" {
		t.Fatalf("workflow config mutated: %v", wf)
	}
	if got := node["transitionTo"].(map[string]any); len(got) != 1 || got["taskStatus"] != "DO Done" {
		t.Fatalf("node config mutated: %v", node)
	}
}

func TestNewSystemRejectsInvalidRepoTransitionStatus(t *testing.T) {
	client := &validationClient{}
	_, err := newSystem(context.Background(), client, taskSpec(nil, config.RawValues{
		"project": "PAY", "component": "api",
		"transitionTo": map[string]any{"parentStatus": "DO Done"},
	}))
	if err == nil || !strings.Contains(err.Error(), `repo config parentStatus "DO Done"`) {
		t.Fatalf("newSystem error = %v", err)
	}
}

func TestValidateConfigProbesWorkflowAssignee(t *testing.T) {
	client := &validationClient{}
	sys, err := newSystem(context.Background(), client, taskSpec(nil, nil))
	if err != nil {
		t.Fatal(err)
	}
	err = sys.ValidateConfig(context.Background(), config.RawValues{"assignee": "missing"}, nil)
	if err == nil || !strings.Contains(err.Error(), `workflow assignee "missing"`) {
		t.Fatalf("ValidateConfig error = %v", err)
	}
}

func taskSpec(root, repo config.RawValues) task.RepoSpec {
	if repo == nil {
		repo = config.RawValues{"project": "PAY", "component": "api"}
	}
	return task.RepoSpec{Name: "payments", RootConfig: root, RepoConfig: repo}
}
