package jira

import (
	"context"
	"reflect"
	"strings"
	"testing"

	"github.com/rajpopat27/relay-flow/internal/config"
	"github.com/rajpopat27/relay-flow/internal/task"
)

func TestStatusRegistrationDefaultsOnlySelectAvailableConventions(t *testing.T) {
	withConventions := statusRegistrationField("statusDefaults.start", "Start", []string{"Open", "In Progress", "Closed"}, defaultStartParentStatus)
	if withConventions.Default != defaultStartParentStatus {
		t.Fatalf("default = %q, want %q", withConventions.Default, defaultStartParentStatus)
	}
	withoutConventions := statusRegistrationField("statusDefaults.end", "End", []string{"Open", "Working", "Closed"}, defaultEndParentStatus)
	if withoutConventions.Default != "" {
		t.Fatalf("missing conventional default = %q, want empty", withoutConventions.Default)
	}
	if !reflect.DeepEqual(withoutConventions.Options, []string{"Open", "Working", "Closed"}) {
		t.Fatalf("options = %v", withoutConventions.Options)
	}
}

func TestRepositoryStatusDefaultsDriveLifecycleAndMailboxCompletion(t *testing.T) {
	fake := &fakeJira{}
	sys, err := newSystem(context.Background(), &fakeClient{fake: fake}, task.RepoSpec{
		Name: "payments",
		RepoConfig: config.RawValues{
			"project":   "PAY",
			"component": "api",
			"statusDefaults": map[string]any{
				"start": "Open",
				"work":  "Working",
				"end":   "Closed",
			},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	defaults := task.LifecycleDefaults(sys)
	parent := task.TicketRef{ID: "1", Key: "PAY-101"}
	mailbox := task.Mailbox{ID: "2", Key: "PAY-102", Node: "coding"}

	if err := sys.ApplyTaskConfig(context.Background(), task.Target{Parent: parent}, defaults.StartDefaults()); err != nil {
		t.Fatal(err)
	}
	if err := sys.ApplyTaskConfig(context.Background(), task.Target{Parent: parent, Mailbox: &mailbox}, defaults.WorkDefaults()); err != nil {
		t.Fatal(err)
	}
	if err := sys.CompleteMailbox(context.Background(), mailbox); err != nil {
		t.Fatal(err)
	}
	if len(fake.parentTransitions) != 1 || fake.parentTransitions[0] != "Open" {
		t.Fatalf("parent transitions = %v, want [Open]", fake.parentTransitions)
	}
	if len(fake.taskTransitions) != 2 || fake.taskTransitions[0] != "Working" || fake.taskTransitions[1] != "Closed" {
		t.Fatalf("mailbox transitions = %v, want [Working Closed]", fake.taskTransitions)
	}
}

func TestJiraRegistrationRejectsComponentOverride(t *testing.T) {
	err := validateJiraRegistration(context.Background(), task.RepoRegistrationSpec{
		Name: "payments",
		RepoConfig: config.RawValues{
			"project":   "PAY",
			"component": "other-component",
			"statusDefaults": map[string]any{
				"start": "Open", "work": "Working", "end": "Closed",
			},
		},
	})
	if err == nil || !strings.Contains(err.Error(), "must match repository name") {
		t.Fatalf("component override error = %v", err)
	}
}

func TestJiraConstructionRejectsRootStatusDefaults(t *testing.T) {
	_, err := newSystem(context.Background(), &fakeClient{fake: &fakeJira{}}, task.RepoSpec{
		Name:       "payments",
		RootConfig: config.RawValues{"statusDefaults": map[string]any{}},
		RepoConfig: testRepoConfig(nil),
	})
	if err == nil || !strings.Contains(err.Error(), "configured per repository") {
		t.Fatalf("root statusDefaults error = %v", err)
	}
}

func TestJiraValidateConfigRejectsNonRepositoryStatusDefaults(t *testing.T) {
	sys := newSystemWithFake(t, &fakeJira{})
	for _, tc := range []struct {
		name string
		node bool
	}{
		{name: "workflow"},
		{name: "node", node: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			workflowConfig := config.RawValues{}
			nodeConfig := map[string]config.RawValues{}
			if tc.node {
				nodeConfig["coding"] = config.RawValues{"statusDefaults": map[string]any{}}
			} else {
				workflowConfig["statusDefaults"] = map[string]any{}
			}
			err := sys.ValidateConfig(context.Background(), workflowConfig, nodeConfig)
			if err == nil || !strings.Contains(err.Error(), "configured per repository") {
				t.Fatalf("validation error = %v", err)
			}
		})
	}
}

func TestJiraConstructionRequiresCompleteRepositoryStatusDefaults(t *testing.T) {
	for _, repoConfig := range []config.RawValues{
		{"project": "PAY", "component": "api"},
		{"project": "PAY", "component": "api", "statusDefaults": map[string]any{}},
		{"project": "PAY", "component": "api", "statusDefaults": map[string]any{
			"start": "Open", "work": "Working",
		}},
	} {
		client := &validationClient{}
		_, err := newSystem(context.Background(), client, task.RepoSpec{Name: "payments", RepoConfig: repoConfig})
		if err == nil {
			t.Fatalf("repo config %#v was accepted without complete status defaults", repoConfig)
		}
		if len(client.statuses) != 0 {
			t.Fatalf("status probes = %v for invalid repository config, want none", client.statuses)
		}
	}
}
