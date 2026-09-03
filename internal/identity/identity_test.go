package identity_test

import (
	"testing"

	"github.com/rajpopat27/relay-flow/internal/identity"
)

func TestAttemptRunIDUsesNumericAttemptSuffix(t *testing.T) {
	logical := identity.NewRunID("payments", "basicFlow", "PAY-101")

	if got := identity.NewAttemptRunID(logical, 1); got != logical {
		t.Fatalf("initial attempt ID = %q, want logical ID %q", got, logical)
	}
	if got := identity.NewAttemptRunID(logical, 2); got != logical+"~attempt~2" {
		t.Fatalf("restart attempt ID = %q, want numeric suffix", got)
	}
	if got := identity.NewAttemptRunID(logical, 3); got == identity.NewAttemptRunID(logical, 2) {
		t.Fatalf("attempt IDs are not fenced: attempt 2 and 3 both use %q", got)
	}
}

func TestAttemptIDsAreNumeric(t *testing.T) {
	var first identity.AttemptID = 1
	var second identity.AttemptID = 2
	if first != 1 || second != 2 {
		t.Fatalf("attempt IDs are not numeric: %d, %d", first, second)
	}
}

func TestLogicalRunIDStripsAttemptSuffix(t *testing.T) {
	logical := identity.NewRunID("payments", "basicFlow", "PAY-101")
	execution := identity.NewAttemptRunID(logical, 4)
	if got := identity.LogicalRunID(execution); got != logical {
		t.Fatalf("logical ID = %q, want %q", got, logical)
	}
	if got := identity.LogicalRunID(logical); got != logical {
		t.Fatalf("first-attempt logical ID = %q, want %q", got, logical)
	}
}
