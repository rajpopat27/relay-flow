package workflow_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
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

func TestStorePutPersistsAcceptedHashAndDetectsEdits(t *testing.T) {
	s := newStore(t)
	raw := []byte(storeValid)
	if err := s.Put("basicFlow", raw); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(raw)
	hash, err := os.ReadFile(filepath.Join(s.Dir, "basicFlow.sha256"))
	if err != nil {
		t.Fatal(err)
	}
	if got, want := strings.TrimSpace(string(hash)), hex.EncodeToString(sum[:]); got != want {
		t.Fatalf("accepted hash = %q, want %q", got, want)
	}
	records, err := s.LoadAllRecords()
	if err != nil || len(records) != 1 {
		t.Fatalf("LoadAllRecords = %#v, %v", records, err)
	}
	if records[0].Status != workflow.HealthHealthy || records[0].Workflow.Status != workflow.HealthHealthy {
		t.Fatalf("stored workflow status = %#v", records[0])
	}
	if err := os.WriteFile(filepath.Join(s.Dir, "basicFlow.yaml"), []byte(strings.Replace(storeValid, "description: work", "description: edited", 1)), 0o644); err != nil {
		t.Fatal(err)
	}
	records, err = s.LoadAllRecords()
	if err != nil || len(records) != 1 {
		t.Fatalf("LoadAllRecords after edit = %#v, %v", records, err)
	}
	if records[0].Status != workflow.HealthOutdated || records[0].Workflow.Status != workflow.HealthOutdated || records[0].Workflow.RepairCommand == "" || !strings.Contains(records[0].StatusReason, "hash mismatch") || !strings.Contains(records[0].Workflow.RepairCommand, "relay-flow workflow submit --file") {
		t.Fatalf("edited workflow status = %#v", records[0])
	}
}

func TestStoreRecoversMixedPairFromStorageTransaction(t *testing.T) {
	s := newStore(t)
	oldYAML := []byte(storeValid)
	newYAML := []byte(strings.Replace(storeValid, "description: work", "description: recovered", 1))
	if err := s.Put("basicFlow", oldYAML); err != nil {
		t.Fatal(err)
	}
	oldHash := mustHashBytes(oldYAML)
	newHash := mustHashBytes(newYAML)
	journal := func() []byte {
		raw, err := json.Marshal(map[string]any{
			"name": "basicFlow", "oldYaml": oldYAML, "oldYamlExists": true,
			"oldHash": oldHash, "oldHashExists": true,
			"newYaml": newYAML, "newYamlExists": true,
			"newHash": newHash, "newHashExists": true,
		})
		if err != nil {
			t.Fatal(err)
		}
		return raw
	}
	// Simulate a crash after the YAML rename but before the hash rename.
	if err := os.WriteFile(filepath.Join(s.Dir, "basicFlow.txn"), journal(), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(s.Dir, "basicFlow.yaml"), newYAML, 0o644); err != nil {
		t.Fatal(err)
	}
	if records, err := s.LoadAllRecords(); err != nil || len(records) != 1 || records[0].Status != workflow.HealthHealthy {
		t.Fatalf("recovered old pair = %#v, %v", records, err)
	}
	if got, err := os.ReadFile(filepath.Join(s.Dir, "basicFlow.yaml")); err != nil || string(got) != string(oldYAML) {
		t.Fatalf("old YAML after recovery = %q, %v", got, err)
	}
	if got, err := os.ReadFile(filepath.Join(s.Dir, "basicFlow.sha256")); err != nil || string(got) != string(oldHash) {
		t.Fatalf("old hash after recovery = %q, %v", got, err)
	}
	if _, err := os.Stat(filepath.Join(s.Dir, "basicFlow.txn")); !os.IsNotExist(err) {
		t.Fatalf("transaction journal remains after rollback: %v", err)
	}

	// A complete new pair wins when the process crashed only before journal
	// cleanup.
	if err := os.WriteFile(filepath.Join(s.Dir, "basicFlow.txn"), journal(), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(s.Dir, "basicFlow.yaml"), newYAML, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(s.Dir, "basicFlow.sha256"), newHash, 0o644); err != nil {
		t.Fatal(err)
	}
	if records, err := s.LoadAllRecords(); err != nil || len(records) != 1 || records[0].Status != workflow.HealthHealthy || records[0].Workflow.Nodes["coding"].Description != "recovered" {
		t.Fatalf("recovered new pair = %#v, %v", records, err)
	}

	// A rollback journal has opposite semantics: Old is the failed candidate
	// and New is the requested prior pair. Even a mixed restoration must move
	// toward New rather than accepting or restoring Old.
	rollbackJournal, err := json.Marshal(map[string]any{
		"name": "basicFlow", "kind": "rollback",
		"oldYaml": newYAML, "oldYamlExists": true,
		"oldHash": newHash, "oldHashExists": true,
		"newYaml": oldYAML, "newYamlExists": true,
		"newHash": oldHash, "newHashExists": true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(s.Dir, "basicFlow.txn"), rollbackJournal, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(s.Dir, "basicFlow.yaml"), oldYAML, 0o644); err != nil {
		t.Fatal(err)
	}
	// Leave the candidate hash in place to simulate an interruption during
	// rollback, then let startup recovery finish the desired prior pair.
	if records, err := s.LoadAllRecords(); err != nil || len(records) != 1 || records[0].Status != workflow.HealthHealthy || records[0].Workflow.Nodes["coding"].Description != "work" {
		t.Fatalf("recovered rollback pair = %#v, %v", records, err)
	}
	if got, err := os.ReadFile(filepath.Join(s.Dir, "basicFlow.sha256")); err != nil || string(got) != string(oldHash) {
		t.Fatalf("rollback hash after recovery = %q, %v", got, err)
	}
}

func mustHashBytes(raw []byte) []byte {
	sum := sha256.Sum256(raw)
	return []byte(hex.EncodeToString(sum[:]) + "\n")
}

func TestStoreLoadAllRecordsIsolatesMalformedAndLegacyFiles(t *testing.T) {
	s := newStore(t)
	if err := s.Put("healthyFlow", []byte(strings.Replace(storeValid, "basicFlow", "healthyFlow", 1))); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(s.Dir, "broken.yaml"), []byte("name: [not valid"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(s.Dir, "legacy.yaml"), []byte(strings.Replace(storeValid, "basicFlow", "legacy", 1)), 0o644); err != nil {
		t.Fatal(err)
	}
	records, err := s.LoadAllRecords()
	if err != nil || len(records) != 3 {
		t.Fatalf("LoadAllRecords = %#v, %v", records, err)
	}
	statuses := map[string]workflow.HealthStatus{}
	for _, record := range records {
		statuses[record.Name] = record.Status
	}
	if statuses["healthyFlow"] != workflow.HealthHealthy {
		t.Fatalf("healthy status = %q", statuses["healthyFlow"])
	}
	if statuses["broken"] != workflow.HealthBlocked {
		t.Fatalf("broken status = %q", statuses["broken"])
	}
	if statuses["legacy"] != workflow.HealthUnverified {
		t.Fatalf("legacy status = %q", statuses["legacy"])
	}
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

func TestServiceSubmitValidatesReferencedRepoConfigBeforeStoring(t *testing.T) {
	active := &fakeActiveRuns{active: map[string]bool{}}
	svc, store := newService(t, active, map[string]bool{"payments": true})
	svc.ValidateTaskConfig = func(_ context.Context, wf *workflow.Workflow) error {
		if wf.Nodes["coding"].TaskConfig["invalid"] == true {
			return errors.New("repo payments node coding: invalid task config")
		}
		return nil
	}
	invalid := []byte(strings.Replace(storeValid, "description: work", "description: work\n    taskConfig:\n      invalid: true", 1))

	if _, err := svc.Submit(context.Background(), invalid); err == nil || !strings.Contains(err.Error(), "invalid task config") {
		t.Fatalf("Submit error = %v", err)
	}
	if _, err := store.Get("basicFlow"); err == nil {
		t.Fatal("workflow was stored despite task validation failure")
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

func TestServiceRebindFailureRollsBackDefinitionAndRegistry(t *testing.T) {
	active := &fakeActiveRuns{active: map[string]bool{}}
	svc, store := newService(t, active, map[string]bool{"payments": true})
	calls := 0
	svc.Rebind = func() error {
		calls++
		if calls == 2 {
			return errors.New("matcher unavailable")
		}
		return nil
	}
	if _, err := svc.Submit(context.Background(), []byte(storeValid)); err != nil {
		t.Fatal(err)
	}
	beforeHash, err := os.ReadFile(filepath.Join(store.Dir, "basicFlow.sha256"))
	if err != nil {
		t.Fatal(err)
	}
	replacement := []byte(strings.Replace(storeValid, "description: work", "description: changed", 1))
	if _, err := svc.Submit(context.Background(), replacement); err == nil {
		t.Fatal("submission succeeded despite binding failure")
	}
	raw, err := os.ReadFile(filepath.Join(store.Dir, "basicFlow.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if string(raw) != storeValid {
		t.Fatalf("stored YAML changed after failed rebind: %q", raw)
	}
	afterHash, err := os.ReadFile(filepath.Join(store.Dir, "basicFlow.sha256"))
	if err != nil {
		t.Fatal(err)
	}
	if string(afterHash) != string(beforeHash) {
		t.Fatalf("accepted hash changed after failed rebind: %q -> %q", beforeHash, afterHash)
	}
	wf, err := svc.Get("basicFlow")
	if err != nil {
		t.Fatal(err)
	}
	if wf.Nodes["coding"].Description != "work" {
		t.Fatalf("registry definition changed after failed rebind: %q", wf.Nodes["coding"].Description)
	}
}

func TestServiceRemoveRebindFailureRollsBackDefinitionAndRegistry(t *testing.T) {
	active := &fakeActiveRuns{active: map[string]bool{}}
	svc, store := newService(t, active, map[string]bool{"payments": true})
	calls := 0
	svc.Rebind = func() error {
		calls++
		if calls == 2 {
			return errors.New("binding unavailable")
		}
		return nil
	}
	if _, err := svc.Submit(context.Background(), []byte(storeValid)); err != nil {
		t.Fatal(err)
	}
	beforeHash, err := os.ReadFile(filepath.Join(store.Dir, "basicFlow.sha256"))
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.Remove(context.Background(), "basicFlow"); err == nil {
		t.Fatal("remove succeeded despite binding failure")
	}
	raw, err := os.ReadFile(filepath.Join(store.Dir, "basicFlow.yaml"))
	if err != nil || string(raw) != storeValid {
		t.Fatalf("stored YAML after failed remove = %q, %v", raw, err)
	}
	afterHash, err := os.ReadFile(filepath.Join(store.Dir, "basicFlow.sha256"))
	if err != nil || string(afterHash) != string(beforeHash) {
		t.Fatalf("accepted hash after failed remove = %q, %v", afterHash, err)
	}
	if _, err := svc.Get("basicFlow"); err != nil {
		t.Fatalf("registry definition lost after failed remove: %v", err)
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
