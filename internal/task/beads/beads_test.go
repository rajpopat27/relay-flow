package beads

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/rajpopat27/relay-flow/internal/config"
	"github.com/rajpopat27/relay-flow/internal/task"
	"github.com/rajpopat27/relay-flow/internal/task/beads/bdcli"
)

func TestBeadsFactoryIsRegisteredWithBeadsDirRequirement(t *testing.T) {
	if !hasString(task.Names(), "beads") {
		t.Fatalf("task plugins = %v, want beads", task.Names())
	}
	keys, err := task.RequiredRepoKeys("beads")
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(keys, []string{"beadsDir"}) {
		t.Fatalf("required repo keys = %v, want [beadsDir]", keys)
	}
}

func TestBeadsConfigRejectsUnknownFields(t *testing.T) {
	for _, field := range []string{"unknownField", "project", "component", "transitionTo"} {
		t.Run(field, func(t *testing.T) {
			err := task.ValidateTextConfig("beads", config.RawValues{field: "must be rejected"})
			if err == nil {
				t.Fatalf("unknown Beads config field %q was accepted", field)
			}
		})
	}
}

func TestBeadsConfigRejectsExplicitNulls(t *testing.T) {
	err := task.ValidateTextConfig("beads", config.RawValues{
		"templates": map[string]any{
			"summaryComment": nil,
		},
	})
	if err == nil || !strings.Contains(err.Error(), "explicit null") {
		t.Fatalf("explicit null error = %v", err)
	}
}

func TestBeadsScopeRequiresBeadsDir(t *testing.T) {
	for _, repoConfig := range []config.RawValues{
		{},
		{"beadsDir": ""},
		{"beadsDir": "   "},
	} {
		if _, err := task.TaskScopeKey("beads", nil, repoConfig); err == nil {
			t.Fatalf("scope accepted repo config %#v without a usable beadsDir", repoConfig)
		}
	}
	if _, err := task.TaskScopeKey("beads", config.RawValues{"beadsDir": t.TempDir()}, nil); err == nil {
		t.Fatal("root beadsDir satisfied the required repo-scoped key")
	}

	file := filepath.Join(t.TempDir(), "not-a-directory")
	if err := os.WriteFile(file, []byte("file"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := task.TaskScopeKey("beads", nil, config.RawValues{"beadsDir": file}); err == nil {
		t.Fatal("file path was accepted as a Beads workspace directory")
	}
}

func TestBeadsScopeIsCanonicalAndIndependent(t *testing.T) {
	root := t.TempDir()
	first := filepath.Join(root, "first")
	second := filepath.Join(root, "second")
	if err := os.Mkdir(filepath.Join(root, "nested"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(first, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(second, 0o755); err != nil {
		t.Fatal(err)
	}

	firstInput := filepath.Join(root, "nested", "..", "first", ".")
	firstScope, err := task.TaskScopeKey("beads", nil, config.RawValues{"beadsDir": firstInput})
	if err != nil {
		t.Fatal(err)
	}
	wantFirst, err := filepath.Abs(first)
	if err != nil {
		t.Fatal(err)
	}
	if firstScope != filepath.Clean(wantFirst) {
		t.Fatalf("first scope = %q, want canonical %q", firstScope, filepath.Clean(wantFirst))
	}

	secondScope, err := task.TaskScopeKey("beads", nil, config.RawValues{"beadsDir": second + string(os.PathSeparator)})
	if err != nil {
		t.Fatal(err)
	}
	if secondScope == firstScope {
		t.Fatalf("independent Beads directories share scope %q", firstScope)
	}
}

func TestBeadsDuplicateScopeInputsResolveIdentically(t *testing.T) {
	root := t.TempDir()
	workspace := filepath.Join(root, "workspace")
	if err := os.Mkdir(workspace, 0o755); err != nil {
		t.Fatal(err)
	}

	first, err := task.TaskScopeKey("beads", nil, config.RawValues{"beadsDir": workspace})
	if err != nil {
		t.Fatal(err)
	}
	second, err := task.TaskScopeKey("beads", nil, config.RawValues{"beadsDir": filepath.Join(workspace, ".")})
	if err != nil {
		t.Fatal(err)
	}
	if first != second {
		t.Fatalf("equivalent Beads directories produced scopes %q and %q", first, second)
	}
}

func TestBeadsAuthIsNoOpWithoutCreatingCredentials(t *testing.T) {
	home := t.TempDir()
	t.Setenv("RELAY_FLOW_HOME", home)
	if err := task.Auth(context.Background(), "beads", nil, strings.NewReader("")); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(home, "credentials.yaml")); !os.IsNotExist(err) {
		t.Fatalf("Beads auth created credentials: %v", err)
	}
}

func TestBeadsAuthRejectsUnsupportedArgumentsWithoutCreatingCredentials(t *testing.T) {
	home := t.TempDir()
	t.Setenv("RELAY_FLOW_HOME", home)
	if err := task.Auth(context.Background(), "beads", []string{"--token", "secret"}, strings.NewReader("")); err == nil {
		t.Fatal("unsupported Beads auth arguments were accepted")
	}
	if _, err := os.Stat(filepath.Join(home, "credentials.yaml")); !os.IsNotExist(err) {
		t.Fatalf("unsupported Beads auth created credentials: %v", err)
	}
}

func TestIssueNormalizationMapsBeadsFieldsAndClaims(t *testing.T) {
	issue := bdcli.Issue{
		ID:          "demo-a1b2",
		Title:       "Implement adapter",
		Description: "Work on the adapter.",
		Status:      "in_progress",
		IssueType:   "epic",
		Priority:    2,
		Assignee:    "relay-bot",
		Labels:      []string{"wf:implementation", "backend"},
	}
	ticket := issueToTicket(issue)
	if ticket.ID != issue.ID || ticket.Key != issue.ID || ticket.Title != issue.Title {
		t.Fatalf("ticket identity = %+v", ticket)
	}
	if !reflect.DeepEqual(ticket.WorkflowClaims, []string{"wf:implementation"}) {
		t.Fatalf("workflow claims = %v", ticket.WorkflowClaims)
	}
	wantFields := map[string]any{
		"status":      "in_progress",
		"issueType":   "epic",
		"assignee":    "relay-bot",
		"priority":    2,
		"description": "Work on the adapter.",
		"labels":      []string{"wf:implementation", "backend"},
	}
	if !reflect.DeepEqual(ticket.Fields, wantFields) {
		t.Fatalf("normalized fields = %#v, want %#v", ticket.Fields, wantFields)
	}
	if got := normalizeFields(issue); !reflect.DeepEqual(got, wantFields) {
		t.Fatalf("normalizeFields = %#v, want %#v", got, wantFields)
	}
}

func TestExtractWorkflowClaimsKeepsOnlyWorkflowLabels(t *testing.T) {
	labels := []string{"backend", "wf:implementation", "wf:review", "wf:"}
	want := []string{"wf:implementation", "wf:review"}
	if got := extractWorkflowClaims(labels); !reflect.DeepEqual(got, want) {
		t.Fatalf("workflow claims = %v, want %v", got, want)
	}
}

func TestPollDeduplicatesReadyAndClaimedParentsByIssueID(t *testing.T) {
	ready := bdcli.Issue{ID: "demo-a1b2", Title: "ready copy", Status: "open", IssueType: "epic"}
	claimedDuplicate := bdcli.Issue{ID: "demo-a1b2", Title: "claimed copy", Status: "in_progress", IssueType: "epic", Labels: []string{"wf:implementation"}}
	claimedOnly := bdcli.Issue{ID: "demo-c3d4", Title: "claimed only", Status: "blocked", IssueType: "task", Labels: []string{"wf:review"}}
	fake := &pollClient{
		ready:   []bdcli.Issue{ready},
		claimed: []bdcli.Issue{claimedDuplicate, claimedOnly},
	}
	sys := &system{cli: fake}

	got, err := sys.Poll(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("Poll returned %d tickets, want 2: %+v", len(got), got)
	}
	seen := map[string]task.Ticket{}
	for _, ticket := range got {
		seen[ticket.ID] = ticket
	}
	if len(seen) != 2 {
		t.Fatalf("Poll returned duplicate IDs: %+v", got)
	}
	if seen["demo-a1b2"].ID != "demo-a1b2" {
		t.Fatalf("duplicate parent has wrong identity: %+v", seen["demo-a1b2"])
	}
	if seen["demo-c3d4"].Title != "claimed only" {
		t.Fatalf("claimed-only parent missing: %+v", seen["demo-c3d4"])
	}
	if fake.readyCalls != 1 || fake.claimedCalls != 1 {
		t.Fatalf("poll queries = ready:%d claimed:%d, want one each", fake.readyCalls, fake.claimedCalls)
	}
}

func TestNormalizedChildIsExcludedFromPolling(t *testing.T) {
	parent := bdcli.Issue{ID: "demo-parent", Title: "parent", Status: "open", IssueType: "epic"}
	child := bdcli.Issue{ID: "demo-parent.1", Title: "demo-parent:implement", Status: "open", IssueType: "task", Parent: "demo-parent"}
	fake := &pollClient{ready: []bdcli.Issue{child, parent}, claimed: []bdcli.Issue{child}}
	got, err := (&system{cli: fake}).Poll(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].ID != parent.ID {
		t.Fatalf("Poll = %+v, want only top-level parent", got)
	}
}

func TestMailboxHelpersUseStableTitleAndIDs(t *testing.T) {
	if got := mailboxTitle("demo-parent", "implement"); got != "demo-parent:implement" {
		t.Fatalf("mailboxTitle = %q", got)
	}
	issue := bdcli.Issue{ID: "demo-parent.1", Title: "demo-parent:implement"}
	mailbox := issueToMailbox(issue, "implement")
	if mailbox.ID != issue.ID || mailbox.Key != issue.ID || mailbox.Node != "implement" {
		t.Fatalf("issueToMailbox = %+v", mailbox)
	}
}

func TestFindMailboxRejectsDuplicateStableTitles(t *testing.T) {
	children := []bdcli.Issue{
		{ID: "demo-parent.1", Title: "demo-parent:implement"},
		{ID: "demo-parent.2", Title: "demo-parent:implement"},
	}
	if _, err := findMailbox(children, "demo-parent:implement"); err == nil {
		t.Fatal("duplicate stable mailbox title was accepted")
	}
}

func TestFindMailboxReportsMissingAndReturnsExisting(t *testing.T) {
	children := []bdcli.Issue{{ID: "demo-parent.1", Title: "demo-parent:implement"}}
	got, err := findMailbox(children, "demo-parent:implement")
	if err != nil || got.ID != "demo-parent.1" {
		t.Fatalf("findMailbox existing = %+v, %v", got, err)
	}
	if _, err := findMailbox(children, "demo-parent:review"); err == nil {
		t.Fatal("missing mailbox was reported as existing")
	}
}

func TestEnsureMailboxesCreatesMissingAndReconcilesExisting(t *testing.T) {
	existing := bdcli.Issue{
		ID:          "demo-parent.1",
		Title:       "demo-parent:implement",
		Description: "stale description",
		Labels:      []string{"old-label"},
		Parent:      "demo-parent",
	}
	fake := &mailboxClient{children: []bdcli.Issue{existing}}
	sys := &system{cli: fake}
	specs := []task.MailboxSpec{
		{Node: "implement", Title: "demo-parent:implement", Description: "updated work"},
		{Node: "review", Title: "demo-parent:review", Description: "review work"},
	}

	got, err := sys.EnsureMailboxes(context.Background(), task.TicketRef{ID: "demo-parent", Key: "demo-parent"}, "implementation", specs)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != len(specs) {
		t.Fatalf("mailboxes = %+v, want one per spec", got)
	}
	if got["implement"].ID != "demo-parent.1" || got["implement"].Key != "demo-parent.1" {
		t.Fatalf("existing mailbox = %+v", got["implement"])
	}
	if got["review"].ID != "demo-parent.2" || got["review"].Key != "demo-parent.2" {
		t.Fatalf("created mailbox = %+v", got["review"])
	}
	if fake.listChildrenCalls != 1 {
		t.Fatalf("ListChildren calls = %d, want 1", fake.listChildrenCalls)
	}
	if len(fake.created) != 1 || fake.created[0].parentID != "demo-parent" || fake.created[0].title != "demo-parent:review" || fake.created[0].description != "review work" || fake.created[0].label != "wf:implementation" {
		t.Fatalf("created children = %+v", fake.created)
	}
	if len(fake.updates) != 1 {
		t.Fatalf("reconciliation updates = %+v, want one existing-child update", fake.updates)
	}
	update := fake.updates[0]
	if update.issueID != "demo-parent.1" || update.input.Description == nil || *update.input.Description != "updated work" || !reflect.DeepEqual(update.input.AddLabels, []string{"wf:implementation"}) {
		t.Fatalf("existing-child update = %+v", update)
	}
}

func TestEnsureMailboxesReusesStableMailboxOnRevisit(t *testing.T) {
	fake := &mailboxClient{children: []bdcli.Issue{{
		ID: "demo-parent.1", Title: "demo-parent:implement", Parent: "demo-parent",
	}}}
	sys := &system{cli: fake}
	specs := []task.MailboxSpec{{Node: "implement", Title: "demo-parent:implement", Description: "same work"}}
	parent := task.TicketRef{ID: "demo-parent", Key: "demo-parent"}

	first, err := sys.EnsureMailboxes(context.Background(), parent, "implementation", specs)
	if err != nil {
		t.Fatal(err)
	}
	second, err := sys.EnsureMailboxes(context.Background(), parent, "implementation", specs)
	if err != nil {
		t.Fatal(err)
	}
	if first["implement"] != second["implement"] {
		t.Fatalf("mailbox changed on revisit: first=%+v second=%+v", first["implement"], second["implement"])
	}
	if len(fake.created) != 0 {
		t.Fatalf("revisit created new mailbox: %+v", fake.created)
	}
	if len(fake.updates) != 2 {
		t.Fatalf("revisit reconciliation updates = %d, want one per call", len(fake.updates))
	}
}

func TestEnsureMailboxesRejectsDuplicateExistingStableTitles(t *testing.T) {
	fake := &mailboxClient{children: []bdcli.Issue{
		{ID: "demo-parent.1", Title: "demo-parent:implement", Parent: "demo-parent"},
		{ID: "demo-parent.2", Title: "demo-parent:implement", Parent: "demo-parent"},
	}}
	sys := &system{cli: fake}
	_, err := sys.EnsureMailboxes(context.Background(), task.TicketRef{ID: "demo-parent", Key: "demo-parent"}, "implementation", []task.MailboxSpec{{Node: "implement", Title: "demo-parent:implement", Description: "work"}})
	if err == nil {
		t.Fatal("duplicate stable mailbox title was accepted")
	}
	if len(fake.updates) != 0 || len(fake.created) != 0 {
		t.Fatalf("duplicate mailbox caused side effects: updates=%+v creates=%+v", fake.updates, fake.created)
	}
}

func TestEnsureMailboxesConcurrentCallersDoNotCreateDuplicateStableTitles(t *testing.T) {
	fake := newConcurrentMailboxClient()
	sys := &system{cli: fake}
	parent := task.TicketRef{ID: "demo-parent", Key: "demo-parent"}
	specs := []task.MailboxSpec{
		{Node: "implement", Title: "demo-parent:implement", Description: "implement work"},
		{Node: "review", Title: "demo-parent:review", Description: "review work"},
	}

	errs := make(chan error, 2)
	go func() {
		_, err := sys.EnsureMailboxes(context.Background(), parent, "implementation", specs)
		errs <- err
	}()
	select {
	case <-fake.firstListStarted:
	case <-time.After(time.Second):
		t.Fatal("first concurrent caller did not start listing children")
	}
	go func() {
		_, err := sys.EnsureMailboxes(context.Background(), parent, "implementation", specs)
		errs <- err
	}()
	for i := 0; i < 2; i++ {
		if err := <-errs; err != nil {
			t.Fatal(err)
		}
	}

	if fake.createCount() != len(specs) {
		t.Fatalf("created %d mailbox children, want %d", fake.createCount(), len(specs))
	}
	if got := fake.titles(); len(got) != len(specs) || got["demo-parent:implement"] != 1 || got["demo-parent:review"] != 1 {
		t.Fatalf("stable mailbox title counts = %v, want one each", got)
	}
}

func hasString(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

type pollClient struct {
	ready, claimed []bdcli.Issue
	readyCalls     int
	claimedCalls   int
}

func (f *pollClient) Probe(context.Context) error { return nil }

func (f *pollClient) ListReady(context.Context) ([]bdcli.Issue, error) {
	f.readyCalls++
	return append([]bdcli.Issue(nil), f.ready...), nil
}

func (f *pollClient) ListClaimed(context.Context) ([]bdcli.Issue, error) {
	f.claimedCalls++
	return append([]bdcli.Issue(nil), f.claimed...), nil
}

func (*pollClient) ListChildren(context.Context, string) ([]bdcli.Issue, error) {
	return nil, errors.New("unexpected ListChildren call")
}

func (*pollClient) Show(context.Context, string) (bdcli.Issue, error) {
	return bdcli.Issue{}, errors.New("unexpected Show call")
}

func (*pollClient) ListComments(context.Context, string) ([]bdcli.Comment, error) {
	return nil, errors.New("unexpected ListComments call")
}

func (*pollClient) CreateChild(context.Context, string, string, string, string) (bdcli.Issue, error) {
	return bdcli.Issue{}, errors.New("unexpected CreateChild call")
}

func (*pollClient) Update(context.Context, string, bdcli.UpdateInput) error {
	return errors.New("unexpected Update call")
}

func (*pollClient) AddComment(context.Context, string, string) error {
	return errors.New("unexpected AddComment call")
}

var _ bdcli.Client = (*pollClient)(nil)

type mailboxClient struct {
	children          []bdcli.Issue
	listChildrenCalls int
	created           []createChildCall
	updates           []updateCall
}

type createChildCall struct {
	parentID    string
	title       string
	description string
	label       string
}

type updateCall struct {
	issueID string
	input   bdcli.UpdateInput
}

func (f *mailboxClient) Probe(context.Context) error { return nil }

func (*mailboxClient) ListReady(context.Context) ([]bdcli.Issue, error) {
	return nil, errors.New("unexpected ListReady call")
}

func (*mailboxClient) ListClaimed(context.Context) ([]bdcli.Issue, error) {
	return nil, errors.New("unexpected ListClaimed call")
}

func (f *mailboxClient) ListChildren(context.Context, string) ([]bdcli.Issue, error) {
	f.listChildrenCalls++
	return append([]bdcli.Issue(nil), f.children...), nil
}

func (*mailboxClient) Show(context.Context, string) (bdcli.Issue, error) {
	return bdcli.Issue{}, errors.New("unexpected Show call")
}

func (*mailboxClient) ListComments(context.Context, string) ([]bdcli.Comment, error) {
	return nil, errors.New("unexpected ListComments call")
}

func (f *mailboxClient) CreateChild(_ context.Context, parentID, title, description, label string) (bdcli.Issue, error) {
	issue := bdcli.Issue{
		ID:          "demo-parent.2",
		Title:       title,
		Description: description,
		Labels:      []string{label},
		Parent:      parentID,
	}
	f.created = append(f.created, createChildCall{parentID: parentID, title: title, description: description, label: label})
	f.children = append(f.children, issue)
	return issue, nil
}

func (f *mailboxClient) Update(_ context.Context, issueID string, input bdcli.UpdateInput) error {
	f.updates = append(f.updates, updateCall{issueID: issueID, input: input})
	return nil
}

func (*mailboxClient) AddComment(context.Context, string, string) error {
	return errors.New("unexpected AddComment call")
}

var _ bdcli.Client = (*mailboxClient)(nil)

type concurrentMailboxClient struct {
	mu               sync.Mutex
	children         []bdcli.Issue
	created          []createChildCall
	listCount        int
	firstListStarted chan struct{}
	secondList       chan struct{}
	firstListOnce    sync.Once
	secondListOnce   sync.Once
}

func newConcurrentMailboxClient() *concurrentMailboxClient {
	return &concurrentMailboxClient{
		firstListStarted: make(chan struct{}),
		secondList:       make(chan struct{}),
	}
}

func (*concurrentMailboxClient) Probe(context.Context) error { return nil }

func (*concurrentMailboxClient) ListReady(context.Context) ([]bdcli.Issue, error) {
	return nil, errors.New("unexpected ListReady call")
}

func (*concurrentMailboxClient) ListClaimed(context.Context) ([]bdcli.Issue, error) {
	return nil, errors.New("unexpected ListClaimed call")
}

func (f *concurrentMailboxClient) ListChildren(context.Context, string) ([]bdcli.Issue, error) {
	f.mu.Lock()
	f.listCount++
	listCount := f.listCount
	children := append([]bdcli.Issue(nil), f.children...)
	f.mu.Unlock()
	if listCount == 1 {
		f.firstListOnce.Do(func() { close(f.firstListStarted) })
	}
	if listCount >= 2 {
		f.secondListOnce.Do(func() { close(f.secondList) })
	}
	return children, nil
}

func (*concurrentMailboxClient) Show(context.Context, string) (bdcli.Issue, error) {
	return bdcli.Issue{}, errors.New("unexpected Show call")
}

func (*concurrentMailboxClient) ListComments(context.Context, string) ([]bdcli.Comment, error) {
	return nil, errors.New("unexpected ListComments call")
}

func (f *concurrentMailboxClient) CreateChild(_ context.Context, parentID, title, description, label string) (bdcli.Issue, error) {
	// Model the CLI's individual-command serialization without making the
	// list-then-create sequence atomic. Two unprotected callers can therefore
	// both observe the same empty snapshot and create duplicate stable titles.
	select {
	case <-f.secondList:
	case <-time.After(100 * time.Millisecond):
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	issue := bdcli.Issue{ID: "demo-parent." + string(rune('0'+len(f.children)+1)), Title: title, Parent: parentID}
	f.children = append(f.children, issue)
	f.created = append(f.created, createChildCall{parentID: parentID, title: title, description: description, label: label})
	return issue, nil
}

func (*concurrentMailboxClient) Update(context.Context, string, bdcli.UpdateInput) error {
	return nil
}

func (*concurrentMailboxClient) AddComment(context.Context, string, string) error {
	return errors.New("unexpected AddComment call")
}

func (f *concurrentMailboxClient) createCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.created)
}

func (f *concurrentMailboxClient) titles() map[string]int {
	f.mu.Lock()
	defer f.mu.Unlock()
	counts := make(map[string]int, len(f.children))
	for _, child := range f.children {
		counts[child.Title]++
	}
	return counts
}

var _ bdcli.Client = (*concurrentMailboxClient)(nil)
