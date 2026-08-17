package orca

import (
	"testing"
	"time"

	"github.com/rajpopat27/relayflow/cli/internal/orcacli"
	"github.com/rajpopat27/relayflow/cli/internal/runner"
	"github.com/rajpopat27/relayflow/cli/internal/tasks"
)

type fakeOrca struct {
	listErr      error
	worktrees    []orcacli.Worktree
	terminals    map[string][]orcacli.Terminal // worktree name → terminals
	created      []string
	sent         []string
	closed       []string
	waitErr      error
	createErr    error
	findBranchFn func(repoPath, key string) (string, bool, error)
	agentExists  bool
}

func (f *fakeOrca) WorktreeList() ([]orcacli.Worktree, error) { return f.worktrees, nil }
func (f *fakeOrca) WorktreeCreate(ticketKey, repoID, parentWorktreeID, baseBranch string) error {
	f.created = append(f.created, ticketKey)
	f.worktrees = append(f.worktrees, orcacli.Worktree{ID: "wt-" + ticketKey, DisplayName: ticketKey, RepoID: repoID, Branch: baseBranch})
	return nil
}
func (f *fakeOrca) FindWorktree(repoID, displayName string) (orcacli.Worktree, bool, error) {
	for _, w := range f.worktrees {
		if w.RepoID == repoID && w.DisplayName == displayName {
			return w, true, nil
		}
	}
	return orcacli.Worktree{}, false, nil
}
func (f *fakeOrca) MainWorktree(repoID string) (orcacli.Worktree, bool, error) {
	for _, w := range f.worktrees {
		if w.RepoID == repoID && w.IsMainWorktree {
			return w, true, nil
		}
	}
	return orcacli.Worktree{}, false, nil
}
func (f *fakeOrca) TerminalList(worktree string) ([]orcacli.Terminal, error) {
	if f.listErr != nil {
		return nil, f.listErr
	}
	return f.terminals[worktree], nil
}
func (f *fakeOrca) TerminalCreate(ticketKey, title, command string) (string, error) {
	if f.createErr != nil {
		return "", f.createErr
	}
	h := "h-" + title
	f.terminals["name:"+ticketKey] = append(f.terminals["name:"+ticketKey], orcacli.Terminal{Handle: h, Title: title, Connected: true})
	return h, nil
}
func (f *fakeOrca) TerminalWait(handle, forState string, timeoutMs int) error { return f.waitErr }
func (f *fakeOrca) TerminalClose(handle string) error {
	f.closed = append(f.closed, handle)
	return nil
}
func (f *fakeOrca) TerminalSend(handle, text string) error {
	f.sent = append(f.sent, handle+":"+text)
	return nil
}

func newTestRunner(f *fakeOrca) *orcaRunner {
	return &orcaRunner{
		repoID: "repo-1",
		orca:   f,
		exists: func(string) (bool, error) { return f.agentExists, nil },
		sleep:  func(time.Duration) {},
		findBranch: func(repoPath, key string) (string, bool, error) {
			if f.findBranchFn != nil {
				return f.findBranchFn(repoPath, key)
			}
			return "", false, nil
		},
	}
}

func TestUnmarshalConfig(t *testing.T) {
	if _, err := unmarshalConfig(map[string]any{}); err != nil {
		t.Fatalf("empty config must be valid: %v", err)
	}
	if _, err := unmarshalConfig(nil); err != nil {
		t.Fatalf("nil config must be valid: %v", err)
	}
	if _, err := unmarshalConfig(map[string]any{"bogus": 1}); err == nil {
		t.Fatal("unknown field must be rejected")
	}
}

func TestSpawnCreatesWorktreeAndTerminal(t *testing.T) {
	f := &fakeOrca{
		worktrees:   []orcacli.Worktree{{ID: "main", DisplayName: "main", RepoID: "repo-1", IsMainWorktree: true, Branch: "main", Path: "/tmp"}},
		terminals:   map[string][]orcacli.Terminal{},
		agentExists: true,
	}
	r := newTestRunner(f)
	tk := tasks.Ticket{Key: "XYZ-1"}
	err := r.Spawn(tk, "coding", "build", "do the work", map[string]string{
		"RELAYFLOW_WORKFLOW": "wf", "RELAYFLOW_TICKET": "XYZ-1", "RELAYFLOW_NODE": "coding", "RELAYFLOW_AGENT": "build",
	})
	if err != nil {
		t.Fatalf("%v", err)
	}
	if len(f.created) != 1 || f.created[0] != "XYZ-1" {
		t.Errorf("worktree created = %v", f.created)
	}
	terms := f.terminals["name:XYZ-1"]
	if len(terms) != 1 || terms[0].Title != "XYZ-1:build:coding" {
		t.Errorf("terminals = %+v", terms)
	}
}

func TestSpawnUnknownAgent(t *testing.T) {
	f := &fakeOrca{terminals: map[string][]orcacli.Terminal{}, agentExists: false}
	r := newTestRunner(f)
	if err := r.Spawn(tasks.Ticket{Key: "XYZ-1"}, "coding", "ghost", "p", nil); err == nil {
		t.Fatal("unknown agent must error")
	}
	if len(f.created) != 0 {
		t.Errorf("no worktree should be created for unknown agent: %v", f.created)
	}
}

func TestFindExactTitle(t *testing.T) {
	f := &fakeOrca{terminals: map[string][]orcacli.Terminal{
		"name:XYZ-1": {
			{Handle: "h1", Title: "XYZ-1:build:coding"},
			{Handle: "h2", Title: "XYZ-1:build:reviewing"},
			{Handle: "h3", Title: "Terminal 1"},
		},
	}}
	r := newTestRunner(f)
	s, ok, err := r.Find(tasks.Ticket{Key: "XYZ-1"}, "coding")
	if err != nil || !ok || s.ID != "h1" {
		t.Errorf("Find coding = %+v ok=%v err=%v", s, ok, err)
	}
	if _, ok, _ := r.Find(tasks.Ticket{Key: "XYZ-1"}, "nowhere"); ok {
		t.Error("Find nowhere must miss")
	}
	if _, ok, _ := r.Find(tasks.Ticket{Key: "XYZ-9"}, "coding"); ok {
		t.Error("Find unknown ticket must miss")
	}
}

func TestFindMissingWorktreeIsNotFound(t *testing.T) {
	// selector_not_found (worktree gone after crash) must be "no session",
	// not an error — otherwise bounce never reaches the respawn branch.
	f := &fakeOrca{listErr: errSelectorNotFound}
	r := newTestRunner(f)
	if _, ok, err := r.Find(tasks.Ticket{Key: "XYZ-9"}, "coding"); err != nil || ok {
		t.Errorf("Find = ok=%v err=%v, want no-session", ok, err)
	}
	// Real errors still propagate.
	f.listErr = errBoom
	if _, _, err := r.Find(tasks.Ticket{Key: "XYZ-9"}, "coding"); err == nil {
		t.Error("real list error must propagate")
	}
}

var errSelectorNotFound = errorString("exit status 1: selector_not_found")
var errBoom = errorString("boom")

type errorString string

func (e errorString) Error() string { return string(e) }

func TestNudgeSendsPrompt(t *testing.T) {
	f := &fakeOrca{}
	r := newTestRunner(f)
	if err := r.Nudge(runner.Session{ID: "h1", Title: "XYZ-1:build:coding"}, "continue please"); err != nil {
		t.Fatalf("%v", err)
	}
	if len(f.sent) != 1 || f.sent[0] != "h1:continue please" {
		t.Errorf("sent = %v", f.sent)
	}
}

func TestCloseClosesTicketTerminals(t *testing.T) {
	f := &fakeOrca{terminals: map[string][]orcacli.Terminal{
		"name:XYZ-1": {
			{Handle: "h1", Title: "XYZ-1:build:coding"},
			{Handle: "h2", Title: "XYZ-1:build:reviewing"},
			{Handle: "h3", Title: "Setup"}, // not ours — survives
		},
	}}
	r := newTestRunner(f)
	if err := r.Close(tasks.Ticket{Key: "XYZ-1"}); err != nil {
		t.Fatalf("%v", err)
	}
	if len(f.closed) != 2 {
		t.Errorf("closed = %v, want h1+h2 only", f.closed)
	}
}
