// Package router is the pure Ticket Router. It has no Jira, SQLite, runner,
// or goroutine dependency.
package router

import (
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/rajpopat27/relay-flow/internal/repo"
	"github.com/rajpopat27/relay-flow/internal/task"
	"github.com/rajpopat27/relay-flow/internal/workflow"
)

// ErrNoMatch means an unclaimed ticket matches no workflow filter; the
// polling handler ignores it.
var ErrNoMatch = errors.New("ticket matches no workflow")

// ErrClaimOwnerMismatch means a ticket's single workflow claim is valid for
// this repo but the task adapter's ownership matcher rejects the current
// owner. It is distinct from invalid claims and no-match routing.
var ErrClaimOwnerMismatch = errors.New("claimed ticket owner does not match workflow")

// AmbiguousError means an unclaimed ticket matches more than one workflow.
// No ticket mutation occurs.
type AmbiguousError struct {
	Ticket    string
	Workflows []string
}

func (e *AmbiguousError) Error() string {
	return fmt.Sprintf("ticket %s matches multiple workflows: %s", e.Ticket, strings.Join(e.Workflows, ", "))
}

// InvalidClaimError means the ticket carries several workflow claims, or a
// claim naming an unknown workflow or one not registered for the repo.
type InvalidClaimError struct {
	Ticket   string
	Workflow string
	Repo     string
}

func (e *InvalidClaimError) Error() string {
	return fmt.Sprintf("ticket %s has invalid workflow claim %q for repo %s", e.Ticket, e.Workflow, e.Repo)
}

// ClaimOwnerMismatchError means a single claim names a workflow bound to the
// repo, but its adapter-owned ownership matcher rejects the current ticket.
type ClaimOwnerMismatchError struct {
	Ticket   string
	Workflow string
	Repo     string
}

func (e *ClaimOwnerMismatchError) Error() string {
	return fmt.Sprintf("ticket %s claim %q has a current owner that does not match repo %s", e.Ticket, e.Workflow, e.Repo)
}

func (e *ClaimOwnerMismatchError) Unwrap() error { return ErrClaimOwnerMismatch }

const claimPrefix = "wf:"

// ResolveWorkflow routes a ticket to exactly one workflow following the
// deterministic routing order: multiple claims are invalid; a single claim
// resolves directly from repo bindings after the adapter-owned ownership
// matcher accepts it; an unknown or unbound claim is invalid; otherwise
// precompiled lifecycle matchers run — zero matches is ErrNoMatch, one match
// wins, several is ambiguous.
func ResolveWorkflow(registered *repo.Repo, ticket task.Ticket) (*workflow.Workflow, error) {
	if registered == nil {
		return nil, &InvalidClaimError{Ticket: ticket.Key, Workflow: "", Repo: ""}
	}
	claims := ticket.WorkflowClaims
	if len(claims) > 1 {
		return nil, &InvalidClaimError{Ticket: ticket.Key, Workflow: strings.Join(claims, ","), Repo: registered.Name}
	}
	bindings := registered.Bindings()
	if len(claims) == 1 {
		name := strings.TrimPrefix(claims[0], claimPrefix)
		for _, b := range bindings {
			if b.Workflow.Name != name {
				continue
			}
			// A claimed ticket intentionally bypasses the complete matcher,
			// preserving lifecycle routing after status/label changes. The
			// separate ownership matcher is the only claimed-path gate. A
			// missing published matcher fails closed rather than transferring a
			// legacy claim to whichever server sees it first.
			if b.Ownership == nil || !b.Ownership(ticket) {
				return nil, &ClaimOwnerMismatchError{
					Ticket: ticket.Key, Workflow: name, Repo: registered.Name,
				}
			}
			return b.Workflow, nil
		}
		return nil, &InvalidClaimError{Ticket: ticket.Key, Workflow: name, Repo: registered.Name}
	}
	var matched []*workflow.Workflow
	for _, b := range bindings {
		if b.Match != nil && b.Match(ticket) {
			matched = append(matched, b.Workflow)
		}
	}
	switch len(matched) {
	case 0:
		return nil, ErrNoMatch
	case 1:
		return matched[0], nil
	default:
		names := make([]string, 0, len(matched))
		for _, wf := range matched {
			names = append(names, wf.Name)
		}
		sort.Strings(names)
		return nil, &AmbiguousError{Ticket: ticket.Key, Workflows: names}
	}
}
