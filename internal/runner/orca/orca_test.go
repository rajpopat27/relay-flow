package orca

import (
	"context"
	"errors"
	"os/exec"
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
	status            string
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
func (f *fakeCLI) SetWorktreeStatus(_ context.Context, _, status string) error {
	f.status = status
	return nil
}
func (f *fakeCLI) DeleteWorktree(context.Context, string) error { return nil }
func (f *fakeCLI) ShowTerminal(context.Context, string) (orcacli.Terminal, error) {
	return orcacli.Terminal{}, orcacli.ErrTerminalUnavailable
}
func (f *fakeCLI) SendTerminal(context.Context, string, string) error { return nil }
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

func TestSetEnvironmentStatus(t *testing.T) {
	fx := &fakeCLI{}
	a, err := New(fx, config.RawValues{})
	if err != nil {
		t.Fatal(err)
	}
	if err := a.SetEnvironmentStatus(context.Background(), runner.Environment{ID: "wt-PAY-1"}, runner.WorkspaceStatusInReview); err != nil {
		t.Fatal(err)
	}
	if fx.status != runner.WorkspaceStatusInReview {
		t.Fatalf("status = %q, want %q", fx.status, runner.WorkspaceStatusInReview)
	}
}

// An existing ticket branch must win even over a configured baseRef. Passing
// any other base would make Orca hit its branch-name collision behavior.
func TestEnsureEnvironment_ExistingTicketBranchAvoidsCollision(t *testing.T) {
	repo := t.TempDir()
	runGit := func(args ...string) {
		t.Helper()
		cmdArgs := append([]string{"-C", repo}, args...)
		if out, err := exec.Command("git", cmdArgs...).CombinedOutput(); err != nil {
			t.Fatalf("git %s: %v: %s", strings.Join(args, " "), err, out)
		}
	}
	runGit("init", "--quiet")
	runGit("-c", "user.name=Relay Flow", "-c", "user.email=relay-flow@example.invalid", "commit", "--allow-empty", "--quiet", "-m", "initial")
	runGit("branch", "alice/PAY-1")

	fx := &fakeCLI{
		repos: []orcacli.Repo{{ID: "r1", DisplayName: "app", Path: repo}},
		worktrees: []orcacli.Worktree{
			{ID: "wt-main", RepoID: "r1", DisplayName: "app-main", Branch: "refs/heads/master", Path: repo, IsMainWorktree: true},
		},
	}
	a, err := New(fx, config.RawValues{"baseRef": "release/1.x"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := a.EnsureEnvironment(context.Background(), runner.RunSpec{TicketKey: "PAY-1", RepoName: "app", RepoPath: repo}); err != nil {
		t.Fatal(err)
	}
	if fx.createdBaseBranch != "alice/PAY-1" {
		t.Fatalf("CreateWorktree baseBranch = %q, want existing ticket branch", fx.createdBaseBranch)
	}
}

func TestPrimaryBranch(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{"refs/heads/master", "master"},
		{"refs/heads/main", "main"},
		{"refs/heads/release/1.x", "release/1.x"},
		{"master", "master"},    // already bare
		{"", "main"},            // empty → main fallback
		{"refs/heads/", "main"}, // empty name → main fallback
	}
	for _, c := range cases {
		got := primaryBranch(&orcacli.Worktree{Branch: c.in})
		if got != c.want {
			t.Errorf("primaryBranch(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}
