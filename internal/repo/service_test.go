package repo_test

import (
	"context"
	"errors"
	"path/filepath"
	"sync"
	"testing"

	"github.com/rajpopat27/relay-flow/internal/config"
	"github.com/rajpopat27/relay-flow/internal/repo"
	"github.com/rajpopat27/relay-flow/internal/runner"
	"github.com/rajpopat27/relay-flow/internal/task"
)

// 3.32-3.33: repo registration/removal per specs/workflow-repo-management
// "Repos are registered independently" and "Repo removal protects
// references".

type fakeRunnerDiscovery struct {
	candidates []runner.RepoCandidate
	validErr   error
}

func (f *fakeRunnerDiscovery) DiscoverRepos(context.Context) ([]runner.RepoCandidate, error) {
	return f.candidates, nil
}
func (f *fakeRunnerDiscovery) ValidateRepo(_ context.Context, _, _ string) error { return f.validErr }
func (f *fakeRunnerDiscovery) EnsureEnvironment(context.Context, runner.RunSpec) (runner.Environment, error) {
	return runner.Environment{}, nil
}
func (f *fakeRunnerDiscovery) FindTerminal(context.Context, runner.Environment, string) (runner.Terminal, bool, error) {
	return runner.Terminal{}, false, nil
}
func (f *fakeRunnerDiscovery) CloseTerminal(context.Context, runner.Terminal) error { return nil }
func (f *fakeRunnerDiscovery) EnsureTerminal(context.Context, runner.Environment, string, runner.Command) (runner.Terminal, error) {
	return runner.Terminal{}, nil
}
func (f *fakeRunnerDiscovery) CloseTerminals(context.Context, runner.RunSpec) error { return nil }
func (f *fakeRunnerDiscovery) CleanupRun(context.Context, runner.RunSpec) error     { return nil }

// fakeActiveRuns / fakeWorkflowRefs implement the consumer interfaces.
type fakeActiveRuns struct{ repos map[string]bool }

// fakeTaskSystem is a minimal working task.System for connectivity success.
type fakeTaskSystem struct{ task.System }

func (fakeTaskSystem) Poll(context.Context) ([]task.Ticket, error) { return nil, nil }

func newFakeSystem() task.System { return fakeTaskSystem{} }

func (f *fakeActiveRuns) HasActiveRepo(_ context.Context, name string) (bool, error) {
	return f.repos[name], nil
}
func (f *fakeActiveRuns) set(name string, on bool) { f.repos[name] = on }

type fakeWorkflowRefs struct{ refs map[string]bool }

func (f *fakeWorkflowRefs) ReferencesRepo(name string) bool { return f.refs[name] }
func (f *fakeWorkflowRefs) set(name string, on bool)        { f.refs[name] = on }

// The test task factory declares required repo keys and a canonical scope
// derived from root+repo config; New returns a working in-memory System so
// successful registration validates connectivity. Registered exactly once
// for the whole package (duplicate registration panics by design).
var registerTestFactoryOnce = sync.Once{}

func registerTestTaskFactory(t *testing.T) {
	t.Helper()
	registerTestFactoryOnce.Do(func() {
		task.Register("testjira", task.Factory{
			RequiredRepoKeys: func() []string { return []string{"project", "component"} },
			TaskScopeKey: func(root, repoCfg config.RawValues) (string, error) {
				proj, _ := repoCfg["project"].(string)
				comp, _ := repoCfg["component"].(string)
				if proj == "" || comp == "" {
					return "", errors.New("project and component required")
				}
				return "site/" + proj + "/" + comp, nil
			},
			New: func(_ context.Context, spec task.RepoSpec) (task.System, error) {
				if spec.Name == "" {
					return nil, errors.New("task system unreachable: empty repo name")
				}
				return newFakeSystem(), nil
			},
		})
	})
}

type serviceFixture struct {
	svc     *repo.Service
	cfgPath string
	active  *fakeActiveRuns
	wfRefs  *fakeWorkflowRefs
}

func newServiceFixture(t *testing.T, rn runner.Runner) serviceFixture {
	t.Helper()
	registerTestTaskFactory(t)
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.yaml")
	cfg := &config.Machine{
		TaskPlugin: "testjira", RunnerPlugin: "orca", HarnessPlugin: "opencode",
		Repos: map[string]config.Repo{},
	}
	if err := config.SaveMachine(cfgPath, cfg); err != nil {
		t.Fatal(err)
	}
	active := &fakeActiveRuns{repos: map[string]bool{}}
	wfRefs := &fakeWorkflowRefs{refs: map[string]bool{}}
	svc := repo.NewService(repo.ServiceConfig{
		ConfigPath: cfgPath,
		TaskPlugin: "testjira",
		Runner:     rn,
		Active:     active,
		Workflows:  wfRefs,
	})
	return serviceFixture{svc: svc, cfgPath: cfgPath, active: active, wfRefs: wfRefs}
}

func TestDiscoverDelegatesToRunner(t *testing.T) {
	rn := &fakeRunnerDiscovery{candidates: []runner.RepoCandidate{{Name: "payments", Path: "/srv/payments"}}}
	fx := newServiceFixture(t, rn)
	got, err := fx.svc.Discover(context.Background())
	if err != nil || len(got) != 1 || got[0].Name != "payments" {
		t.Fatalf("Discover = %v, %v", got, err)
	}
}

func TestRequiredRepoKeysDelegated(t *testing.T) {
	fx := newServiceFixture(t, &fakeRunnerDiscovery{})
	keys := fx.svc.RequiredRepoKeys()
	if len(keys) != 2 || keys[0] != "project" || keys[1] != "component" {
		t.Fatalf("RequiredRepoKeys = %v, want [project component]", keys)
	}
}

func TestRegisterMissingRequiredKeyFails(t *testing.T) {
	fx := newServiceFixture(t, &fakeRunnerDiscovery{})
	// Missing "component".
	_, err := fx.svc.Register(context.Background(), repo.RegisterInput{
		Name: "payments", Path: "/srv/payments",
		TaskConfig: config.RawValues{"project": "PAY"},
	})
	if err == nil {
		t.Fatal("registration succeeded without a required repo key")
	}
	// Machine config unchanged.
	cfg, _ := config.LoadMachine(fx.cfgPath)
	if len(cfg.Repos) != 0 {
		t.Fatal("machine config changed despite failed registration")
	}
}

func TestRegisterRejectsDuplicateName(t *testing.T) {
	fx := newServiceFixture(t, &fakeRunnerDiscovery{})
	in := repo.RegisterInput{Name: "payments", Path: "/srv/payments", TaskConfig: config.RawValues{"project": "PAY", "component": "api"}}
	if _, err := fx.svc.Register(context.Background(), in); err != nil {
		t.Fatalf("first Register failed: %v", err)
	}
	if _, err := fx.svc.Register(context.Background(), in); err == nil {
		t.Fatal("duplicate repo name accepted")
	}
}

func TestRegisterRejectsDuplicateCanonicalPath(t *testing.T) {
	fx := newServiceFixture(t, &fakeRunnerDiscovery{})
	okCfg := config.RawValues{"project": "PAY", "component": "api"}
	if _, err := fx.svc.Register(context.Background(), repo.RegisterInput{Name: "payments", Path: "/srv/payments", TaskConfig: okCfg}); err != nil {
		t.Fatal(err)
	}
	if _, err := fx.svc.Register(context.Background(), repo.RegisterInput{Name: "pay2", Path: "/srv/payments/./", TaskConfig: config.RawValues{"project": "OT", "component": "x"}}); err == nil {
		t.Fatal("duplicate canonical path accepted")
	}
}

func TestRegisterRejectsDuplicateTaskScope(t *testing.T) {
	fx := newServiceFixture(t, &fakeRunnerDiscovery{})
	scope := config.RawValues{"project": "PAY", "component": "api"}
	if _, err := fx.svc.Register(context.Background(), repo.RegisterInput{Name: "payments", Path: "/srv/payments", TaskConfig: scope}); err != nil {
		t.Fatal(err)
	}
	// A different repo path but the same Jira site/project/component scope.
	if _, err := fx.svc.Register(context.Background(), repo.RegisterInput{Name: "other", Path: "/srv/other", TaskConfig: scope}); err == nil {
		t.Fatal("duplicate task-system scope accepted")
	}
}

func TestRegisterValidatesRunnerRepo(t *testing.T) {
	rn := &fakeRunnerDiscovery{validErr: errInvalidRepo{}}
	fx := newServiceFixture(t, rn)
	if _, err := fx.svc.Register(context.Background(), repo.RegisterInput{Name: "bad", Path: "/nope", TaskConfig: config.RawValues{"project": "P", "component": "c"}}); err == nil {
		t.Fatal("registration succeeded despite runner validation failure")
	}
}

func TestRegisterValidatesTaskConnectivity(t *testing.T) {
	// The task factory's New is invoked to validate connectivity; its error
	// must reject registration before persisting.
	task.Register("failingtask", task.Factory{
		RequiredRepoKeys: func() []string { return []string{"project"} },
		TaskScopeKey:     func(_, repoCfg config.RawValues) (string, error) { return "s/" + repoCfg["project"].(string), nil },
		New: func(context.Context, task.RepoSpec) (task.System, error) {
			return nil, errors.New("jira unreachable")
		},
	})
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.yaml")
	cfg := &config.Machine{TaskPlugin: "failingtask", RunnerPlugin: "orca", HarnessPlugin: "opencode", Repos: map[string]config.Repo{}}
	if err := config.SaveMachine(cfgPath, cfg); err != nil {
		t.Fatal(err)
	}
	svc := repo.NewService(repo.ServiceConfig{
		ConfigPath: cfgPath, TaskPlugin: "failingtask",
		Runner: &fakeRunnerDiscovery{}, Active: &fakeActiveRuns{repos: map[string]bool{}}, Workflows: &fakeWorkflowRefs{refs: map[string]bool{}},
	})
	_, err := svc.Register(context.Background(), repo.RegisterInput{Name: "payments", Path: "/srv/payments", TaskConfig: config.RawValues{"project": "PAY"}})
	if err == nil {
		t.Fatal("registration succeeded despite task connectivity failure")
	}
	cfg2, _ := config.LoadMachine(cfgPath)
	if len(cfg2.Repos) != 0 {
		t.Fatal("machine config persisted despite connectivity validation failure")
	}
}

type errInvalidRepo struct{}

func (errInvalidRepo) Error() string { return "invalid repo" }

func TestRegisterAtomicallyPersists(t *testing.T) {
	fx := newServiceFixture(t, &fakeRunnerDiscovery{})
	if _, err := fx.svc.Register(context.Background(), repo.RegisterInput{Name: "payments", Path: "/srv/payments", TaskConfig: config.RawValues{"project": "PAY", "component": "api"}}); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.LoadMachine(fx.cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	r, ok := cfg.Repos["payments"]
	if !ok || r.Path != "/srv/payments" {
		t.Fatalf("repo not persisted: %+v", cfg.Repos)
	}
	if r.TaskConfig["project"] != "PAY" {
		t.Fatalf("repo taskConfig not persisted: %+v", r.TaskConfig)
	}
}

func TestRemoveStopsPollerAndRemoves(t *testing.T) {
	fx := newServiceFixture(t, &fakeRunnerDiscovery{})
	if _, err := fx.svc.Register(context.Background(), repo.RegisterInput{Name: "payments", Path: "/srv/payments", TaskConfig: config.RawValues{"project": "PAY", "component": "api"}}); err != nil {
		t.Fatal(err)
	}
	if err := fx.svc.Remove(context.Background(), "payments"); err != nil {
		t.Fatalf("Remove failed: %v", err)
	}
	if _, err := fx.svc.Get("payments"); err == nil {
		t.Fatal("removed repo still returned by Get")
	}
	// The repo's poller is stopped: the registry no longer lists it.
	for _, info := range fx.svc.List() {
		if info.Name == "payments" {
			t.Fatal("removed repo still listed (poller not stopped)")
		}
	}
}

func TestRemoveRejectedWhileWorkflowReferences(t *testing.T) {
	fx := newServiceFixture(t, &fakeRunnerDiscovery{})
	if _, err := fx.svc.Register(context.Background(), repo.RegisterInput{Name: "payments", Path: "/srv/payments", TaskConfig: config.RawValues{"project": "PAY", "component": "api"}}); err != nil {
		t.Fatal(err)
	}
	fx.wfRefs.set("payments", true)
	if err := fx.svc.Remove(context.Background(), "payments"); err == nil {
		t.Fatal("removal accepted while a stored workflow references the repo")
	}
}

func TestRemoveRejectedWhileRunActive(t *testing.T) {
	fx := newServiceFixture(t, &fakeRunnerDiscovery{})
	if _, err := fx.svc.Register(context.Background(), repo.RegisterInput{Name: "payments", Path: "/srv/payments", TaskConfig: config.RawValues{"project": "PAY", "component": "api"}}); err != nil {
		t.Fatal(err)
	}
	fx.active.set("payments", true)
	if err := fx.svc.Remove(context.Background(), "payments"); err == nil {
		t.Fatal("removal accepted while an active run uses the repo")
	}
}
