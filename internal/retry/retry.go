// Package retry provides the single shared error classification and backoff
// policy used by durable activities, repo pollers, and (mirrored) the
// TypeScript harness plugin.
package retry

import (
	"context"
	"errors"
	"math"
	"time"
)

// Kind classifies a failure for retry purposes.
type Kind string

const (
	// Transient failures are retried with backoff until they succeed.
	Transient Kind = "transient"
	// Conflict failures indicate a manual task-system change; the run blocks
	// and retries with the shared backoff until external state becomes
	// compatible again.
	Conflict Kind = "conflict"
)

// Failure is the classified form of an error.
type Failure struct {
	Kind    Kind   `json:"kind"`
	Message string `json:"message"`
}

// conflictError marks err as a Conflict.
type conflictError struct{ err error }

func (e *conflictError) Error() string { return e.err.Error() }
func (e *conflictError) Unwrap() error { return e.err }

// ConflictError wraps err so Classify reports Kind=Conflict.
func ConflictError(err error) error {
	if err == nil {
		return nil
	}
	return &conflictError{err: err}
}

// Classify returns the Failure for err. nil yields a zero Failure.
func Classify(err error) Failure {
	if err == nil {
		return Failure{}
	}
	kind := Transient
	var ce *conflictError
	if errors.As(err, &ce) {
		kind = Conflict
	}
	return Failure{Kind: kind, Message: err.Error()}
}

// BackoffPolicy computes exponential backoff delays with jitter.
type BackoffPolicy struct {
	Initial time.Duration
	Maximum time.Duration
	Factor  float64
	Jitter  float64
}

// DefaultBackoffPolicy is the shared policy: 2s initial, factor 2, jitter 0.2,
// capped at 5 minutes.
var DefaultBackoffPolicy = BackoffPolicy{
	Initial: 2 * time.Second,
	Maximum: 5 * time.Minute,
	Factor:  2,
	Jitter:  0.2,
}

// Delay returns the delay before the given attempt (0-based). random must be
// in [0,1) and supplies the jitter so durable callers can stay replay-safe.
func (p BackoffPolicy) Delay(attempt int, random float64) time.Duration {
	if attempt < 0 {
		attempt = 0
	}
	base := float64(p.Initial) * math.Pow(p.Factor, float64(attempt))
	if base > float64(p.Maximum) {
		base = float64(p.Maximum)
	}
	// Symmetric jitter in [1-Jitter, 1+Jitter].
	j := (random*2 - 1) * p.Jitter
	d := time.Duration(base * (1 + j))
	if d < 0 {
		return 0
	}
	if d > p.Maximum {
		return p.Maximum
	}
	return d
}

// Do runs fn with the shared backoff until it succeeds, returns a Conflict,
// or ctx is canceled. Conflict failures are returned to the caller so the
// run can mark itself blocked.
func Do(ctx context.Context, policy BackoffPolicy, fn func() error) error {
	attempt := 0
	for {
		err := fn()
		if err == nil {
			return nil
		}
		if Classify(err).Kind == Conflict {
			return err
		}
		delay := policy.Delay(attempt, 0.5)
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(delay):
		}
		attempt++
	}
}
