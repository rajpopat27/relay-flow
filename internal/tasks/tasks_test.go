package tasks

import (
	"strings"
	"testing"

	"github.com/rajpopat27/relay-flow/internal/config"
)

func testNodes() map[string]config.Node {
	return map[string]config.Node{
		"coding":    {Agent: "build", When: "In Progress", OnSuccess: "reviewing", OnFailure: "coding"},
		"reviewing": {Agent: "build", When: "In Review", OnSuccess: "done", OnFailure: "coding"},
		"done":      {When: "Done"},
	}
}

type fakeTasks struct {
	listed  []Ticket
	claims  []string
	reports []string
}

func (f *fakeTasks) List() ([]Ticket, error) { return f.listed, nil }
func (f *fakeTasks) Claim(t Ticket) error {
	f.claims = append(f.claims, t.Key)
	return nil
}
func (f *fakeTasks) Report(t Ticket, outcome, targetNode, summary string) error {
	f.reports = append(f.reports, t.Key+":"+outcome+":"+targetNode)
	return nil
}

func TestRegistryNew(t *testing.T) {
	fake := &fakeTasks{}
	Register("fake", Factory{
		UnmarshalConfig: func(m map[string]any) (any, error) {
			if m["bad"] != nil {
				return nil, errFakeConfig
			}
			return m, nil
		},
		New: func(cfg any, wfName string, nodes map[string]config.Node, assignee, repoName string) (Tasks, error) {
			if repoName != "repo:xyz" {
				t.Errorf("repoName = %q", repoName)
			}
			if wfName == "" {
				t.Error("wfName empty")
			}
			if nodes["coding"].When != "In Progress" {
				t.Error("nodes not passed")
			}
			return fake, nil
		},
	})

	tk, err := New("fake", map[string]any{"k": "v"}, "xyzFlow", testNodes(), "Jane Doe", "repo:xyz")
	if err != nil {
		t.Fatalf("%v", err)
	}
	tk.Claim(Ticket{Key: "XYZ-1"})
	if len(fake.claims) != 1 || fake.claims[0] != "XYZ-1" {
		t.Errorf("claims = %v", fake.claims)
	}

	if _, err := New("nonexistent-adapter", nil, "w", nil, "", ""); err == nil ||
		!strings.Contains(err.Error(), "unknown tasks type") || !strings.Contains(err.Error(), "fake") {
		t.Errorf("unknown adapter error = %v", err)
	}

	if _, err := New("fake", map[string]any{"bad": true}, "w", testNodes(), "", ""); err == nil || !strings.Contains(err.Error(), "bad config") {
		t.Errorf("config unmarshal err = %v", err)
	}
}

var errFakeConfig = errorString("bad config")

type errorString string

func (e errorString) Error() string { return string(e) }

func TestDuplicateRegisterPanics(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatal("expected panic on duplicate registration")
		}
	}()
	f := Factory{}
	Register("dup", f)
	Register("dup", f)
}
