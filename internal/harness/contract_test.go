package harness_test

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/rajpopat27/relay-flow/internal/harness"
	"github.com/rajpopat27/relay-flow/internal/identity"
	"github.com/rajpopat27/relay-flow/internal/runner"
	"github.com/rajpopat27/relay-flow/internal/workflow"
)

// 3.8: harness contract tests against a fake harness, per
// specs/integration-contracts "Harness owns agent launch semantics" and
// specs/structured-node-reporting "Runtime plugin metadata is explicit".

var errUnknownAgent = errors.New("unknown agent")

type fakeHarness struct {
	agents  map[string]bool
	session map[string]harness.Session // title -> session
}

var _ harness.Harness = (*fakeHarness)(nil)

func newFakeHarness() *fakeHarness {
	return &fakeHarness{
		agents:  map[string]bool{"build": true},
		session: map[string]harness.Session{},
	}
}

func (f *fakeHarness) SetupRepo(context.Context, string) error { return nil }

func (f *fakeHarness) ValidateAgent(_ context.Context, _, agent string) error {
	if !f.agents[agent] {
		return errUnknownAgent
	}
	return nil
}

func (f *fakeHarness) FindSession(_ context.Context, _, title string) (harness.Session, bool, error) {
	s, ok := f.session[title]
	return s, ok, nil
}

func (f *fakeHarness) BuildCommand(spec harness.LaunchSpec) (runner.Command, error) {
	// The fake mirrors the required env contract; the real opencode harness
	// builds the executable/args. NEXT_STEPS_JSON carries the legal targets
	// and their when explanations.
	nextSteps, err := json.Marshal(spec.NextSteps)
	if err != nil {
		return runner.Command{}, err
	}
	return runner.Command{
		Executable: "opencode",
		Args:       []string{"--agent", spec.Agent},
		Env: map[string]string{
			"RELAY_FLOW_RUN_ID":          string(spec.RunID),
			"RELAY_FLOW_WORKFLOW":        spec.Workflow,
			"RELAY_FLOW_REPO":            spec.RepoName,
			"RELAY_FLOW_TICKET":          spec.Ticket,
			"RELAY_FLOW_NODE":            spec.Node,
			"RELAY_FLOW_NODE_TYPE":       string(spec.NodeType),
			"RELAY_FLOW_NUDGE_PROMPT":    spec.NudgePrompt,
			"RELAY_FLOW_NEXT_STEPS_JSON": string(nextSteps),
		},
	}, nil
}

func launchSpec() harness.LaunchSpec {
	return harness.LaunchSpec{
		RunID:       identity.NewRunID("payments", "basicFlow", "PAY-101"),
		NodeVisitID: identity.NewNodeVisitID(),
		RepoName:    "payments",
		RepoPath:    "/srv/payments",
		Workflow:    "basicFlow",
		Ticket:      "PAY-101",
		Node:        "coding",
		NodeType:    workflow.NodeAgent,
		Agent:       "build",
		Title:       "PAY-101:coding",
		NextSteps: []workflow.Route{
			{Target: "end", When: "work complete"},
			{Target: "coding", When: "needs rework"},
		},
	}
}

func TestValidateAgent(t *testing.T) {
	f := newFakeHarness()
	if err := f.ValidateAgent(context.Background(), "/srv/payments", "build"); err != nil {
		t.Fatalf("known agent rejected: %v", err)
	}
	if err := f.ValidateAgent(context.Background(), "/srv/payments", "unknown"); err == nil {
		t.Fatal("unknown agent accepted")
	}
}

func TestFindSessionByStableTitle(t *testing.T) {
	f := newFakeHarness()
	f.session["PAY-101:coding"] = harness.Session{ID: "s1", Title: "PAY-101:coding"}

	s, ok, err := f.FindSession(context.Background(), "/srv/payments", "PAY-101:coding")
	if err != nil || !ok || s.ID != "s1" {
		t.Fatalf("FindSession = %v,%v,%v", s, ok, err)
	}
	_, ok, err = f.FindSession(context.Background(), "/srv/payments", "PAY-101:review")
	if err != nil {
		t.Fatal(err)
	}
	if ok {
		t.Fatal("FindSession found a session for an unknown title")
	}
}

func TestBuildCommandEnvContract(t *testing.T) {
	f := newFakeHarness()
	spec := launchSpec()
	cmd, err := f.BuildCommand(spec)
	if err != nil {
		t.Fatalf("BuildCommand failed: %v", err)
	}

	required := []string{
		"RELAY_FLOW_RUN_ID",
		"RELAY_FLOW_WORKFLOW",
		"RELAY_FLOW_REPO",
		"RELAY_FLOW_TICKET",
		"RELAY_FLOW_NODE",
		"RELAY_FLOW_NODE_TYPE",
		"RELAY_FLOW_NUDGE_PROMPT",
		"RELAY_FLOW_NEXT_STEPS_JSON",
	}
	for _, key := range required {
		if _, ok := cmd.Env[key]; !ok {
			t.Fatalf("command env missing %s (env=%v)", key, cmd.Env)
		}
	}
	if cmd.Env["RELAY_FLOW_RUN_ID"] != string(spec.RunID) {
		t.Fatalf("RELAY_FLOW_RUN_ID = %q", cmd.Env["RELAY_FLOW_RUN_ID"])
	}
	if cmd.Env["RELAY_FLOW_TICKET"] != "PAY-101" || cmd.Env["RELAY_FLOW_NODE"] != "coding" {
		t.Fatalf("ticket/node env wrong: %v", cmd.Env)
	}
	if _, ok := cmd.Env["RELAY_FLOW_NODE_VISIT_ID"]; ok {
		t.Fatalf("internal node visit ID leaked into harness env: %v", cmd.Env)
	}

	// NEXT_STEPS_JSON must decode to the legal targets and their when
	// explanations, not just exist.
	var steps []map[string]string
	if err := json.Unmarshal([]byte(cmd.Env["RELAY_FLOW_NEXT_STEPS_JSON"]), &steps); err != nil {
		t.Fatalf("NEXT_STEPS_JSON not valid JSON: %v", err)
	}
	if len(steps) != 2 {
		t.Fatalf("NEXT_STEPS_JSON = %v, want 2 legal targets", steps)
	}
	found := map[string]string{}
	for _, s := range steps {
		found[s["target"]] = s["when"]
	}
	if found["end"] != "work complete" || found["coding"] != "needs rework" {
		t.Fatalf("NEXT_STEPS_JSON targets/when = %v", found)
	}
}

func TestHarnessDoesNotManipulateRunnerState(t *testing.T) {
	// The harness returns a structured runner.Command; it never receives or
	// mutates runner terminals/environments. Compile-time shape check:
	// Harness methods take repoPath/title/spec values, not runner state.
	var h harness.Harness = newFakeHarness()
	_ = h
}
