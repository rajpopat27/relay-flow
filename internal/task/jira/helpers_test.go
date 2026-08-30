package jira

import (
	"context"

	"github.com/rajpopat27/relay-flow/internal/task"
	"github.com/rajpopat27/relay-flow/internal/task/jira/rest"
)

// fakeClient adapts the test-local Jira fake to the production REST seam.
type fakeClient struct {
	fake *fakeJira
}

func (f *fakeClient) Search(context.Context, string) ([]byte, error) {
	return f.fake.searchJSON, nil
}

func (f *fakeClient) ValidateCredentials(context.Context) error { return nil }

func (f *fakeClient) ValidateAssignee(context.Context, string, string) error { return nil }

func (f *fakeClient) ValidateStatus(context.Context, string, string) error { return nil }

func (f *fakeClient) View(context.Context, string) ([]byte, error) {
	return []byte(`{"fields":{"labels":[],"subtasks":[]}}`), nil
}

func (f *fakeClient) CreateSubtasks(context.Context, string, string, string, []rest.SubtaskSpec) ([]rest.CreatedSubtask, error) {
	return nil, errNotFaked
}

func (f *fakeClient) Transition(_ context.Context, key, status, assignee string) error {
	if assignee != "" {
		f.fake.assignments = append(f.fake.assignments, key+":"+assignee)
		if f.fake.assignErr != nil {
			return f.fake.assignErr
		}
	}
	f.fake.events = append(f.fake.events, "transition")
	f.fake.transition(key, status)
	return nil
}

func (f *fakeClient) EnsureLabel(_ context.Context, key, label string) error {
	f.fake.labelCalls = append(f.fake.labelCalls, key+":"+label)
	return nil
}

func (f *fakeClient) UpdateMailbox(context.Context, string, string, string) error { return nil }

func (f *fakeClient) ListComments(context.Context, string) ([]string, error) {
	return append([]string(nil), f.fake.comments...), nil
}

func (f *fakeClient) AddComment(_ context.Context, _ string, body string) error {
	f.fake.addedComments = append(f.fake.addedComments, body)
	return nil
}

type notFakedError struct{}

func (notFakedError) Error() string { return "not faked" }

var errNotFaked = notFakedError{}

// newSystemForTest builds the adapter around the test-local REST seam.
func newSystemForTest(fake *fakeJira) (task.System, error) {
	return newSystemForCLI(&fakeClient{fake: fake})
}
