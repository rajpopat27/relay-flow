package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/rajpopat27/relay-flow/internal/config"
	"github.com/rajpopat27/relay-flow/internal/execution/goworkflows"
	"github.com/rajpopat27/relay-flow/internal/repo"
	runsvc "github.com/rajpopat27/relay-flow/internal/run"
	"github.com/rajpopat27/relay-flow/internal/server"
	"github.com/rajpopat27/relay-flow/internal/workflow"
)

// TestBeadsCompositionUsesRealRepoServiceAndDurableRun exercises the real
// composition path with the production Beads factory and bd subprocess seam.
// The fake bd intentionally blocks mailbox discovery until the test observes
// the durable run, making claim -> run creation -> mailbox creation ordering
// explicit rather than inferred from timing.
func TestBeadsCompositionUsesRealRepoServiceAndDurableRun(t *testing.T) {
	base := t.TempDir()
	relayHome := filepath.Join(base, "relay-flow")
	codePath := filepath.Join(base, "code", "payments")
	beadsDir := filepath.Join(base, "beads", "payments", ".beads")
	for _, dir := range []string{relayHome, codePath, beadsDir} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("RELAY_FLOW_HOME", relayHome)

	logPath, releasePath, stateDir := installCompositionFakeBD(t, codePath, beadsDir)

	p, err := home()
	if err != nil {
		t.Fatal(err)
	}
	if err := config.SaveMachine(p.Config, &config.Machine{
		PollIntervalSeconds:       1,
		CompletedRunRetentionDays: 1,
		TaskPlugin:                "beads",
		RunnerPlugin:              scenarioRunnerPlugin,
		HarnessPlugin:             scenarioHarnessPlugin,
		Repos:                     map[string]config.Repo{},
	}); err != nil {
		t.Fatal(err)
	}

	log := newScenarioLog()
	rnr := newScenarioRunner(log)
	hrn := newScenarioHarness(log)
	// The scenario runner and harness are the existing replaceable seams used
	// by the composition tests; only the task system is the real Beads adapter.
	setScenarioFactoryAdapters(nil, rnr, hrn)
	repoService := repo.NewService(repo.ServiceConfig{
		ConfigPath: p.Config,
		TaskPlugin: "beads",
		Runner:     rnr,
		Harness:    hrn,
		Active:     compositionNoActiveRuns{},
		Workflows:  compositionNoWorkflowRefs{},
	})
	if _, err := repoService.Register(context.Background(), repo.RegisterInput{
		Name:       "payments",
		Path:       codePath,
		TaskConfig: config.RawValues{"beadsDir": beadsDir},
	}); err != nil {
		t.Fatalf("register Beads repo: %v", err)
	}
	// Registration probes the real adapter once. Start the composition
	// assertions with a clean command trace and clean fake state.
	if err := os.WriteFile(logPath, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(stateDir); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(stateDir, 0o700); err != nil {
		t.Fatal(err)
	}

	if err := os.MkdirAll(p.Workflows, 0o700); err != nil {
		t.Fatal(err)
	}
	store := &workflow.Store{Dir: p.Workflows}
	if err := store.Put("beadsComposition", []byte(beadsCompositionWorkflowYAML)); err != nil {
		t.Fatal(err)
	}
	if err := goworkflows.InitDatabase(p.Database); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- serveRoot(ctx, p, false) }()

	released := false
	cleanup := func() {
		if !released {
			_ = os.WriteFile(releasePath, []byte("release\n"), 0o600)
			released = true
		}
		cancel()
		select {
		case <-done:
		case <-time.After(15 * time.Second):
		}
	}
	t.Cleanup(cleanup)

	client := server.NewClient(p.Socket)
	waitForServer(t, client)

	var active runsvc.Run
	waitScenario(t, 15*time.Second, func() bool {
		var getErr error
		active, getErr = client.GetRunByTicket(context.Background(), "demo-parent")
		return getErr == nil
	})
	if active.ID == "" {
		t.Fatal("durable run was not created after the Beads claim")
	}
	if err := appendCompositionLog(logPath, "test|run-observed\n"); err != nil {
		t.Fatal(err)
	}
	lines := readCompositionLog(t, logPath)
	readyIndex := compositionLogIndex(lines, "list --ready --no-parent --limit 0 --json")
	claimIndex := compositionLogIndex(lines, "update demo-parent --add-label wf:beadsComposition --json")
	runObservedIndex := compositionLogIndex(lines, "test|run-observed")
	if readyIndex < 0 || claimIndex < 0 || runObservedIndex < 0 {
		t.Fatalf("composition trace missing poll/claim/run events: %v", lines)
	}
	if !(readyIndex < claimIndex && claimIndex < runObservedIndex) {
		t.Fatalf("poll -> claim -> run ordering = ready:%d claim:%d run:%d; trace=%v", readyIndex, claimIndex, runObservedIndex, lines)
	}

	if err := os.WriteFile(releasePath, []byte("release\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	released = true
	waitScenario(t, 15*time.Second, func() bool {
		current, getErr := client.GetRunByTicket(context.Background(), "demo-parent")
		return getErr == nil && current.State == runsvc.StateWaiting && current.CurrentNode == "implement"
	})
	lines = readCompositionLog(t, logPath)
	createImplementIndex := compositionLogIndex(lines, "create demo-parent:implement")
	if createImplementIndex <= runObservedIndex {
		t.Fatalf("mailbox creation did not follow run creation: run:%d create:%d; trace=%v", runObservedIndex, createImplementIndex, lines)
	}

	current, err := client.GetRunByTicket(context.Background(), "demo-parent")
	if err != nil {
		t.Fatal(err)
	}
	if ack, err := client.SubmitReport(context.Background(), runsvc.ReportRequest{
		RunID:    current.ID,
		Node:     current.CurrentNode,
		ReportID: string(current.CurrentNodeVisitID) + ":composition-implement",
		Report:   scenarioReport(workflow.OutcomeSuccess, "verify"),
	}); err != nil || !ack.Accepted {
		t.Fatalf("submit implement report: ack=%+v err=%v", ack, err)
	}
	waitScenario(t, 15*time.Second, func() bool {
		current, getErr := client.GetRunByTicket(context.Background(), "demo-parent")
		return getErr == nil && current.State == runsvc.StateWaiting && current.CurrentNode == "verify"
	})

	current, err = client.GetRunByTicket(context.Background(), "demo-parent")
	if err != nil {
		t.Fatal(err)
	}
	if ack, err := client.SubmitReport(context.Background(), runsvc.ReportRequest{
		RunID:    current.ID,
		Node:     current.CurrentNode,
		ReportID: string(current.CurrentNodeVisitID) + ":composition-verify",
		Report:   scenarioReport(workflow.OutcomeSuccess, "end"),
	}); err != nil || !ack.Accepted {
		t.Fatalf("submit verify report: ack=%+v err=%v", ack, err)
	}
	waitScenario(t, 15*time.Second, func() bool {
		finished, getErr := client.GetRunByTicket(context.Background(), "demo-parent")
		return getErr == nil && finished.State == runsvc.StateCompleted
	})

	lines = readCompositionLog(t, logPath)
	if compositionLogIndex(lines, "create demo-parent:verify") < 0 {
		t.Fatalf("verify mailbox was not created: %v", lines)
	}
	comments := readCompositionState(t, stateDir, "comments.log")
	for _, want := range []string{
		"comment demo-parent.1",
		"comment demo-parent.2",
		"Summary for implement",
		"Feedback from implement to verify mailbox demo-parent.2",
		"Summary for verify",
	} {
		if !strings.Contains(comments, want) {
			t.Fatalf("comments missing %q: %q", want, comments)
		}
	}
	if strings.Contains(comments, "comment demo-parent ") {
		t.Fatalf("summary/feedback was written to the parent: %q", comments)
	}
	if log.count("runner-repo-validated") == 0 || log.count("harness-agent-validated:implementer") == 0 {
		t.Fatalf("existing runner/harness seams were not used: %v", log.all())
	}

	if err := client.Stop(context.Background()); err != nil {
		t.Fatal(err)
	}
	select {
	case serveErr := <-done:
		if serveErr != nil {
			t.Fatalf("serveRoot: %v", serveErr)
		}
	case <-time.After(15 * time.Second):
		t.Fatal("serveRoot did not stop")
	}
	released = true
}

func installCompositionFakeBD(t *testing.T, repoPath, beadsDir string) (logPath, releasePath, stateDir string) {
	t.Helper()
	binDir := t.TempDir()
	logPath = filepath.Join(t.TempDir(), "bd.log")
	releasePath = filepath.Join(t.TempDir(), "release")
	stateDir = filepath.Join(t.TempDir(), "state")
	if err := os.WriteFile(logPath, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(stateDir, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(binDir, "bd")
	if err := os.WriteFile(path, []byte(compositionFakeBDScript), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("BD_LOG", logPath)
	t.Setenv("BD_STATE", stateDir)
	t.Setenv("BD_GATE_RELEASE", releasePath)
	t.Setenv("BD_EXPECT_CWD", repoPath)
	t.Setenv("BD_EXPECT_BEADS_DIR", beadsDir)
	// These ambient selectors must be removed by bdcli.commandEnvironment.
	t.Setenv("BEADS_DIR", "/ambient/workspace")
	t.Setenv("BEADS_DB", "/ambient/beads.db")
	t.Setenv("BD_DB", "/ambient/legacy.db")
	return logPath, releasePath, stateDir
}

func appendCompositionLog(path, text string) error {
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = f.WriteString(text)
	return err
}

func readCompositionLog(t *testing.T, path string) []string {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return strings.Split(strings.TrimSpace(string(raw)), "\n")
}

func compositionLogIndex(lines []string, want string) int {
	for i, line := range lines {
		if strings.Contains(line, want) {
			return i
		}
	}
	return -1
}

func readCompositionState(t *testing.T, dir, name string) string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(dir, name))
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

type compositionNoActiveRuns struct{}

func (compositionNoActiveRuns) HasActiveRepo(context.Context, string) (bool, error) {
	return false, nil
}

type compositionNoWorkflowRefs struct{}

func (compositionNoWorkflowRefs) ReferencesRepo(string) bool { return false }

const beadsCompositionWorkflowYAML = `name: beadsComposition
repos: [payments]
cleanupRunnerOnEnd: true
nodes:
  start:
    onSuccess: [{target: implement}]
  implement:
    type: agent
    agent: implementer
    description: Implement the requested change.
    onSuccess: [{target: verify}]
    onFailure: [{target: implement, when: implementation cannot proceed}]
  verify:
    type: agent
    agent: verifier
    description: Verify the implementation.
    onSuccess: [{target: end}]
    onFailure: [{target: implement, when: verification fails}]
  end: {}
`

const compositionFakeBDScript = `#!/bin/sh
set -eu

fail() {
  printf 'unsupported or malformed composition bd invocation: %s\n' "$*" >&2
  exit 64
}

[ -n "${BD_LOG-}" ] || fail "BD_LOG is missing"
[ -n "${BD_STATE-}" ] || fail "BD_STATE is missing"
[ -n "${BD_GATE_RELEASE-}" ] || fail "BD_GATE_RELEASE is missing"
[ "$PWD" = "${BD_EXPECT_CWD-}" ] || fail "wrong cwd: $PWD"
[ "${BEADS_DIR-}" = "${BD_EXPECT_BEADS_DIR-}" ] || fail "wrong BEADS_DIR: ${BEADS_DIR-}"
[ "${BEADS_DB+x}" != x ] || fail "ambient BEADS_DB leaked"
[ "${BD_DB+x}" != x ] || fail "ambient BD_DB leaked"
printf 'bd|%s\n' "$*" >> "$BD_LOG"

status_file() {
  case "$1" in
    demo-parent) printf '%s/parent.status' "$BD_STATE" ;;
    demo-parent.1) printf '%s/implement.status' "$BD_STATE" ;;
    demo-parent.2) printf '%s/verify.status' "$BD_STATE" ;;
    *) fail "unknown issue $1" ;;
  esac
}

issue_status() {
  file=$(status_file "$1")
  if [ -f "$file" ]; then
    tr -d '\n' < "$file"
  else
    printf 'open'
  fi
}

issue_json() {
  issue=$1
  status=$(issue_status "$issue")
  case "$issue" in
    demo-parent)
      if [ -f "$BD_STATE/claimed" ]; then
        labels='["wf:beadsComposition"]'
      else
        labels='[]'
      fi
      printf '[{"id":"demo-parent","title":"Composition parent","status":"%s","issue_type":"epic","priority":1,"labels":%s}]\n' "$status" "$labels"
      ;;
    demo-parent.1)
      printf '[{"id":"demo-parent.1","title":"demo-parent:implement","status":"%s","issue_type":"task","priority":2,"labels":["wf:beadsComposition"],"parent":"demo-parent"}]\n' "$status"
      ;;
    demo-parent.2)
      printf '[{"id":"demo-parent.2","title":"demo-parent:verify","status":"%s","issue_type":"task","priority":2,"labels":["wf:beadsComposition"],"parent":"demo-parent"}]\n' "$status"
      ;;
    *) fail "unknown issue $issue" ;;
  esac
}

if [ "$#" -eq 6 ] && [ "$1" = list ] && [ "$2" = --ready ] && [ "$3" = --limit ] && [ "$4" = 1 ] && [ "$5" = --no-parent ] && [ "$6" = --json ]; then
  printf '[]\n'
  exit 0
fi
if [ "$#" -eq 6 ] && [ "$1" = list ] && [ "$2" = --ready ] && [ "$3" = --no-parent ] && [ "$4" = --limit ] && [ "$5" = 0 ] && [ "$6" = --json ]; then
  if [ -f "$BD_STATE/claimed" ]; then
    printf '[]\n'
  else
    printf '[{"id":"demo-parent","title":"Composition parent","status":"open","issue_type":"epic","priority":1,"labels":[]}]\n'
  fi
  exit 0
fi
if [ "$#" -eq 9 ] && [ "$1" = list ] && [ "$2" = --no-parent ] && [ "$3" = --status ] && [ "$4" = open,in_progress,blocked,deferred ] && [ "$5" = --label-pattern ] && [ "$6" = 'wf:*' ] && [ "$7" = --limit ] && [ "$8" = 0 ] && [ "$9" = --json ]; then
  printf '[]\n'
  exit 0
fi
if [ "$#" -eq 7 ] && [ "$1" = list ] && [ "$2" = --parent ] && [ "$3" = demo-parent ] && [ "$4" = --all ] && [ "$5" = --limit ] && [ "$6" = 0 ] && [ "$7" = --json ]; then
  while [ ! -f "$BD_GATE_RELEASE" ]; do
    sleep 0.01
  done
  out='['
  sep=
  if [ -f "$BD_STATE/implement" ]; then
    out="${out}${sep}{\"id\":\"demo-parent.1\",\"title\":\"demo-parent:implement\",\"status\":\"$(issue_status demo-parent.1)\",\"issue_type\":\"task\",\"priority\":2,\"labels\":[\"wf:beadsComposition\"],\"parent\":\"demo-parent\"}"
    sep=,
  fi
  if [ -f "$BD_STATE/verify" ]; then
    out="${out}${sep}{\"id\":\"demo-parent.2\",\"title\":\"demo-parent:verify\",\"status\":\"$(issue_status demo-parent.2)\",\"issue_type\":\"task\",\"priority\":2,\"labels\":[\"wf:beadsComposition\"],\"parent\":\"demo-parent\"}"
  fi
  printf '%s]\n' "$out"
  exit 0
fi
if [ "$#" -eq 3 ] && [ "$1" = show ] && [ "$3" = --json ]; then
  issue_json "$2"
  exit 0
fi
if [ "$#" -eq 11 ] && [ "$1" = create ] && [ "$3" = --type ] && [ "$4" = task ] && [ "$5" = --parent ] && [ "$7" = --no-inherit-labels ] && [ "$8" = --labels ] && [ "$9" = wf:beadsComposition ] && [ "${10}" = --stdin ] && [ "${11}" = --json ]; then
  case "$2" in
    demo-parent:implement) id=demo-parent.1; node=implement ;;
    demo-parent:verify) id=demo-parent.2; node=verify ;;
    *) fail "unknown child title $2" ;;
  esac
  cat > "$BD_STATE/$node.description"
  : > "$BD_STATE/$node"
  printf '%s\n' open > "$BD_STATE/$node.status"
  printf '{"id":"%s","title":"%s","status":"open","issue_type":"task","priority":2,"labels":["wf:beadsComposition"],"parent":"demo-parent"}\n' "$id" "$2"
  exit 0
fi
if [ "$#" -eq 5 ] && [ "$1" = update ] && [ "$2" = demo-parent ] && [ "$3" = --add-label ] && [ "$4" = wf:beadsComposition ] && [ "$5" = --json ]; then
  : > "$BD_STATE/claimed"
  printf '{}\n'
  exit 0
fi
if [ "$#" -eq 5 ] && [ "$1" = update ] && [ "$3" = --status ] && [ "$5" = --json ]; then
  file=$(status_file "$2")
  printf '%s\n' "$4" > "$file"
  printf '{}\n'
  exit 0
fi
if [ "$#" -eq 3 ] && [ "$1" = comments ] && [ "$3" = --json ]; then
  printf '[]\n'
  exit 0
fi
if [ "$#" -eq 4 ] && [ "$1" = comment ] && [ "$3" = --stdin ] && [ "$4" = --json ]; then
  printf 'comment %s\n' "$2" >> "$BD_STATE/comments.log"
  cat >> "$BD_STATE/comments.log"
  printf '\n---\n' >> "$BD_STATE/comments.log"
  printf '{}\n'
  exit 0
fi
fail "$@"
`
