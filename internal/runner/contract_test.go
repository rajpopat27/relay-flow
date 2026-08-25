package runner_test

import (
	"context"
	"strings"
	"testing"

	"github.com/rajpopat27/relay-flow/internal/identity"
	"github.com/rajpopat27/relay-flow/internal/runner"
)

// 3.7: runner contract tests against a fake runner, per
// specs/integration-contracts "Runner executes harnesses in ticket-scoped
// environments" and "Runner terminal titles are stable and minimal".

type fakeRunner struct {
	envs       map[string]runner.Environment
	terminals  map[string]runner.Terminal // key: envID/title
	dead       map[string]bool            // terminal key -> unusable
	closed     []string
	cleaned    []string
	ensureN    int
	ensureEnvN int
}

func newFakeRunner() *fakeRunner {
	return &fakeRunner{
		envs:      map[string]runner.Environment{},
		terminals: map[string]runner.Terminal{},
		dead:      map[string]bool{},
	}
}

func spec() runner.RunSpec {
	return runner.RunSpec{
		RunID:     identity.NewRunID("payments", "basicFlow", "PAY-101"),
		RepoName:  "payments",
		RepoPath:  "/srv/payments",
		TicketKey: "PAY-101",
	}
}

func (f *fakeRunner) DiscoverRepos(context.Context) ([]runner.RepoCandidate, error) {
	return []runner.RepoCandidate{{Name: "payments", Path: "/srv/payments"}}, nil
}

func (f *fakeRunner) ValidateRepo(context.Context, string, string) error { return nil }

func (f *fakeRunner) EnsureEnvironment(_ context.Context, s runner.RunSpec) (runner.Environment, error) {
	f.ensureEnvN++
	key := string(s.RunID)
	if env, ok := f.envs[key]; ok {
		return env, nil
	}
	env := runner.Environment{ID: "env-" + key, Path: s.RepoPath + "/.wt/" + s.TicketKey}
	f.envs[key] = env
	return env, nil
}

func (f *fakeRunner) FindTerminal(_ context.Context, env runner.Environment, title string) (runner.Terminal, bool, error) {
	key := env.ID + "/" + title
	term, ok := f.terminals[key]
	if !ok || f.dead[key] {
		// Only a live usable terminal is returned.
		return runner.Terminal{}, false, nil
	}
	return term, true, nil
}
func (f *fakeRunner) InspectTerminal(_ context.Context, term runner.Terminal) (runner.Terminal, bool, error) {
	for key, current := range f.terminals {
		if current.ID == term.ID && !f.dead[key] {
			return current, true, nil
		}
	}
	return runner.Terminal{}, false, nil
}
func (f *fakeRunner) SendTerminal(context.Context, runner.Terminal, string) error { return nil }
func (f *fakeRunner) CreateTerminal(ctx context.Context, env runner.Environment, title string, command runner.Command) (runner.Terminal, error) {
	return f.EnsureTerminal(ctx, env, title, command)
}

func (f *fakeRunner) CloseTerminal(_ context.Context, term runner.Terminal) error {
	for k, v := range f.terminals {
		if v.ID == term.ID {
			delete(f.terminals, k)
		}
	}
	f.closed = append(f.closed, term.ID)
	return nil
}

func (f *fakeRunner) EnsureTerminal(_ context.Context, env runner.Environment, title string, _ runner.Command) (runner.Terminal, error) {
	f.ensureN++
	key := env.ID + "/" + title
	if term, ok := f.terminals[key]; ok && !f.dead[key] {
		return term, nil
	}
	term := runner.Terminal{ID: "term-" + key, Title: title}
	f.terminals[key] = term
	delete(f.dead, key)
	return term, nil
}

func (f *fakeRunner) CloseTerminals(_ context.Context, s runner.RunSpec) error {
	prefix := "env-" + string(s.RunID) + "/"
	for k := range f.terminals {
		if strings.HasPrefix(k, prefix) {
			delete(f.terminals, k)
			f.closed = append(f.closed, k)
		}
	}
	// Environment/workspace preserved: f.envs untouched.
	return nil
}

func (f *fakeRunner) CleanupRun(_ context.Context, s runner.RunSpec) error {
	_ = f.CloseTerminals(context.Background(), s)
	delete(f.envs, string(s.RunID))
	f.cleaned = append(f.cleaned, string(s.RunID))
	return nil
}

func TestTerminalTitleIsTicketColonNode(t *testing.T) {
	f := newFakeRunner()
	ctx := context.Background()
	sp := spec()
	env, err := f.EnsureEnvironment(ctx, sp)
	if err != nil {
		t.Fatal(err)
	}
	term, err := f.EnsureTerminal(ctx, env, "PAY-101:coding", runner.Command{Executable: "opencode"})
	if err != nil {
		t.Fatal(err)
	}
	if term.Title != "PAY-101:coding" {
		t.Fatalf("title = %q, want PAY-101:coding", term.Title)
	}
	// Title must never carry nodeVisitID, workflow, or agent metadata.
	visit := identity.NewNodeVisitID()
	for _, forbidden := range []string{string(visit), "basicFlow", "build"} {
		if strings.Contains(term.Title, forbidden) {
			t.Fatalf("title %q contains forbidden metadata %q", term.Title, forbidden)
		}
	}
}

func TestFindTerminalReturnsOnlyLiveUsable(t *testing.T) {
	f := newFakeRunner()
	ctx := context.Background()
	env, _ := f.EnsureEnvironment(ctx, spec())
	term, _ := f.EnsureTerminal(ctx, env, "PAY-101:coding", runner.Command{})

	got, ok, err := f.FindTerminal(ctx, env, "PAY-101:coding")
	if err != nil || !ok || got.ID != term.ID {
		t.Fatalf("FindTerminal = %v,%v,%v; want live terminal", got, ok, err)
	}

	// A stale/dead record is treated as absent.
	f.dead[env.ID+"/PAY-101:coding"] = true
	_, ok, err = f.FindTerminal(ctx, env, "PAY-101:coding")
	if err != nil {
		t.Fatal(err)
	}
	if ok {
		t.Fatal("FindTerminal returned a dead terminal as usable")
	}
}

func TestCloseTerminalsPreservesEnvironment(t *testing.T) {
	f := newFakeRunner()
	ctx := context.Background()
	sp := spec()
	env, _ := f.EnsureEnvironment(ctx, sp)
	_, _ = f.EnsureTerminal(ctx, env, "PAY-101:coding", runner.Command{})

	if err := f.CloseTerminals(ctx, sp); err != nil {
		t.Fatal(err)
	}
	if len(f.terminals) != 0 {
		t.Fatalf("terminals remain after CloseTerminals: %v", f.terminals)
	}
	if _, ok := f.envs[string(sp.RunID)]; !ok {
		t.Fatal("CloseTerminals removed the environment; it must preserve workspace/code")
	}
}

func TestCleanupRunRemovesAllRunResources(t *testing.T) {
	f := newFakeRunner()
	ctx := context.Background()
	sp := spec()
	env, _ := f.EnsureEnvironment(ctx, sp)
	_, _ = f.EnsureTerminal(ctx, env, "PAY-101:coding", runner.Command{})

	if err := f.CleanupRun(ctx, sp); err != nil {
		t.Fatal(err)
	}
	if len(f.terminals) != 0 {
		t.Fatalf("terminals remain after CleanupRun: %v", f.terminals)
	}
	if _, ok := f.envs[string(sp.RunID)]; ok {
		t.Fatal("environment remains after CleanupRun; it must remove run-owned resources")
	}
}

func TestEnsureIdempotent(t *testing.T) {
	f := newFakeRunner()
	ctx := context.Background()
	sp := spec()

	env1, _ := f.EnsureEnvironment(ctx, sp)
	env2, _ := f.EnsureEnvironment(ctx, sp)
	if env1 != env2 {
		t.Fatalf("EnsureEnvironment not idempotent: %v vs %v", env1, env2)
	}

	term1, _ := f.EnsureTerminal(ctx, env1, "PAY-101:coding", runner.Command{})
	term2, _ := f.EnsureTerminal(ctx, env1, "PAY-101:coding", runner.Command{})
	if term1 != term2 {
		t.Fatalf("EnsureTerminal not idempotent: %v vs %v", term1, term2)
	}
}
