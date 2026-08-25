package jira

import (
	"context"

	"github.com/rajpopat27/relay-flow/internal/task"
)

// fakeClient adapts the test-local fakeACLI (transitions + raw search batch)
// to the production acli.Client seam.
type fakeClient struct {
	fake *fakeACLI
}

func (f *fakeClient) Search(context.Context, string) ([]byte, error) {
	return f.fake.searchJSON, nil
}

func (f *fakeClient) ValidateAssignee(context.Context, string) error { return nil }

func (f *fakeClient) ValidateStatus(context.Context, string, string) error { return nil }

func (f *fakeClient) View(context.Context, string) ([]byte, error) {
	return []byte(`{"fields":{"labels":[],"subtasks":[]}}`), nil
}

func (f *fakeClient) CreateSubtask(context.Context, string, string, string) (string, string, error) {
	return "", "", errNotFaked
}

func (f *fakeClient) Transition(_ context.Context, key, status string) error {
	f.fake.transition(key, status)
	return nil
}

func (f *fakeClient) EnsureLabel(context.Context, string, string) error { return nil }

func (f *fakeClient) UpdateDescription(context.Context, string, string) error { return nil }

func (f *fakeClient) ListComments(context.Context, string) ([]string, error) { return nil, nil }

func (f *fakeClient) AddComment(context.Context, string, string) error { return nil }

type notFakedError struct{}

func (notFakedError) Error() string { return "not faked" }

var errNotFaked = notFakedError{}

// newSystemForTest builds the adapter around the test-local fake ACLI seam.
func newSystemForTest(fake *fakeACLI) (task.System, error) {
	return newSystemForCLI(&fakeClient{fake: fake})
}
