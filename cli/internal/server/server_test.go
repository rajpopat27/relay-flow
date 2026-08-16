package server

import (
	"encoding/json"
	"fmt"
	"net"
	"net/http/httptest"
	"strings"
	"testing"

	"relay/internal/config"
	"relay/internal/runner"
	"relay/internal/tasks"
)

const goodYAML = `
name: testFlow
tasks:
  type: faketasks
  config: {}
runner:
  type: fakerunner
closeOn: [done]
nodes:
  coding:
    agent: build
    when: "In Progress"
    onSuccess: done
    onFailure: coding
  done:
    when: "Done"
`

// registerFakes installs test adapters (unique names per process via init
// in server package tests would collide across files — register once here).
var fakesOnce = registerFakes()

var (
	lastFakeTasks  *fakeTasks
	lastFakeRunner *fakeRunner
)

func registerFakes() bool {
	tasks.Register("faketasks", tasks.Factory{
		UnmarshalConfig: func(m map[string]any) (any, error) { return m, nil },
		New: func(cfg any, wfName string, nodes map[string]config.Node, assignee string) (tasks.Tasks, error) {
			lastFakeTasks = &fakeTasks{}
			return lastFakeTasks, nil
		},
	})
	runner.Register("fakerunner", runner.Factory{
		UnmarshalConfig: func(m map[string]any) (any, error) { return m, nil },
		New: func(cfg any) (runner.Runner, error) {
			lastFakeRunner = &fakeRunner{}
			return lastFakeRunner, nil
		},
	})
	return true
}

type fakeTasks struct{ reports []string }

func (f *fakeTasks) List() ([]tasks.Ticket, error) { return nil, nil }
func (f *fakeTasks) Claim(t tasks.Ticket) error    { return nil }
func (f *fakeTasks) Report(t tasks.Ticket, outcome, targetNode, summary string) error {
	f.reports = append(f.reports, fmt.Sprintf("%s:%s:%s:%s", t.Key, outcome, targetNode, summary))
	return nil
}

type fakeRunner struct{}

func (f *fakeRunner) Spawn(t tasks.Ticket, node, agent, prompt string, env map[string]string) error {
	return nil
}
func (f *fakeRunner) Find(t tasks.Ticket, node string) (runner.Session, bool, error) {
	return runner.Session{}, false, nil
}
func (f *fakeRunner) Nudge(s runner.Session, prompt string) error { return nil }
func (f *fakeRunner) Close(t tasks.Ticket) error                  { return nil }

func testServer(t *testing.T) *Server {
	t.Helper()
	_ = fakesOnce
	s := New(true, Deps{
		ResolveRepo:    func(path string) (string, string, error) { return "repo-1", "repo:xyz", nil },
		ValidateConfig: func(y []byte) ([]string, error) { return nil, nil },
	})
	return s
}

func post(t *testing.T, s *Server, path string, body string) (int, map[string]any) {
	t.Helper()
	req := httptest.NewRequest("POST", path, strings.NewReader(body))
	rec := httptest.NewRecorder()
	s.handler().ServeHTTP(rec, req)
	var out map[string]any
	json.NewDecoder(rec.Body).Decode(&out)
	return rec.Code, out
}

func TestSubmitStartsWorkflow(t *testing.T) {
	s := testServer(t)
	code, out := post(t, s, "/submit", `{"repoPath":"/x","yaml":`+jsonStr(goodYAML)+`}`)
	if code != 200 || out["ok"] != true {
		t.Fatalf("code=%d out=%v", code, out)
	}
	if s.entries["testFlow"] == nil {
		t.Fatal("entry not registered")
	}
	if lastFakeTasks == nil || lastFakeRunner == nil {
		t.Fatal("adapters not constructed")
	}
	// Duplicate name → 409.
	code, _ = post(t, s, "/submit", `{"repoPath":"/x","yaml":`+jsonStr(goodYAML)+`}`)
	if code != 409 {
		t.Errorf("dup submit code=%d, want 409", code)
	}
	s.Shutdown()
}

func TestSubmitRejectsBadYAML(t *testing.T) {
	s := testServer(t)
	defer s.Shutdown()
	code, _ := post(t, s, "/submit", `{"repoPath":"/x","yaml":"name: \"\""}`)
	if code != 400 {
		t.Errorf("code=%d", code)
	}
}

func TestSubmitRejectsInvalidStatuses(t *testing.T) {
	s := New(true, Deps{
		ResolveRepo:    func(path string) (string, string, error) { return "r", "n", nil },
		ValidateConfig: func(y []byte) ([]string, error) { return []string{"DO Done"}, nil },
	})
	defer s.Shutdown()
	code, out := post(t, s, "/submit", `{"repoPath":"/x","yaml":`+jsonStr(goodYAML)+`}`)
	if code != 400 || !strings.Contains(fmt.Sprint(out["error"]), "DO Done") {
		t.Errorf("code=%d out=%v", code, out)
	}
}

func TestReportSuccessTransitions(t *testing.T) {
	s := testServer(t)
	post(t, s, "/submit", `{"repoPath":"/x","yaml":`+jsonStr(goodYAML)+`}`)
	code, out := post(t, s, "/report",
		`{"workflow":"testFlow","ticket":"XYZ-1","node":"coding","outcome":"success","summary":"did it"}`)
	s.Shutdown()
	if code != 200 || out["action"] != "transitioned" {
		t.Fatalf("code=%d out=%v", code, out)
	}
	if len(lastFakeTasks.reports) != 1 || lastFakeTasks.reports[0] != "XYZ-1:success:done:did it" {
		t.Errorf("reports = %v", lastFakeTasks.reports)
	}
}

func TestReportSelfLoopActionCommented(t *testing.T) {
	s := testServer(t)
	post(t, s, "/submit", `{"repoPath":"/x","yaml":`+jsonStr(goodYAML)+`}`)
	code, out := post(t, s, "/report",
		`{"workflow":"testFlow","ticket":"XYZ-1","node":"coding","outcome":"failure","summary":"broke"}`)
	s.Shutdown()
	if code != 200 || out["action"] != "commented" {
		t.Fatalf("self-loop must be commented: code=%d out=%v", code, out)
	}
	if lastFakeTasks.reports[0] != "XYZ-1:failure:coding:broke" {
		t.Errorf("reports = %v", lastFakeTasks.reports)
	}
}

func TestReportValidation(t *testing.T) {
	s := testServer(t)
	post(t, s, "/submit", `{"repoPath":"/x","yaml":`+jsonStr(goodYAML)+`}`)
	defer s.Shutdown()
	cases := []struct{ name, body string }{
		{"bad outcome", `{"workflow":"testFlow","ticket":"XYZ-1","node":"coding","outcome":"done","summary":"x"}`},
		{"unknown node", `{"workflow":"testFlow","ticket":"XYZ-1","node":"nope","outcome":"success","summary":"x"}`},
		{"unknown workflow", `{"workflow":"nope","ticket":"XYZ-1","node":"coding","outcome":"success","summary":"x"}`},
		{"missing fields", `{"workflow":"testFlow"}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			code, _ := post(t, s, "/report", tc.body)
			if code != 400 && code != 404 {
				t.Errorf("code=%d", code)
			}
		})
	}
}

func jsonStr(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}

var _ = net.Dial // keep net import if unused later
