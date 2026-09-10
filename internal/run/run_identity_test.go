package run_test

import (
	"testing"

	"github.com/rajpopat27/relay-flow/internal/identity"
	"github.com/rajpopat27/relay-flow/internal/run"
)

// 3.14: run identity per specs/durable-run-execution "Each parent ticket
// has one durable run" and "Every node entry has a distinct visit identity".

func TestRunIDDeterministic(t *testing.T) {
	a := identity.NewRunID("payments", "basicFlow", "PAY-101")
	b := identity.NewRunID("payments", "basicFlow", "PAY-101")
	if a != b {
		t.Fatalf("run ID not deterministic: %q vs %q", a, b)
	}
	c := identity.NewRunID("payments", "basicFlow", "PAY-102")
	if a == c {
		t.Fatal("different tickets produced the same run ID")
	}
	d := identity.NewRunID("other", "basicFlow", "PAY-101")
	if a == d {
		t.Fatal("different repos produced the same run ID")
	}
}

func TestRunIDDelimiterSafe(t *testing.T) {
	// Components containing delimiters must not collide.
	a := identity.NewRunID("pay/ments", "basicFlow", "PAY-101")
	b := identity.NewRunID("pay", "ments/basicFlow", "PAY-101")
	if a == b {
		t.Fatalf("delimiter collision: %q == %q", a, b)
	}
}

func TestNodeVisitIDsUniquePerEntry(t *testing.T) {
	first := identity.NewNodeVisitID()
	second := identity.NewNodeVisitID()
	if first == second {
		t.Fatal("two node entries generated the same nodeVisitID")
	}
}

// EnsureRun idempotence is covered with the RunManager tests (3.12) using a
// fake executor: a repeated EnsureRun with the same deterministic ID
// returns the existing run without restarting. The durable side-effect
// stability of nodeVisitID across replay is an engine-level behavior tested
// in internal/execution/goworkflows.

var _ = run.ID("")
