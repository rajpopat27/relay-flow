package goworkflows_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/rajpopat27/relay-flow/internal/config"
	"github.com/rajpopat27/relay-flow/internal/execution/goworkflows"
	"github.com/rajpopat27/relay-flow/internal/identity"
	recoverpkg "github.com/rajpopat27/relay-flow/internal/recover"
	"github.com/rajpopat27/relay-flow/internal/router"
	"github.com/rajpopat27/relay-flow/internal/run"
	"github.com/rajpopat27/relay-flow/internal/task"
	"github.com/rajpopat27/relay-flow/internal/workflow"
)

// ownershipCompositionState models the provider-owned labels and current
// assignee shared by two independent relay-flow databases. The adapter fake
// persists a durable wf-owner marker when the first server claims the ticket.
type ownershipCompositionState struct {
	mu       sync.Mutex
	assignee string
	labels   []string
}

type ownershipCompositionTaskSystem struct {
	*fakeTaskSystem
	owner string
	state *ownershipCompositionState
}

func newOwnershipCompositionTaskSystem(owner string, state *ownershipCompositionState, log *eventLog) *ownershipCompositionTaskSystem {
	return &ownershipCompositionTaskSystem{
		fakeTaskSystem: newFakeTaskSystem(log), owner: owner, state: state,
	}
}

func (s *ownershipCompositionTaskSystem) Poll(context.Context) ([]task.Ticket, error) {
	s.state.mu.Lock()
	defer s.state.mu.Unlock()
	claims := make([]string, 0, 1)
	for _, label := range s.state.labels {
		if len(label) > len("wf:") && label[:len("wf:")] == "wf:" {
			claims = append(claims, label)
		}
	}
	return []task.Ticket{{
		ID: "1", Key: "PAY-101", Title: "ownership composition",
		WorkflowClaims: claims,
		Fields: map[string]any{
			"assignee": s.state.assignee,
			"labels":   append([]string(nil), s.state.labels...),
		},
	}}, nil
}

func (s *ownershipCompositionTaskSystem) CompileFilter(config.RawValues) (func(task.Ticket) bool, error) {
	return func(ticket task.Ticket) bool {
		assignee, _ := ticket.Fields["assignee"].(string)
		return assignee == s.owner
	}, nil
}

func (s *ownershipCompositionTaskSystem) CompileOwnershipFilter(config.RawValues) (func(task.Ticket) bool, error) {
	return func(ticket task.Ticket) bool {
		if len(ticket.WorkflowClaims) != 1 {
			return false
		}
		assignee, _ := ticket.Fields["assignee"].(string)
		if assignee != s.owner {
			return false
		}
		want := "wf-owner:" + ticket.WorkflowClaims[0][len("wf:"):] + ":" + s.owner
		labels, _ := ticket.Fields["labels"].([]string)
		for _, label := range labels {
			if label == want {
				return true
			}
		}
		return false
	}, nil
}

func (s *ownershipCompositionTaskSystem) ValidateOwnership(context.Context, task.TicketRef, string, config.RawValues) error {
	s.state.mu.Lock()
	defer s.state.mu.Unlock()
	if s.hasOwnerMarkerLocked("ownershipFlow") {
		return nil
	}
	return &task.OwnershipMismatchError{Ticket: "PAY-101", Workflow: "ownershipFlow"}
}

func (s *ownershipCompositionTaskSystem) BackfillClaimOwner(context.Context, task.TicketRef, string, config.RawValues) error {
	s.state.mu.Lock()
	defer s.state.mu.Unlock()
	if !containsLabel(s.state.labels, "wf:ownershipFlow") {
		return &task.OwnershipMismatchError{Ticket: "PAY-101", Workflow: "ownershipFlow"}
	}
	marker := "wf-owner:ownershipFlow:" + s.owner
	if !containsLabel(s.state.labels, marker) {
		s.state.labels = append(s.state.labels, marker)
	}
	return nil
}

func (s *ownershipCompositionTaskSystem) ClaimIfOwned(ctx context.Context, ticket task.TicketRef, workflowName string, _ config.RawValues) error {
	s.state.mu.Lock()
	defer s.state.mu.Unlock()
	if s.state.assignee != s.owner {
		return &task.OwnershipMismatchError{Ticket: ticket.Key, Workflow: workflowName}
	}
	ownerMarker := "wf-owner:" + workflowName + ":" + s.owner
	claim := "wf:" + workflowName
	for _, label := range s.state.labels {
		if label == ownerMarker && containsLabel(s.state.labels, claim) {
			return nil
		}
		if len(label) > len("wf:") && label[:len("wf:")] == "wf:" {
			return &task.OwnershipMismatchError{Ticket: ticket.Key, Workflow: workflowName}
		}
	}
	s.state.labels = append(s.state.labels, ownerMarker, claim)
	return nil
}

func (s *ownershipCompositionTaskSystem) hasOwnerMarkerLocked(workflowName string) bool {
	return containsLabel(s.state.labels, "wf-owner:"+workflowName+":"+s.owner)
}

func containsLabel(labels []string, want string) bool {
	for _, label := range labels {
		if label == want {
			return true
		}
	}
	return false
}

func TestExplicitLegacyBackfillPreservesClaimAndEnablesRoutingAndRecovery(t *testing.T) {
	state := &ownershipCompositionState{assignee: "alice", labels: []string{"wf:ownershipFlow"}}
	log := newEventLog()
	sys := newOwnershipCompositionTaskSystem("alice", state, log)
	reg := repoRegistryWith("payments", sys)
	wf := linearWorkflow(false)
	wf.Name = "ownershipFlow"
	wf.TaskConfig = config.RawValues{"filters": map[string]any{"assignees": []any{"currentUser()"}}}
	if err := reg.BindWorkflows([]*workflow.Workflow{&wf}); err != nil {
		t.Fatal(err)
	}
	workflowReg := &workflow.Registry{}
	workflowReg.Replace(&wf)
	fr := newFakeRunner(log)
	engine := newEngine(t, goworkflows.Dependencies{Repos: reg, Runner: fr, Harness: newFakeHarness(log)})
	manager := &run.RunManager{Executor: engine, Runs: engine, Repos: reg, Workflows: workflowReg}
	if err := manager.BackfillClaimOwner(context.Background(), "payments", "PAY-101", "ownershipFlow"); err != nil {
		t.Fatalf("explicit backfill failed: %v", err)
	}
	state.mu.Lock()
	labels := append([]string(nil), state.labels...)
	state.mu.Unlock()
	if !containsLabel(labels, "wf:ownershipFlow") || !containsLabel(labels, "wf-owner:ownershipFlow:alice") || len(labels) != 2 {
		t.Fatalf("backfill labels = %v, want preserved claim plus one provenance marker", labels)
	}
	rp, _ := reg.Get("payments")
	ticket, _ := sys.Poll(context.Background())
	if _, err := router.ResolveWorkflow(rp, ticket[0]); err != nil {
		t.Fatalf("backfilled claim did not route normally: %v", err)
	}
	specsFor := func(system task.System, work run.Work, w *workflow.Workflow) ([]task.MailboxSpec, error) {
		return goworkflows.RenderMailboxSpecs(system, work, w)
	}
	if err := recoverpkg.FromTaskSystem(context.Background(), reg, fr, manager, specsFor); err != nil {
		t.Fatalf("backfilled claim did not recover: %v", err)
	}
	if runs, err := engine.ListRuns(context.Background(), run.Filter{Repo: "payments", Workflow: "ownershipFlow", Ticket: "PAY-101"}); err != nil || len(runs) != 1 {
		t.Fatalf("recovered runs = %v, %v; want one run", runs, err)
	}
}

func TestIndependentRunDatabasesCannotTakeOverReassignedClaim(t *testing.T) {
	state := &ownershipCompositionState{assignee: "alice"}
	logA, logB := newEventLog(), newEventLog()
	sysA := newOwnershipCompositionTaskSystem("alice", state, logA)
	sysB := newOwnershipCompositionTaskSystem("bob", state, logB)
	regA := repoRegistryWith("payments", sysA)
	regB := repoRegistryWith("payments", sysB)
	wf := linearWorkflow(false)
	wf.Name = "ownershipFlow"
	wf.TaskConfig = config.RawValues{"filters": map[string]any{
		"assignees": []any{"currentUser()"},
	}}
	if err := regA.BindWorkflows([]*workflow.Workflow{&wf}); err != nil {
		t.Fatal(err)
	}
	if err := regB.BindWorkflows([]*workflow.Workflow{&wf}); err != nil {
		t.Fatal(err)
	}

	engineA := newEngine(t, goworkflows.Dependencies{
		Repos: regA, Runner: newFakeRunner(logA), Harness: newFakeHarness(logA),
	})
	engineB := newEngine(t, goworkflows.Dependencies{
		Repos: regB, Runner: newFakeRunner(logB), Harness: newFakeHarness(logB),
	})
	managerA := &run.RunManager{Executor: engineA, Runs: engineA}
	managerB := &run.RunManager{Executor: engineB, Runs: engineB}

	ticketA, err := sysA.Poll(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	rpA, _ := regA.Get("payments")
	resolvedA, err := router.ResolveWorkflow(rpA, ticketA[0])
	if err != nil {
		t.Fatalf("owner A route failed: %v", err)
	}
	if err := managerA.EnsureRun(context.Background(), rpA, resolvedA, ticketA[0]); err != nil {
		t.Fatalf("owner A EnsureRun failed: %v", err)
	}
	waitFor(t, 10*time.Second, func() bool {
		current, err := engineA.GetRun(context.Background(), identity.NewRunID("payments", "ownershipFlow", "PAY-101"))
		return err == nil && current.State != run.StateCompleted && current.State != run.StateCanceled
	})

	state.mu.Lock()
	state.assignee = "bob"
	state.mu.Unlock()
	ticketB, err := sysB.Poll(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	rpB, _ := regB.Get("payments")
	if _, err := router.ResolveWorkflow(rpB, ticketB[0]); !errors.Is(err, router.ErrClaimOwnerMismatch) {
		t.Fatalf("owner B route error = %v, want claim-owner-mismatch", err)
	}
	if err := managerB.EnsureRun(context.Background(), rpB, &wf, ticketB[0]); !errors.Is(err, task.ErrOwnershipMismatch) {
		t.Fatalf("owner B EnsureRun error = %v, want ownership mismatch", err)
	}
	if runs, err := engineB.ListRuns(context.Background(), run.Filter{Repo: "payments", Workflow: "ownershipFlow", Ticket: "PAY-101"}); err != nil || len(runs) != 0 {
		t.Fatalf("owner B database runs = %v, %v; want none", runs, err)
	}

	// Reassignment is a current-owner mismatch for both servers. The
	// original durable run remains in database A, but neither server calls
	// EnsureRun to reconcile or create execution state after the mismatch.
	ticketAAfterReassignment, _ := sysA.Poll(context.Background())
	if _, err := router.ResolveWorkflow(rpA, ticketAAfterReassignment[0]); !errors.Is(err, router.ErrClaimOwnerMismatch) {
		t.Fatalf("owner A route after reassignment = %v, want claim-owner-mismatch", err)
	}
	if err := managerA.EnsureRun(context.Background(), rpA, &wf, ticketAAfterReassignment[0]); !errors.Is(err, task.ErrOwnershipMismatch) {
		t.Fatalf("owner A EnsureRun after reassignment = %v, want ownership mismatch", err)
	}
	if runs, err := engineA.ListRuns(context.Background(), run.Filter{Repo: "payments", Workflow: "ownershipFlow", Ticket: "PAY-101"}); err != nil || len(runs) != 1 {
		t.Fatalf("owner A database runs after reassignment = %v, %v; want one existing run", runs, err)
	}
}
