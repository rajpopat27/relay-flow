package jira

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/rajpopat27/relay-flow/internal/config"
	"github.com/rajpopat27/relay-flow/internal/task"
)

func TestClaimUsesOneLabelAdd(t *testing.T) {
	fake := &fakeJira{}
	sys := newSystemWithFake(t, fake)
	if err := sys.Claim(context.Background(), task.TicketRef{Key: "PAY-1"}, "flow"); err != nil {
		t.Fatal(err)
	}
	if len(fake.labelCalls) != 1 || fake.labelCalls[0] != "PAY-1:wf:flow" {
		t.Fatalf("label calls = %v, want one claim update", fake.labelCalls)
	}
}

func TestCommentKeepsMarkerIdempotency(t *testing.T) {
	fake := &fakeJira{comments: []string{"existing\n<!-- visit:summary -->"}}
	sys := newSystemWithFake(t, fake)
	target := task.Target{Parent: task.TicketRef{Key: "PAY-1"}}
	if err := sys.Comment(context.Background(), target, "summary", "visit:summary"); err != nil {
		t.Fatal(err)
	}
	if len(fake.addedComments) != 0 {
		t.Fatal("duplicate marked comment was posted")
	}
	fake.comments = nil
	if err := sys.Comment(context.Background(), target, "summary", "visit:summary"); err != nil {
		t.Fatal(err)
	}
	if len(fake.addedComments) != 1 || !strings.Contains(fake.addedComments[0], "visit:summary") {
		t.Fatalf("posted comments = %v", fake.addedComments)
	}
}

func TestJiraClaimIfOwnedRejectsReassignmentBeforeLabelWrite(t *testing.T) {
	fake := &fakeJira{viewJSON: []byte(`{"id":"1","key":"PAY-1","fields":{"summary":"work","assignee":{"emailAddress":"bob@example.com"},"labels":[]}}`)}
	sys := newSystemWithFake(t, fake)
	err := sys.(task.ConditionalClaimer).ClaimIfOwned(context.Background(), task.TicketRef{Key: "PAY-1"}, "flow", config.RawValues{
		"filters": map[string]any{"assignees": []any{"alice@example.com"}},
	})
	if !errors.Is(err, task.ErrOwnershipMismatch) {
		t.Fatalf("ClaimIfOwned error = %v, want ownership mismatch", err)
	}
	if len(fake.labelCalls) != 0 {
		t.Fatalf("label calls = %v, want none after reassignment", fake.labelCalls)
	}
}

func TestJiraClaimIfOwnedAddsLabelForCurrentOwner(t *testing.T) {
	fake := &fakeJira{viewJSON: []byte(`{"id":"1","key":"PAY-1","fields":{"summary":"work","assignee":{"emailAddress":"ALICE@EXAMPLE.COM"},"labels":[]}}`)}
	sys := newSystemWithFake(t, fake)
	if err := sys.(task.ConditionalClaimer).ClaimIfOwned(context.Background(), task.TicketRef{Key: "PAY-1"}, "flow", config.RawValues{
		"filters": map[string]any{"assignees": []any{"alice@example.com"}},
	}); err != nil {
		t.Fatalf("ClaimIfOwned failed for current owner: %v", err)
	}
	want := []string{
		"PAY-1:" + claimOwnerLabel("flow", "alice@example.com"),
		"PAY-1:wf:flow",
	}
	if !reflect.DeepEqual(fake.labelCalls, want) {
		t.Fatalf("label calls = %v, want one owner+workflow label write %v", fake.labelCalls, want)
	}
}

func TestJiraClaimIfOwnedAcceptsClaimAppearingAfterPollForSameOwner(t *testing.T) {
	view := fmt.Sprintf(`{"id":"1","key":"PAY-1","fields":{"assignee":{"emailAddress":"alice@example.com"},"labels":[%q,"wf:flow"]}}`, claimOwnerLabel("flow", "alice@example.com"))
	fake := &fakeJira{viewJSON: []byte(view)}
	sys := newSystemWithFake(t, fake)
	if err := sys.(task.ConditionalClaimer).ClaimIfOwned(context.Background(), task.TicketRef{Key: "PAY-1"}, "flow", config.RawValues{
		"filters": map[string]any{"assignees": []any{"alice@example.com", "bob@example.com"}},
	}); err != nil {
		t.Fatalf("ClaimIfOwned rejected an existing same-owner claim: %v", err)
	}
	if len(fake.labelCalls) != 2 {
		t.Fatalf("idempotent claim label calls = %v, want owner and wf labels", fake.labelCalls)
	}
}

func TestJiraClaimIfOwnedRejectsAppearingClaimAfterReassignment(t *testing.T) {
	view := fmt.Sprintf(`{"id":"1","key":"PAY-1","fields":{"assignee":{"emailAddress":"bob@example.com"},"labels":[%q,"wf:flow"]}}`, claimOwnerLabel("flow", "alice@example.com"))
	fake := &fakeJira{viewJSON: []byte(view)}
	sys := newSystemWithFake(t, fake)
	err := sys.(task.ConditionalClaimer).ClaimIfOwned(context.Background(), task.TicketRef{Key: "PAY-1"}, "flow", config.RawValues{
		"filters": map[string]any{"assignees": []any{"alice@example.com", "bob@example.com"}},
	})
	if !errors.Is(err, task.ErrOwnershipMismatch) || len(fake.labelCalls) != 0 {
		t.Fatalf("ClaimIfOwned error=%v labels=%v, want mismatch with no writes", err, fake.labelCalls)
	}
}

func TestJiraValidateOwnershipRejectsCurrentAssigneeMismatch(t *testing.T) {
	fake := &fakeJira{viewJSON: []byte(fmt.Sprintf(`{"id":"1","key":"PAY-1","fields":{"assignee":{"emailAddress":"bob@example.com"},"labels":[%q,"wf:flow"]}}`, claimOwnerLabel("flow", "alice@example.com")))}
	sys := newSystemWithFake(t, fake)
	err := sys.(task.OwnershipValidator).ValidateOwnership(context.Background(), task.TicketRef{Key: "PAY-1"}, "flow", config.RawValues{
		"filters": map[string]any{"assignees": []any{"alice@example.com"}},
	})
	if !errors.Is(err, task.ErrOwnershipMismatch) {
		t.Fatalf("ValidateOwnership error = %v, want current-assignee mismatch", err)
	}
}

func TestJiraBackfillLegacyClaimAddsOnlyProvenance(t *testing.T) {
	fake := &fakeJira{viewJSON: []byte(`{"id":"1","key":"PAY-1","fields":{"assignee":{"emailAddress":"alice@example.com"},"labels":["wf:flow"]}}`)}
	sys := newSystemWithFake(t, fake)
	if err := sys.(task.ClaimOwnerBackfiller).BackfillClaimOwner(context.Background(), task.TicketRef{Key: "PAY-1"}, "flow", config.RawValues{
		"filters": map[string]any{"assignees": []any{"alice@example.com"}},
	}); err != nil {
		t.Fatalf("BackfillClaimOwner failed: %v", err)
	}
	want := []string{"PAY-1:" + claimOwnerLabel("flow", "alice@example.com")}
	if !reflect.DeepEqual(fake.labelCalls, want) {
		t.Fatalf("backfill label calls = %v, want provenance only %v", fake.labelCalls, want)
	}
}
