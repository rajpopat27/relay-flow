package main

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/rajpopat27/relay-flow/internal/config"
	"github.com/rajpopat27/relay-flow/internal/execution/projection"
	"github.com/rajpopat27/relay-flow/internal/paths"
	"github.com/rajpopat27/relay-flow/internal/server"
	"github.com/rajpopat27/relay-flow/internal/task"
)

func onboardingPaths(root string) paths.Paths {
	return paths.Paths{
		Root: root, Config: filepath.Join(root, "config.yaml"),
		Credentials: filepath.Join(root, "credentials.yaml"),
		Database:    filepath.Join(root, "state.db"),
		Socket:      filepath.Join(root, "server.sock"),
		Lock:        filepath.Join(root, "server.lock"),
	}
}

func setInteractiveInitTestSeams(t *testing.T, picker func() ([]string, error), prompt func() (bool, error), setup func(paths.Paths, io.Reader) error) {
	t.Helper()
	oldTTY := interactiveInitTTY
	oldPicker := interactiveInitPluginPick
	oldPrompt := interactiveInitRepoPrompt
	oldSetup := interactiveInitRepoSetup
	interactiveInitTTY = func(io.Reader) bool { return true }
	interactiveInitPluginPick = picker
	interactiveInitRepoPrompt = prompt
	interactiveInitRepoSetup = setup
	t.Cleanup(func() {
		interactiveInitTTY = oldTTY
		interactiveInitPluginPick = oldPicker
		interactiveInitRepoPrompt = oldPrompt
		interactiveInitRepoSetup = oldSetup
	})
}

func TestInteractiveFirstRunSkipAndRegistration(t *testing.T) {
	registerGuidedAuthPlugin()
	for _, tc := range []struct {
		name     string
		register bool
	}{
		{name: "skip", register: false},
		{name: "register", register: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			guidedFirstRunAuthCalls = 0
			home := t.TempDir()
			root := filepath.Join(home, ".relay-flow")
			var setupCalls int
			setInteractiveInitTestSeams(t,
				func() ([]string, error) {
					return []string{guidedFirstRunAuthPlugin, "orca", "opencode", executorGoworkflows}, nil
				},
				func() (bool, error) { return tc.register, nil },
				func(got paths.Paths, _ io.Reader) error {
					setupCalls++
					if got.Config != filepath.Join(root, "config.yaml") {
						t.Fatalf("setup config path = %q", got.Config)
					}
					return nil
				},
			)
			t.Setenv("RELAY_FLOW_HOME", root)
			if code := run([]string{"init"}, strings.NewReader("")); code != exitOK {
				t.Fatalf("interactive init exit = %d", code)
			}
			if guidedFirstRunAuthCalls != 1 {
				t.Fatalf("auth calls = %d, want 1", guidedFirstRunAuthCalls)
			}
			if got := setupCalls; got != map[bool]int{false: 0, true: 1}[tc.register] {
				t.Fatalf("repository setup calls = %d, want %d", got, map[bool]int{false: 0, true: 1}[tc.register])
			}
			for _, path := range []string{filepath.Join(root, "config.yaml"), filepath.Join(root, "state.db")} {
				if _, err := os.Stat(path); err != nil {
					t.Fatalf("missing initialized artifact %s: %v", path, err)
				}
			}
			if _, err := os.Stat(filepath.Join(root, "credentials.yaml")); !os.IsNotExist(err) {
				t.Fatalf("unexpected credentials file: %v", err)
			}
		})
	}
}

func TestInteractiveFirstRunAuthenticationCancellationCleansHome(t *testing.T) {
	registerCanceledFirstRunAuth.Do(func() {
		task.Register(canceledFirstRunAuthPlugin, task.Factory{
			Auth: func(context.Context, []string, io.Reader) error {
				return errCanceledFirstRunAuth
			},
		})
	})
	root := filepath.Join(t.TempDir(), ".relay-flow")
	setInteractiveInitTestSeams(t,
		func() ([]string, error) {
			return []string{canceledFirstRunAuthPlugin, "orca", "opencode", executorGoworkflows}, nil
		},
		func() (bool, error) { return false, errors.New("repository prompt should not run") },
		func(paths.Paths, io.Reader) error { return errors.New("repository setup should not run") },
	)
	t.Setenv("RELAY_FLOW_HOME", root)
	if code := run([]string{"init"}, strings.NewReader("")); code != exitFail {
		t.Fatalf("canceled interactive init exit = %d, want %d", code, exitFail)
	}
	for _, path := range []string{filepath.Join(root, "config.yaml"), filepath.Join(root, "credentials.yaml"), filepath.Join(root, "state.db")} {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatalf("canceled auth left %s: %v", path, err)
		}
	}
}

func TestNonInteractiveInitAndForceDoNotAuthenticateOrOnboard(t *testing.T) {
	registerGuidedAuthPlugin()
	guidedFirstRunAuthCalls = 0
	root := filepath.Join(t.TempDir(), ".relay-flow")
	setInteractiveInitTestSeams(t,
		func() ([]string, error) { return nil, errors.New("plugin picker should not run") },
		func() (bool, error) { return false, errors.New("repository prompt should not run") },
		func(paths.Paths, io.Reader) error { return errors.New("repository setup should not run") },
	)
	t.Setenv("RELAY_FLOW_HOME", root)
	args := []string{"init", "--task-plugin", guidedFirstRunAuthPlugin, "--runner-plugin", "orca", "--harness-plugin", "opencode"}
	if code := run(args, strings.NewReader("")); code != exitOK {
		t.Fatalf("non-interactive init exit = %d", code)
	}
	if code := run(append([]string{"init", "--force"}, args[1:]...), strings.NewReader("")); code != exitOK {
		t.Fatalf("forced non-interactive init exit = %d", code)
	}
	if guidedFirstRunAuthCalls != 0 {
		t.Fatalf("non-interactive auth calls = %d, want 0", guidedFirstRunAuthCalls)
	}
}

func TestCommitFirstRunPublishesStagedState(t *testing.T) {
	parent := t.TempDir()
	p := onboardingPaths(filepath.Join(parent, ".relay-flow"))
	stage := filepath.Join(parent, "stage")
	if err := os.Mkdir(stage, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := config.SaveMachine(filepath.Join(stage, "config.yaml"), &config.Machine{
		TaskPlugin: "beads", RunnerPlugin: "orca", HarnessPlugin: "opencode",
	}); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(stage, "credentials.yaml"), []byte("plugin-owned\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	if err := commitFirstRun(p, stage, projection.ExecutorIdentity{ExecutorPlugin: executorGoworkflows}); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{p.Config, p.Credentials, p.Database} {
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("committed artifact %s: %v", path, err)
		}
	}
	for _, path := range []string{p.Config, p.Credentials, p.Database} {
		info, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm() != 0o600 {
			t.Fatalf("%s mode = %o, want 600", path, info.Mode().Perm())
		}
	}
	if _, err := os.Stat(firstRunPendingPath(p)); !os.IsNotExist(err) {
		t.Fatalf("initialization marker remains: %v", err)
	}
}

const (
	canceledFirstRunAuthPlugin = "canceled-first-run-auth-test"
	guidedFirstRunAuthPlugin   = "guided-first-run-auth-test"
)

var (
	registerCanceledFirstRunAuth sync.Once
	registerGuidedFirstRunAuth   sync.Once
	errCanceledFirstRunAuth      = errors.New("authentication canceled")
	guidedFirstRunAuthCalls      int
)

func registerGuidedAuthPlugin() {
	registerGuidedFirstRunAuth.Do(func() {
		task.Register(guidedFirstRunAuthPlugin, task.Factory{
			Auth: func(context.Context, []string, io.Reader) error {
				guidedFirstRunAuthCalls++
				return nil
			},
		})
	})
}

func TestRecoverFirstRunPublicationRemovesIncompleteArtifacts(t *testing.T) {
	root := filepath.Join(t.TempDir(), ".relay-flow")
	p := onboardingPaths(root)
	if err := os.MkdirAll(root, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p.Config, []byte("partial\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p.Database, []byte("partial\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := config.WriteAtomic(firstRunPendingPath(p), []byte(`{"credentials":true}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := recoverFirstRunPublication(p); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{p.Config, p.Credentials, p.Database, firstRunPendingPath(p)} {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatalf("incomplete publication left %s: %v", path, err)
		}
	}
}

func TestRecoverFirstRunPublicationKeepsCompleteArtifacts(t *testing.T) {
	root := filepath.Join(t.TempDir(), ".relay-flow")
	p := onboardingPaths(root)
	if err := os.MkdirAll(root, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := config.SaveMachine(p.Config, &config.Machine{TaskPlugin: "beads", RunnerPlugin: "orca", HarnessPlugin: "opencode"}); err != nil {
		t.Fatal(err)
	}
	if err := projection.InitDatabase(p.Database); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p.Credentials, []byte("plugin-owned\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := config.WriteAtomic(firstRunPendingPath(p), []byte(`{"credentials":true}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := recoverFirstRunPublication(p); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{p.Config, p.Credentials, p.Database} {
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("complete publication lost %s: %v", path, err)
		}
	}
	if _, err := os.Stat(firstRunPendingPath(p)); !os.IsNotExist(err) {
		t.Fatalf("completed publication marker remains: %v", err)
	}
}

func TestCleanupFailureLeavesFirstRunMarkerForRecovery(t *testing.T) {
	root := filepath.Join(t.TempDir(), ".relay-flow")
	p := onboardingPaths(root)
	if err := os.MkdirAll(root, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p.Config, []byte("partial\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p.Database, []byte("partial\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(p.Credentials, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(p.Credentials, "held"), []byte("secret\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := config.WriteAtomic(firstRunPendingPath(p), []byte(`{"credentials":true}`), 0o600); err != nil {
		t.Fatal(err)
	}

	if err := removeFirstRunArtifacts(p); err == nil {
		t.Fatal("cleanup unexpectedly removed a non-empty credential directory")
	}
	if _, err := os.Stat(firstRunPendingPath(p)); err != nil {
		t.Fatalf("cleanup failure removed recovery marker: %v", err)
	}

	if err := os.Remove(filepath.Join(p.Credentials, "held")); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(p.Credentials); err != nil {
		t.Fatal(err)
	}
	if err := recoverFirstRunPublication(p); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{p.Config, p.Credentials, p.Database, firstRunPendingPath(p)} {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatalf("recovery left %s: %v", path, err)
		}
	}
}

func TestStageFirstRunAuthenticationLeavesFinalHomeUntouchedOnFailure(t *testing.T) {
	registerCanceledFirstRunAuth.Do(func() {
		task.Register(canceledFirstRunAuthPlugin, task.Factory{
			Auth: func(context.Context, []string, io.Reader) error {
				return errCanceledFirstRunAuth
			},
		})
	})
	root := filepath.Join(t.TempDir(), "nested", ".relay-flow")
	p := onboardingPaths(root)
	_, stage, err := stageFirstRunAuthentication(p, &config.Machine{TaskPlugin: canceledFirstRunAuthPlugin}, canceledFirstRunAuthPlugin, nil)
	if !errors.Is(err, errCanceledFirstRunAuth) {
		t.Fatalf("authentication error = %v, want cancellation", err)
	}
	if stage != "" {
		t.Fatalf("failed authentication returned staging path %q", stage)
	}
	for _, path := range []string{p.Config, p.Credentials, p.Database} {
		if _, statErr := os.Stat(path); !os.IsNotExist(statErr) {
			t.Fatalf("failed authentication left %s: %v", path, statErr)
		}
	}
}

func TestEnsureFirstRunServerReusesReadySocket(t *testing.T) {
	root := t.TempDir()
	p := onboardingPaths(filepath.Join(root, ".relay-flow"))
	if err := os.MkdirAll(p.Root, 0o700); err != nil {
		t.Fatal(err)
	}
	listener, err := net.Listen("unix", p.Socket)
	if err != nil {
		t.Fatal(err)
	}
	server := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/repos" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"ok":true,"data":[]}`)
	})}
	go func() { _ = server.Serve(listener) }()
	defer server.Close()

	if err := ensureFirstRunServer(p); err != nil {
		t.Fatalf("reuse ready server: %v", err)
	}
}

func TestEnsureFirstRunServerWaitsForExistingOwner(t *testing.T) {
	root := t.TempDir()
	p := onboardingPaths(filepath.Join(root, ".relay-flow"))
	if err := os.MkdirAll(p.Root, 0o700); err != nil {
		t.Fatal(err)
	}
	lock, err := os.OpenFile(p.Lock, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		lock.Close()
		t.Fatal(err)
	}
	defer func() {
		_ = syscall.Flock(int(lock.Fd()), syscall.LOCK_UN)
		_ = lock.Close()
	}()

	oldTimeout, oldPoll := backgroundServeStartupTimeout, backgroundServePollInterval
	backgroundServeStartupTimeout = 2 * time.Second
	backgroundServePollInterval = 10 * time.Millisecond
	defer func() {
		backgroundServeStartupTimeout = oldTimeout
		backgroundServePollInterval = oldPoll
	}()

	started := make(chan *http.Server, 1)
	go func() {
		time.Sleep(300 * time.Millisecond)
		listener, listenErr := net.Listen("unix", p.Socket)
		if listenErr != nil {
			started <- nil
			return
		}
		srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"ok":true,"data":[]}`)
		})}
		started <- srv
		_ = srv.Serve(listener)
	}()
	if err := ensureFirstRunServer(p); err != nil {
		t.Fatalf("wait for existing owner: %v", err)
	}
	if srv := <-started; srv != nil {
		_ = srv.Close()
	} else {
		t.Fatal("existing owner test server failed to start")
	}
}

func TestEnsureFirstRunServerTimesOutForUnreadyOwner(t *testing.T) {
	root := t.TempDir()
	p := onboardingPaths(filepath.Join(root, ".relay-flow"))
	if err := os.MkdirAll(p.Root, 0o700); err != nil {
		t.Fatal(err)
	}
	lock, err := os.OpenFile(p.Lock, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		lock.Close()
		t.Fatal(err)
	}
	defer func() {
		_ = syscall.Flock(int(lock.Fd()), syscall.LOCK_UN)
		_ = lock.Close()
	}()
	oldTimeout, oldPoll := backgroundServeStartupTimeout, backgroundServePollInterval
	backgroundServeStartupTimeout = 300 * time.Millisecond
	backgroundServePollInterval = 10 * time.Millisecond
	defer func() {
		backgroundServeStartupTimeout = oldTimeout
		backgroundServePollInterval = oldPoll
	}()

	if err := ensureFirstRunServer(p); err == nil || !strings.Contains(err.Error(), "startup timed out") {
		t.Fatalf("unready owner error = %v, want startup timeout", err)
	}
}

func TestRunFirstRunRepositorySetupDelegatesRegistration(t *testing.T) {
	root := t.TempDir()
	p := onboardingPaths(filepath.Join(root, ".relay-flow"))
	if err := os.MkdirAll(p.Root, 0o700); err != nil {
		t.Fatal(err)
	}
	listener, err := net.Listen("unix", p.Socket)
	if err != nil {
		t.Fatal(err)
	}
	srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/repos" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"ok":true,"data":[]}`)
	})}
	go func() { _ = srv.Serve(listener) }()
	defer srv.Close()

	oldRegistration := firstRunRegistration
	firstRunRegistration = func(c *server.Client, name, path string, sets kvFlags, stdin io.Reader) int {
		if c == nil || name != "" || path != "" || sets != nil || stdin == nil {
			t.Errorf("registration delegation = client=%v name=%q path=%q sets=%v stdin=%v", c != nil, name, path, sets, stdin)
		}
		return exitOK
	}
	defer func() { firstRunRegistration = oldRegistration }()
	if err := runFirstRunRepositorySetup(p, strings.NewReader("input")); err != nil {
		t.Fatal(err)
	}
}

func TestRunOnboardingSpinner(t *testing.T) {
	called := false
	if err := runOnboardingSpinner("test", func() error {
		called = true
		return nil
	}); err != nil {
		t.Fatalf("spinner success = %v", err)
	}
	if !called {
		t.Fatal("spinner action was not called")
	}

	wantErr := errors.New("startup failed")
	if err := runOnboardingSpinner("test", func() error { return wantErr }); !errors.Is(err, wantErr) {
		t.Fatalf("spinner error = %v, want %v", err, wantErr)
	}
}

func TestStandaloneRepoServerHint(t *testing.T) {
	err := standaloneRepoServerHint(errors.New("server call GET /repos: dial unix /tmp/server.sock: connect: no such file or directory"))
	if err == nil || !containsAll(err.Error(), "serve --background", "guided init") {
		t.Fatalf("server hint = %v", err)
	}
	original := errors.New("server bad request")
	if got := standaloneRepoServerHint(original); got != original {
		t.Fatalf("non-connectivity error changed: %v", got)
	}
}

func containsAll(value string, parts ...string) bool {
	for _, part := range parts {
		if !strings.Contains(value, part) {
			return false
		}
	}
	return true
}
