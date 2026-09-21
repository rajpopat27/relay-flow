package repo

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/rajpopat27/relay-flow/internal/config"
	"github.com/rajpopat27/relay-flow/internal/task"
	"github.com/rajpopat27/relay-flow/internal/workflow"
)

type bindingTaskSystem struct {
	task.System
	compileErr map[string]error
}

type ownershipBindingTaskSystem struct{ bindingTaskSystem }

type legacyBindingTaskSystem struct{ task.System }

func (legacyBindingTaskSystem) CompileFilter(config.RawValues) (func(task.Ticket) bool, error) {
	return func(task.Ticket) bool { return true }, nil
}

func (s ownershipBindingTaskSystem) CompileOwnershipFilter(values config.RawValues) (func(task.Ticket) bool, error) {
	owner, _ := values["owner"].(string)
	return func(ticket task.Ticket) bool {
		return owner == "" || ticket.Fields["assignee"] == owner
	}, nil
}

func (s bindingTaskSystem) CompileFilter(values config.RawValues) (func(task.Ticket) bool, error) {
	if name, ok := values["name"].(string); ok {
		if err := s.compileErr[name]; err != nil {
			return nil, err
		}
		return func(ticket task.Ticket) bool { return ticket.Key == name }, nil
	}
	return func(task.Ticket) bool { return true }, nil
}

func (s bindingTaskSystem) CompileOwnershipFilter(config.RawValues) (func(task.Ticket) bool, error) {
	return func(task.Ticket) bool { return true }, nil
}

func (s bindingTaskSystem) ValidateOwnership(context.Context, task.TicketRef, string, config.RawValues) error {
	return nil
}

func (s bindingTaskSystem) ClaimIfOwned(ctx context.Context, ticket task.TicketRef, workflow string, _ config.RawValues) error {
	return s.Claim(ctx, ticket, workflow)
}

func TestBindWorkflowsRejectsMissingOwnershipCapabilities(t *testing.T) {
	registered := &Repo{Name: "payments", TaskSystem: legacyBindingTaskSystem{}}
	registry := NewRegistry()
	registry.Replace(registered)
	wf := &workflow.Workflow{Name: "legacyFlow", Repos: []string{"payments"}, Status: workflow.HealthHealthy}
	if err := registry.BindWorkflows([]*workflow.Workflow{wf}); err == nil || !strings.Contains(err.Error(), "ownership capabilities") {
		t.Fatalf("BindWorkflows error = %v, want missing-capability failure", err)
	}
	if bindings := registered.Bindings(); len(bindings) != 0 {
		t.Fatalf("bindings = %#v, want none after capability rejection", bindings)
	}
}

func TestBindWorkflowsPublishesSeparateOwnershipMatcher(t *testing.T) {
	system := ownershipBindingTaskSystem{bindingTaskSystem: bindingTaskSystem{}}
	registered := &Repo{Name: "payments", TaskSystem: system}
	registry := NewRegistry()
	registry.Replace(registered)
	wf := &workflow.Workflow{
		Name: "ownerFlow", Repos: []string{"payments"}, Status: workflow.HealthHealthy,
		TaskConfig: config.RawValues{"owner": "alice"},
	}
	if err := registry.BindWorkflows([]*workflow.Workflow{wf}); err != nil {
		t.Fatal(err)
	}
	bindings := registered.Bindings()
	if len(bindings) != 1 || bindings[0].Ownership == nil {
		t.Fatalf("bindings = %#v, want a published ownership matcher", bindings)
	}
	if !bindings[0].Ownership(task.Ticket{Fields: map[string]any{"assignee": "alice"}}) ||
		bindings[0].Ownership(task.Ticket{Fields: map[string]any{"assignee": "bob"}}) {
		t.Fatal("published ownership matcher did not enforce the adapter predicate")
	}
}

func TestBindWorkflowsIsolatedPublishesHealthyWorkflows(t *testing.T) {
	system := bindingTaskSystem{compileErr: map[string]error{"bad": errors.New("invalid filter")}}
	registered := &Repo{Name: "payments", TaskSystem: system}
	registry := NewRegistry()
	registry.Replace(registered)

	bad := &workflow.Workflow{Name: "badFlow", Repos: []string{"payments"}, Status: workflow.HealthHealthy, TaskConfig: config.RawValues{"name": "bad"}}
	good := &workflow.Workflow{Name: "goodFlow", Repos: []string{"payments"}, Status: workflow.HealthHealthy, TaskConfig: config.RawValues{"name": "good"}}
	issues := registry.BindWorkflowsIsolated([]*workflow.Workflow{bad, good})
	if len(issues) != 1 || issues[0].Workflow != bad {
		t.Fatalf("binding issues = %#v, want only badFlow", issues)
	}
	if bad.Status != workflow.HealthBlocked || bad.StatusReason == "" {
		t.Fatalf("bad workflow status = %q reason=%q", bad.Status, bad.StatusReason)
	}
	bindings := registered.Bindings()
	if len(bindings) != 1 || bindings[0].Workflow != good {
		t.Fatalf("published bindings = %#v, want only goodFlow", bindings)
	}
}

func TestBindWorkflowsIsolatedSkipsOutdatedWorkflow(t *testing.T) {
	registered := &Repo{Name: "payments", TaskSystem: bindingTaskSystem{}}
	registry := NewRegistry()
	registry.Replace(registered)
	wf := &workflow.Workflow{Name: "editedFlow", Repos: []string{"payments"}, Status: workflow.HealthOutdated}
	if issues := registry.BindWorkflowsIsolated([]*workflow.Workflow{wf}); len(issues) != 0 {
		t.Fatalf("outdated workflow binding issues = %#v, want none", issues)
	}
	if bindings := registered.Bindings(); len(bindings) != 0 {
		t.Fatalf("outdated workflow bindings = %#v, want none", bindings)
	}
}

func TestBindWorkflowsIsolatedBlocksUnavailableTaskSystem(t *testing.T) {
	registered := &Repo{Name: "payments", TaskSystemError: errors.New("task service offline")}
	registry := NewRegistry()
	registry.Replace(registered)
	wf := &workflow.Workflow{Name: "paymentsFlow", Repos: []string{"payments"}, Status: workflow.HealthHealthy}
	issues := registry.BindWorkflowsIsolated([]*workflow.Workflow{wf})
	if len(issues) != 1 || wf.Status != workflow.HealthBlocked || !strings.Contains(wf.StatusReason, "task service offline") {
		t.Fatalf("unavailable task system issues=%#v status=%q reason=%q", issues, wf.Status, wf.StatusReason)
	}
	if bindings := registered.Bindings(); len(bindings) != 0 {
		t.Fatalf("unavailable repo bindings = %#v, want none", bindings)
	}
}

func TestBindWorkflowsIsolatedClearsStaleBindings(t *testing.T) {
	registered := &Repo{Name: "payments", TaskSystem: bindingTaskSystem{}}
	registry := NewRegistry()
	registry.Replace(registered)
	first := &workflow.Workflow{Name: "firstFlow", Repos: []string{"payments"}, Status: workflow.HealthHealthy}
	if issues := registry.BindWorkflowsIsolated([]*workflow.Workflow{first}); len(issues) != 0 {
		t.Fatalf("initial binding issues = %#v", issues)
	}
	second := &workflow.Workflow{Name: "secondFlow", Repos: []string{"payments"}, Status: workflow.HealthHealthy}
	if issues := registry.BindWorkflowsIsolated([]*workflow.Workflow{second}); len(issues) != 0 {
		t.Fatalf("replacement binding issues = %#v", issues)
	}
	bindings := registered.Bindings()
	if len(bindings) != 1 || bindings[0].Workflow != second {
		t.Fatalf("replacement bindings = %#v, want only secondFlow", bindings)
	}
}
