package main

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/rajpopat27/relay-flow/internal/config"
	"github.com/rajpopat27/relay-flow/internal/execution/projection"
	"github.com/rajpopat27/relay-flow/internal/paths"
	"github.com/rajpopat27/relay-flow/internal/server"
	_ "modernc.org/sqlite"
)

// These integration tests are deliberately live-gated because they exercise
// the selected durable backend and the local Temporal service. They use the
// same fake task/runner/harness factories as the composition scenario, but no
// task-system state or workflow is needed to prove worker selection.
func TestServeSelectsConfiguredTemporalExecutor(t *testing.T) {
	if os.Getenv("RELAY_FLOW_TEMPORAL_LIVE") != "1" {
		t.Skip("set RELAY_FLOW_TEMPORAL_LIVE=1 to run backend selection against Temporal")
	}
	assertServeUsesTemporal(t, false)
}

func TestServeRecoverSelectsConfiguredTemporalExecutor(t *testing.T) {
	if os.Getenv("RELAY_FLOW_TEMPORAL_LIVE") != "1" {
		t.Skip("set RELAY_FLOW_TEMPORAL_LIVE=1 to run backend recovery against Temporal")
	}
	assertServeUsesTemporal(t, true)
}

func assertServeUsesTemporal(t *testing.T, recover bool) {
	t.Helper()
	log := newScenarioLog()
	setScenarioFactoryAdapters(newScenarioTaskSystem(log), newScenarioRunner(log), newScenarioHarness(log))

	root := filepath.Join(t.TempDir(), ".relay-flow")
	t.Setenv("RELAY_FLOW_HOME", root)
	p := pathsForRoot(root)
	if err := paths.Ensure(p); err != nil {
		t.Fatal(err)
	}
	namespace := fmt.Sprintf("relay-flow-serve-%d", time.Now().UnixNano())
	if err := ensureTemporalNamespace(context.Background(), "localhost:7233", namespace, 30); err != nil {
		t.Fatal(err)
	}
	// Namespace registration is acknowledged before every frontend worker
	// necessarily observes it.
	time.Sleep(5 * time.Second)
	if err := config.SaveMachine(p.Config, &config.Machine{
		TaskPlugin:                scenarioTaskPlugin,
		RunnerPlugin:              scenarioRunnerPlugin,
		HarnessPlugin:             scenarioHarnessPlugin,
		ExecutorPlugin:            "temporal",
		TemporalAddress:           "localhost:7233",
		TemporalNamespace:         namespace,
		CompletedRunRetentionDays: 30,
	}); err != nil {
		t.Fatal(err)
	}
	if err := projection.InitDatabaseWithIdentity(p.Database, projection.ExecutorIdentity{
		ExecutorPlugin: "temporal", TemporalAddress: "localhost:7233", TemporalNamespace: namespace,
	}); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- serveRoot(ctx, p, recover) }()
	client := server.NewClient(p.Socket)
	waitForServerOrError(t, client, done)

	assertNoEmbeddedExecutionTables(t, p.Database)
	if err := client.Stop(context.Background()); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("serveRoot(%v): %v", recover, err)
		}
	case <-time.After(15 * time.Second):
		t.Fatalf("serveRoot(%v) did not stop", recover)
	}
}

func waitForServerOrError(t *testing.T, client *server.Client, done <-chan error) {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		select {
		case err := <-done:
			t.Fatalf("serveRoot exited before socket became ready: %v", err)
		default:
		}
		ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
		_, err := client.ListRepos(ctx)
		cancel()
		if err == nil {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	select {
	case err := <-done:
		t.Fatalf("serveRoot exited before socket became ready: %v", err)
	default:
		t.Fatal("server did not become ready")
	}
}

func assertNoEmbeddedExecutionTables(t *testing.T, databasePath string) {
	t.Helper()
	db, err := sql.Open("sqlite", databasePath)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var count int
	if err := db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name IN ('instances', 'history_events', 'activity_tasks')`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("Temporal projection contains embedded go-workflows tables: %d", count)
	}
	var plugin, address, namespace string
	if err := db.QueryRow(`SELECT executor_plugin, temporal_address, temporal_namespace FROM relay_executor_identity WHERE singleton = 1`).Scan(&plugin, &address, &namespace); err != nil {
		t.Fatal(err)
	}
	if plugin != "temporal" || address != "localhost:7233" || namespace == "" {
		t.Fatalf("executor identity = %q/%q/%q", plugin, address, namespace)
	}
}

func pathsForRoot(root string) paths.Paths {
	return paths.Paths{
		Root: root, Config: filepath.Join(root, "config.yaml"),
		Credentials: filepath.Join(root, "credentials.yaml"), Workflows: filepath.Join(root, "workflows"),
		Database: filepath.Join(root, "state.db"), Socket: filepath.Join(root, "server.sock"),
		Lock: filepath.Join(root, "server.lock"), ServerLog: filepath.Join(root, "server.log"),
		PluginLog: filepath.Join(root, "plugin.log"),
	}
}
