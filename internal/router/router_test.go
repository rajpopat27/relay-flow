package router_test

import (
	"context"
	"errors"
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	"github.com/rajpopat27/relay-flow/internal/config"
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

type routingTaskSystem struct{ task.System }

func (routingTaskSystem) Poll(context.Context) ([]task.Ticket, error) {
	return []task.Ticket{{Key: "PAY-101"}}, nil
}

func (routingTaskSystem) CompileFilter(config.RawValues) (func(task.Ticket) bool, error) {
	return func(task.Ticket) bool { return true }, nil
}

func TestPollerRoutesWhileBindingsAreReplaced(t *testing.T) {
	registry := repo.NewRegistry()
	registered := &repo.Repo{Name: "payments", TaskSystem: routingTaskSystem{}}
	registry.Replace(registered)

	first, second := wf("firstFlow"), wf("secondFlow")
	if err := registry.BindWorkflows([]*workflow.Workflow{first}); err != nil {
		t.Fatal(err)
	}

	var routed atomic.Int64
	failures := make(chan error, 1)
	poller := &repo.RepoPoller{
		Repo:     registered,
		Interval: time.Microsecond,
		Handle: func(_ context.Context, registered *repo.Repo, tickets []task.Ticket) {
			for _, ticket := range tickets {
				resolved, err := router.ResolveWorkflow(registered, ticket)
				if err != nil {
					select {
					case failures <- err:
					default:
					}
					continue
				}
				if resolved != first && resolved != second {
					select {
					case failures <- fmt.Errorf("resolved unexpected workflow %q", resolved.Name):
					default:
					}
				}
				routed.Add(1)
			}
		},
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		poller.Run(ctx)
		close(done)
	}()

	for i := 0; i < 1_000; i++ {
		bindings := []*workflow.Workflow{first}
		if i%2 == 1 {
			bindings = []*workflow.Workflow{second}
		}
		if err := registry.BindWorkflows(bindings); err != nil {
			cancel()
			<-done
			t.Fatal(err)
		}
	}

	deadline := time.Now().Add(time.Second)
	for routed.Load() == 0 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	cancel()
	<-done

	select {
	case err := <-failures:
		t.Fatalf("routing during binding replacement failed: %v", err)
	default:
	}
	if routed.Load() == 0 {
		t.Fatal("poller did not route while bindings were replaced")
	}
}
