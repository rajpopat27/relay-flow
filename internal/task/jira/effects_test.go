package jira

import (
	"context"
	"strings"
	"testing"

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
