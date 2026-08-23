package workflow_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/rajpopat27/relay-flow/internal/workflow"
)

// 3.34: workflow storage/service per specs/workflow-repo-management
// "Workflow definitions are persisted atomically" and "Workflow replacement
// has no concurrent versions" / "Workflow removal protects active runs".

const storeValid = `
name: basicFlow
repos: [payments]
nodes:
  start:
    onSuccess:
      - target: coding
  coding:
    type: agent
    agent: build
    description: work
    onSuccess:
      - target: end
    onFailure:
      - target: coding
  end: {}
`

type fakeActiveRuns struct{ active map[string]bool }

func (f *fakeActiveRuns) HasActiveWorkflow(_ context.Context, name string) (bool, error) {
	return f.active[name], nil
}

func (f *fakeActiveRuns) set(name string, on bool) { f.active[name] = on }

type fakeRepoLookup struct{ repos map[string]bool }

func (f fakeRepoLookup) Exists(name string) bool { return f.repos[name] }

func newStore(t *testing.T) *workflow.Store {
	t.Helper()
	return &workflow.Store{Dir: t.TempDir()}
}

// newService builds a Service via its constructor over a temp store and the
// fake ActiveRuns/RepoLookup consumer interfaces.
func newService(t *testing.T, active *fakeActiveRuns, repos map[string]bool) (*workflow.Service, *workflow.Store) {
	t.Helper()
	store := newStore(t)
	svc := workflow.NewService(store, active, fakeRepoLookup{repos: repos})
	return svc, store
}

func TestStorePutGetLoadAll(t *testing.T) {
	s := newStore(t)
	if err := s.Put("basicFlow", []byte(storeValid)); err != nil {
		t.Fatalf("Put failed: %v", err)
	}
	// File lands at <dir>/<name>.yaml with 0644.
	fi, err := os.Stat(filepath.Join(s.Dir, "basicFlow.yaml"))
	if err != nil {
		t.Fatalf("workflow file not stored: %v", err)
	}
	if fi.Mode().Perm() != 0644 {
		t.Fatalf("workflow file mode = %o, want 0644", fi.Mode().Perm())
	}

	wf, err := s.Get("basicFlow")
	if err != nil {
		t.Fatalf("Get failed: %v", err)
	}
	if wf.Name != "basicFlow" {
		t.Fatalf("Get name = %q", wf.Name)
	}

	all, err := s.LoadAll()
	if err != nil || len(all) != 1 {
		t.Fatalf("LoadAll = %d, %v", len(all), err)
	}

	if err := s.Remove("basicFlow"); err != nil {
		t.Fatalf("Remove failed: %v", err)
	}
	if _, err := s.Get("basicFlow"); err == nil {
		t.Fatal("Get after Remove succeeded")
	}
}

func TestServiceSubmitValidatesBeforeStoring(t *testing.T) {
	active := &fakeActiveRuns{active: map[string]bool{}}
	svc, store := newService(t, active, map[string]bool{"payments": true})

	// Invalid YAML must fail before any file is written.
	if _, err := svc.Submit(context.Background(), []byte("name: bad name!!")); err == nil {
		t.Fatal("invalid workflow submitted")
	}
	if _, err := store.Get("bad name!!"); err == nil {
		t.Fatal("invalid workflow was stored")
	}

	wf, err := svc.Submit(context.Background(), []byte(storeValid))
	if err != nil {
		t.Fatalf("valid Submit failed: %v", err)
	}
	if wf.Name != "basicFlow" {
		t.Fatalf("submitted name = %q", wf.Name)
	}
	// Submission updates the in-memory registry: the workflow is retrievable.
	if _, err := svc.Get("basicFlow"); err != nil {
		t.Fatal("submitted workflow not in registry after submit")
	}
}

func TestServiceSubmitRejectsUnknownRepo(t *testing.T) {
	active := &fakeActiveRuns{active: map[string]bool{}}
	svc, store := newService(t, active, map[string]bool{}) // no repos registered
	if _, err := svc.Submit(context.Background(), []byte(storeValid)); err == nil {
		t.Fatal("workflow referencing unregistered repo accepted")
	}
	if _, err := store.Get("basicFlow"); err == nil {
		t.Fatal("unknown-repo workflow stored")
	}
}

func TestServiceReplaceRejectedWhileActive(t *testing.T) {
	active := &fakeActiveRuns{active: map[string]bool{}}
	svc, store := newService(t, active, map[string]bool{"payments": true})
	if _, err := svc.Submit(context.Background(), []byte(storeValid)); err != nil {
		t.Fatal(err)
	}
	before, err := store.Get("basicFlow")
	if err != nil {
		t.Fatal(err)
	}

	// Now a run is active; replacement must be rejected and the stored file
	// and in-memory definition must remain unchanged. Change a REAL field
	// (the coding node description), not just a comment.
	active.set("basicFlow", true)
	replacement := []byte(strings.Replace(storeValid, "description: work", "description: changed work", 1))
	if _, err := svc.Submit(context.Background(), replacement); err == nil {
		t.Fatal("replacement during active run accepted")
	}
	after, err := store.Get("basicFlow")
	if err != nil {
		t.Fatal(err)
	}
	if after.Name != before.Name {
		t.Fatal("stored definition changed during rejected replacement")
	}
	if after.Nodes["coding"].Description != before.Nodes["coding"].Description {
		t.Fatalf("in-memory definition changed during rejected replacement: %q -> %q",
			before.Nodes["coding"].Description, after.Nodes["coding"].Description)
	}
	rawOnDisk, rerr := os.ReadFile(filepath.Join(store.Dir, "basicFlow.yaml"))
	if rerr == nil && strings.Contains(string(rawOnDisk), "changed work") {
		t.Fatal("stored workflow file changed during rejected replacement")
	}

	// After completion, replacement succeeds and becomes the definition.
	active.set("basicFlow", false)
	if _, err := svc.Submit(context.Background(), replacement); err != nil {
		t.Fatalf("replacement after completion rejected: %v", err)
	}
	installed, err := store.Get("basicFlow")
	if err != nil {
		t.Fatal(err)
	}
	if installed.Nodes["coding"].Description != "changed work" {
		t.Fatalf("changed definition not installed after completion: %q", installed.Nodes["coding"].Description)
	}
}

func TestServiceFailedWritePreservesExisting(t *testing.T) {
	active := &fakeActiveRuns{active: map[string]bool{}}
	svc, store := newService(t, active, map[string]bool{"payments": true})
	if _, err := svc.Submit(context.Background(), []byte(storeValid)); err != nil {
		t.Fatal(err)
	}

	// Make the store directory read-only so the atomic replacement fails.
	if err := os.Chmod(store.Dir, 0500); err != nil {
		t.Fatal(err)
	}
	defer os.Chmod(store.Dir, 0700)

	replacement := []byte(storeValid + "\n# changed\n")
	if _, err := svc.Submit(context.Background(), replacement); err == nil {
		t.Fatal("submission with failing write succeeded")
	}
	// Prior file and in-memory definition remain usable/active.
	os.Chmod(store.Dir, 0700)
	got, err := store.Get("basicFlow")
	if err != nil {
		t.Fatalf("prior workflow unusable after failed write: %v", err)
	}
	if got.Name != "basicFlow" {
		t.Fatal("in-memory definition lost after failed write")
	}
	if _, err := svc.Get("basicFlow"); err != nil {
		t.Fatal("in-memory definition lost after failed write")
	}
}

func TestServiceRemoveProtectsActiveRuns(t *testing.T) {
	active := &fakeActiveRuns{active: map[string]bool{}}
	svc, _ := newService(t, active, map[string]bool{"payments": true})
	if _, err := svc.Submit(context.Background(), []byte(storeValid)); err != nil {
		t.Fatal(err)
	}

	active.set("basicFlow", true)
	if err := svc.Remove(context.Background(), "basicFlow"); err == nil {
		t.Fatal("removal with active run accepted")
	}

	active.set("basicFlow", false)
	if err := svc.Remove(context.Background(), "basicFlow"); err != nil {
		t.Fatalf("removal without active runs rejected: %v", err)
	}
	if _, err := svc.Get("basicFlow"); err == nil {
		t.Fatal("removed workflow still returned by Get")
	}
	// Registry updated on removal: List no longer returns it.
	for _, w := range svc.List() {
		if w.Name == "basicFlow" {
			t.Fatal("removed workflow still listed")
		}
	}
}

func TestRegistryReferencesRepoAndReplace(t *testing.T) {
	reg := &workflow.Registry{}
	wf, err := workflow.Parse("basicFlow", []byte(storeValid))
	if err != nil {
		t.Fatal(err)
	}
	reg.Replace(wf)
	if !reg.ReferencesRepo("payments") {
		t.Fatal("ReferencesRepo(payments) = false")
	}
	if reg.ReferencesRepo("other") {
		t.Fatal("ReferencesRepo(other) = true")
	}
	if got, ok := reg.Get("basicFlow"); !ok || got.Name != "basicFlow" {
		t.Fatalf("Registry.Get = %v, %v", got, ok)
	}
	reg.Remove("basicFlow")
	if _, ok := reg.Get("basicFlow"); ok {
		t.Fatal("Registry.Get after Remove found workflow")
	}
	if reg.ReferencesRepo("payments") {
		t.Fatal("ReferencesRepo still true after removal")
	}
}
