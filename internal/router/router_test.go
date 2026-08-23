package router_test

import (
	"errors"
	"testing"

	"github.com/rajpopat27/relay-flow/internal/repo"
	"github.com/rajpopat27/relay-flow/internal/router"
	"github.com/rajpopat27/relay-flow/internal/task"
	"github.com/rajpopat27/relay-flow/internal/workflow"
)

// 3.11: Ticket Router tests (pure) per specs/repo-workflow-routing
// "Existing workflow claims take precedence" and "Unclaimed routing is
// unambiguous", following design decision 6's exact routing order.

func wf(name string) *workflow.Workflow {
	return &workflow.Workflow{
		Name:  name,
		Repos: []string{"payments"},
		Nodes: map[string]workflow.Node{
			"start": {OnSuccess: []workflow.Route{{Target: "coding"}}},
			"coding": {
				Type: workflow.NodeAgent, Agent: "build", Description: "work",
				OnSuccess: []workflow.Route{{Target: "end"}},
				OnFailure: []workflow.Route{{Target: "coding"}},
			},
			"end": {},
		},
	}
}

func binding(w *workflow.Workflow, match func(task.Ticket) bool) repo.WorkflowBinding {
	return repo.WorkflowBinding{Workflow: w, Match: match}
}

func repoWith(bindings ...repo.WorkflowBinding) *repo.Repo {
	return &repo.Repo{Name: "payments", Path: "/srv/payments", Workflows: bindings}
}

func TestMultipleClaimsInvalid(t *testing.T) {
	r := repoWith(binding(wf("aFlow"), nil), binding(wf("bFlow"), nil))
	ticket := task.Ticket{Key: "PAY-101", WorkflowClaims: []string{"wf:aFlow", "wf:bFlow"}}

	_, err := router.ResolveWorkflow(r, ticket)
	var ice *router.InvalidClaimError
	if !errors.As(err, &ice) {
		t.Fatalf("err = %v, want InvalidClaimError", err)
	}
}

func TestSingleClaimResolvesDirectly(t *testing.T) {
	// Filters must NOT be re-run for a claimed ticket: the matcher would
	// reject this ticket, and the claim still wins.
	matchCalled := false
	r := repoWith(binding(wf("basicFlow"), func(task.Ticket) bool {
		matchCalled = true
		return false
	}))
	ticket := task.Ticket{Key: "PAY-101", WorkflowClaims: []string{"wf:basicFlow"}}

	got, err := router.ResolveWorkflow(r, ticket)
	if err != nil {
		t.Fatalf("ResolveWorkflow failed: %v", err)
	}
	if got.Name != "basicFlow" {
		t.Fatalf("resolved %q, want basicFlow", got.Name)
	}
	if matchCalled {
		t.Fatal("matcher re-evaluated for a singly claimed ticket; claim resolves directly")
	}
}

func TestUnknownClaimInvalid(t *testing.T) {
	r := repoWith(binding(wf("basicFlow"), nil))
	ticket := task.Ticket{Key: "PAY-101", WorkflowClaims: []string{"wf:ghost"}}

	_, err := router.ResolveWorkflow(r, ticket)
	var ice *router.InvalidClaimError
	if !errors.As(err, &ice) {
		t.Fatalf("err = %v, want InvalidClaimError for unknown claim", err)
	}
}

func TestClaimForWorkflowNotBoundToRepo(t *testing.T) {
	// otherFlow exists but targets another repo, so it is not in this
	// repo's bindings.
	r := repoWith(binding(wf("basicFlow"), nil))
	ticket := task.Ticket{Key: "PAY-101", WorkflowClaims: []string{"wf:otherFlow"}}

	_, err := router.ResolveWorkflow(r, ticket)
	var ice *router.InvalidClaimError
	if !errors.As(err, &ice) {
		t.Fatalf("err = %v, want InvalidClaimError for unbound claim", err)
	}
}

func TestZeroMatchesIgnored(t *testing.T) {
	r := repoWith(binding(wf("basicFlow"), func(task.Ticket) bool { return false }))
	ticket := task.Ticket{Key: "PAY-101"}

	_, err := router.ResolveWorkflow(r, ticket)
	if !errors.Is(err, router.ErrNoMatch) {
		t.Fatalf("err = %v, want ErrNoMatch", err)
	}
}

func TestOneMatchSelected(t *testing.T) {
	r := repoWith(
		binding(wf("basicFlow"), func(task.Ticket) bool { return true }),
		binding(wf("otherFlow"), func(task.Ticket) bool { return false }),
	)
	ticket := task.Ticket{Key: "PAY-101"}

	got, err := router.ResolveWorkflow(r, ticket)
	if err != nil {
		t.Fatalf("ResolveWorkflow failed: %v", err)
	}
	if got.Name != "basicFlow" {
		t.Fatalf("resolved %q, want basicFlow", got.Name)
	}
}

func TestMultipleMatchesAmbiguous(t *testing.T) {
	r := repoWith(
		binding(wf("basicFlow"), func(task.Ticket) bool { return true }),
		binding(wf("otherFlow"), func(task.Ticket) bool { return true }),
	)
	ticket := task.Ticket{Key: "PAY-101", WorkflowClaims: nil}

	before := ticket
	_, err := router.ResolveWorkflow(r, ticket)
	var amb *router.AmbiguousError
	if !errors.As(err, &amb) {
		t.Fatalf("err = %v, want AmbiguousError", err)
	}
	if len(amb.Workflows) != 2 {
		t.Fatalf("AmbiguousError.Workflows = %v, want both workflows", amb.Workflows)
	}
	// No mutation on ambiguity.
	if ticket.WorkflowClaims != nil || ticket.Key != before.Key {
		t.Fatalf("ticket mutated on ambiguity: %+v", ticket)
	}
}
