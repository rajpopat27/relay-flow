package jira

import (
	"context"
	"fmt"

	"github.com/rajpopat27/relay-flow/internal/config"
)

// probeStartup uses system-wide credentials once, even without registered
// repositories. It does not need project, component, assignee or status data.
func probeStartup(ctx context.Context, _ config.RawValues, _ map[string]config.Repo) error {
	creds, err := loadCredentialsDefault()
	if err != nil {
		return fmt.Errorf("jira startup credentials: %w", err)
	}
	client, err := sharedClient(creds.Site, creds.Email, creds.Token)
	if err != nil {
		return fmt.Errorf("jira startup credentials: %w", err)
	}
	return client.ValidateCredentialsStartup(ctx)
}
