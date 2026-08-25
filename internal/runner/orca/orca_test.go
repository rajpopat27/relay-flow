package orca

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/rajpopat27/relay-flow/internal/config"
	"github.com/rajpopat27/relay-flow/internal/runner"
	"github.com/rajpopat27/relay-flow/internal/runner/orca/orcacli"
)

// 9.5: info-level outcome logs must never embed argv payloads. sanitizeErr
// strips the "[args...]" middle from orcacli errors so the agent prompt and
// RELAY_FLOW_* env carried by --command are never written to server.log.
func TestSanitizeErr(t *testing.T) {
	// Shape from runJSON/run: "orca [terminal create ... --command 'PAYLOAD']: exit status 1: boom"
	// wrapped by CreateTerminal as "orca terminal create: %w".
	wrapped := errors.New("orca terminal create: orca [terminal create --worktree name:PAY-1 --title PAY-1:coding --command 'PAYLOAD']: exit status 1: boom")
	got := sanitizeErr(wrapped)
	if strings.Contains(got, "PAYLOAD") || strings.Contains(got, "--command") {
		t.Fatalf("sanitizeErr leaked argv payload: %q", got)
	}
	if !strings.Contains(got, "boom") {
		t.Fatalf("sanitizeErr dropped failure reason: %q", got)
	}

	if got := sanitizeErr(nil); got != "" {
		t.Fatalf("sanitizeErr(nil) = %q, want empty", got)
	}

	plain := errors.New("unwrapped failure")
	if got := sanitizeErr(plain); got != "unwrapped failure" {
		t.Fatalf("sanitizeErr(plain) = %q", got)
	}
}

// fakeCLI is the orcacli.Client seam used by the Orca runner tests.
type fakeCLI struct {
	repos     []orcacli.Repo
	worktrees []orcacli.Worktree

	createdBaseBranch string
	createdParent     string
}

func (f *fakeCLI) ListRepos(context.Context) ([]orcacli.Repo, error) { return f.repos, nil }
func (f *fakeCLI) ListWorktrees(context.Context) ([]orcacli.Worktree, error) {
	return f.worktrees, nil
}
func (f *fakeCLI) CreateWorktree(_ context.Context, ticketKey, repoID, parentWorktreeID, baseBranch string) error {
	f.createdBaseBranch = baseBranch
	f.createdParent = parentWorktreeID
	f.worktrees = append(f.worktrees, orcacli.Worktree{
		ID:          "wt-new",
		RepoID:      repoID,
		DisplayName: ticketKey,
		Branch:      "refs/heads/" + ticketKey,
		Path:        "/wt/" + ticketKey,
	})
	return nil
}
func (f *fakeCLI) DeleteWorktree(context.Context, string) error                  { return nil }
func (f *fakeCLI) ListTerminals(context.Context, string) ([]orcacli.Terminal, error) {
	return nil, nil
}
func (f *fakeCLI) CreateTerminal(context.Context, string, string, string) (string, error) {
	return "", nil
}
func (f *fakeCLI) CloseTerminal(context.Context, string) error { return nil }

// 9.16: when the repo's primary worktree is on master (refs/heads/master),
// EnsureEnvironment must pass --base-branch master (via CreateWorktree), not
// the hardcoded "main".
func TestEnsureEnvironment_PrimaryBranchMaster(t *testing.T) {
	fx := &fakeCLI{
		repos: []orcacli.Repo{{ID: "r1", DisplayName: "app", Path: "/srv/app"}},
		worktrees: []orcacli.Worktree{
			{ID: "wt-main", RepoID: "r1", DisplayName: "app-main", Branch: "refs/heads/master", Path: "/srv/app", IsMainWorktree: true},
		},
	}
	a, err := New(fx, config.RawValues{})
	if err != nil {
		t.Fatal(err)
	}
	_, err = a.EnsureEnvironment(context.Background(), runner.RunSpec{TicketKey: "PAY-1", RepoName: "app", RepoPath: "/srv/app"})
	if err != nil {
		t.Fatal(err)
	}
	if fx.createdBaseBranch != "master" {
		t.Fatalf("CreateWorktree baseBranch = %q, want %q", fx.createdBaseBranch, "master")
	}
	if fx.createdParent != "wt-main" {
		t.Fatalf("CreateWorktree parent = %q, want %q", fx.createdParent, "wt-main")
	}
}

// Explicit baseRef config overrides the primary worktree's branch.
func TestEnsureEnvironment_BaseRefOverride(t *testing.T) {
	fx := &fakeCLI{
		repos: []orcacli.Repo{{ID: "r1", DisplayName: "app", Path: "/srv/app"}},
		worktrees: []orcacli.Worktree{
			{ID: "wt-main", RepoID: "r1", DisplayName: "app-main", Branch: "refs/heads/master", Path: "/srv/app", IsMainWorktree: true},
		},
	}
	a, err := New(fx, config.RawValues{"baseRef": "release/1.x"})
	if err != nil {
		t.Fatal(err)
	}
	_, err = a.EnsureEnvironment(context.Background(), runner.RunSpec{TicketKey: "PAY-1", RepoName: "app", RepoPath: "/srv/app"})
	if err != nil {
		t.Fatal(err)
	}
	if fx.createdBaseBranch != "release/1.x" {
		t.Fatalf("CreateWorktree baseBranch = %q, want %q", fx.createdBaseBranch, "release/1.x")
	}
}

func TestPrimaryBranch(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{"refs/heads/master", "master"},
		{"refs/heads/main", "main"},
		{"refs/heads/release/1.x", "release/1.x"},
		{"master", "master"},   // already bare
		{"", "main"},           // empty → main fallback
		{"refs/heads/", "main"}, // empty name → main fallback
	}
	for _, c := range cases {
		got := primaryBranch(&orcacli.Worktree{Branch: c.in})
		if got != c.want {
			t.Errorf("primaryBranch(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}
