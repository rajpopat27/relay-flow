package beads

import (
	"context"
	"reflect"
	"strings"
	"testing"

	"github.com/rajpopat27/relay-flow/internal/task"
	"github.com/rajpopat27/relay-flow/internal/task/beads/bdcli"
)

func TestHasCommentFindsMarkerOnParentOrMailbox(t *testing.T) {
	for _, tc := range []struct {
		name      string
		target    task.Target
		issueID   string
		wantFound bool
	}{
		{
			name:      "parent summary",
			target:    task.Target{Parent: task.TicketRef{ID: "demo-parent", Key: "demo-parent"}},
			issueID:   "demo-parent",
			wantFound: true,
		},
		{
			name: "mailbox feedback",
			target: task.Target{
				Parent:  task.TicketRef{ID: "demo-parent", Key: "demo-parent"},
				Mailbox: &task.Mailbox{ID: "demo-parent.1", Key: "demo-parent.1", Node: "implement"},
			},
			issueID:   "demo-parent.1",
			wantFound: true,
		},
		{
			name:      "missing marker",
			target:    task.Target{Parent: task.TicketRef{ID: "demo-parent", Key: "demo-parent"}},
			issueID:   "demo-parent",
			wantFound: false,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			client := &commentClient{comments: map[string][]bdcli.Comment{
				"demo-parent": {
					{ID: "comment-parent", Text: "parent note\n<!-- visit-parent:summary -->"},
				},
				"demo-parent.1": {
					{ID: "comment-mailbox", Text: "feedback\n<!-- visit-mailbox:feedback -->"},
				},
			}}
			marker := "visit-parent:summary"
			if tc.name == "mailbox feedback" {
				marker = "visit-mailbox:feedback"
			}
			if tc.name == "missing marker" {
				marker = "visit-missing:summary"
			}
			sys := &system{cli: client}
			got, err := sys.HasComment(context.Background(), tc.target, marker)
			if err != nil {
				t.Fatal(err)
			}
			if got != tc.wantFound {
				t.Fatalf("HasComment = %v, want %v", got, tc.wantFound)
			}
			if !reflect.DeepEqual(client.listed, []string{tc.issueID}) {
				t.Fatalf("comment lookup IDs = %v, want [%s]", client.listed, tc.issueID)
			}
		})
	}
}

func TestCommentAvoidsDuplicateMarkerAndPreservesMultilineBody(t *testing.T) {
	marker := "visit-1:summary"
	target := task.Target{
		Parent:  task.TicketRef{ID: "demo-parent", Key: "demo-parent"},
		Mailbox: &task.Mailbox{ID: "demo-parent.1", Key: "demo-parent.1", Node: "implement"},
	}
	client := &commentClient{comments: map[string][]bdcli.Comment{
		"demo-parent.1": {{ID: "existing", Text: "already stored\n<!-- " + marker + " -->"}},
	}}
	sys := &system{cli: client}

	if err := sys.Comment(context.Background(), target, "SUMMARY:\nfirst line\nsecond line", marker); err != nil {
		t.Fatal(err)
	}
	if len(client.added) != 0 {
		t.Fatalf("duplicate marker caused comment write: %+v", client.added)
	}

	client.comments[target.Mailbox.Key] = nil
	body := "SUMMARY:\nfirst line\nsecond line"
	if err := sys.Comment(context.Background(), target, body, marker); err != nil {
		t.Fatal(err)
	}
	if len(client.added) != 1 {
		t.Fatalf("comment writes = %+v, want one", client.added)
	}
	if client.added[0].issueID != target.Mailbox.Key {
		t.Fatalf("comment destination = %q, want %q", client.added[0].issueID, target.Mailbox.Key)
	}
	wantBody := body + "\n\n<!-- " + marker + " -->"
	if client.added[0].body != wantBody {
		t.Fatalf("comment body = %q, want %q", client.added[0].body, wantBody)
	}
	if !strings.Contains(client.added[0].body, "first line\nsecond line") {
		t.Fatalf("multiline content was not preserved: %q", client.added[0].body)
	}
}

func TestCommentUsesSummaryAndSelectedFeedbackMailboxDestinations(t *testing.T) {
	client := &commentClient{comments: map[string][]bdcli.Comment{}}
	sys := &system{cli: client}
	parent := task.TicketRef{ID: "demo-parent", Key: "demo-parent"}
	current := &task.Mailbox{ID: "demo-parent.1", Key: "demo-parent.1", Node: "implement"}
	selectedNext := &task.Mailbox{ID: "demo-parent.2", Key: "demo-parent.2", Node: "review"}

	if err := sys.Comment(context.Background(), task.Target{Parent: parent, Mailbox: current}, "summary", "visit-1:summary"); err != nil {
		t.Fatal(err)
	}
	if err := sys.Comment(context.Background(), task.Target{Parent: parent, Mailbox: selectedNext}, "feedback", "visit-1:feedback"); err != nil {
		t.Fatal(err)
	}
	got := []string{client.added[0].issueID, client.added[1].issueID}
	want := []string{current.Key, selectedNext.Key}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("comment destinations = %v, want %v", got, want)
	}
}

func TestBeadsRenderTextExposesSupportedTemplateValues(t *testing.T) {
	sys := &system{effective: Config{Templates: Templates{
		MailboxDescription: "mailbox {{runID}}|{{ticket}}|{{workflow}}|{{repo}}|{{node}}|{{nodeType}}|{{agent}}|{{nodeDescription}}|{{nextSteps}}|{{successRoutes}}|{{failureRoutes}}|{{mailbox}}",
		SummaryComment:     "summary {{sourceNode}}|{{targetNode}}|{{mailbox}}|{{summaryReport}}",
		FeedbackComment:    "feedback {{sourceNode}}|{{targetNode}}|{{mailbox}}|{{feedbackReport}}",
	}}}
	data := task.TextData{
		RunID:           "run-1",
		Ticket:          "demo-parent",
		Workflow:        "implementation",
		Repo:            "payments",
		Node:            "review",
		NodeType:        "hitl",
		Agent:           "reviewer",
		NodeDescription: "Review the change.",
		NextSteps:       "implement; end",
		SuccessRoutes:   "end",
		FailureRoutes:   "implement",
		Mailbox:         "demo-parent.2",
		SourceNode:      "review",
		TargetNode:      "implement",
		SummaryReport:   "COMPLETED:\nreviewed",
		FeedbackReport:  "REQUIRED ACTIONS:\nfix it",
	}

	description, err := sys.RenderText(task.TextMailboxDescription, data)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"run-1", "demo-parent", "implementation", "payments", "review", "hitl", "reviewer",
		"Review the change.", "implement; end", "end", "implement", "demo-parent.2",
	} {
		if !strings.Contains(description, want) {
			t.Fatalf("mailbox description missing %q: %q", want, description)
		}
	}

	summary, err := sys.RenderText(task.TextSummaryComment, data)
	if err != nil || !strings.Contains(summary, data.SummaryReport) || !strings.Contains(summary, "review|implement|demo-parent.2") {
		t.Fatalf("summary = %q, %v", summary, err)
	}
	feedback, err := sys.RenderText(task.TextFeedbackComment, data)
	if err != nil || !strings.Contains(feedback, data.FeedbackReport) || !strings.Contains(feedback, "review|implement|demo-parent.2") {
		t.Fatalf("feedback = %q, %v", feedback, err)
	}
}

func TestBeadsTemplateValidationRequiresReportPlaceholders(t *testing.T) {
	for _, tc := range []struct {
		name      string
		templates Templates
		wantError string
	}{
		{
			name: "summary report",
			templates: Templates{
				SummaryComment:  "summary",
				FeedbackComment: "feedback {{feedbackReport}}",
			},
			wantError: "summaryComment must contain {{summaryReport}}",
		},
		{
			name: "feedback report",
			templates: Templates{
				SummaryComment:  "summary {{summaryReport}}",
				FeedbackComment: "feedback",
			},
			wantError: "feedbackComment must contain {{feedbackReport}}",
		},
		{
			name: "unknown variable",
			templates: Templates{
				MailboxDescription: "mailbox {{mailboxInstructions}}",
				SummaryComment:     "summary {{summaryReport}}",
				FeedbackComment:    "feedback {{feedbackReport}}",
			},
			wantError: "unknown template variable {{mailboxInstructions}}",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := validateTemplates(tc.templates)
			if err == nil || !strings.Contains(err.Error(), tc.wantError) {
				t.Fatalf("validateTemplates error = %v, want %q", err, tc.wantError)
			}
		})
	}
}

type commentClient struct {
	mailboxClient
	comments map[string][]bdcli.Comment
	listed   []string
	added    []addedComment
}

type addedComment struct {
	issueID string
	body    string
}

func (f *commentClient) ListComments(_ context.Context, issueID string) ([]bdcli.Comment, error) {
	f.listed = append(f.listed, issueID)
	return append([]bdcli.Comment(nil), f.comments[issueID]...), nil
}

func (f *commentClient) AddComment(_ context.Context, issueID, body string) error {
	f.added = append(f.added, addedComment{issueID: issueID, body: body})
	return nil
}

var _ bdcli.Client = (*commentClient)(nil)
