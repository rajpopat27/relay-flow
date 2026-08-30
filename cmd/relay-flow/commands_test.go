package main

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/charmbracelet/huh"
	"github.com/rajpopat27/relay-flow/internal/config"
	"github.com/rajpopat27/relay-flow/internal/repo"
	runsvc "github.com/rajpopat27/relay-flow/internal/run"
	"github.com/rajpopat27/relay-flow/internal/runner"
	"github.com/rajpopat27/relay-flow/internal/server"
	"github.com/rajpopat27/relay-flow/internal/task"
	"github.com/rajpopat27/relay-flow/internal/workflow"
)

// 3.36 command surface, 3.31 init, 3.22 report exit codes. Settled seam (e):
// a thin run(args, stdin) int CLI entry in cmd/relay-flow, driven in-process
// with a temp relay-flow root. Red until 4.15 implements the parser.

// cli invokes the parser entry with args and stdin against a temp home.
func cli(t *testing.T, home string, stdin string, args ...string) (code int) {
	t.Helper()
	root := filepath.Join(home, ".relay-flow")
	t.Setenv("RELAY_FLOW_HOME", root)
	return run(args, strings.NewReader(stdin))
}

func TestCommandSurfaceExists(t *testing.T) {
	home := t.TempDir()
	commands := [][]string{
		{"init"}, {"task", "auth"}, {"serve", "--recover"}, {"stop"}, {"report"},
		{"workflow", "submit", "--file", "x.yaml"}, {"workflow", "remove", "--name", "x"},
		{"workflow", "list"}, {"workflow", "get", "--name", "x"},
		{"repo", "register"}, {"repo", "remove", "--name", "x"}, {"repo", "list"}, {"repo", "get", "--name", "x"},
		{"run", "list"}, {"run", "get", "--ticket", "PAY-101"}, {"run", "cancel", "--ticket", "PAY-101"},
	}
	for _, argv := range commands {
		// Recognized commands do not exit 2 ("usage/unknown"); they may exit
		// 0 or 1 (server/validation) depending on environment.
		if code := cli(t, home, "", argv...); code == 2 {
			t.Fatalf("command %v not recognized (exit 2)", argv)
		}
	}
}

func TestUnknownFlagExits2(t *testing.T) {
	if code := cli(t, t.TempDir(), "", "workflow", "list", "--bogus"); code != 2 {
		t.Fatalf("unknown flag exit = %d, want 2", code)
	}
}

// Required-flag enforcement: commands that take an identifier exit 2 (usage)
// when it is missing, before any server contact.
func TestRequiredFlagMissingExits2(t *testing.T) {
	home := t.TempDir()
	for _, argv := range [][]string{
		{"workflow", "submit"}, // missing --file
		{"workflow", "remove"}, // missing --name
		{"workflow", "get"},    // missing --name
		{"repo", "remove"},     // missing --name
		{"repo", "get"},        // missing --name
		{"run", "get"},         // missing --ticket
		{"run", "cancel"},      // missing --ticket
	} {
		if code := cli(t, home, "", argv...); code != 2 {
			t.Fatalf("%v with missing required flag exit = %d, want 2", argv, code)
		}
	}
}

func TestRunListRowShowsLifecycleAndActiveRetry(t *testing.T) {
	next := time.Date(2026, 8, 25, 12, 0, 0, 0, time.UTC)
	r := runsvc.Run{
		ID: "payments/basicFlow/PAY-101", Workflow: "basicFlow", State: runsvc.StateStarting,
		Retry: &runsvc.RetryStatus{Attempt: 3, LastError: "jira unavailable", NextRetryAt: next},
	}
	got := formatRunListRow(r)
	for _, want := range []string{"starting", "retrying", "attempt=3", "next=2026-08-25T12:00:00Z", `error="jira unavailable"`} {
		if !strings.Contains(got, want) {
			t.Fatalf("run list row %q missing %q", got, want)
		}
	}
}

// initHome seeds a temp relay-flow home via the real init path so serve
// has a valid machine config and database (normal serve refuses to start
// without them per 5.5).
func initHome(t *testing.T, home string) {
	t.Helper()
	if code := cli(t, home, "", initArgs()...); code != 0 {
		t.Fatalf("init exit = %d, want 0", code)
	}
}

func initArgs() []string {
	return []string{"init", "--task-plugin", "jira", "--runner-plugin", "orca", "--harness-plugin", "opencode"}
}

func initInput() string {
	return "jira\norca\nopencode\n"
}

const authTestPlugin = "auth-dispatch-test"

var (
	registerAuthTestPlugin sync.Once
	authTestArgs           []string
	authTestInput          string
)

func ensureAuthTestPlugin() {
	registerAuthTestPlugin.Do(func() {
		task.Register(authTestPlugin, task.Factory{
			RequiredRepoKeys: func() []string { return nil },
			TaskScopeKey:     func(_, _ config.RawValues) (string, error) { return "auth-test", nil },
			Auth: func(_ context.Context, args []string, stdin io.Reader) error {
				authTestArgs = append([]string(nil), args...)
				body, err := io.ReadAll(stdin)
				if err != nil {
					return err
				}
				authTestInput = string(body)
				return nil
			},
			New: func(context.Context, task.RepoSpec) (task.System, error) { return nil, nil },
		})
	})
}

func TestTaskAuthDispatchesSelectedPlugin(t *testing.T) {
	ensureAuthTestPlugin()
	home := t.TempDir()
	root := filepath.Join(home, ".relay-flow")
	if err := os.MkdirAll(root, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := config.SaveMachine(filepath.Join(root, "config.yaml"), &config.Machine{TaskPlugin: authTestPlugin}); err != nil {
		t.Fatal(err)
	}
	authTestArgs = nil
	authTestInput = ""
	if code := cli(t, home, "plugin-owned-input", "task", "auth", "--custom", "value"); code != 0 {
		t.Fatalf("task auth exit = %d, want 0", code)
	}
	if got := strings.Join(authTestArgs, " "); got != "--custom value" {
		t.Fatalf("plugin args = %q", got)
	}
	if authTestInput != "plugin-owned-input" {
		t.Fatalf("plugin stdin = %q", authTestInput)
	}
}

// 3.29 (lock): serve creates server.lock owner-only. Asserted through the
// serve fixture.
func TestServerLockIsOwnerOnly(t *testing.T) {
	home := t.TempDir()
	initHome(t, home)
	// Start serve in the background of the test via the parser entry; it
	// creates the flock file then blocks. We assert the lock file's mode.
	go cli(t, home, "", "serve")
	lock := filepath.Join(home, ".relay-flow", "server.lock")
	var fi os.FileInfo
	var err error
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		fi, err = os.Stat(lock)
		if err == nil {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if err != nil {
		t.Fatalf("server.lock not created: %v", err)
	}
	if fi.Mode().Perm() != 0600 {
		t.Fatalf("server.lock mode = %o, want 0600", fi.Mode().Perm())
	}
}

// 3.29 (socket): the serve startup path owns server.sock creation and chmods
// it 0600. Asserted through the serve fixture (the component that binds the
// socket), not a test-created listener.
func TestServerSocketIsOwnerOnly(t *testing.T) {
	home := t.TempDir()
	initHome(t, home)
	go cli(t, home, "", "serve")
	sock := filepath.Join(home, ".relay-flow", "server.sock")
	var fi os.FileInfo
	var err error
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		fi, err = os.Stat(sock)
		if err == nil {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if err != nil {
		t.Fatalf("server.sock not created by serve: %v", err)
	}
	if fi.Mode().Perm() != 0600 {
		t.Fatalf("server.sock mode = %o, want 0600", fi.Mode().Perm())
	}
}

func TestReportReadsOneJSONObjectFromStdin(t *testing.T) {
	if code := cli(t, t.TempDir(), "{not json", "report"); code != 1 {
		t.Fatalf("malformed report JSON exit = %d, want 1", code)
	}
}

// 3.22: report ack semantics — exit 0 on any ack (incl. stale/duplicate),
// 1 on server/validation failure, never non-zero for a stale/duplicate.
// Drives the real CLI report against an in-process server (seam d) over the
// relay-flow Unix socket in the temp home.
func TestReportAckMatrix(t *testing.T) {
	valid := `{"runId":"payments/basicFlow/PAY-101","node":"coding","reportId":"s:m","report":{"status":"success","nextStep":"end","summary":{"completed":"x","commits":"abc123","notCompleted":"None","issuesDiscovered":"None","verification":"x","notes":"None"},"feedback":{"reasonForNextStep":"None","requiredActions":"None","relevantContext":"None","expectedResult":"None"}}}`

	// Any ack (accepted fresh, or accepted duplicate/stale) exits 0.
	for name, ack := range map[string]runsvc.ReportAck{
		"accepted fresh":     {Accepted: true, Duplicate: false},
		"accepted duplicate": {Accepted: true, Duplicate: true},
	} {
		home := t.TempDir()
		serveAck(t, home, ack, nil) // seam d server on the home socket
		if code := cli(t, home, valid, "report"); code != 0 {
			t.Fatalf("%s: exit = %d, want 0", name, code)
		}
	}

	// Server/validation failure exits 1.
	home := t.TempDir()
	serveAck(t, home, runsvc.ReportAck{}, errReportInvalid)
	if code := cli(t, home, valid, "report"); code != 1 {
		t.Fatalf("validation failure exit = %d, want 1", code)
	}
}

func TestReportUnreachableServerExits1(t *testing.T) {
	valid := `{"runId":"payments/basicFlow/PAY-101","node":"coding","reportId":"s:m","report":{"status":"success","nextStep":"end","summary":{"completed":"x","commits":"abc123","notCompleted":"None","issuesDiscovered":"None","verification":"x","notes":"None"},"feedback":{"reasonForNextStep":"None","requiredActions":"None","relevantContext":"None","expectedResult":"None"}}}`
	if code := cli(t, t.TempDir(), valid, "report"); code != 1 {
		t.Fatalf("unreachable server exit = %d, want 1", code)
	}
}

func TestInitRefusesToOverwrite(t *testing.T) {
	home := t.TempDir()
	// init reads the three plugin selections from stdin.
	if code := cli(t, home, initInput(), "init"); code != 0 {
		t.Fatalf("first init exit = %d, want 0", code)
	}
	cfgPath := filepath.Join(home, ".relay-flow", "config.yaml")
	dbPath := filepath.Join(home, ".relay-flow", "state.db")
	cfg := readFile(t, cfgPath)
	for _, want := range []string{
		"taskPlugin:", "runnerPlugin:", "harnessPlugin:",
		"keepTerminalsAlive: true", "keepSessionsAlive: true",
	} {
		if !strings.Contains(cfg, want) {
			t.Fatalf("config missing %q:\n%s", want, cfg)
		}
	}
	dbBefore := readFile(t, dbPath)
	// state.db is a REAL SQLite database, not an arbitrary non-empty file:
	// the first 16 bytes are the SQLite format-3 magic header.
	if !strings.HasPrefix(dbBefore, "SQLite format 3\x00") {
		t.Fatalf("state.db is not a SQLite database (missing magic header); got %q", dbBefore[:min(16, len(dbBefore))])
	}

	if code := cli(t, home, initInput(), "init"); code == 0 {
		t.Fatal("init overwrote existing config/history")
	}
	if readFile(t, cfgPath) != cfg {
		t.Fatal("init changed existing machine config")
	}
	if readFile(t, dbPath) != dbBefore {
		t.Fatal("init changed existing execution history")
	}
}

func TestInitSelectionTitlesAndSingletons(t *testing.T) {
	for _, tc := range []struct {
		title string
		value string
	}{
		{"Select task system", "jira"},
		{"Select runner", "orca"},
		{"Select harness", "opencode"},
	} {
		var selected string
		field, err := pluginSelectField(tc.title, []string{tc.value}, &selected)
		if err != nil {
			t.Fatal(err)
		}
		if field == nil {
			t.Fatalf("%s singleton field is nil", tc.title)
		}
		var singletonOut bytes.Buffer
		if err := field.RunAccessible(&singletonOut, strings.NewReader("1\n")); err != nil {
			t.Fatal(err)
		}
		if selected != tc.value || !strings.Contains(singletonOut.String(), tc.title) {
			t.Fatalf("%s singleton selection = %q, output %q", tc.title, selected, singletonOut.String())
		}

		field, err = pluginSelectField(tc.title, []string{tc.value, "other"}, &selected)
		if err != nil {
			t.Fatal(err)
		}
		var out bytes.Buffer
		if err := field.RunAccessible(&out, strings.NewReader("1\n")); err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(out.String(), tc.title) {
			t.Fatalf("selection output %q missing title %q", out.String(), tc.title)
		}
	}
}

func TestInitPrintsSelectedValuesAndCompletion(t *testing.T) {
	code, out := captureStdout(t, func() int {
		return cli(t, t.TempDir(), "", initArgs()...)
	})
	if code != 0 {
		t.Fatalf("init exit = %d", code)
	}
	for _, want := range []string{"Task system: jira", "Runner: orca", "Harness: opencode", "Relay-flow initialized"} {
		if !strings.Contains(out, want) {
			t.Fatalf("init output %q missing %q", out, want)
		}
	}
}

func TestInitDoesNotCollectOrWriteTaskCredentials(t *testing.T) {
	home := t.TempDir()
	if code := cli(t, home, "", initArgs()...); code != 0 {
		t.Fatalf("init exit = %d", code)
	}
	root := filepath.Join(home, ".relay-flow")
	configText := readFile(t, filepath.Join(root, "config.yaml"))
	if strings.Contains(configText, "jira-site") || strings.Contains(configText, "email") || strings.Contains(configText, "token") {
		t.Fatalf("normal config contains task credentials: %s", configText)
	}
	if _, err := os.Stat(filepath.Join(root, "credentials.yaml")); !os.IsNotExist(err) {
		t.Fatalf("init wrote credentials.yaml: %v", err)
	}
}

func TestInitForceRejectsRunningServerAndNonterminalRun(t *testing.T) {
	t.Run("server running", func(t *testing.T) {
		home := t.TempDir()
		initHome(t, home)
		lockPath := filepath.Join(home, ".relay-flow", "server.lock")
		lock, err := os.OpenFile(lockPath, os.O_CREATE|os.O_RDWR, 0600)
		if err != nil {
			t.Fatal(err)
		}
		defer lock.Close()
		if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
			t.Fatal(err)
		}
		defer syscall.Flock(int(lock.Fd()), syscall.LOCK_UN)
		if code := cli(t, home, "", "init", "--force",
			"--task-plugin", "jira", "--runner-plugin", "orca", "--harness-plugin", "opencode"); code != 1 {
			t.Fatalf("forced init with server lock exit = %d, want 1", code)
		}
	})

	t.Run("nonterminal run", func(t *testing.T) {
		home := t.TempDir()
		initHome(t, home)
		root := filepath.Join(home, ".relay-flow")
		insertRun(t, filepath.Join(root, "state.db"), "active", "starting")
		configBefore := readFile(t, filepath.Join(root, "config.yaml"))
		if code := cli(t, home, "", "init", "--force",
			"--task-plugin", "jira", "--runner-plugin", "orca", "--harness-plugin", "opencode"); code != 1 {
			t.Fatalf("forced init with active run exit = %d, want 1", code)
		}
		if got := readFile(t, filepath.Join(root, "config.yaml")); got != configBefore {
			t.Fatal("rejected forced init changed config")
		}
	})
}

func TestInitForcePreservesDurableAndUserState(t *testing.T) {
	home := t.TempDir()
	initHome(t, home)
	root := filepath.Join(home, ".relay-flow")
	dbPath := filepath.Join(root, "state.db")
	insertRun(t, dbPath, "completed", "completed")
	dbBefore, err := os.Stat(dbPath)
	if err != nil {
		t.Fatal(err)
	}

	cfg, err := config.LoadMachine(filepath.Join(root, "config.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	cfg.Repos = map[string]config.Repo{"existing": {Path: "/srv/existing"}}
	if err := config.SaveMachine(filepath.Join(root, "config.yaml"), cfg); err != nil {
		t.Fatal(err)
	}
	workflowDir := filepath.Join(root, "workflows")
	if err := os.MkdirAll(workflowDir, 0700); err != nil {
		t.Fatal(err)
	}
	workflowPath := filepath.Join(workflowDir, "saved.yaml")
	logPath := filepath.Join(root, "server.log")
	if err := os.WriteFile(workflowPath, []byte("saved workflow\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(logPath, []byte("saved log\n"), 0600); err != nil {
		t.Fatal(err)
	}

	args := append([]string{"init", "--force"}, initArgs()[1:]...)
	if code := cli(t, home, "", args...); code != 0 {
		t.Fatalf("forced init exit = %d, want 0", code)
	}
	if got := readFile(t, workflowPath); got != "saved workflow\n" {
		t.Fatalf("workflow changed: %q", got)
	}
	if got := readFile(t, logPath); got != "saved log\n" {
		t.Fatalf("log changed: %q", got)
	}
	dbAfter, err := os.Stat(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	if !os.SameFile(dbBefore, dbAfter) {
		t.Fatal("forced init recreated state.db")
	}
	cfg, err = config.LoadMachine(filepath.Join(root, "config.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := cfg.Repos["existing"]; !ok {
		t.Fatal("forced init removed existing repo config")
	}
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var state string
	if err := db.QueryRow(`SELECT state FROM relay_runs WHERE id = 'completed'`).Scan(&state); err != nil {
		t.Fatalf("completed history missing: %v", err)
	}
	if state != "completed" {
		t.Fatalf("completed history state = %q", state)
	}
}

// 8.2: --task-plugin/--runner-plugin/--harness-plugin run init without any
// prompt/stdin and write the same machine config as the stdin path.
func TestInitFlagsNonInteractive(t *testing.T) {
	home := t.TempDir()
	// Flags fully replace stdin: empty stdin must still succeed.
	if code := cli(t, home, "", initArgs()...); code != 0 {
		t.Fatalf("flagged init exit = %d, want 0", code)
	}
	cfgFlags := readFile(t, filepath.Join(home, ".relay-flow", "config.yaml"))

	home2 := t.TempDir()
	if code := cli(t, home2, initInput(), "init"); code != 0 {
		t.Fatalf("stdin init exit = %d, want 0", code)
	}
	cfgStdin := readFile(t, filepath.Join(home2, ".relay-flow", "config.yaml"))
	if cfgFlags != cfgStdin {
		t.Fatalf("flagged vs stdin config differ:\nflags:\n%s\nstdin:\n%s", cfgFlags, cfgStdin)
	}

	// Partial flags are a usage error and must not write config.
	home3 := t.TempDir()
	if code := cli(t, home3, "", "init", "--task-plugin", "jira"); code != 2 {
		t.Fatalf("partial flags exit = %d, want 2", code)
	}
	if _, err := os.Stat(filepath.Join(home3, ".relay-flow", "config.yaml")); !os.IsNotExist(err) {
		t.Fatal("partial-flag init wrote config")
	}
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(b)
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func captureStdout(t *testing.T, fn func() int) (int, string) {
	t.Helper()
	old := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stdout = w
	done := make(chan []byte, 1)
	go func() {
		b, _ := io.ReadAll(r)
		done <- b
	}()
	code := fn()
	_ = w.Close()
	os.Stdout = old
	out := <-done
	_ = r.Close()
	return code, string(out)
}

func insertRun(t *testing.T, path, id, state string) {
	t.Helper()
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	now := time.Now().UTC()
	var finished any
	if state == "completed" || state == "canceled" {
		finished = now
	}
	_, err = db.Exec(`INSERT INTO relay_runs
		(id, repo, workflow, ticket_id, ticket_key, state, started_at, updated_at, finished_at)
		VALUES (?, 'repo', 'workflow', ?, ?, ?, ?, ?, ?)`, id, id, id, state, now, now, finished)
	if err != nil {
		t.Fatal(err)
	}
}

// 8.4: fully-flagged repo register never prompts and produces the same
// repo entry the interactive run would post.
type registerServer struct {
	ackServer  // embed for the unreachable stubs
	fields     []string
	gotInput   *repo.RegisterInput
	gotInputs  []repo.RegisterInput
	successful []repo.RegisterInput
	failName   string
	calls      int
}

func (s *registerServer) TaskFields(context.Context) ([]string, error) {
	s.calls++
	return s.fields, nil
}
func (s *registerServer) RegisterRepo(_ context.Context, in repo.RegisterInput) (repo.Info, error) {
	s.calls++
	cp := in
	s.gotInput = &cp
	s.gotInputs = append(s.gotInputs, cp)
	if in.Name == s.failName {
		return repo.Info{}, errors.New("registration failed")
	}
	s.successful = append(s.successful, cp)
	return repo.Info{Name: in.Name, Path: in.Path, TaskConfig: in.TaskConfig}, nil
}

func serveRegister(t *testing.T, home string, fields []string) *registerServer {
	t.Helper()
	root := filepath.Join(home, ".relay-flow")
	if err := os.MkdirAll(root, 0700); err != nil {
		t.Fatal(err)
	}
	sock := filepath.Join(root, "server.sock")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	deps := &registerServer{fields: fields}
	srv := &http.Server{Handler: server.New(deps)}
	go srv.Serve(ln)
	t.Cleanup(func() {
		_ = srv.Shutdown(context.Background())
		_ = os.Remove(sock)
	})
	return deps
}

func TestRepoRegisterFlagsNonInteractive(t *testing.T) {
	home := t.TempDir()
	deps := serveRegister(t, home, []string{"project", "component"})

	// Fully-flagged run: no stdin, no TTY — must not prompt.
	code := cli(t, home, "", "repo", "register",
		"--name", "payments", "--path", "/srv/payments",
		"--set", "project=PAY")
	if code != 0 {
		t.Fatalf("fully-flagged register exit = %d, want 0", code)
	}
	if deps.gotInput == nil {
		t.Fatal("server never received registration")
	}
	if deps.gotInput.Name != "payments" || deps.gotInput.Path != "/srv/payments" {
		t.Fatalf("name/path = %q/%q", deps.gotInput.Name, deps.gotInput.Path)
	}
	if deps.gotInput.TaskConfig["project"] != "PAY" || deps.gotInput.TaskConfig["component"] != "payments" {
		t.Fatalf("taskConfig = %v", deps.gotInput.TaskConfig)
	}

	// Missing project is a usage error; component is derived from --name.
	// is allowed to hit the server (RegisterRepo must not be called).
	home2 := t.TempDir()
	deps2 := serveRegister(t, home2, []string{"project", "component"})
	if code := cli(t, home2, "", "repo", "register",
		"--name", "x", "--path", "/x"); code != 2 {
		t.Fatalf("missing required key exit = %d, want 2", code)
	}
	if deps2.gotInput != nil {
		t.Fatal("RegisterRepo called despite missing required key")
	}

	// Missing --name/--path must exit 2 with ZERO server contact.
	home3 := t.TempDir()
	deps3 := serveRegister(t, home3, []string{"project"})
	for _, argv := range [][]string{
		{"repo", "register", "--path", "/x", "--set", "project=PAY"},
		{"repo", "register", "--name", "x", "--set", "project=PAY"},
		{"repo", "register", "--set", "project=PAY"},
	} {
		before := deps3.calls
		if code := cli(t, home3, "", argv...); code != 2 {
			t.Fatalf("%v exit = %d, want 2", argv, code)
		}
		if deps3.calls != before {
			t.Fatalf("%v contacted server (%d calls)", argv, deps3.calls-before)
		}
	}

	// Invalid --set forms are usage errors at parse time: no server contact.
	home4 := t.TempDir()
	deps4 := serveRegister(t, home4, []string{"project"})
	for _, argv := range [][]string{
		{"repo", "register", "--name", "x", "--path", "/x", "--set", "noequals"},
		{"repo", "register", "--name", "x", "--path", "/x", "--set", "=v"},
		{"repo", "register", "--name", "x", "--path", "/x", "--set", "project="},
		{"repo", "register", "--name", "x", "--path", "/x", "--set", "project=A", "--set", "project=B"},
	} {
		before := deps4.calls
		if code := cli(t, home4, "", argv...); code != 2 {
			t.Fatalf("%v exit = %d, want 2", argv, code)
		}
		if deps4.calls != before {
			t.Fatalf("%v contacted server", argv)
		}
	}

	// Unknown --set keys are usage errors (flags map exactly to required keys).
	home5 := t.TempDir()
	serveRegister(t, home5, []string{"project"})
	if code := cli(t, home5, "", "repo", "register",
		"--name", "x", "--path", "/x",
		"--set", "project=PAY", "--set", "bogus=v"); code != 2 {
		t.Fatalf("unknown key exit = %d, want 2", code)
	}

	// Component can never be prompted or overridden.
	home6 := t.TempDir()
	deps6 := serveRegister(t, home6, []string{"project", "component"})
	if code := cli(t, home6, "", "repo", "register",
		"--name", "x", "--path", "/x",
		"--set", "project=PAY", "--set", "component=override"); code != 2 {
		t.Fatalf("component override exit = %d, want 2", code)
	}
	if deps6.gotInput != nil {
		t.Fatal("component override reached RegisterRepo")
	}
}

func TestRepoMultiSelectAndSharedJiraMapping(t *testing.T) {
	selected := []int{0, 1}
	field := repoMultiSelect([]huh.Option[int]{
		huh.NewOption("payments", 0),
		huh.NewOption("checkout", 1),
	}, &selected)
	var out bytes.Buffer
	if err := field.RunAccessible(&out, strings.NewReader("0\n")); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "Select repositories") {
		t.Fatalf("multi-select output %q missing title", out.String())
	}
	if fmt.Sprint(selected) != "[0 1]" {
		t.Fatalf("selected = %v, want [0 1]", selected)
	}
	if got := registrationSharedFields([]string{"project", "component"}); fmt.Sprint(got) != "[project]" {
		t.Fatalf("prompted fields = %v, want project only", got)
	}

	home := t.TempDir()
	deps := serveRegister(t, home, []string{"project", "component"})
	client := server.NewClient(filepath.Join(home, ".relay-flow", "server.sock"))
	candidates := []runner.RepoCandidate{
		{Name: "payments", Path: "/srv/payments"},
		{Name: "checkout", Path: "/srv/checkout"},
	}
	if err := registerSelectedRepos(context.Background(), client, candidates, selected,
		[]string{"project", "component"}, kvFlags{"project": "PAY"}); err != nil {
		t.Fatal(err)
	}
	if len(deps.successful) != 2 {
		t.Fatalf("registrations = %d, want 2", len(deps.successful))
	}
	for _, input := range deps.successful {
		if input.TaskConfig["project"] != "PAY" || input.TaskConfig["component"] != input.Name {
			t.Fatalf("registration %+v has task config %v", input, input.TaskConfig)
		}
	}
}

func TestRepoRegistrationPartialFailureKeepsPriorSuccess(t *testing.T) {
	home := t.TempDir()
	deps := serveRegister(t, home, []string{"project", "component"})
	deps.failName = "checkout"
	client := server.NewClient(filepath.Join(home, ".relay-flow", "server.sock"))
	candidates := []runner.RepoCandidate{
		{Name: "payments", Path: "/srv/payments"},
		{Name: "checkout", Path: "/srv/checkout"},
		{Name: "later", Path: "/srv/later"},
	}
	err := registerSelectedRepos(context.Background(), client, candidates, []int{0, 1, 2},
		[]string{"project", "component"}, kvFlags{"project": "PAY"})
	if err == nil || !strings.Contains(err.Error(), "checkout") {
		t.Fatalf("partial failure = %v, want failed repo name", err)
	}
	if len(deps.successful) != 1 || deps.successful[0].Name != "payments" {
		t.Fatalf("successful registrations = %+v, want payments preserved", deps.successful)
	}
	if len(deps.gotInputs) != 2 {
		t.Fatalf("registration attempts = %d, want stop after failed second repo", len(deps.gotInputs))
	}
}

func TestBackgroundServeReadinessAndStop(t *testing.T) {
	home := t.TempDir()
	root := filepath.Join(home, ".relay-flow")
	t.Setenv("RELAY_FLOW_HOME", root)
	if code := run(initArgs(), strings.NewReader("")); code != 0 {
		t.Fatalf("init exit = %d", code)
	}
	binary := buildCLIBinary(t)
	start := exec.Command(binary, "serve", "--background")
	start.Env = os.Environ()
	out, err := start.CombinedOutput()
	if err != nil {
		t.Fatalf("background serve: %v\n%s", err, out)
	}
	if !strings.Contains(string(out), "Relay-flow server started") {
		t.Fatalf("background output = %q", out)
	}
	client := server.NewClient(filepath.Join(root, "server.sock"))
	if _, err := client.ListRepos(context.Background()); err != nil {
		t.Fatalf("ready server did not respond: %v", err)
	}
	stop := exec.Command(binary, "stop")
	stop.Env = os.Environ()
	if out, err := stop.CombinedOutput(); err != nil {
		t.Fatalf("stop: %v\n%s", err, out)
	}
	waitForServerStop(t, client)
}

func TestBackgroundServeFailurePointsToLog(t *testing.T) {
	home := t.TempDir()
	root := filepath.Join(home, ".relay-flow")
	t.Setenv("RELAY_FLOW_HOME", root)
	if code := run(initArgs(), strings.NewReader("")); code != 0 {
		t.Fatalf("init exit = %d", code)
	}
	if err := os.WriteFile(filepath.Join(root, "config.yaml"), []byte("invalid: true\n"), 0600); err != nil {
		t.Fatal(err)
	}
	binary := buildCLIBinary(t)
	start := exec.Command(binary, "serve", "--background")
	start.Env = os.Environ()
	out, err := start.CombinedOutput()
	if err == nil {
		t.Fatalf("background serve unexpectedly succeeded: %s", out)
	}
	if !strings.Contains(string(out), "server.log") {
		t.Fatalf("background failure %q does not point to server.log", out)
	}
	log := readFile(t, filepath.Join(root, "server.log"))
	if !strings.Contains(log, "server startup failed") {
		t.Fatalf("server.log missing startup error: %q", log)
	}
}

func TestForegroundServeRemainsBlocking(t *testing.T) {
	home := t.TempDir()
	root := filepath.Join(home, ".relay-flow")
	t.Setenv("RELAY_FLOW_HOME", root)
	if code := run(initArgs(), strings.NewReader("")); code != 0 {
		t.Fatalf("init exit = %d", code)
	}
	binary := buildCLIBinary(t)
	serve := exec.Command(binary, "serve")
	serve.Env = os.Environ()
	if err := serve.Start(); err != nil {
		t.Fatal(err)
	}
	wait := make(chan error, 1)
	go func() { wait <- serve.Wait() }()
	t.Cleanup(func() {
		if serve.Process != nil {
			_ = serve.Process.Kill()
		}
	})
	client := server.NewClient(filepath.Join(root, "server.sock"))
	waitForServer(t, client)
	select {
	case err := <-wait:
		t.Fatalf("foreground serve returned before stop: %v", err)
	case <-time.After(200 * time.Millisecond):
	}
	stop := exec.Command(binary, "stop")
	stop.Env = os.Environ()
	if out, err := stop.CombinedOutput(); err != nil {
		t.Fatalf("stop: %v\n%s", err, out)
	}
	select {
	case err := <-wait:
		if err != nil {
			t.Fatalf("foreground serve exit: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("foreground serve did not exit after stop")
	}
}

func buildCLIBinary(t *testing.T) string {
	t.Helper()
	binary := filepath.Join(t.TempDir(), "relay-flow")
	cmd := exec.Command("go", "build", "-buildvcs=false", "-o", binary, ".")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("build relay-flow: %v\n%s", err, out)
	}
	return binary
}

func waitForServer(t *testing.T, client *server.Client) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
		_, err := client.ListRepos(ctx)
		cancel()
		if err == nil {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("server did not become ready")
}

func waitForServerStop(t *testing.T, client *server.Client) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
		_, err := client.ListRepos(ctx)
		cancel()
		if err != nil {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("server remained reachable after stop")
}

var errReportInvalid = errors.New("invalid report")

// ackServer is the minimal server.Deps implementation for the report path.
// Only SubmitReport is exercised; all other methods are unreachable stubs.
type ackServer struct {
	ack runsvc.ReportAck
	err error
}

func (s *ackServer) SubmitReport(context.Context, runsvc.ReportRequest) (runsvc.ReportAck, error) {
	return s.ack, s.err
}
func (s *ackServer) HasProcessedReport(context.Context, runsvc.ID, string) (bool, error) {
	return false, nil
}
func (s *ackServer) RegisterNodeSession(context.Context, runsvc.NodeRuntimeRegistration) (runsvc.NodeRuntimeRegistrationAck, error) {
	panic("unreachable")
}

// Unreachable Deps stubs — the report endpoint never calls them.
func (s *ackServer) SubmitWorkflow(context.Context, []byte) (*workflow.Workflow, error) {
	panic("unreachable")
}
func (s *ackServer) GetWorkflow(context.Context, string) (*workflow.Workflow, error) {
	panic("unreachable")
}
func (s *ackServer) ListWorkflows(context.Context) ([]*workflow.Workflow, error) {
	panic("unreachable")
}
func (s *ackServer) RemoveWorkflow(context.Context, string) error { panic("unreachable") }
func (s *ackServer) ListRuns(context.Context, runsvc.Filter) ([]runsvc.Run, error) {
	panic("unreachable")
}
func (s *ackServer) GetRunByTicket(context.Context, string) (runsvc.Run, error) {
	panic("unreachable")
}
func (s *ackServer) CancelRun(context.Context, string, string) error { panic("unreachable") }
func (s *ackServer) DiscoverRepos(context.Context) ([]runner.RepoCandidate, error) {
	panic("unreachable")
}
func (s *ackServer) TaskFields(context.Context) ([]string, error) { panic("unreachable") }
func (s *ackServer) RegisterRepo(context.Context, repo.RegisterInput) (repo.Info, error) {
	panic("unreachable")
}
func (s *ackServer) ListRepos(context.Context) ([]repo.Info, error) { panic("unreachable") }
func (s *ackServer) GetRepo(context.Context, string) (repo.Info, error) {
	panic("unreachable")
}
func (s *ackServer) RemoveRepo(context.Context, string) error { panic("unreachable") }
func (s *ackServer) Shutdown(context.Context) error           { panic("unreachable") }

// serveAck starts a thin server.New(deps) http.Handler on the relay-flow
// Unix socket inside home, returning the canned report ack/error (seam d).
func serveAck(t *testing.T, home string, ack runsvc.ReportAck, ackErr error) {
	t.Helper()
	root := filepath.Join(home, ".relay-flow")
	if err := os.MkdirAll(root, 0700); err != nil {
		t.Fatal(err)
	}
	sock := filepath.Join(root, "server.sock")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	h := server.New(&ackServer{ack: ack, err: ackErr})
	srv := &http.Server{Handler: h}
	go srv.Serve(ln)
	t.Cleanup(func() {
		_ = srv.Shutdown(context.Background())
		_ = os.Remove(sock)
	})
}
