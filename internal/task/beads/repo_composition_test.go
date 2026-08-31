package beads

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/rajpopat27/relay-flow/internal/config"
	"github.com/rajpopat27/relay-flow/internal/harness"
	"github.com/rajpopat27/relay-flow/internal/repo"
	"github.com/rajpopat27/relay-flow/internal/runner"
	"github.com/rajpopat27/relay-flow/internal/task"
)

func TestBeadsRepoRegistrationUsesIndependentSystemsAndPollers(t *testing.T) {
	root := t.TempDir()
	logPath := installRepoCompositionFakeBD(t)
	configPath := filepath.Join(root, "config.yaml")
	if err := config.SaveMachine(configPath, &config.Machine{
		TaskPlugin: "beads", RunnerPlugin: "orca", HarnessPlugin: "opencode",
		Repos: map[string]config.Repo{},
	}); err != nil {
		t.Fatal(err)
	}

	paymentsPath := filepath.Join(root, "code", "payments")
	platformPath := filepath.Join(root, "code", "platform")
	paymentsBeads := filepath.Join(root, "beads", "payments", ".beads")
	platformBeads := filepath.Join(root, "beads", "platform", ".beads")
	for _, path := range []string{paymentsPath, platformPath, paymentsBeads, platformBeads} {
		if err := os.MkdirAll(path, 0o755); err != nil {
			t.Fatal(err)
		}
	}

	svc := repo.NewService(repo.ServiceConfig{
		ConfigPath: configPath,
		TaskPlugin: "beads",
		Runner:     &compositionRunner{},
		Harness:    &compositionHarness{},
		Active:     &compositionActive{},
		Workflows:  &compositionWorkflowRefs{},
	})
	if _, err := svc.Register(context.Background(), repo.RegisterInput{
		Name: "payments", Path: paymentsPath,
		TaskConfig: config.RawValues{"beadsDir": paymentsBeads},
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Register(context.Background(), repo.RegisterInput{
		Name: "platform", Path: platformPath,
		TaskConfig: config.RawValues{"beadsDir": platformBeads},
	}); err != nil {
		t.Fatal(err)
	}

	registered := svc.Registry().List()
	if len(registered) != 2 {
		t.Fatalf("registered repos = %d, want 2", len(registered))
	}
	if registered[0].TaskSystem == registered[1].TaskSystem {
		t.Fatal("independent Beads repos shared one task system")
	}

	var handledMu sync.Mutex
	handled := map[string]int{}
	group := repo.NewPollerGroup(10, func(_ context.Context, r *repo.Repo, _ []task.Ticket) {
		handledMu.Lock()
		handled[r.Name]++
		handledMu.Unlock()
	})
	group.Interval = 10 * time.Millisecond
	group.ReplaceRepos(registered)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go group.Run(ctx)

	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		handledMu.Lock()
		complete := handled["payments"] > 0 && handled["platform"] > 0
		handledMu.Unlock()
		if complete {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	handledMu.Lock()
	if handled["payments"] == 0 || handled["platform"] == 0 {
		t.Fatalf("poller handlers = %v, want both registered repos", handled)
	}
	handledMu.Unlock()

	assertBDInvocationReachedRepoAndWorkspace(t, logPath, paymentsPath, paymentsBeads)
	assertBDInvocationReachedRepoAndWorkspace(t, logPath, platformPath, platformBeads)
}

func TestBeadsRepoRegistrationRejectsSharedCanonicalWorkspace(t *testing.T) {
	root := t.TempDir()
	installRepoCompositionFakeBD(t)
	configPath := filepath.Join(root, "config.yaml")
	if err := config.SaveMachine(configPath, &config.Machine{
		TaskPlugin: "beads", RunnerPlugin: "orca", HarnessPlugin: "opencode",
		Repos: map[string]config.Repo{},
	}); err != nil {
		t.Fatal(err)
	}
	firstPath := filepath.Join(root, "code", "payments")
	secondPath := filepath.Join(root, "code", "platform")
	workspace := filepath.Join(root, "beads", "shared", ".beads")
	for _, path := range []string{firstPath, secondPath, workspace} {
		if err := os.MkdirAll(path, 0o755); err != nil {
			t.Fatal(err)
		}
	}

	svc := repo.NewService(repo.ServiceConfig{
		ConfigPath: configPath,
		TaskPlugin: "beads",
		Runner:     &compositionRunner{},
		Harness:    &compositionHarness{},
		Active:     &compositionActive{},
		Workflows:  &compositionWorkflowRefs{},
	})
	if _, err := svc.Register(context.Background(), repo.RegisterInput{
		Name: "payments", Path: firstPath,
		TaskConfig: config.RawValues{"beadsDir": workspace},
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Register(context.Background(), repo.RegisterInput{
		Name: "platform", Path: secondPath,
		TaskConfig: config.RawValues{"beadsDir": filepath.Join(workspace, ".")},
	}); err == nil {
		t.Fatal("second repo sharing canonical beadsDir was accepted")
	}
	if len(svc.Registry().List()) != 1 {
		t.Fatalf("registry after duplicate scope rejection = %d, want 1", len(svc.Registry().List()))
	}
}

func installRepoCompositionFakeBD(t *testing.T) string {
	t.Helper()
	binDir := t.TempDir()
	fake := filepath.Join(binDir, "bd")
	script, err := os.ReadFile(filepath.Join("testdata", "strict-bd-repo.sh"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(fake, script, 0o755); err != nil {
		t.Fatal(err)
	}
	logPath := filepath.Join(t.TempDir(), "bd-invocations.log")
	if err := os.WriteFile(logPath, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("BD_LOG", logPath)
	// These must not leak into the child command environment. The adapter
	// explicitly selects BEADS_DIR and removes unrelated Beads selectors.
	t.Setenv("BEADS_DIR", "/ambient/workspace")
	t.Setenv("BEADS_DB", "/ambient/beads.db")
	t.Setenv("BD_DB", "/ambient/legacy.db")
	return logPath
}

func assertBDInvocationReachedRepoAndWorkspace(t *testing.T, logPath, repoPath, beadsDir string) {
	t.Helper()
	raw, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	wantPrefix := repoPath + "|" + beadsDir + "|"
	for _, line := range strings.Split(strings.TrimSpace(string(raw)), "\n") {
		if strings.HasPrefix(line, wantPrefix) {
			return
		}
	}
	t.Fatalf("no bd invocation used cmd.Dir=%q and BEADS_DIR=%q; log=%q", repoPath, beadsDir, raw)
}

type compositionRunner struct{}

func (*compositionRunner) DiscoverRepos(context.Context) ([]runner.RepoCandidate, error) {
	return nil, nil
}
func (*compositionRunner) ValidateRepo(context.Context, string, string) error { return nil }
func (*compositionRunner) EnsureEnvironment(context.Context, runner.RunSpec) (runner.Environment, error) {
	return runner.Environment{}, nil
}
func (*compositionRunner) SetEnvironmentStatus(context.Context, runner.Environment, string) error {
	return nil
}
func (*compositionRunner) FindTerminal(context.Context, runner.Terminal) (runner.Terminal, bool, error) {
	return runner.Terminal{}, false, nil
}
func (*compositionRunner) CreateTerminal(context.Context, runner.Environment, string, runner.Command) (runner.Terminal, error) {
	return runner.Terminal{}, nil
}
func (*compositionRunner) EnsureTerminal(context.Context, runner.Environment, runner.Terminal, string, runner.Command) (runner.Terminal, error) {
	return runner.Terminal{}, nil
}
func (*compositionRunner) SendTerminal(context.Context, runner.Terminal, string) error { return nil }
func (*compositionRunner) CloseTerminal(context.Context, runner.Terminal) error        { return nil }
func (*compositionRunner) CloseTerminals(context.Context, runner.RunSpec) error        { return nil }
func (*compositionRunner) CleanupRun(context.Context, runner.RunSpec) error            { return nil }

type compositionHarness struct{}

func (*compositionHarness) SetupRepo(context.Context, string) error             { return nil }
func (*compositionHarness) ValidateAgent(context.Context, string, string) error { return nil }
func (*compositionHarness) FindSession(context.Context, string, string) (harness.Session, bool, error) {
	return harness.Session{}, false, nil
}
func (*compositionHarness) RenderPrompt(harness.PromptKind, harness.PromptData, string) (string, error) {
	return "", nil
}
func (*compositionHarness) BuildCommand(harness.LaunchSpec) (runner.Command, error) {
	return runner.Command{}, nil
}

type compositionActive struct{}

func (*compositionActive) HasActiveRepo(context.Context, string) (bool, error) { return false, nil }

type compositionWorkflowRefs struct{}

func (*compositionWorkflowRefs) ReferencesRepo(string) bool { return false }
