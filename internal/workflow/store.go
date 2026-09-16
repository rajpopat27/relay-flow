package workflow

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"github.com/rajpopat27/relay-flow/internal/config"
)

// Store persists workflow definitions as YAML files under Dir. The workflow
// file is the durable definition.
type Store struct {
	Dir string
}

const (
	acceptedHashSuffix = ".sha256"
	transactionSuffix  = ".txn"

	transactionForward  = "forward"
	transactionRollback = "rollback"
)

func (s *Store) path(name string) string {
	return filepath.Join(s.Dir, name+".yaml")
}

func (s *Store) hashPath(name string) string {
	return filepath.Join(s.Dir, name+acceptedHashSuffix)
}

func (s *Store) transactionPath(name string) string {
	return filepath.Join(s.Dir, name+transactionSuffix)
}

// StoredWorkflow is one workflow file plus the startup trust result derived
// from its accepted hash and local definition. A malformed file still gets a
// record with a placeholder Workflow so it remains visible for repair.
type StoredWorkflow struct {
	Name          string
	Raw           []byte
	AcceptedHash  string
	Workflow      *Workflow
	Status        HealthStatus
	StatusReason  string
	RepairCommand string
}

type fileSnapshot struct {
	yaml      []byte
	yamlExist bool
	hash      []byte
	hashExist bool
}

// transactionRecord makes the two-file definition/hash replacement
// recoverable. The journal is written before either target changes. On the
// next startup, a complete new pair is finalized; otherwise the old pair is
// restored. This prevents a crash from leaving a permanently mixed pair.
type transactionRecord struct {
	Name          string `json:"name"`
	Kind          string `json:"kind"`
	OldYAML       []byte `json:"oldYaml,omitempty"`
	OldYAMLExists bool   `json:"oldYamlExists"`
	OldHash       []byte `json:"oldHash,omitempty"`
	OldHashExists bool   `json:"oldHashExists"`
	NewYAML       []byte `json:"newYaml,omitempty"`
	NewYAMLExists bool   `json:"newYamlExists"`
	NewHash       []byte `json:"newHash,omitempty"`
	NewHashExists bool   `json:"newHashExists"`
}

func newTransaction(name string, old fileSnapshot, yamlBytes, hashBytes []byte) transactionRecord {
	return transactionRecord{
		Name: name, Kind: transactionForward,
		OldYAML: old.yaml, OldYAMLExists: old.yamlExist,
		OldHash: old.hash, OldHashExists: old.hashExist,
		NewYAML: yamlBytes, NewYAMLExists: true,
		NewHash: hashBytes, NewHashExists: true,
	}
}

func (t transactionRecord) oldSnapshot() fileSnapshot {
	return fileSnapshot{yaml: t.OldYAML, yamlExist: t.OldYAMLExists, hash: t.OldHash, hashExist: t.OldHashExists}
}

func (t transactionRecord) newSnapshot() fileSnapshot {
	return fileSnapshot{yaml: t.NewYAML, yamlExist: t.NewYAMLExists, hash: t.NewHash, hashExist: t.NewHashExists}
}

// LoadAll reads every stored workflow file independently. Per-file parse or
// integrity failures are represented by a non-routable workflow record so a
// caller that only needs definitions cannot accidentally make startup global.
func (s *Store) LoadAll() ([]*Workflow, error) {
	records, err := s.LoadAllRecords()
	if err != nil {
		return nil, err
	}
	out := make([]*Workflow, 0, len(records))
	for _, record := range records {
		if record.Workflow != nil {
			out = append(out, record.Workflow)
		}
	}
	return out, nil
}

// Get reads and parses one stored workflow by name. It intentionally keeps
// the original definition-only API; startup callers that need trust metadata
// use LoadAllRecords.
func (s *Store) Get(name string) (*Workflow, error) {
	raw, err := os.ReadFile(s.path(name))
	if err != nil {
		return nil, fmt.Errorf("read workflow %q: %w", name, err)
	}
	return Parse(name, raw)
}

// LoadAllRecords reads every stored workflow independently. A malformed,
// tampered, or legacy file becomes a blocked/unverified record rather than
// aborting the entire load, allowing the server and other workflows to start.
func (s *Store) LoadAllRecords() ([]StoredWorkflow, error) {
	recoveryIssues, err := s.recoverTransactions()
	if err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(s.Dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("read workflow dir %s: %w", s.Dir, err)
	}
	nameSet := map[string]bool{}
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".yaml") {
			continue
		}
		nameSet[strings.TrimSuffix(entry.Name(), ".yaml")] = true
	}
	for name := range recoveryIssues {
		nameSet[name] = true
	}
	names := make([]string, 0, len(nameSet))
	for name := range nameSet {
		names = append(names, name)
	}
	sort.Strings(names)
	records := make([]StoredWorkflow, 0, len(names))
	for _, name := range names {
		record := s.loadRecord(name)
		if reason, ok := recoveryIssues[name]; ok {
			record.Status = HealthBlocked
			record.StatusReason = fmt.Sprintf("recover workflow storage transaction: %v", reason)
			if record.Workflow == nil {
				record.Workflow = &Workflow{Name: name}
			}
			record.Workflow.MarkBlocked(record.StatusReason, s.repairCommand(name))
		}
		records = append(records, record)
	}
	return records, nil
}

// loadRecord never returns a per-file error. The record is retained with a
// diagnostic so one bad workflow cannot prevent management startup.
func (s *Store) loadRecord(name string) StoredWorkflow {
	record := StoredWorkflow{
		Name:          name,
		Status:        HealthBlocked,
		RepairCommand: s.repairCommand(name),
		Workflow:      &Workflow{Name: name},
	}
	raw, err := os.ReadFile(s.path(name))
	if err != nil {
		record.StatusReason = fmt.Sprintf("read workflow definition: %v", err)
		record.Workflow.MarkBlocked(record.StatusReason, record.RepairCommand)
		return record
	}
	record.Raw = raw

	wf, parseErr := Parse(name, raw)
	if parseErr != nil {
		record.StatusReason = parseErr.Error()
		record.Workflow.MarkBlocked(record.StatusReason, record.RepairCommand)
		return record
	}
	record.Workflow = wf
	if wf.Name != name {
		record.StatusReason = fmt.Sprintf("workflow file %q contains definition named %q", name+".yaml", wf.Name)
		wf.MarkBlocked(record.StatusReason, record.RepairCommand)
		return record
	}

	hashBytes, hashErr := os.ReadFile(s.hashPath(name))
	switch {
	case os.IsNotExist(hashErr):
		record.Status = HealthUnverified
		record.StatusReason = "accepted SHA-256 hash is missing"
		record.Workflow.MarkUnverified(record.StatusReason, record.RepairCommand)
		return record
	case hashErr != nil:
		record.StatusReason = fmt.Sprintf("read accepted SHA-256 hash: %v", hashErr)
		record.Workflow.MarkBlocked(record.StatusReason, record.RepairCommand)
		return record
	}
	record.AcceptedHash = strings.TrimSpace(string(hashBytes))
	if len(record.AcceptedHash) != sha256.Size*2 {
		record.StatusReason = "accepted SHA-256 hash has invalid length"
		record.Workflow.MarkBlocked(record.StatusReason, record.RepairCommand)
		return record
	}
	if _, err := hex.DecodeString(record.AcceptedHash); err != nil {
		record.StatusReason = fmt.Sprintf("accepted SHA-256 hash is invalid: %v", err)
		record.Workflow.MarkBlocked(record.StatusReason, record.RepairCommand)
		return record
	}
	actual := sha256.Sum256(raw)
	if !strings.EqualFold(record.AcceptedHash, hex.EncodeToString(actual[:])) {
		record.Status = HealthOutdated
		record.StatusReason = fmt.Sprintf("workflow YAML hash mismatch (accepted %s, actual %s)", record.AcceptedHash, hex.EncodeToString(actual[:]))
		record.Workflow.MarkOutdated(record.StatusReason, record.RepairCommand)
		return record
	}
	if err := wf.Validate(); err != nil {
		record.StatusReason = fmt.Sprintf("local workflow validation failed: %v", err)
		record.Workflow.MarkBlocked(record.StatusReason, record.RepairCommand)
		return record
	}
	record.Status = HealthHealthy
	record.Workflow.MarkHealthy()
	return record
}

func (s *Store) repairCommand(name string) string {
	return fmt.Sprintf("relay-flow workflow submit --file %s", s.path(name))
}

// snapshot captures both files so Service can restore the previous accepted
// definition if a later in-memory rebind fails.
func (s *Store) snapshot(name string) (fileSnapshot, error) {
	var snapshot fileSnapshot
	yamlBytes, err := os.ReadFile(s.path(name))
	if err == nil {
		snapshot.yaml, snapshot.yamlExist = yamlBytes, true
	} else if !os.IsNotExist(err) {
		return fileSnapshot{}, fmt.Errorf("read workflow %q for rollback: %w", name, err)
	}
	hashBytes, err := os.ReadFile(s.hashPath(name))
	if err == nil {
		snapshot.hash, snapshot.hashExist = hashBytes, true
	} else if !os.IsNotExist(err) {
		return fileSnapshot{}, fmt.Errorf("read workflow hash %q for rollback: %w", name, err)
	}
	return snapshot, nil
}

func (s *Store) restore(name string, snapshot fileSnapshot) error {
	if snapshot.yamlExist {
		if err := config.WriteAtomic(s.path(name), snapshot.yaml, 0o644); err != nil {
			return err
		}
	} else if err := removeIfExists(s.path(name)); err != nil {
		return err
	}
	if snapshot.hashExist {
		if err := config.WriteAtomic(s.hashPath(name), snapshot.hash, 0o644); err != nil {
			return err
		}
	} else if err := removeIfExists(s.hashPath(name)); err != nil {
		return err
	}
	return nil
}

// restorePair applies a rollback target through its own journal. This keeps a
// failed rebind from turning the rollback itself into a mixed YAML/hash pair.
func (s *Store) restorePair(name string, desired fileSnapshot) error {
	current, err := s.snapshot(name)
	if err != nil {
		return err
	}
	transaction := transactionRecord{
		Name: name, Kind: transactionRollback,
		OldYAML: current.yaml, OldYAMLExists: current.yamlExist,
		OldHash: current.hash, OldHashExists: current.hashExist,
		NewYAML: desired.yaml, NewYAMLExists: desired.yamlExist,
		NewHash: desired.hash, NewHashExists: desired.hashExist,
	}
	if err := s.writeTransaction(transaction); err != nil {
		return err
	}
	if err := s.restore(name, desired); err != nil {
		// Leave the rollback journal in place. Startup recovery will retry
		// the desired target rather than restoring the failed candidate.
		return fmt.Errorf("restore workflow %q: %w", name, err)
	}
	if err := removeIfExists(s.transactionPath(name)); err != nil {
		// The desired pair is already applied. Do not invoke the forward
		// rollback path here; that would restore the candidate pair.
		return fmt.Errorf("finish restoring workflow %q: %w", name, err)
	}
	return nil
}

func pairMatches(got, want fileSnapshot) bool {
	return got.yamlExist == want.yamlExist && got.hashExist == want.hashExist &&
		(!got.yamlExist || bytes.Equal(got.yaml, want.yaml)) &&
		(!got.hashExist || bytes.Equal(got.hash, want.hash))
}

func (s *Store) writeTransaction(transaction transactionRecord) error {
	raw, err := json.Marshal(transaction)
	if err != nil {
		return fmt.Errorf("marshal workflow storage transaction %q: %w", transaction.Name, err)
	}
	if err := config.WriteAtomic(s.transactionPath(transaction.Name), raw, 0o600); err != nil {
		return fmt.Errorf("write workflow storage transaction %q: %w", transaction.Name, err)
	}
	return nil
}

// recoverTransactions resolves any journal left by a process crash. A
// complete new pair wins; any mixed or incomplete pair rolls back to the old
// snapshot. The returned per-workflow errors are surfaced as blocked health
// records instead of aborting the server globally.
func (s *Store) recoverTransactions() (map[string]error, error) {
	entries, err := os.ReadDir(s.Dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("read workflow transaction dir %s: %w", s.Dir, err)
	}
	issues := map[string]error{}
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), transactionSuffix) {
			continue
		}
		name := strings.TrimSuffix(entry.Name(), transactionSuffix)
		raw, readErr := os.ReadFile(filepath.Join(s.Dir, entry.Name()))
		if readErr != nil {
			issues[name] = readErr
			continue
		}
		var transaction transactionRecord
		if unmarshalErr := json.Unmarshal(raw, &transaction); unmarshalErr != nil {
			issues[name] = unmarshalErr
			continue
		}
		if transaction.Name == "" {
			transaction.Name = name
		}
		if recoverErr := s.recoverTransaction(transaction, false); recoverErr != nil {
			issues[transaction.Name] = recoverErr
		}
	}
	return issues, nil
}

func (s *Store) recoverTransaction(transaction transactionRecord, forceOld bool) error {
	name := transaction.Name
	current, err := s.snapshot(name)
	if err != nil {
		return err
	}
	old := transaction.oldSnapshot()
	newPair := transaction.newSnapshot()
	if transaction.Kind == transactionRollback {
		// Rollback journals have the opposite semantic: the requested prior
		// pair is New and must win even when the process stopped before or
		// during restoration. Never restore Old for this journal kind.
		if pairMatches(current, newPair) {
			return removeIfExists(s.transactionPath(name))
		}
		if err := s.restore(name, newPair); err != nil {
			return fmt.Errorf("restore rollback target: %w", err)
		}
		return removeIfExists(s.transactionPath(name))
	}
	if !forceOld && (pairMatches(current, newPair) || pairMatches(current, old)) {
		return removeIfExists(s.transactionPath(name))
	}
	if err := s.restore(name, old); err != nil {
		return fmt.Errorf("restore prior workflow pair: %w", err)
	}
	return removeIfExists(s.transactionPath(name))
}

// Put replaces the exact submitted YAML and its accepted SHA-256 hash. The
// journal is committed before either target changes; a crash therefore
// recovers either the complete old pair or the complete new pair. Synchronous
// write errors force the old pair back before returning.
func (s *Store) Put(name string, yamlBytes []byte) error {
	if err := os.MkdirAll(s.Dir, 0o755); err != nil {
		return fmt.Errorf("create workflow dir %s: %w", s.Dir, err)
	}
	before, err := s.snapshot(name)
	if err != nil {
		return err
	}
	sum := sha256.Sum256(yamlBytes)
	hashBytes := []byte(hex.EncodeToString(sum[:]) + "\n")
	transaction := newTransaction(name, before, yamlBytes, hashBytes)
	if err := s.writeTransaction(transaction); err != nil {
		return err
	}
	if err := config.WriteAtomic(s.path(name), yamlBytes, 0o644); err != nil {
		return s.rollbackTransaction(name, transaction, fmt.Errorf("store workflow %q: %w", name, err))
	}
	if err := config.WriteAtomic(s.hashPath(name), hashBytes, 0o644); err != nil {
		return s.rollbackTransaction(name, transaction, fmt.Errorf("store workflow %q accepted hash: %w", name, err))
	}
	if err := removeIfExists(s.transactionPath(name)); err != nil {
		return s.rollbackTransaction(name, transaction, fmt.Errorf("finish storing workflow %q: %w", name, err))
	}
	return nil
}

func (s *Store) rollbackTransaction(name string, transaction transactionRecord, cause error) error {
	if rollbackErr := s.recoverTransaction(transaction, true); rollbackErr != nil {
		return fmt.Errorf("%w (rollback failed: %v)", cause, rollbackErr)
	}
	return cause
}

// Remove deletes the workflow definition and its accepted hash using the same
// recoverable pair transaction as Put.
func (s *Store) Remove(name string) error {
	before, err := s.snapshot(name)
	if err != nil {
		return err
	}
	if !before.yamlExist {
		return fmt.Errorf("remove workflow %q: file does not exist", name)
	}
	transaction := newTransaction(name, before, nil, nil)
	transaction.NewYAMLExists = false
	transaction.NewHashExists = false
	if err := s.writeTransaction(transaction); err != nil {
		return err
	}
	if err := removeIfExists(s.path(name)); err != nil {
		return s.rollbackTransaction(name, transaction, fmt.Errorf("remove workflow %q: %w", name, err))
	}
	if err := removeIfExists(s.hashPath(name)); err != nil {
		return s.rollbackTransaction(name, transaction, fmt.Errorf("remove workflow %q accepted hash: %w", name, err))
	}
	if err := removeIfExists(s.transactionPath(name)); err != nil {
		return s.rollbackTransaction(name, transaction, fmt.Errorf("finish removing workflow %q: %w", name, err))
	}
	return nil
}

func removeIfExists(path string) error {
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

// Registry is the in-memory workflow set. The registry has no repo index;
// repo bindings are the only derived repo-to-workflow index.
type Registry struct {
	mu   sync.RWMutex
	byID map[string]*Workflow
}

func (r *Registry) get(name string) (*Workflow, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	wf, ok := r.byID[name]
	return wf, ok
}

// Get returns the workflow by name.
func (r *Registry) Get(name string) (*Workflow, bool) {
	return r.get(name)
}

// List returns all registered workflows sorted by name.
func (r *Registry) List() []*Workflow {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]*Workflow, 0, len(r.byID))
	for _, wf := range r.byID {
		out = append(out, wf)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// ReferencesRepo reports whether any registered workflow lists the repo.
func (r *Registry) ReferencesRepo(repo string) bool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	for _, wf := range r.byID {
		for _, rr := range wf.Repos {
			if rr == repo {
				return true
			}
		}
	}
	return false
}

// Replace creates or replaces the workflow in the registry.
func (r *Registry) Replace(wf *Workflow) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.byID == nil {
		r.byID = map[string]*Workflow{}
	}
	r.byID[wf.Name] = wf
}

// Remove deletes the workflow from the registry.
func (r *Registry) Remove(name string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.byID, name)
}
