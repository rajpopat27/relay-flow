package beads

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

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
