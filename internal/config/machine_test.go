package config_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/rajpopat27/relay-flow/internal/config"
	"github.com/rajpopat27/relay-flow/internal/paths"
)

// 3.29: machine config per specs/workflow-repo-management "Machine config
// stores global settings and registered repos" and "Global defaults are
// deterministic".

func TestLoadMachineDefaults(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	yaml := `
taskPlugin: jira
runnerPlugin: orca
harnessPlugin: opencode
`
	if err := os.WriteFile(path, []byte(yaml), 0600); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.LoadMachine(path)
	if err != nil {
		t.Fatalf("LoadMachine failed: %v", err)
	}
	if cfg.PollIntervalSeconds != 15 {
		t.Fatalf("PollIntervalSeconds = %d, want default 15", cfg.PollIntervalSeconds)
	}
	if cfg.CompletedRunRetentionDays != 30 {
		t.Fatalf("CompletedRunRetentionDays = %d, want default 30", cfg.CompletedRunRetentionDays)
	}
	if !cfg.KeepTerminalsAlive {
		t.Fatal("KeepTerminalsAlive = false, want default true")
	}
	if !cfg.KeepSessionsAlive {
		t.Fatal("KeepSessionsAlive = false, want default true")
	}
}

func TestLoadMachineValidatesRuntimeKeepSettings(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	invalid := "taskPlugin: jira\nrunnerPlugin: orca\nharnessPlugin: opencode\nkeepTerminalsAlive: true\nkeepSessionsAlive: false\n"
	if err := os.WriteFile(path, []byte(invalid), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := config.LoadMachine(path); err == nil || !strings.Contains(err.Error(), "keepTerminalsAlive requires keepSessionsAlive") {
		t.Fatalf("invalid keep settings error = %v", err)
	}

	valid := "taskPlugin: jira\nrunnerPlugin: orca\nharnessPlugin: opencode\nkeepTerminalsAlive: false\nkeepSessionsAlive: false\n"
	if err := os.WriteFile(path, []byte(valid), 0600); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.LoadMachine(path)
	if err != nil || cfg.KeepSessionsAlive {
		t.Fatalf("explicit session cleanup = %+v, %v", cfg, err)
	}

	keepSession := "taskPlugin: jira\nrunnerPlugin: orca\nharnessPlugin: opencode\nkeepTerminalsAlive: false\nkeepSessionsAlive: true\n"
	if err := os.WriteFile(path, []byte(keepSession), 0600); err != nil {
		t.Fatal(err)
	}
	cfg, err = config.LoadMachine(path)
	if err != nil || cfg.KeepTerminalsAlive || !cfg.KeepSessionsAlive {
		t.Fatalf("explicit terminal cleanup with session retention = %+v, %v", cfg, err)
	}
}

func TestLoadMachineRejectsNonPositiveGlobals(t *testing.T) {
	for _, tc := range []struct{ name, yaml string }{
		{"zero interval", "pollIntervalSeconds: 0\ntaskPlugin: jira\nrunnerPlugin: orca\nharnessPlugin: opencode\n"},
		{"negative interval", "pollIntervalSeconds: -5\ntaskPlugin: jira\nrunnerPlugin: orca\nharnessPlugin: opencode\n"},
		{"zero retention", "completedRunRetentionDays: 0\ntaskPlugin: jira\nrunnerPlugin: orca\nharnessPlugin: opencode\n"},
		{"negative retention", "completedRunRetentionDays: -1\ntaskPlugin: jira\nrunnerPlugin: orca\nharnessPlugin: opencode\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "config.yaml")
			if err := os.WriteFile(path, []byte(tc.yaml), 0600); err != nil {
				t.Fatal(err)
			}
			if _, err := config.LoadMachine(path); err == nil {
				t.Fatal("non-positive global accepted")
			}
		})
	}
}

func TestLoadMachineRejectsUnknownFields(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	yaml := `
taskPlugin: jira
runnerPlugin: orca
harnessPlugin: opencode
unknownField: x
repos:
  payments:
    path: /srv/payments
    bogusRepoField: 1
`
	if err := os.WriteFile(path, []byte(yaml), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := config.LoadMachine(path); err == nil {
		t.Fatal("unknown root/repo fields accepted by strict decode")
	}
}

func TestSaveAndLoadRoundTrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	cfg := &config.Machine{
		TaskPlugin:    "jira",
		RunnerPlugin:  "orca",
		HarnessPlugin: "opencode",
		Repos: map[string]config.Repo{
			"payments": {Path: "/srv/payments", TaskConfig: config.RawValues{"project": "PAY"}},
		},
	}
	if err := config.SaveMachine(path, cfg); err != nil {
		t.Fatalf("SaveMachine failed: %v", err)
	}
	// Owner-only permissions.
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0600 {
		t.Fatalf("config mode = %o, want 0600", fi.Mode().Perm())
	}

	loaded, err := config.LoadMachine(path)
	if err != nil {
		t.Fatalf("LoadMachine failed: %v", err)
	}
	if loaded.TaskPlugin != "jira" || loaded.Repos["payments"].Path != "/srv/payments" {
		t.Fatalf("round trip mismatch: %+v", loaded)
	}
	if loaded.Repos["payments"].TaskConfig["project"] != "PAY" {
		t.Fatalf("repo taskConfig lost: %+v", loaded.Repos["payments"].TaskConfig)
	}
}

func TestRepoEntryHoldsOnlyPathAndTaskConfig(t *testing.T) {
	// Repo entries hold only path and taskConfig; unknown keys are rejected
	// by strict decode (covered above). Compile-time shape check.
	var r config.Repo
	_ = r.Path
	_ = r.TaskConfig
}

func TestFixedFilesystemLayoutAndPermissions(t *testing.T) {
	// Fixed layout: every artifact lives under the relay-flow root with the
	// documented filename. Assert the fixed names without coupling to the
	// ambient user home.
	p, err := paths.ForUserHome()
	if err != nil {
		t.Fatal(err)
	}
	for name, got := range map[string]string{
		"Root":      p.Root,
		"Config":    p.Config,
		"Workflows": p.Workflows,
		"Database":  p.Database,
		"Socket":    p.Socket,
		"Lock":      p.Lock,
		"ServerLog": p.ServerLog,
		"PluginLog": p.PluginLog,
	} {
		if got == "" {
			t.Fatalf("%s is empty", name)
		}
	}
	// Fixed filenames under the root.
	if filepath.Base(p.Config) != "config.yaml" ||
		filepath.Base(p.Database) != "state.db" ||
		filepath.Base(p.Socket) != "server.sock" ||
		filepath.Base(p.Lock) != "server.lock" ||
		filepath.Base(p.ServerLog) != "server.log" ||
		filepath.Base(p.PluginLog) != "plugin.log" ||
		filepath.Base(p.Workflows) != "workflows" {
		t.Fatalf("non-fixed layout: %+v", p)
	}
	// Everything is under the root.
	for _, sub := range []string{p.Config, p.Database, p.Socket, p.Lock, p.ServerLog, p.PluginLog, p.Workflows} {
		if !strings.HasPrefix(sub, p.Root) {
			t.Fatalf("%q not under root %q", sub, p.Root)
		}
	}

	// Permissions on a controlled root: root 0700, logs 0600.
	tmp := filepath.Join(t.TempDir(), ".relay-flow")
	tp := p
	tp.Root = tmp
	tp.Config = filepath.Join(tmp, "config.yaml")
	tp.Workflows = filepath.Join(tmp, "workflows")
	tp.Database = filepath.Join(tmp, "state.db")
	tp.Socket = filepath.Join(tmp, "server.sock")
	tp.Lock = filepath.Join(tmp, "server.lock")
	tp.ServerLog = filepath.Join(tmp, "server.log")
	tp.PluginLog = filepath.Join(tmp, "plugin.log")
	if err := paths.Ensure(tp); err != nil {
		t.Fatal(err)
	}
	fi, err := os.Stat(tmp)
	if err != nil || fi.Mode().Perm() != 0700 {
		t.Fatalf("root mode = %v, want 0700", fi)
	}
	for _, lf := range []string{tp.ServerLog, tp.PluginLog} {
		fi, err := os.Stat(lf)
		if err != nil || fi.Mode().Perm() != 0600 {
			t.Fatalf("log %s mode = %v, want 0600", lf, fi)
		}
	}

	// Config (0600) and workflow files (0644) via the atomic writer.
	if err := config.SaveMachine(tp.Config, &config.Machine{TaskPlugin: "jira", RunnerPlugin: "orca", HarnessPlugin: "opencode"}); err != nil {
		t.Fatal(err)
	}
	fi, _ = os.Stat(tp.Config)
	if fi.Mode().Perm() != 0600 {
		t.Fatalf("config mode = %o, want 0600", fi.Mode().Perm())
	}
	wfPath := filepath.Join(tp.Workflows, "basicFlow.yaml")
	if err := config.WriteAtomic(wfPath, []byte("name: basicFlow\n"), 0644); err != nil {
		t.Fatal(err)
	}
	fi, _ = os.Stat(wfPath)
	if fi.Mode().Perm() != 0644 {
		t.Fatalf("workflow mode = %o, want 0644", fi.Mode().Perm())
	}

	// Database (state.db), socket (server.sock), and lock (server.lock) are
	// mode 0600 and are created by their owning components: state.db by the
	// engine (asserted in TestDatabaseFileIsOwnerOnly, internal/execution/
	// goworkflows), server.sock by the server (asserted in
	// TestSocketIsOwnerOnly, internal/server), and server.lock by the serve
	// startup flock (section 5.5). This test pins the fixed paths and the
	// config/log/workflow modes config owns.
	_ = tp.Database
	_ = tp.Lock
	_ = tp.Socket
}
