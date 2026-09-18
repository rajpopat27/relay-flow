package goworkflows

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/cschleiden/go-workflows/backend"
)

func TestDetachedContextPreservesDeadline(t *testing.T) {
	deadline := time.Now().Add(2 * time.Second)
	parent, cancel := context.WithDeadline(context.Background(), deadline)
	defer cancel()
	detached, cleanup := detachedContext(parent)
	defer cleanup()
	cancel()
	if err := detached.Err(); err != nil {
		t.Fatalf("detached context inherited parent cancellation: %v", err)
	}
	got, ok := detached.Deadline()
	if !ok || got.After(deadline.Add(10*time.Millisecond)) || got.Before(deadline.Add(-10*time.Millisecond)) {
		t.Fatalf("detached deadline = %v, want approximately %v", got, deadline)
	}
}

func TestIsMissingWorkflowInstanceOnlyMatchesConfirmedAbsence(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{name: "sql no rows", err: sql.ErrNoRows, want: true},
		{name: "wrapped sql no rows", err: fmt.Errorf("lookup instance: %w", sql.ErrNoRows), want: true},
		{name: "backend not found", err: backend.ErrInstanceNotFound, want: true},
		{name: "wrapped backend not found", err: fmt.Errorf("cancel instance: %w", backend.ErrInstanceNotFound), want: true},
		{name: "database locked", err: errors.New("database is locked"), want: false},
		{name: "context canceled", err: errors.New("context canceled"), want: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isMissingWorkflowInstance(tt.err); got != tt.want {
				t.Fatalf("isMissingWorkflowInstance(%v) = %v, want %v", tt.err, got, tt.want)
			}
		})
	}
}
