package bdcli

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestRunJSONUsesRepoDirectoryAndConfiguredWorkspace(t *testing.T) {
	repoPath, beadsDir := newTestPaths(t)
	installStrictFakeBD(t, repoPath, beadsDir)

	var got []map[string]any
	cli := New(repoPath, beadsDir)
	if err := cli.runJSON(context.Background(), []string{"array", "--json"}, nil, &got); err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0]["id"] != "demo-1" {
		t.Fatalf("decoded array = %#v", got)
	}
}

func TestCLIUsesExactReadCommands(t *testing.T) {
	repoPath, beadsDir := newTestPaths(t)
	installStrictFakeBD(t, repoPath, beadsDir)
	cli := New(repoPath, beadsDir)
	ctx := context.Background()

	if err := cli.Probe(ctx); err != nil {
		t.Fatalf("Probe: %v", err)
	}
	if issues, err := cli.ListReady(ctx); err != nil || len(issues) != 1 || issues[0].ID != "demo-1" {
		t.Fatalf("ListReady = %#v, %v", issues, err)
	}
	if issues, err := cli.ListClaimed(ctx); err != nil || len(issues) != 1 || issues[0].ID != "demo-1" {
		t.Fatalf("ListClaimed = %#v, %v", issues, err)
	}
	if issues, err := cli.ListChildren(ctx, "demo-parent"); err != nil || len(issues) != 1 || issues[0].Parent != "demo-parent" {
		t.Fatalf("ListChildren = %#v, %v", issues, err)
	}
	if issue, err := cli.Show(ctx, "demo-parent"); err != nil || issue.ID != "demo-parent" {
		t.Fatalf("Show = %#v, %v", issue, err)
	}
	if comments, err := cli.ListComments(ctx, "demo-parent"); err != nil || len(comments) != 1 || comments[0].Text != "existing comment" {
		t.Fatalf("ListComments = %#v, %v", comments, err)
	}
}

func TestCLIUsesExactWriteCommandsAndStdin(t *testing.T) {
	repoPath, beadsDir := newTestPaths(t)
	installStrictFakeBD(t, repoPath, beadsDir)
	cli := New(repoPath, beadsDir)
	ctx := context.Background()

	if _, err := cli.CreateChild(ctx, "demo-parent", "demo-parent:implement", "mailbox description\nsecond line\n", "wf:implementation"); err != nil {
		t.Fatalf("CreateChild: %v", err)
	}
	description := "reconciled description\nwith two lines\n"
	if err := cli.Update(ctx, "demo-parent.1", UpdateInput{
		Description: &description,
		AddLabels:   []string{"wf:implementation"},
	}); err != nil {
		t.Fatalf("Update description/label: %v", err)
	}
	if err := cli.Update(ctx, "demo-parent", UpdateInput{AddLabels: []string{"wf:implementation"}}); err != nil {
		t.Fatalf("Update claim label: %v", err)
	}
	if err := cli.Update(ctx, "demo-parent.1", UpdateInput{Status: "in_progress"}); err != nil {
		t.Fatalf("Update status: %v", err)
	}
	if err := cli.AddComment(ctx, "demo-parent.1", "summary line\n\nsecond line\n"); err != nil {
		t.Fatalf("AddComment: %v", err)
	}
	if err := cli.Update(ctx, "demo-parent", UpdateInput{Status: "open", ClearDefer: true}); err != nil {
		t.Fatalf("Update recovery reset: %v", err)
	}
}

func TestCLIUsesCloseAndReopenUpdateCommands(t *testing.T) {
	repoPath, beadsDir := newTestPaths(t)
	installStrictFakeBD(t, repoPath, beadsDir)
	cli := New(repoPath, beadsDir)
	ctx := context.Background()

	if err := cli.Update(ctx, "demo-parent.1", UpdateInput{Status: "closed"}); err != nil {
		t.Fatalf("Close update: %v", err)
	}
	if err := cli.Update(ctx, "demo-parent.1", UpdateInput{Status: "open", ClearDefer: true}); err != nil {
		t.Fatalf("Reopen update: %v", err)
	}
}

func TestStrictFakeBDRejectsUnexpectedArgv(t *testing.T) {
	repoPath, beadsDir := newTestPaths(t)
	fake := installStrictFakeBD(t, repoPath, beadsDir)

	cmd := exec.Command(fake, "array", "--limit", "1", "--json")
	cmd.Dir = repoPath
	cmd.Env = []string{
		"PATH=" + os.Getenv("PATH"),
		"BD_EXPECT_CWD=" + repoPath,
		"BD_EXPECT_BEADS_DIR=" + beadsDir,
		"BEADS_DIR=" + beadsDir,
		"BD_FIXTURE_DIR=" + os.Getenv("BD_FIXTURE_DIR"),
	}
	if err := cmd.Run(); err == nil {
		t.Fatal("strict fake accepted an unsupported argv")
	}
}

func TestRunJSONDecodesObjectAndAcceptsEmptyArray(t *testing.T) {
	repoPath, beadsDir := newTestPaths(t)
	installStrictFakeBD(t, repoPath, beadsDir)
	cli := New(repoPath, beadsDir)

	var object map[string]any
	if err := cli.runJSON(context.Background(), []string{"object", "demo-1", "--json"}, nil, &object); err != nil {
		t.Fatal(err)
	}
	if object["id"] != "demo-1" || object["title"] != "Shown issue" {
		t.Fatalf("decoded object = %#v", object)
	}

	var empty []map[string]any
	if err := cli.runJSON(context.Background(), []string{"empty", "--json"}, nil, &empty); err != nil {
		t.Fatal(err)
	}
	if empty == nil || len(empty) != 0 {
		t.Fatalf("decoded empty array = %#v", empty)
	}
}

func TestRunJSONPreservesMultilineStdin(t *testing.T) {
	repoPath, beadsDir := newTestPaths(t)
	installStrictFakeBD(t, repoPath, beadsDir)

	var got map[string]any
	cli := New(repoPath, beadsDir)
	stdin := strings.NewReader("first line\nsecond line\n\nfinal line\n")
	if err := cli.runJSON(context.Background(), []string{"stdin", "--json"}, stdin, &got); err != nil {
		t.Fatal(err)
	}
	if got["stdin"] != "accepted" {
		t.Fatalf("decoded stdin response = %#v", got)
	}
}

func TestCommandEnvironmentIsolatesBeadsSelectors(t *testing.T) {
	t.Setenv("BEADS_DIR", "/ambient/workspace")
	t.Setenv("BEADS_DB", "/ambient/beads.db")
	t.Setenv("BD_DB", "/ambient/legacy.db")
	t.Setenv("UNRELATED_ENV", "preserve-me")

	env := commandEnvironment("/repo", "/configured/workspace")
	values := make(map[string][]string)
	for _, entry := range env {
		key, value, ok := strings.Cut(entry, "=")
		if !ok {
			continue
		}
		values[key] = append(values[key], value)
	}

	if got := values["BEADS_DIR"]; len(got) != 1 || got[0] != "/configured/workspace" {
		t.Fatalf("BEADS_DIR entries = %#v", got)
	}
	for _, key := range []string{"BEADS_DB", "BD_DB"} {
		if got := values[key]; len(got) != 0 {
			t.Fatalf("ambient %s leaked into command environment: %#v", key, got)
		}
	}
	if got := values["UNRELATED_ENV"]; len(got) != 1 || got[0] != "preserve-me" {
		t.Fatalf("unrelated environment = %#v", got)
	}
}

func TestParseJSONSkipsInformationalOutputBeforeValue(t *testing.T) {
	var got map[string]string
	if err := parseJSON([]byte("bd: using workspace\n{\"id\":\"demo-1\"}\n"), &got); err != nil {
		t.Fatal(err)
	}
	if got["id"] != "demo-1" {
		t.Fatalf("parsed value = %#v", got)
	}
}

func TestRunJSONRejectsMalformedOutput(t *testing.T) {
	repoPath, beadsDir := newTestPaths(t)
	installStrictFakeBD(t, repoPath, beadsDir)

	var got map[string]any
	err := New(repoPath, beadsDir).runJSON(context.Background(), []string{"malformed", "--json"}, nil, &got)
	if err == nil {
		t.Fatal("malformed JSON was accepted")
	}
	var commandErr *CommandError
	if errors.As(err, &commandErr) {
		t.Fatalf("malformed JSON reported as command failure: %#v", commandErr)
	}
}

func TestRunReportsExitCodeStdoutAndStderrSeparately(t *testing.T) {
	repoPath, beadsDir := newTestPaths(t)
	installStrictFakeBD(t, repoPath, beadsDir)

	_, err := New(repoPath, beadsDir).run(context.Background(), []string{"fail"}, nil)
	if err == nil {
		t.Fatal("ordinary non-zero bd exit was accepted")
	}
	var commandErr *CommandError
	if !errors.As(err, &commandErr) {
		t.Fatalf("error = %T %v, want CommandError", err, err)
	}
	if commandErr.ExitCode != 23 {
		t.Fatalf("exit code = %d, want 23", commandErr.ExitCode)
	}
	if !strings.Contains(commandErr.Stdout, "partial stdout") {
		t.Fatalf("stdout = %q", commandErr.Stdout)
	}
	if !strings.Contains(commandErr.Stderr, "failure detail") {
		t.Fatalf("stderr = %q", commandErr.Stderr)
	}
	if len(commandErr.Args) != 1 || commandErr.Args[0] != "fail" {
		t.Fatalf("command args = %#v", commandErr.Args)
	}
}

func TestRunJSONDoesNotTreatStderrAsJSONInput(t *testing.T) {
	repoPath, beadsDir := newTestPaths(t)
	installStrictFakeBD(t, repoPath, beadsDir)

	var got map[string]any
	if err := New(repoPath, beadsDir).runJSON(context.Background(), []string{"stderr", "--json"}, nil, &got); err != nil {
		t.Fatal(err)
	}
	if got["id"] != "demo-1" {
		t.Fatalf("decoded stderr response = %#v", got)
	}
}

func TestRunJSONToleratesInformationalStdout(t *testing.T) {
	repoPath, beadsDir := newTestPaths(t)
	installStrictFakeBD(t, repoPath, beadsDir)

	var got map[string]any
	if err := New(repoPath, beadsDir).runJSON(context.Background(), []string{"info", "--json"}, nil, &got); err != nil {
		t.Fatal(err)
	}
	if got["id"] != "demo-1" {
		t.Fatalf("decoded informational response = %#v", got)
	}
}

func newTestPaths(t *testing.T) (repoPath, beadsDir string) {
	t.Helper()
	repoPath = t.TempDir()
	beadsDir = filepath.Join(t.TempDir(), ".beads")
	if err := os.Mkdir(beadsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	return repoPath, beadsDir
}

func installStrictFakeBD(t *testing.T, repoPath, beadsDir string) string {
	t.Helper()
	binDir := t.TempDir()
	fake := filepath.Join(binDir, "bd")
	script, err := os.ReadFile(filepath.Join("testdata", "strict-bd.sh"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(fake, script, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("BD_EXPECT_CWD", repoPath)
	t.Setenv("BD_EXPECT_BEADS_DIR", beadsDir)
	fixtureDir, err := filepath.Abs("testdata")
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("BD_FIXTURE_DIR", fixtureDir)
	t.Setenv("BEADS_DIR", "/ambient/workspace")
	t.Setenv("BEADS_DB", "/ambient/beads.db")
	t.Setenv("BD_DB", "/ambient/legacy.db")
	return fake
}
