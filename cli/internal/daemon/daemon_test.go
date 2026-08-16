package daemon

import (
	"strings"
	"testing"

	"relay/internal/config"
	"relay/internal/runner"
	"relay/internal/tasks"
)

func testConfig() *config.Config {
	return &config.Config{
		Name:                "wf",
		PollIntervalSeconds: 15,
		CloseOn:             config.StringList{"done"},
		Nodes: map[string]config.Node{
			"coding":    {Agent: "build", When: "In Progress", OnSuccess: "reviewing", OnFailure: "coding", NudgePrompt: "back to work on {{ticket}} at {{node}}"},
			"reviewing": {Agent: "build", When: "In Review", OnSuccess: "done", OnFailure: "coding", NudgePrompt: "review {{ticket}}"},
			"done":      {When: "Done"},
		},
	}
}

type fakeTasks struct {
	listed  []Ticket
	claims  []string
	reports []string
}

type Ticket = tasks.Ticket

func (f *fakeTasks) List() ([]tasks.Ticket, error) { return f.listed, nil }
func (f *fakeTasks) Claim(t tasks.Ticket) error {
	f.claims = append(f.claims, t.Key)
	return nil
}
func (f *fakeTasks) Report(t tasks.Ticket, outcome, targetNode, summary string) error {
	f.reports = append(f.reports, t.Key+":"+outcome+":"+targetNode)
	return nil
}

type fakeRunner struct {
	spawned []string
	nudged  []string
	closed  []string
	found   map[string]runner.Session
}

func (f *fakeRunner) Spawn(t tasks.Ticket, node, agent, prompt string, env map[string]string) error {
	f.spawned = append(f.spawned, t.Key+":"+node+":"+agent+":"+env["RELAY_WORKFLOW"]+":"+env["RELAY_TICKET"])
	return nil
}
func (f *fakeRunner) Find(t tasks.Ticket, node string) (runner.Session, bool, error) {
	s, ok := f.found[t.Key+":"+node]
	return s, ok, nil
}
func (f *fakeRunner) Nudge(s runner.Session, prompt string) error {
	f.nudged = append(f.nudged, s.Title+":"+prompt)
	return nil
}
func (f *fakeRunner) Close(t tasks.Ticket) error {
	f.closed = append(f.closed, t.Key)
	return nil
}

func newDaemon(ft *fakeTasks, fr *fakeRunner) *Daemon {
	return New(testConfig(), ft, fr, "repo-1", "repo:xyz", false)
}

func TestPollDispatchesUnclaimed(t *testing.T) {
	ft := &fakeTasks{listed: []Ticket{{Key: "XYZ-1", Node: "coding"}}}
	fr := &fakeRunner{}
	d := newDaemon(ft, fr)
	d.PollOnce()
	d.Wait()
	if len(ft.claims) != 1 || ft.claims[0] != "XYZ-1" {
		t.Errorf("claims = %v", ft.claims)
	}
	if len(fr.spawned) != 1 || fr.spawned[0] != "XYZ-1:coding:build:wf:XYZ-1" {
		t.Errorf("spawned = %v", fr.spawned)
	}
}

func TestPollSkipsForeignClaim(t *testing.T) {
	ft := &fakeTasks{listed: []Ticket{{Key: "XYZ-1", Node: "coding", ClaimedBy: "otherFlow"}}}
	fr := &fakeRunner{}
	d := newDaemon(ft, fr)
	d.PollOnce()
	d.Wait()
	if len(ft.claims) != 0 || len(fr.spawned) != 0 {
		t.Errorf("foreign ticket touched: claims=%v spawned=%v", ft.claims, fr.spawned)
	}
}

func TestPollSkipsUnmappedState(t *testing.T) {
	ft := &fakeTasks{listed: []Ticket{{Key: "XYZ-1", Node: ""}}}
	fr := &fakeRunner{}
	d := newDaemon(ft, fr)
	d.PollOnce()
	d.Wait()
	if len(fr.spawned) != 0 || len(fr.closed) != 0 {
		t.Errorf("unmapped ticket touched: spawned=%v closed=%v", fr.spawned, fr.closed)
	}
}

func TestPollHumanGateNode(t *testing.T) {
	cfg := testConfig()
	cfg.Nodes["gate"] = config.Node{When: "In Review"} // agentless, not in closeOn
	ft := &fakeTasks{listed: []Ticket{{Key: "XYZ-5", Node: "gate"}}}
	fr := &fakeRunner{}
	d := New(cfg, ft, fr, "repo-1", "repo:xyz", false)
	d.PollOnce()
	d.Wait()
	if len(fr.spawned) != 0 || len(fr.nudged) != 0 || len(fr.closed) != 0 {
		t.Errorf("gate node must not spawn/nudge/close: %+v", fr)
	}
	if len(ft.claims) != 1 {
		t.Errorf("gate node must claim: %v", ft.claims)
	}
}

func TestPollClosesTerminalNode(t *testing.T) {
	ft := &fakeTasks{listed: []Ticket{{Key: "XYZ-1", Node: "done", ClaimedBy: "wf"}}}
	fr := &fakeRunner{}
	d := newDaemon(ft, fr)
	d.PollOnce()
	d.Wait()
	if len(fr.closed) != 1 || fr.closed[0] != "XYZ-1" {
		t.Errorf("closed = %v", fr.closed)
	}
	if len(fr.spawned) != 0 {
		t.Errorf("terminal node must not spawn: %v", fr.spawned)
	}
}

func TestBounceNudgesExistingSession(t *testing.T) {
	ft := &fakeTasks{listed: []Ticket{{Key: "XYZ-1", Node: "coding", ClaimedBy: "wf"}}}
	fr := &fakeRunner{found: map[string]runner.Session{
		"XYZ-1:coding": {ID: "h1", Title: "XYZ-1:build:coding"},
	}}
	d := newDaemon(ft, fr)
	d.PollOnce()
	d.Wait()
	if len(fr.spawned) != 0 {
		t.Errorf("bounce must not spawn: %v", fr.spawned)
	}
	if len(fr.nudged) != 1 || fr.nudged[0] != "XYZ-1:build:coding:back to work on XYZ-1 at coding" {
		t.Errorf("nudged = %v", fr.nudged)
	}
}

func TestBounceWithoutSessionSpawnsFresh(t *testing.T) {
	ft := &fakeTasks{listed: []Ticket{{Key: "XYZ-1", Node: "coding", ClaimedBy: "wf"}}}
	fr := &fakeRunner{found: map[string]runner.Session{}}
	d := newDaemon(ft, fr)
	d.PollOnce()
	d.Wait()
	if len(fr.spawned) != 1 {
		t.Errorf("crash-without-terminal must respawn: spawned=%v", fr.spawned)
	}
	if len(ft.claims) != 0 {
		t.Errorf("already-claimed ticket must not re-claim: %v", ft.claims)
	}
}

func TestBounceNudgesOncePerNodeVisit(t *testing.T) {
	ft := &fakeTasks{listed: []Ticket{{Key: "XYZ-1", Node: "coding", ClaimedBy: "wf"}}}
	fr := &fakeRunner{found: map[string]runner.Session{
		"XYZ-1:coding": {ID: "h1", Title: "XYZ-1:build:coding"},
	}}
	d := newDaemon(ft, fr)
	d.PollOnce()
	d.Wait()
	d.PollOnce()
	d.Wait()
	if len(fr.nudged) != 1 {
		t.Errorf("same node visit must nudge once: %v", fr.nudged)
	}
	// Status change (report moved it) re-arms the marker.
	d.ClearNudged("XYZ-1")
	d.PollOnce()
	d.Wait()
	if len(fr.nudged) != 2 {
		t.Errorf("re-armed marker must allow another nudge: %v", fr.nudged)
	}
}

func TestSpawnPromptMentionsOutcomes(t *testing.T) {
	p := initialPrompt(testConfig(), "coding", tasks.Ticket{Key: "XYZ-1"})
	if !strings.Contains(p, "XYZ-1") || !strings.Contains(p, "success") || !strings.Contains(p, "failure") {
		t.Errorf("prompt = %q", p)
	}
	if strings.Contains(p, "\n") {
		t.Errorf("prompt must be flattened to one line")
	}
}

func TestNudgeTemplating(t *testing.T) {
	got := renderNudge(testConfig().Nodes["coding"].NudgePrompt, "XYZ-7", "coding")
	if got != "back to work on XYZ-7 at coding" {
		t.Errorf("%q", got)
	}
}
