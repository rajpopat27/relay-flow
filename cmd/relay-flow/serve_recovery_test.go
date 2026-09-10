package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/rajpopat27/relay-flow/internal/config"
	"github.com/rajpopat27/relay-flow/internal/execution/projection"
	"github.com/rajpopat27/relay-flow/internal/paths"
)

func TestCorruptProjectionIsClassifiedUnusableForRecovery(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.db")
	if err := os.WriteFile(path, []byte("not sqlite"), 0o600); err != nil {
		t.Fatal(err)
	}
	err := verifyExecutorIdentity(path, projection.ExecutorIdentity{ExecutorPlugin: "temporal", TemporalAddress: "localhost:7233", TemporalNamespace: "corrupt"})
	if !errors.Is(err, ErrProjectionUnusable) {
		t.Fatalf("corrupt projection error = %v, want ErrProjectionUnusable", err)
	}
}

func TestTemporalRecoveryRejectsIdentityMismatchBeforeBackup(t *testing.T) {
	log := newScenarioLog()
	setScenarioFactoryAdapters(newScenarioTaskSystem(log), newScenarioRunner(log), newScenarioHarness(log))
	root := filepath.Join(t.TempDir(), ".relay-flow")
	p := pathsForRoot(root)
	if err := paths.Ensure(p); err != nil {
		t.Fatal(err)
	}
	if err := config.SaveMachine(p.Config, &config.Machine{
		TaskPlugin: scenarioTaskPlugin, RunnerPlugin: scenarioRunnerPlugin, HarnessPlugin: scenarioHarnessPlugin,
		ExecutorPlugin: "temporal", TemporalAddress: "localhost:7233", TemporalNamespace: "identity-recovery",
	}); err != nil {
		t.Fatal(err)
	}
	if err := projection.InitDatabaseWithIdentity(p.Database, projection.ExecutorIdentity{
		ExecutorPlugin: "temporal", TemporalAddress: "other-host:7233", TemporalNamespace: "identity-recovery",
	}); err != nil {
		t.Fatal(err)
	}
	if err := serveRoot(context.Background(), p, true); !errors.Is(err, projection.ErrIdentityMismatch) {
		t.Fatalf("recovery identity mismatch error = %v", err)
	}
	if _, err := os.Stat(p.Database); err != nil {
		t.Fatalf("identity mismatch moved database before failing: %v", err)
	}
	backups, err := filepath.Glob(p.Database + ".recover-*.bak")
	if err != nil {
		t.Fatal(err)
	}
	if len(backups) != 0 {
		t.Fatalf("identity mismatch created recovery backups: %v", backups)
	}
}

func TestTemporalRecoveryRejectsMarkerlessExistingDatabase(t *testing.T) {
	log := newScenarioLog()
	setScenarioFactoryAdapters(newScenarioTaskSystem(log), newScenarioRunner(log), newScenarioHarness(log))
	root := filepath.Join(t.TempDir(), ".relay-flow")
	p := pathsForRoot(root)
	if err := paths.Ensure(p); err != nil {
		t.Fatal(err)
	}
	if err := config.SaveMachine(p.Config, &config.Machine{
		TaskPlugin: scenarioTaskPlugin, RunnerPlugin: scenarioRunnerPlugin, HarnessPlugin: scenarioHarnessPlugin,
		ExecutorPlugin: "temporal", TemporalAddress: "localhost:7233", TemporalNamespace: "markerless-recovery",
	}); err != nil {
		t.Fatal(err)
	}
	if err := projection.InitDatabase(p.Database); err != nil {
		t.Fatal(err)
	}
	if err := serveRoot(context.Background(), p, true); !errors.Is(err, projection.ErrIdentityMissing) {
		t.Fatalf("markerless Temporal recovery error = %v", err)
	}
	if _, err := os.Stat(p.Database); err != nil {
		t.Fatalf("markerless database moved before failing: %v", err)
	}
}

func TestRecoveryBackupPathAvoidsSameTimestampCollision(t *testing.T) {
	database := filepath.Join(t.TempDir(), "state.db")
	stamp := "20260905T010203.000000000Z"
	first := database + ".recover-" + stamp + ".bak"
	if err := os.WriteFile(first, []byte("existing"), 0o600); err != nil {
		t.Fatal(err)
	}
	stem, err := recoveryBackupStem(database, stamp, []string{"", "-wal", "-shm"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stem, ".recover-"+stamp+"-1") {
		t.Fatalf("collision-safe backup stem = %q, want alternate", stem)
	}
}
