package main

import (
	"bytes"
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/rajpopat27/relay-flow/internal/config"
	"github.com/rajpopat27/relay-flow/internal/execution/projection"
	"github.com/rajpopat27/relay-flow/internal/paths"
	_ "modernc.org/sqlite"
)

func TestInitExecutorSelectionFieldUsesExactTitleAndEmbeddedDefault(t *testing.T) {
	var selected string
	field, err := executorSelectField(&selected)
	if err != nil {
		t.Fatal(err)
	}
	if field == nil {
		t.Fatal("executor selection field is nil")
	}
	var out bytes.Buffer
	if err := field.RunAccessible(&out, strings.NewReader("1\n")); err != nil {
		t.Fatal(err)
	}
	if selected != "goworkflows" {
		t.Fatalf("default executor selection = %q, want goworkflows", selected)
	}
	if !strings.Contains(out.String(), "Select executor") {
		t.Fatalf("executor selection output %q missing exact title", out.String())
	}
}

func TestTemporalSettingsFieldsUseApprovedTitlesAndAddressDefault(t *testing.T) {
	address := ""
	field, err := temporalAddressField(&address)
	if err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if err := field.RunAccessible(&out, strings.NewReader("\n")); err != nil {
		t.Fatal(err)
	}
	if address != defaultTemporalAddr || !strings.Contains(out.String(), "Temporal server address") {
		t.Fatalf("address field = %q, output %q", address, out.String())
	}
	namespace := ""
	field, err = temporalNamespaceField(&namespace)
	if err != nil {
		t.Fatal(err)
	}
	out.Reset()
	if err := field.RunAccessible(&out, strings.NewReader("relay-flow-team\n")); err != nil {
		t.Fatal(err)
	}
	if namespace != "relay-flow-team" || !strings.Contains(out.String(), "Temporal namespace/team name") {
		t.Fatalf("namespace field = %q, output %q", namespace, out.String())
	}
}

func TestInitRejectsTemporalOnlyFlagsForEmbeddedExecutor(t *testing.T) {
	home := t.TempDir()
	code := cli(t, home, "", "init",
		"--task-plugin", "jira", "--runner-plugin", "orca", "--harness-plugin", "opencode",
		"--executor-plugin", "goworkflows", "--temporal-address", "localhost:7233",
	)
	if code == 0 {
		t.Fatal("embedded init accepted Temporal-only flags")
	}
	if _, err := os.Stat(filepath.Join(home, ".relay-flow", "config.yaml")); !os.IsNotExist(err) {
		t.Fatalf("rejected embedded init wrote config: %v", err)
	}
}

func TestInitTemporalFailureDoesNotWritePartialConfiguration(t *testing.T) {
	home := t.TempDir()
	code := cli(t, home, "", "init",
		"--task-plugin", "jira", "--runner-plugin", "orca", "--harness-plugin", "opencode",
		"--executor-plugin", "temporal", "--temporal-address", "127.0.0.1:1", "--temporal-namespace", "relay-flow-failure",
	)
	if code == 0 {
		t.Fatal("Temporal init unexpectedly succeeded against unavailable server")
	}
	root := filepath.Join(home, ".relay-flow")
	for _, name := range []string{"config.yaml", "state.db"} {
		if _, err := os.Stat(filepath.Join(root, name)); !os.IsNotExist(err) {
			t.Fatalf("failed Temporal init wrote %s: %v", name, err)
		}
	}
}

func TestTemporalInitLiveCreatesNamedNamespaceAndIdentity(t *testing.T) {
	if os.Getenv("RELAY_FLOW_TEMPORAL_LIVE") != "1" {
		t.Skip("set RELAY_FLOW_TEMPORAL_LIVE=1 to run Temporal init against the local server")
	}
	home := t.TempDir()
	namespace := fmt.Sprintf("relay-flow-init-%d", time.Now().UnixNano())
	code := cli(t, home, "", "init",
		"--task-plugin", "jira", "--runner-plugin", "orca", "--harness-plugin", "opencode",
		"--executor-plugin", "temporal", "--temporal-address", "localhost:7233", "--temporal-namespace", namespace,
	)
	if code != 0 {
		t.Fatalf("Temporal init exit = %d, want 0", code)
	}
	root := filepath.Join(home, ".relay-flow")
	cfg, err := config.LoadMachine(filepath.Join(root, "config.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.ExecutorPlugin != "temporal" || cfg.TemporalAddress != "localhost:7233" || cfg.TemporalNamespace != namespace {
		t.Fatalf("Temporal config = %+v", cfg)
	}
	db, err := sql.Open("sqlite", filepath.Join(root, "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var plugin, address, persistedNamespace string
	if err := db.QueryRowContext(context.Background(), `SELECT executor_plugin, temporal_address, temporal_namespace FROM relay_executor_identity WHERE singleton = 1`).Scan(&plugin, &address, &persistedNamespace); err != nil {
		t.Fatalf("read initialized executor identity: %v", err)
	}
	if plugin != "temporal" || address != "localhost:7233" || persistedNamespace != namespace {
		t.Fatalf("persisted Temporal identity = %q/%q/%q", plugin, address, persistedNamespace)
	}
}

func TestInitForceAdoptsLegacyMarkerlessGoworkflowsDatabase(t *testing.T) {
	home := t.TempDir()
	root := filepath.Join(home, ".relay-flow")
	p := pathsForRoot(root)
	if err := paths.Ensure(p); err != nil {
		t.Fatal(err)
	}
	if err := config.SaveMachine(p.Config, &config.Machine{TaskPlugin: "jira", RunnerPlugin: "orca", HarnessPlugin: "opencode"}); err != nil {
		t.Fatal(err)
	}
	if err := projection.InitDatabase(p.Database); err != nil {
		t.Fatal(err)
	}
	if code := cli(t, home, "", "init", "--force", "--task-plugin", "jira", "--runner-plugin", "orca", "--harness-plugin", "opencode", "--executor-plugin", "goworkflows"); code != 0 {
		t.Fatalf("legacy markerless init --force exit = %d", code)
	}
	db, err := sql.Open("sqlite", p.Database)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var plugin string
	if err := db.QueryRow(`SELECT executor_plugin FROM relay_executor_identity WHERE singleton = 1`).Scan(&plugin); err != nil {
		t.Fatal(err)
	}
	if plugin != "goworkflows" {
		t.Fatalf("adopted legacy executor identity = %q", plugin)
	}
}

func TestInitForceRejectsExecutorChange(t *testing.T) {
	home := t.TempDir()
	initHome(t, home)
	before := readFile(t, filepath.Join(home, ".relay-flow", "config.yaml"))
	if code := cli(t, home, "", "init", "--force",
		"--task-plugin", "jira", "--runner-plugin", "orca", "--harness-plugin", "opencode",
		"--executor-plugin", "temporal", "--temporal-namespace", "relay-flow-change"); code == 0 {
		t.Fatal("init --force accepted an executor change")
	}
	if got := readFile(t, filepath.Join(home, ".relay-flow", "config.yaml")); got != before {
		t.Fatal("rejected executor change modified config")
	}
}

func TestTemporalInitForceRejectsDurableIdentityChangeLive(t *testing.T) {
	if os.Getenv("RELAY_FLOW_TEMPORAL_LIVE") != "1" {
		t.Skip("set RELAY_FLOW_TEMPORAL_LIVE=1 to run Temporal init against the local server")
	}
	home := t.TempDir()
	namespace := fmt.Sprintf("relay-flow-force-%d", time.Now().UnixNano())
	args := []string{"init", "--task-plugin", "jira", "--runner-plugin", "orca", "--harness-plugin", "opencode", "--executor-plugin", "temporal", "--temporal-address", "localhost:7233", "--temporal-namespace", namespace}
	if code := cli(t, home, "", args...); code != 0 {
		t.Fatalf("initial Temporal init exit = %d", code)
	}
	before := readFile(t, filepath.Join(home, ".relay-flow", "config.yaml"))
	for name, changed := range map[string][]string{
		"address":   {"init", "--force", "--task-plugin", "jira", "--runner-plugin", "orca", "--harness-plugin", "opencode", "--executor-plugin", "temporal", "--temporal-address", "localhost:7443", "--temporal-namespace", namespace},
		"namespace": {"init", "--force", "--task-plugin", "jira", "--runner-plugin", "orca", "--harness-plugin", "opencode", "--executor-plugin", "temporal", "--temporal-address", "localhost:7233", "--temporal-namespace", namespace + "-changed"},
		"executor":  {"init", "--force", "--task-plugin", "jira", "--runner-plugin", "orca", "--harness-plugin", "opencode", "--executor-plugin", "goworkflows"},
	} {
		t.Run(name, func(t *testing.T) {
			if code := cli(t, home, "", changed...); code == 0 {
				t.Fatalf("init --force accepted changed Temporal %s", name)
			}
			if got := readFile(t, filepath.Join(home, ".relay-flow", "config.yaml")); got != before {
				t.Fatalf("rejected init --force %s change modified config", name)
			}
		})
	}
}

func TestInitTemporalNonInteractiveRequiresExplicitNamespace(t *testing.T) {
	home := t.TempDir()
	code := cli(t, home, "", "init",
		"--task-plugin", "jira", "--runner-plugin", "orca", "--harness-plugin", "opencode",
		"--executor-plugin", "temporal", "--temporal-address", "localhost:7233",
	)
	if code == 0 {
		t.Fatal("non-interactive Temporal init succeeded without namespace")
	}
	if _, err := os.Stat(filepath.Join(home, ".relay-flow", "config.yaml")); !os.IsNotExist(err) {
		t.Fatalf("missing-namespace init wrote config: %v", err)
	}
}
