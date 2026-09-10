package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/charmbracelet/huh"
	huhspinner "github.com/charmbracelet/huh/spinner"
	"github.com/rajpopat27/relay-flow/internal/config"
	"github.com/rajpopat27/relay-flow/internal/execution/projection"
	"github.com/rajpopat27/relay-flow/internal/paths"
	"github.com/rajpopat27/relay-flow/internal/server"
	"github.com/rajpopat27/relay-flow/internal/task"
)

// Small command-level seams keep the interactive orchestration testable
// without changing task, runner, or server package boundaries.
var (
	interactiveInitTTY        = isTTY
	interactiveInitPluginPick = pickPluginsInteractive
	interactiveInitRepoPrompt = promptFirstRunRepositorySetup
	interactiveInitRepoSetup  = runFirstRunRepositorySetup
	firstRunRegistration      = cmdRepoRegister
)

// stageFirstRunAuthentication gives the selected task plugin its normal auth
// entry point without exposing a partially initialized relay-flow home. Jira's
// auth flow also writes the first mailbox-assignee default into config.yaml;
// staging that file keeps the plugin's existing behavior while allowing the
// final config, credentials, and database to be committed only after auth.
func stageFirstRunAuthentication(p paths.Paths, cfg *config.Machine, plugin string, stdin io.Reader) (*config.Machine, string, error) {
	if err := os.MkdirAll(filepath.Dir(p.Root), 0o700); err != nil {
		return nil, "", fmt.Errorf("create initialization parent directory: %w", err)
	}
	stage, err := os.MkdirTemp(filepath.Dir(p.Root), ".relay-flow-init-")
	if err != nil {
		return nil, "", fmt.Errorf("create initialization staging directory: %w", err)
	}
	cleanup := func() {
		_ = os.RemoveAll(stage)
	}
	if err := config.SaveMachine(filepath.Join(stage, "config.yaml"), cfg); err != nil {
		cleanup()
		return nil, "", fmt.Errorf("stage machine config: %w", err)
	}

	previousHome, hadHome := os.LookupEnv("RELAY_FLOW_HOME")
	if err := os.Setenv("RELAY_FLOW_HOME", stage); err != nil {
		cleanup()
		return nil, "", fmt.Errorf("stage relay-flow home: %w", err)
	}
	authErr := task.Auth(context.Background(), plugin, nil, stdin)
	if hadHome {
		_ = os.Setenv("RELAY_FLOW_HOME", previousHome)
	} else {
		_ = os.Unsetenv("RELAY_FLOW_HOME")
	}
	if authErr != nil {
		cleanup()
		return nil, "", authErr
	}

	staged, err := config.LoadMachine(filepath.Join(stage, "config.yaml"))
	if err != nil {
		cleanup()
		return nil, "", fmt.Errorf("load authenticated machine config: %w", err)
	}
	return staged, stage, nil
}

const firstRunPendingName = ".init-pending"

type firstRunPending struct {
	Credentials bool `json:"credentials"`
}

func firstRunPendingPath(p paths.Paths) string {
	return filepath.Join(p.Root, firstRunPendingName)
}

// commitFirstRun publishes the staged config and task credentials only after
// the durable database has been initialized. The pending marker makes the
// multi-file publication recoverable if the process is interrupted; each
// regular file is written with renameio through config.WriteAtomic.
func commitFirstRun(p paths.Paths, stage string, identity projection.ExecutorIdentity) (err error) {
	stagedConfig := filepath.Join(stage, "config.yaml")
	stagedCredentials := filepath.Join(stage, "credentials.yaml")
	configData, err := os.ReadFile(stagedConfig)
	if err != nil {
		return fmt.Errorf("read staged machine config: %w", err)
	}
	credentialsData, credentials, err := stagedCredentialsData(stagedCredentials)
	if err != nil {
		return err
	}

	for _, path := range []string{p.Config, p.Database, p.Credentials} {
		if _, statErr := os.Stat(path); statErr == nil {
			return fmt.Errorf("initialization artifact %s already exists; refusing to overwrite it", path)
		} else if !os.IsNotExist(statErr) {
			return fmt.Errorf("stat initialization artifact %s: %w", path, statErr)
		}
	}
	if err := os.MkdirAll(p.Root, 0o700); err != nil {
		return fmt.Errorf("create relay-flow home: %w", err)
	}
	if err := os.Chmod(p.Root, 0o700); err != nil {
		return fmt.Errorf("chmod relay-flow home: %w", err)
	}

	pending := firstRunPending{Credentials: credentials}
	pendingData, err := json.Marshal(pending)
	if err != nil {
		return fmt.Errorf("marshal initialization marker: %w", err)
	}
	if err := config.WriteAtomic(firstRunPendingPath(p), pendingData, 0o600); err != nil {
		return fmt.Errorf("write initialization marker: %w", err)
	}
	published := false
	defer func() {
		if published {
			return
		}
		_ = removeFirstRunArtifacts(p)
	}()

	if err := projection.InitDatabaseWithIdentity(p.Database, identity); err != nil {
		return fmt.Errorf("initialize durable state: %w", err)
	}
	if err := config.WriteAtomic(p.Config, configData, 0o600); err != nil {
		return fmt.Errorf("publish machine config: %w", err)
	}
	if credentials {
		if err := config.WriteAtomic(p.Credentials, credentialsData, 0o600); err != nil {
			return fmt.Errorf("publish task credentials: %w", err)
		}
	}

	// Once all durable artifacts exist, leave them in place even if marker
	// cleanup is interrupted. The next init removes the marker idempotently.
	published = true
	if err := os.Remove(firstRunPendingPath(p)); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("finish initialization publication: %w", err)
	}
	if err := syncDirectory(p.Root); err != nil {
		return fmt.Errorf("sync relay-flow home: %w", err)
	}
	return nil
}

func stagedCredentialsData(path string) ([]byte, bool, error) {
	data, err := os.ReadFile(path)
	if err == nil {
		return data, true, nil
	}
	if os.IsNotExist(err) {
		return nil, false, nil
	}
	return nil, false, fmt.Errorf("read staged task credentials: %w", err)
}

func removeFirstRunArtifacts(p paths.Paths) error {
	// Keep the marker until every owned artifact is gone. If cleanup fails or
	// the process is interrupted, the next init can retry from the marker.
	for _, path := range []string{
		p.Config, p.Credentials, p.Database,
		p.Database + "-wal", p.Database + "-shm",
	} {
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("remove initialization artifact %s: %w", path, err)
		}
	}
	if _, err := os.Stat(p.Root); os.IsNotExist(err) {
		return nil
	} else if err != nil {
		return fmt.Errorf("stat relay-flow home: %w", err)
	}
	// Persist the artifact removals while the marker still protects recovery.
	if err := syncDirectory(p.Root); err != nil {
		return fmt.Errorf("sync cleaned initialization artifacts: %w", err)
	}
	if err := os.Remove(firstRunPendingPath(p)); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("remove initialization marker: %w", err)
	}
	if err := syncDirectory(p.Root); err != nil {
		return fmt.Errorf("sync initialization marker removal: %w", err)
	}
	return nil
}

func syncDirectory(path string) error {
	dir, err := os.Open(path)
	if err != nil {
		return err
	}
	defer dir.Close()
	return dir.Sync()
}

// recoverFirstRunPublication completes marker cleanup for a complete
// publication or removes only the artifacts owned by an incomplete first-run.
// It runs before init's normal existing-state refusal so an interrupted setup
// can be retried instead of wedging the home.
func recoverFirstRunPublication(p paths.Paths) error {
	marker := firstRunPendingPath(p)
	raw, err := os.ReadFile(marker)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("read initialization marker: %w", err)
	}
	var pending firstRunPending
	if err := json.Unmarshal(raw, &pending); err != nil {
		if cleanupErr := removeFirstRunArtifacts(p); cleanupErr != nil {
			return fmt.Errorf("recover invalid initialization marker: %v; cleanup: %w", err, cleanupErr)
		}
		return nil
	}
	complete := true
	for _, path := range []string{p.Config, p.Database} {
		if _, statErr := os.Stat(path); statErr != nil {
			if !os.IsNotExist(statErr) {
				return fmt.Errorf("inspect initialization artifact %s: %w", path, statErr)
			}
			complete = false
		}
	}
	if pending.Credentials {
		if _, statErr := os.Stat(p.Credentials); statErr != nil {
			if !os.IsNotExist(statErr) {
				return fmt.Errorf("inspect initialization credentials: %w", statErr)
			}
			complete = false
		}
	}
	if complete {
		if err := os.Remove(marker); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("remove completed initialization marker: %w", err)
		}
		return syncDirectory(p.Root)
	}
	return removeFirstRunArtifacts(p)
}

func cleanupFirstRunStaging(p paths.Paths) error {
	entries, err := os.ReadDir(filepath.Dir(p.Root))
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("read initialization staging parent: %w", err)
	}
	for _, entry := range entries {
		if !entry.IsDir() || !strings.HasPrefix(entry.Name(), ".relay-flow-init-") {
			continue
		}
		if err := os.RemoveAll(filepath.Join(filepath.Dir(p.Root), entry.Name())); err != nil {
			return fmt.Errorf("remove stale initialization staging %s: %w", entry.Name(), err)
		}
	}
	return nil
}

func promptFirstRunRepositorySetup() (bool, error) {
	register := false
	form := huh.NewForm(huh.NewGroup(
		huh.NewConfirm().
			Title("Register repositories now?").
			Description("You can register repositories later with relay-flow repo register.").
			Affirmative("Yes").
			Negative("No").
			Value(&register),
	))
	if err := form.Run(); err != nil {
		return false, fmt.Errorf("repository setup prompt: %w", err)
	}
	return register, nil
}

// runFirstRunRepositorySetup starts or reuses the server, showing a small
// terminal spinner while the Unix socket becomes ready, then delegates to the
// existing interactive repository-registration flow.
func runFirstRunRepositorySetup(p paths.Paths, stdin io.Reader) error {
	if err := runOnboardingSpinner("Starting relay-flow server", func() error {
		return ensureFirstRunServer(p)
	}); err != nil {
		return err
	}

	if code := firstRunRegistration(server.NewClient(p.Socket), "", "", nil, stdin); code != exitOK {
		return fmt.Errorf("repository registration failed")
	}
	return nil
}

func ensureFirstRunServer(p paths.Paths) error {
	client := server.NewClient(p.Socket)
	if serverResponding(client, 200*time.Millisecond) {
		return nil
	}
	if err := startBackgroundServe(p, false, false); err != nil {
		// A server may have become ready between the initial probe and the
		// child launch. Reuse it when that race is harmless.
		if serverResponding(client, 200*time.Millisecond) {
			return nil
		}
		return err
	}
	return nil
}

// runOnboardingSpinner keeps the server-readiness wait visible without
// changing the existing Huh forms or the non-interactive command paths.
func runOnboardingSpinner(title string, action func() error) error {
	return huhspinner.New().
		Type(huhspinner.Line).
		Title(title + "...").
		Output(os.Stderr).
		ActionWithErr(func(context.Context) error { return action() }).
		Run()
}

// standaloneRepoServerHint adds the actionable startup hint required for the
// independent repo-register command while preserving server/API messages for
// failures that occur after a successful connection.
func standaloneRepoServerHint(err error) error {
	if err == nil {
		return nil
	}
	message := err.Error()
	for _, marker := range []string{"dial unix", "connect: no such file or directory", "connection refused"} {
		if strings.Contains(message, marker) {
			return fmt.Errorf("%w; start the server with `relay-flow serve --background` (or use the guided init path)", err)
		}
	}
	return err
}
