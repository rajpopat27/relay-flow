package goworkflows_test

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"github.com/rajpopat27/relay-flow/internal/config"
	"github.com/rajpopat27/relay-flow/internal/harness"
	"github.com/rajpopat27/relay-flow/internal/retry"
	"github.com/rajpopat27/relay-flow/internal/run"
	"github.com/rajpopat27/relay-flow/internal/runner"
	"github.com/rajpopat27/relay-flow/internal/task"
	"github.com/rajpopat27/relay-flow/internal/workflow"
)

// Shared test fakes for the engine-level section-3 tests. Every external
// effect is appended to one ordered event log so tests can assert
// cross-primitive ordering (3.16), claim/cancellation sequencing (3.19),
// reconcile signals (3.21), and no-rollback (3.17).

type eventLog struct {
	mu     sync.Mutex
	events []string
}

var testReportID atomic.Uint64

func reportRequest(id run.ID, node string, report workflow.Report) run.ReportRequest {
	return run.ReportRequest{
		RunID: id, Node: node,
		ReportID: fmt.Sprintf("test:%d", testReportID.Add(1)),
		Report:   report,
	}
}

func newEventLog() *eventLog { return &eventLog{} }

func (l *eventLog) add(e string) {
	l.mu.Lock()
	l.events = append(l.events, e)
	l.mu.Unlock()
}

func (l *eventLog) all() []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]string{}, l.events...)
}

func (l *eventLog) count(prefix string) int {
	l.mu.Lock()
	defer l.mu.Unlock()
	n := 0
	for _, e := range l.events {
		if len(e) >= len(prefix) && e[:len(prefix)] == prefix {
			n++
		}
	}
	return n
}

// --- task system ---

type fakeTaskSystem struct {
	log *eventLog

	mu            sync.Mutex
	mailboxes     map[string]task.Mailbox
	mailboxStatus map[string]string   // mailbox key -> task status
	labels        map[string][]string // mailbox key -> labels
	specs         []task.MailboxSpec
	comments      []recordedComment
	resets        []string
	renderText    func(task.TextKind, task.TextData) (string, error)

	// Failure/crash/slow injection — the fake IS the documented injection
	// seam (allowed seam a). These make the fake adapter fail/stall so the
	// engine's durable retry/crash behavior is observable through outcomes.
	completeFail     int           // next N CompleteMailbox calls return transient error
	completeConflict bool          // CompleteMailbox returns retry.ConflictError
	completeSlow     time.Duration // CompleteMailbox sleeps (a running activity)
	failComments     bool          // Comment returns transient error

	// Recovery fixtures.
	parentsToRecover []task.Ticket
	canceledParents  map[string]bool
}

type recordedComment struct {
	Key    string
	Body   string
	Marker string
}

func newFakeTaskSystem(log *eventLog) *fakeTaskSystem {
	return &fakeTaskSystem{
		log:           log,
		mailboxes:     map[string]task.Mailbox{},
		mailboxStatus: map[string]string{},
		labels:        map[string][]string{},
	}
}

func (s *fakeTaskSystem) Poll(context.Context) ([]task.Ticket, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]task.Ticket{}, s.parentsToRecover...), nil
}

func (s *fakeTaskSystem) CompileFilter(config.RawValues) (func(task.Ticket) bool, error) {
	return func(task.Ticket) bool { return true }, nil
}

func (s *fakeTaskSystem) Claim(_ context.Context, ref task.TicketRef, wf string) error {
	s.log.add("claim:" + ref.Key + ":" + wf)
	return nil
}

func (s *fakeTaskSystem) ValidateConfig(context.Context, config.RawValues, map[string]config.RawValues) error {
	return nil
}

func (s *fakeTaskSystem) RenderText(kind task.TextKind, data task.TextData) (string, error) {
	if s.renderText != nil {
		return s.renderText(kind, data)
	}
	switch kind {
	case task.TextMailboxDescription:
		return "Parent ticket: " + data.Ticket + "\nNode: " + data.Node + "\nType: " + data.NodeType + "\nAgent: " + data.Agent + "\nWork: " + data.NodeDescription + "\nMailbox: " + data.Mailbox, nil
	case task.TextSummaryComment:
		return "SUMMARY\n" + data.SummaryReport, nil
	case task.TextFeedbackComment:
		return "Feedback from " + data.SourceNode + " to " + data.TargetNode + " mailbox " + data.Mailbox + "\n" + data.FeedbackReport, nil
	default:
		return "", fmt.Errorf("unknown task text kind %q", kind)
	}
}

func (s *fakeTaskSystem) EnsureMailboxes(_ context.Context, parent task.TicketRef, wf string, specs []task.MailboxSpec) (map[string]task.Mailbox, error) {
	s.log.add("ensureMailboxes:" + parent.Key)
	s.mu.Lock()
	defer s.mu.Unlock()
	s.specs = append(s.specs, specs...)
	out := map[string]task.Mailbox{}
	for _, sp := range specs {
		// Per-parent mailbox identity: parent key + node.
		key := parent.Key + "/" + sp.Node
		mb, ok := s.mailboxes[key]
		if !ok {
			mb = task.Mailbox{ID: "mb-" + parent.Key + "-" + sp.Node, Key: parent.Key + "-" + sp.Node, Node: sp.Node}
			s.mailboxes[key] = mb
			s.mailboxStatus[mb.Key] = "To Do"
			s.labels[mb.Key] = []string{"wf:" + wf}
			s.log.add("createMailbox:" + parent.Key + ":" + sp.Node)
		} else {
			s.log.add("foundMailbox:" + parent.Key + ":" + sp.Node)
		}
		out[sp.Node] = mb
	}
	return out, nil
}

func (s *fakeTaskSystem) ApplyTaskConfig(_ context.Context, target task.Target, cfg config.RawValues) error {
	key := target.Parent.Key
	if target.Mailbox != nil {
		key = target.Mailbox.Key
		s.mu.Lock()
		s.mailboxStatus[target.Mailbox.Key] = "In Progress"
		s.mu.Unlock()
	}
	s.log.add("applyTaskConfig:" + key)
	return nil
}

func (s *fakeTaskSystem) CompleteMailbox(_ context.Context, mb task.Mailbox) error {
	if s.completeSlow > 0 {
		s.log.add("completeMailboxStart:" + mb.Key)
		time.Sleep(s.completeSlow) // a running activity cancellation cannot interrupt
	}
	if s.completeFail > 0 {
		s.completeFail--
		s.log.add("completeMailboxFail:" + mb.Key)
		return errTransient
	}
	if s.completeConflict {
		s.log.add("completeMailboxConflict:" + mb.Key)
		return retry.ConflictError(errConflict)
	}
	s.mu.Lock()
	s.mailboxStatus[mb.Key] = "Done"
	s.mu.Unlock()
	s.log.add("completeMailbox:" + mb.Key)
	return nil
}

func (s *fakeTaskSystem) HasComment(_ context.Context, target task.Target, marker string) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, c := range s.comments {
		if c.Marker == marker {
			return true, nil
		}
	}
	// Recovery cancellation-marker check on a parent.
	if s.canceledParents[target.Parent.Key] && hasSuffixStr(marker, ":cancellation") {
		return true, nil
	}
	return false, nil
}

func (s *fakeTaskSystem) Comment(_ context.Context, target task.Target, body, marker string) error {
	key := target.Parent.Key
	if target.Mailbox != nil {
		key = target.Mailbox.Key
	}
	if s.failComments {
		s.log.add("commentFail:" + key)
		return errTransient
	}
	s.mu.Lock()
	s.comments = append(s.comments, recordedComment{Key: key, Body: body, Marker: marker})
	s.mu.Unlock()
	s.log.add("comment:" + key)
	return nil
}

func (s *fakeTaskSystem) ResetForRecovery(_ context.Context, parent task.TicketRef, mbs []task.Mailbox, _ config.RawValues) error {
	s.mu.Lock()
	for _, mb := range mbs {
		s.mailboxStatus[mb.Key] = "To Do"
	}
	s.resets = append(s.resets, parent.Key)
	s.mu.Unlock()
	s.log.add("resetForRecovery:" + parent.Key)
	return nil
}

func (s *fakeTaskSystem) commentBodies(key string) []recordedComment {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []recordedComment
	for _, c := range s.comments {
		if c.Key == key {
			out = append(out, c)
		}
	}
	return out
}

// mailboxesSnapshot returns a copy of the per-parent mailbox map, safe for
// tests to iterate without holding the lock.
func (s *fakeTaskSystem) mailboxesSnapshot() map[string]task.Mailbox {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := map[string]task.Mailbox{}
	for k, v := range s.mailboxes {
		out[k] = v
	}
	return out
}

func (s *fakeTaskSystem) labelsFor(key string) []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string{}, s.labels[key]...)
}

func (s *fakeTaskSystem) mailboxStatusOf(key string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.mailboxStatus[key]
}

func (s *fakeTaskSystem) mailboxFor(parentKey, node string) (task.Mailbox, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	mb, ok := s.mailboxes[parentKey+"/"+node]
	return mb, ok
}

func (s *fakeTaskSystem) setMailboxStatus(key, status string) {
	s.mu.Lock()
	s.mailboxStatus[key] = status
	s.mu.Unlock()
}

// seedMailbox pre-populates an existing mailbox (with labels) for recovery
// and reuse tests.
func (s *fakeTaskSystem) seedMailbox(parentKey string, mb task.Mailbox, labels []string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.mailboxes[parentKey+"/"+mb.Node] = mb
	s.mailboxStatus[mb.Key] = "To Do"
	s.labels[mb.Key] = append([]string{}, labels...)
}

// parentByKey returns a pollable parent by key (for label assertions).
func (s *fakeTaskSystem) parentByKey(key string) (task.Ticket, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, p := range s.parentsToRecover {
		if p.Key == key {
			return p, true
		}
	}
	return task.Ticket{}, false
}

// --- runner ---

type fakeRunner struct {
	log *eventLog

	mu        sync.Mutex
	envs      map[string]runner.Environment
	terminals map[string]*fakeTerminal // envID/title
	cleaned   []string
	closedRun []string
	createErr error
	nextID    int
}

type fakeTerminal struct {
	term  runner.Terminal
	live  bool
	title string
}

func newFakeRunner(log *eventLog) *fakeRunner {
	return &fakeRunner{
		log:       log,
		envs:      map[string]runner.Environment{},
		terminals: map[string]*fakeTerminal{},
	}
}

func (f *fakeRunner) DiscoverRepos(context.Context) ([]runner.RepoCandidate, error) {
	return []runner.RepoCandidate{{Name: "payments", Path: "/srv/payments"}}, nil
}
func (f *fakeRunner) ValidateRepo(context.Context, string, string) error { return nil }

func (f *fakeRunner) EnsureEnvironment(_ context.Context, spec runner.RunSpec) (runner.Environment, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if e, ok := f.envs[string(spec.RunID)]; ok {
		return e, nil
	}
	e := runner.Environment{ID: "env-" + string(spec.RunID), Path: spec.RepoPath}
	f.envs[string(spec.RunID)] = e
	f.log.add("ensureEnvironment:" + string(spec.RunID))
	return e, nil
}

func (f *fakeRunner) SetEnvironmentStatus(_ context.Context, _ runner.Environment, status string) error {
	f.log.add("environmentStatus:" + status)
	return nil
}

func (f *fakeRunner) FindTerminal(_ context.Context, terminal runner.Terminal) (runner.Terminal, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if terminal.ID != "" {
		f.log.add("findTerminalID:" + terminal.ID)
	}
	for _, ft := range f.terminals {
		if ft.term.ID == terminal.ID && ft.live {
			return ft.term, true, nil
		}
	}
	return runner.Terminal{}, false, nil
}

func (f *fakeRunner) SendTerminal(_ context.Context, terminal runner.Terminal, text string) error {
	f.log.add("sendTerminal:" + terminal.ID + ":" + text)
	return nil
}

func (f *fakeRunner) CreateTerminal(ctx context.Context, env runner.Environment, title string, command runner.Command) (runner.Terminal, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.createErr != nil {
		err := f.createErr
		f.createErr = nil
		return runner.Terminal{}, err
	}
	f.nextID++
	t := runner.Terminal{ID: fmt.Sprintf("t-%d-%s", f.nextID, title), Title: title}
	f.terminals[env.ID+"/"+title] = &fakeTerminal{term: t, live: true, title: title}
	f.log.add("ensureTerminal:" + title)
	f.log.add("createTerminal:" + title + ":" + command.Executable)
	return t, nil
}

func (f *fakeRunner) EnsureTerminal(ctx context.Context, env runner.Environment, stored runner.Terminal, title string, command runner.Command) (runner.Terminal, error) {
	if terminal, ok, err := f.FindTerminal(ctx, stored); err != nil {
		return runner.Terminal{}, err
	} else if ok {
		return terminal, nil
	}
	return f.CreateTerminal(ctx, env, title, command)
}

func (f *fakeRunner) CloseTerminal(_ context.Context, t runner.Terminal) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, ft := range f.terminals {
		if ft.term.ID == t.ID {
			ft.live = false
		}
	}
	f.log.add("closeTerminal:" + t.Title)
	return nil
}

func (f *fakeRunner) CloseTerminals(_ context.Context, spec runner.RunSpec) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	prefix := "env-" + string(spec.RunID) + "/"
	for k, ft := range f.terminals {
		if len(k) >= len(prefix) && k[:len(prefix)] == prefix {
			ft.live = false
			f.log.add("closeTerminals:" + ft.title)
		}
	}
	f.closedRun = append(f.closedRun, string(spec.RunID))
	// Environment/workspace preserved.
	return nil
}

func (f *fakeRunner) CleanupRun(_ context.Context, spec runner.RunSpec) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	prefix := "env-" + string(spec.RunID) + "/"
	for k := range f.terminals {
		if len(k) >= len(prefix) && k[:len(prefix)] == prefix {
			delete(f.terminals, k)
		}
	}
	delete(f.envs, string(spec.RunID))
	f.cleaned = append(f.cleaned, string(spec.RunID))
	f.log.add("cleanupRun:" + string(spec.RunID))
	return nil
}

func (f *fakeRunner) liveTerminals() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for _, ft := range f.terminals {
		if ft.live {
			n++
		}
	}
	return n
}

// killTerminals marks every terminal unusable (simulates terminal death for
// reconcile tests).
func (f *fakeRunner) killTerminals() {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, ft := range f.terminals {
		ft.live = false
	}
}

// --- harness ---

type fakeHarness struct {
	log *eventLog

	mu             sync.Mutex
	validated      []string
	sessions       map[string]harness.Session
	reconcileNudge int // nudges sent to idle live HITL sessions (must stay 0)
}

func newFakeHarness(log *eventLog) *fakeHarness {
	return &fakeHarness{log: log, sessions: map[string]harness.Session{}}
}

func (f *fakeHarness) SetupRepo(context.Context, string) error { return nil }

func (f *fakeHarness) ValidateAgent(_ context.Context, _, agent string) error {
	f.mu.Lock()
	f.validated = append(f.validated, agent)
	f.mu.Unlock()
	f.log.add("validateAgent:" + agent)
	return nil
}

func (f *fakeHarness) FindSession(_ context.Context, _, title string) (harness.Session, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.log.add("findSession:" + title)
	s, ok := f.sessions[title]
	return s, ok, nil
}

func (f *fakeHarness) RenderPrompt(kind harness.PromptKind, data harness.PromptData, nudge string) (string, error) {
	prompt := string(kind) + ":" + data.TaskSystem + ":" + data.Mailbox
	if data.NodeType == workflow.NodeHITL {
		prompt += ":hitl"
	}
	if nudge != "" {
		prompt += ":" + nudge
	}
	return prompt, nil
}

func (f *fakeHarness) BuildCommand(spec harness.LaunchSpec) (runner.Command, error) {
	f.log.add("buildCommand:" + spec.Node + ":" + string(spec.NodeVisitID) + ":resume=" + spec.ResumeID)
	return runner.Command{Executable: "opencode"}, nil
}

func hasPrefix(s, prefix string) bool {
	return len(s) >= len(prefix) && s[:len(prefix)] == prefix
}

func hasSuffixStr(s, suffix string) bool {
	return len(s) >= len(suffix) && s[len(s)-len(suffix):] == suffix
}

// transientError is a Kind=Transient failure.
type transientError struct{ msg string }

func (e *transientError) Error() string { return e.msg }

var errTransient = &transientError{msg: "jira unavailable"}

// conflictError is wrapped by retry.ConflictError for Kind=Conflict.
type conflictError struct{ msg string }

func (e *conflictError) Error() string { return e.msg }

var errConflict = &conflictError{msg: "human moved mailbox"}
