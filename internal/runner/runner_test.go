package runner

import (
	"strings"
	"testing"

	"github.com/rajpopat27/relay-flow/internal/tasks"
)

type fakeRunner struct {
	spawned []string
	nudged  []string
	closed  []string
	found   map[string]Session
}

func (f *fakeRunner) Spawn(t tasks.Ticket, node, agent, prompt string, env map[string]string) error {
	f.spawned = append(f.spawned, t.Key+":"+node+":"+agent+":"+env["RELAY_WORKFLOW"])
	return nil
}
func (f *fakeRunner) Find(t tasks.Ticket, node string) (Session, bool, error) {
	s, ok := f.found[t.Key+":"+node]
	return s, ok, nil
}
func (f *fakeRunner) Nudge(s Session, prompt string) error {
	f.nudged = append(f.nudged, s.Title+":"+prompt)
	return nil
}
func (f *fakeRunner) Close(t tasks.Ticket) error {
	f.closed = append(f.closed, t.Key)
	return nil
}

func TestRegistryNew(t *testing.T) {
	fake := &fakeRunner{}
	Register("fakerun", Factory{
		UnmarshalConfig: func(m map[string]any) (any, error) { return m, nil },
		New: func(cfg any) (Runner, error) { return fake, nil },
	})

	r, err := New("fakerun", nil)
	if err != nil {
		t.Fatalf("%v", err)
	}
	tk := tasks.Ticket{Key: "XYZ-2"}
	r.Spawn(tk, "coding", "build", "go", map[string]string{"RELAY_WORKFLOW": "w"})
	if len(fake.spawned) != 1 || fake.spawned[0] != "XYZ-2:coding:build:w" {
		t.Errorf("spawned = %v", fake.spawned)
	}

	if _, err := New("nope", nil); err == nil || !strings.Contains(err.Error(), "unknown runner type") {
		t.Errorf("unknown runner error = %v", err)
	}
}

func TestDuplicateRegisterPanics(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatal("expected panic on duplicate registration")
		}
	}()
	Register("duprun", Factory{})
	Register("duprun", Factory{})
}
