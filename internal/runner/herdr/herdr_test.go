package herdr

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/rajpopat27/relay-flow/internal/runner"
	"github.com/rajpopat27/relay-flow/internal/runner/herdr/herdrclicli"
)

// fakeClient is the typed adapter seam from design.md section 1a. The
// adapter tests deliberately provide only captured Herdr value shapes; strict
// executable tests cover the production CLI wrapper separately.
type fakeClient struct {
	mu sync.Mutex

	snapshot    herdrclicli.Snapshot
	snapshotErr error

	snapshotDelay       time.Duration
	snapshotCalls       int
	activeSnapshots     int
	maxConcurrentLookup int
}

var _ herdrclicli.Client = (*fakeClient)(nil)

func (f *fakeClient) Snapshot(context.Context) (herdrclicli.Snapshot, error) {
	f.mu.Lock()
	f.snapshotCalls++
	f.activeSnapshots++
	if f.activeSnapshots > f.maxConcurrentLookup {
		f.maxConcurrentLookup = f.activeSnapshots
	}
	snapshot := f.snapshot
	snapshotErr := f.snapshotErr
	delay := f.snapshotDelay
	f.mu.Unlock()

	if delay > 0 {
		time.Sleep(delay)
	}

	f.mu.Lock()
	f.activeSnapshots--
	f.mu.Unlock()
	return snapshot, snapshotErr
}

func (*fakeClient) CreateTab(context.Context, string, string, string, map[string]string) (herdrclicli.Tab, herdrclicli.Pane, error) {
	return herdrclicli.Tab{}, herdrclicli.Pane{}, errors.New("unexpected CreateTab call")
}

func (*fakeClient) ListTabs(context.Context, string) ([]herdrclicli.Tab, error) {
	return nil, errors.New("unexpected ListTabs call")
}

func (*fakeClient) ListPanes(context.Context, string) ([]herdrclicli.Pane, error) {
	return nil, errors.New("unexpected ListPanes call")
}

func (*fakeClient) GetPane(context.Context, string) (herdrclicli.Pane, error) {
	return herdrclicli.Pane{}, errors.New("unexpected GetPane call")
}

func (*fakeClient) ProcessInfo(context.Context, string) (herdrclicli.ProcessInfo, error) {
	return herdrclicli.ProcessInfo{}, errors.New("unexpected ProcessInfo call")
}

func (*fakeClient) RenamePane(context.Context, string, string) error {
	return errors.New("unexpected RenamePane call")
}

func (*fakeClient) RunPane(context.Context, string, string) error {
	return errors.New("unexpected RunPane call")
}

func (*fakeClient) ClosePane(context.Context, string) error {
	return errors.New("unexpected ClosePane call")
}

func newWorkspaceTestPaths(t *testing.T) (canonical, paneCWD, registered string) {
	t.Helper()
	root := t.TempDir()
	separator := string(filepath.Separator)
	canonical = filepath.Join(root, "payments")
	if err := os.Mkdir(canonical, 0o755); err != nil {
		t.Fatal(err)
	}
	paneCWD = root + separator + "payments" + separator + "."
	registered = root + separator + "payments" + separator + ".." + separator + "payments"
	return canonical, paneCWD, registered
}

func newWorkspaceSnapshot(workspaces []herdrclicli.Workspace, panes []herdrclicli.Pane) herdrclicli.Snapshot {
	return herdrclicli.Snapshot{Workspaces: workspaces, Panes: panes}
}

func runSpec(name, path string) runner.RunSpec {
	return runner.RunSpec{RepoName: name, RepoPath: path, TicketKey: "PAY-101"}
}

func TestDiscoverReposDerivesCandidatesFromWorkspacePanes(t *testing.T) {
	canonical, paneCWD, _ := newWorkspaceTestPaths(t)
	cli := &fakeClient{snapshot: newWorkspaceSnapshot(
		[]herdrclicli.Workspace{
			{ID: "workspace-payments", Label: "payments"},
		},
		[]herdrclicli.Pane{
			{ID: "pane-root", WorkspaceID: "workspace-payments", CWD: paneCWD},
			// A repository workspace can contain multiple panes; it still
			// produces one repository candidate.
			{ID: "pane-ticket", WorkspaceID: "workspace-payments", CWD: canonical},
		},
	)}
	a := newAdapter(cli, Config{})

	got, err := a.DiscoverRepos(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	want := []runner.RepoCandidate{{Name: "payments", Path: canonical}}
	if len(got) != len(want) || got[0] != want[0] {
		t.Fatalf("DiscoverRepos = %+v, want %+v", got, want)
	}
}

func TestEnsureEnvironmentMatchesNormalizedPanePath(t *testing.T) {
	canonical, paneCWD, registered := newWorkspaceTestPaths(t)
	cli := &fakeClient{snapshot: newWorkspaceSnapshot(
		[]herdrclicli.Workspace{{ID: "workspace-payments", Label: "payments"}},
		[]herdrclicli.Pane{{ID: "pane-root", WorkspaceID: "workspace-payments", CWD: paneCWD}},
	)}
	a := newAdapter(cli, Config{})

	if err := a.ValidateRepo(context.Background(), "payments", registered); err != nil {
		t.Fatalf("ValidateRepo with normalized path: %v", err)
	}
	env, err := a.EnsureEnvironment(context.Background(), runSpec("payments", registered))
	if err != nil {
		t.Fatal(err)
	}
	if env.ID != "workspace-payments" {
		t.Fatalf("environment ID = %q, want workspace-payments", env.ID)
	}
	if env.Path != registered {
		t.Fatalf("environment path = %q, want registered path %q", env.Path, registered)
	}
	if filepath.Clean(paneCWD) != canonical || filepath.Clean(registered) != canonical {
		t.Fatalf("test paths did not normalize to %q: pane=%q registered=%q", canonical, paneCWD, registered)
	}
}

func TestEnsureEnvironmentUsesUniqueWorkspaceLabelTieBreaker(t *testing.T) {
	canonical, paneCWD, _ := newWorkspaceTestPaths(t)
	cli := &fakeClient{snapshot: newWorkspaceSnapshot(
		[]herdrclicli.Workspace{
			{ID: "workspace-copy", Label: "payments-copy"},
			{ID: "workspace-payments", Label: "payments"},
		},
		[]herdrclicli.Pane{
			{ID: "pane-copy", WorkspaceID: "workspace-copy", CWD: paneCWD},
			{ID: "pane-payments", WorkspaceID: "workspace-payments", CWD: canonical},
		},
	)}
	a := newAdapter(cli, Config{})

	env, err := a.EnsureEnvironment(context.Background(), runSpec("payments", canonical))
	if err != nil {
		t.Fatal(err)
	}
	if env.ID != "workspace-payments" {
		t.Fatalf("environment ID = %q, want label-matched workspace-payments", env.ID)
	}
}

func TestEnsureEnvironmentRejectsAmbiguousWorkspacePath(t *testing.T) {
	canonical, paneCWD, _ := newWorkspaceTestPaths(t)
	cli := &fakeClient{snapshot: newWorkspaceSnapshot(
		[]herdrclicli.Workspace{
			{ID: "workspace-one", Label: "payments-copy-a"},
			{ID: "workspace-two", Label: "payments-copy-b"},
		},
		[]herdrclicli.Pane{
			{ID: "pane-one", WorkspaceID: "workspace-one", CWD: paneCWD},
			{ID: "pane-two", WorkspaceID: "workspace-two", CWD: canonical},
		},
	)}
	a := newAdapter(cli, Config{})

	err := a.ValidateRepo(context.Background(), "payments", canonical)
	if err == nil {
		t.Fatal("ValidateRepo accepted an ambiguous workspace path")
	}
	if !strings.Contains(strings.ToLower(err.Error()), "ambiguous") {
		t.Fatalf("ValidateRepo error = %v, want ambiguous workspace error", err)
	}

	if _, err := a.EnsureEnvironment(context.Background(), runSpec("payments", canonical)); err == nil {
		t.Fatal("EnsureEnvironment accepted an ambiguous workspace path")
	}
}

func TestEnsureEnvironmentReusesExistingWorkspace(t *testing.T) {
	canonical, paneCWD, _ := newWorkspaceTestPaths(t)
	cli := &fakeClient{snapshot: newWorkspaceSnapshot(
		[]herdrclicli.Workspace{{ID: "workspace-payments", Label: "payments"}},
		[]herdrclicli.Pane{{ID: "pane-root", WorkspaceID: "workspace-payments", CWD: paneCWD}},
	)}
	a := newAdapter(cli, Config{})
	spec := runSpec("payments", canonical)

	first, err := a.EnsureEnvironment(context.Background(), spec)
	if err != nil {
		t.Fatal(err)
	}
	second, err := a.EnsureEnvironment(context.Background(), spec)
	if err != nil {
		t.Fatal(err)
	}
	if first != second {
		t.Fatalf("EnsureEnvironment changed an existing workspace: first=%+v second=%+v", first, second)
	}
}

func TestEnsureEnvironmentReportsMissingWorkspace(t *testing.T) {
	canonical, paneCWD, _ := newWorkspaceTestPaths(t)
	cli := &fakeClient{snapshot: newWorkspaceSnapshot(
		[]herdrclicli.Workspace{{ID: "workspace-other", Label: "other"}},
		[]herdrclicli.Pane{{ID: "pane-other", WorkspaceID: "workspace-other", CWD: paneCWD + "-other"}},
	)}
	a := newAdapter(cli, Config{})

	err := a.ValidateRepo(context.Background(), "payments", canonical)
	if err == nil {
		t.Fatal("ValidateRepo accepted a missing workspace")
	}
	if !strings.Contains(strings.ToLower(err.Error()), "workspace") {
		t.Fatalf("ValidateRepo error = %v, want missing workspace error", err)
	}

	err = func() error {
		_, err := a.EnsureEnvironment(context.Background(), runSpec("payments", canonical))
		return err
	}()
	if err == nil {
		t.Fatal("EnsureEnvironment accepted a missing workspace")
	}
	if !strings.Contains(strings.ToLower(err.Error()), "workspace") {
		t.Fatalf("EnsureEnvironment error = %v, want missing workspace error", err)
	}
}

func TestConcurrentWorkspaceLookupIsSerializedAndIdempotent(t *testing.T) {
	canonical, paneCWD, _ := newWorkspaceTestPaths(t)
	cli := &fakeClient{
		snapshot: newWorkspaceSnapshot(
			[]herdrclicli.Workspace{{ID: "workspace-payments", Label: "payments"}},
			[]herdrclicli.Pane{{ID: "pane-root", WorkspaceID: "workspace-payments", CWD: paneCWD}},
		),
		snapshotDelay: 2 * time.Millisecond,
	}
	a := newAdapter(cli, Config{})
	spec := runSpec("payments", canonical)

	const lookupCount = 12
	start := make(chan struct{})
	results := make(chan struct {
		env runner.Environment
		err error
	}, lookupCount)
	var wg sync.WaitGroup
	for i := 0; i < lookupCount; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			env, err := a.EnsureEnvironment(context.Background(), spec)
			results <- struct {
				env runner.Environment
				err error
			}{env: env, err: err}
		}()
	}
	close(start)
	wg.Wait()
	close(results)

	for result := range results {
		if result.err != nil {
			t.Fatal(result.err)
		}
		if result.env.ID != "workspace-payments" || result.env.Path != canonical {
			t.Fatalf("concurrent EnsureEnvironment result = %+v", result.env)
		}
	}

	cli.mu.Lock()
	maxConcurrent := cli.maxConcurrentLookup
	cli.mu.Unlock()
	if maxConcurrent != 1 {
		t.Fatalf("maximum concurrent workspace lookups = %d, want serialized lookups", maxConcurrent)
	}
}
