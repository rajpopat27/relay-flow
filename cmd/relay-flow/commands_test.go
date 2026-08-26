package main

import (
	"context"
	"errors"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/rajpopat27/relay-flow/internal/repo"
	runsvc "github.com/rajpopat27/relay-flow/internal/run"
	"github.com/rajpopat27/relay-flow/internal/runner"
	"github.com/rajpopat27/relay-flow/internal/server"
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
		{"init"}, {"serve", "--recover"}, {"stop"}, {"report"},
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
	if code := cli(t, home, "jira\norca\nopencode\n", "init"); code != 0 {
		t.Fatalf("init exit = %d, want 0", code)
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
	valid := `{"runId":"payments/basicFlow/PAY-101","node":"coding","reportId":"s:m","report":{"status":"success","nextStep":"end","summary":{"completed":"x","notCompleted":"None","issuesDiscovered":"None","verification":"x","notes":"None"},"feedback":{"reasonForNextStep":"None","requiredActions":"None","relevantContext":"None","expectedResult":"None"}}}`

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
	valid := `{"runId":"payments/basicFlow/PAY-101","node":"coding","reportId":"s:m","report":{"status":"success","nextStep":"end","summary":{"completed":"x","notCompleted":"None","issuesDiscovered":"None","verification":"x","notes":"None"},"feedback":{"reasonForNextStep":"None","requiredActions":"None","relevantContext":"None","expectedResult":"None"}}}`
	if code := cli(t, t.TempDir(), valid, "report"); code != 1 {
		t.Fatalf("unreachable server exit = %d, want 1", code)
	}
}

func TestInitRefusesToOverwrite(t *testing.T) {
	home := t.TempDir()
	// init reads the three plugin selections from stdin.
	if code := cli(t, home, "jira\norca\nopencode\n", "init"); code != 0 {
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

	if code := cli(t, home, "jira\norca\nopencode\n", "init"); code == 0 {
		t.Fatal("init overwrote existing config/history")
	}
	if readFile(t, cfgPath) != cfg {
		t.Fatal("init changed existing machine config")
	}
	if readFile(t, dbPath) != dbBefore {
		t.Fatal("init changed existing execution history")
	}
}

// 8.2: --task-plugin/--runner-plugin/--harness-plugin run init without any
// prompt/stdin and write the same machine config as the stdin path.
func TestInitFlagsNonInteractive(t *testing.T) {
	home := t.TempDir()
	// Flags fully replace stdin: empty stdin must still succeed.
	if code := cli(t, home, "", "init",
		"--task-plugin", "jira", "--runner-plugin", "orca", "--harness-plugin", "opencode"); code != 0 {
		t.Fatalf("flagged init exit = %d, want 0", code)
	}
	cfgFlags := readFile(t, filepath.Join(home, ".relay-flow", "config.yaml"))

	home2 := t.TempDir()
	if code := cli(t, home2, "jira\norca\nopencode\n", "init"); code != 0 {
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

// 8.4: fully-flagged repo register never prompts and produces the same
// repo entry the interactive run would post.
type registerServer struct {
	ackServer // embed for the unreachable stubs
	fields    []string
	gotInput  *repo.RegisterInput
	calls     int
}

func (s *registerServer) TaskFields(context.Context) ([]string, error) {
	s.calls++
	return s.fields, nil
}
func (s *registerServer) RegisterRepo(_ context.Context, in repo.RegisterInput) (repo.Info, error) {
	s.calls++
	cp := in
	s.gotInput = &cp
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
		"--set", "project=PAY", "--set", "component=core")
	if code != 0 {
		t.Fatalf("fully-flagged register exit = %d, want 0", code)
	}
	if deps.gotInput == nil {
		t.Fatal("server never received registration")
	}
	if deps.gotInput.Name != "payments" || deps.gotInput.Path != "/srv/payments" {
		t.Fatalf("name/path = %q/%q", deps.gotInput.Name, deps.gotInput.Path)
	}
	if deps.gotInput.TaskConfig["project"] != "PAY" || deps.gotInput.TaskConfig["component"] != "core" {
		t.Fatalf("taskConfig = %v", deps.gotInput.TaskConfig)
	}

	// Missing a required key is a usage error; only the TaskFields lookup
	// is allowed to hit the server (RegisterRepo must not be called).
	home2 := t.TempDir()
	deps2 := serveRegister(t, home2, []string{"project", "component"})
	if code := cli(t, home2, "", "repo", "register",
		"--name", "x", "--path", "/x", "--set", "project=PAY"); code != 2 {
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
