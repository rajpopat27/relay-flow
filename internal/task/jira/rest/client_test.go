package rest

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

type requestRecord struct {
	Method string
	Path   string
	Body   []byte
}

type jiraServer struct {
	t       *testing.T
	server  *httptest.Server
	mu      sync.Mutex
	records []requestRecord
	handle  func(http.ResponseWriter, *http.Request, []byte)
}

func newJiraServer(t *testing.T, handle func(http.ResponseWriter, *http.Request, []byte)) *jiraServer {
	t.Helper()
	s := &jiraServer{t: t, handle: handle}
	s.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Fatal(err)
		}
		user, token, ok := r.BasicAuth()
		if !ok || user != "bot@example.com" || token != "secret" {
			http.Error(w, "bad auth", http.StatusUnauthorized)
			return
		}
		s.mu.Lock()
		s.records = append(s.records, requestRecord{Method: r.Method, Path: r.URL.Path, Body: body})
		s.mu.Unlock()
		handle(w, r, body)
	}))
	t.Cleanup(s.server.Close)
	return s
}

func (s *jiraServer) client(t *testing.T) *HTTPClient {
	t.Helper()
	c, err := New(s.server.URL, "bot@example.com", "secret")
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func (s *jiraServer) count(method, path string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	n := 0
	for _, record := range s.records {
		if record.Method == method && record.Path == path {
			n++
		}
	}
	return n
}

func writeJSON(w http.ResponseWriter, value any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(value)
}

func TestValidateCredentialsUsesMyselfAndBasicAuth(t *testing.T) {
	s := newJiraServer(t, func(w http.ResponseWriter, r *http.Request, _ []byte) {
		if r.Method != http.MethodGet || r.URL.Path != "/rest/api/3/myself" {
			http.NotFound(w, r)
			return
		}
		writeJSON(w, map[string]any{"accountId": "abc"})
	})
	if err := s.client(t).ValidateCredentials(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestNewRejectsNonHTTPSRemoteSite(t *testing.T) {
	if _, err := New("http://jira.example.com", "bot@example.com", "secret"); err == nil {
		t.Fatal("insecure remote Jira site accepted")
	}
}

func TestSearchPaginatesAndRequestsIssueLinks(t *testing.T) {
	s := newJiraServer(t, func(w http.ResponseWriter, r *http.Request, body []byte) {
		if r.URL.Path != "/rest/api/3/search/jql" || r.Method != http.MethodPost {
			http.NotFound(w, r)
			return
		}
		var request struct {
			Fields        []string `json:"fields"`
			NextPageToken string   `json:"nextPageToken"`
		}
		if err := json.Unmarshal(body, &request); err != nil {
			t.Fatal(err)
		}
		if !contains(request.Fields, "issuelinks") {
			t.Fatalf("search fields = %v, missing issuelinks", request.Fields)
		}
		if request.NextPageToken == "" {
			writeJSON(w, map[string]any{"issues": []any{issue("PAY-1")}, "nextPageToken": "next", "isLast": false})
			return
		}
		writeJSON(w, map[string]any{"issues": []any{issue("PAY-2")}, "isLast": true})
	})
	raw, err := s.client(t).Search(context.Background(), "project = PAY")
	if err != nil {
		t.Fatal(err)
	}
	var issues []json.RawMessage
	if err := json.Unmarshal(raw, &issues); err != nil || len(issues) != 2 {
		t.Fatalf("issues = %s, err=%v", raw, err)
	}
	if s.count(http.MethodPost, "/rest/api/3/search/jql") != 2 {
		t.Fatal("search did not use exactly one call per page")
	}
}

func issue(key string) map[string]any {
	return map[string]any{"id": key, "key": key, "fields": map[string]any{
		"summary": key, "status": map[string]any{"name": "To Do"}, "issuetype": map[string]any{"name": "Task"}, "labels": []string{},
	}}
}

func TestCreateSubtasksBatchesAndIncludesADFParentAndLabel(t *testing.T) {
	s := newJiraServer(t, func(w http.ResponseWriter, r *http.Request, body []byte) {
		if r.URL.Path != "/rest/api/3/issue/bulk" {
			http.NotFound(w, r)
			return
		}
		var request struct {
			IssueUpdates []struct {
				Fields map[string]json.RawMessage `json:"fields"`
			} `json:"issueUpdates"`
		}
		if err := json.Unmarshal(body, &request); err != nil {
			t.Fatal(err)
		}
		created := make([]any, len(request.IssueUpdates))
		for i, update := range request.IssueUpdates {
			for _, field := range []string{"parent", "description", "labels"} {
				if len(update.Fields[field]) == 0 {
					t.Fatalf("bulk create missing %s: %s", field, body)
				}
			}
			if !strings.Contains(string(update.Fields["description"]), `"type":"doc"`) {
				t.Fatalf("description is not ADF: %s", update.Fields["description"])
			}
			created[i] = map[string]any{"id": fmt.Sprint(i + 1), "key": fmt.Sprintf("PAY-%d", i+2)}
		}
		writeJSON(w, map[string]any{"issues": created, "errors": []any{}})
	})
	client := s.client(t)
	client.subtaskTypes["PAY"] = "10001"
	specs := make([]SubtaskSpec, 51)
	for i := range specs {
		specs[i] = SubtaskSpec{Title: fmt.Sprintf("PAY-1:n%d", i), Description: "Work:\nDo it"}
	}
	created, err := client.CreateSubtasks(context.Background(), "PAY-1", "PAY", "wf:flow", specs)
	if err != nil {
		t.Fatal(err)
	}
	if len(created) != 51 || s.count(http.MethodPost, "/rest/api/3/issue/bulk") != 2 {
		t.Fatalf("created=%d calls=%d, want 51/2", len(created), s.count(http.MethodPost, "/rest/api/3/issue/bulk"))
	}
}

func TestTransitionCombinesAssigneeAndCachesLookups(t *testing.T) {
	s := newJiraServer(t, func(w http.ResponseWriter, r *http.Request, body []byte) {
		switch {
		case r.URL.Path == "/rest/api/3/search/jql":
			writeJSON(w, map[string]any{"issues": []any{issue("PAY-1")}, "isLast": true})
		case r.URL.Path == "/rest/api/3/issue/PAY-1/transitions" && r.Method == http.MethodGet:
			writeJSON(w, map[string]any{"transitions": []any{map[string]any{
				"id": "21", "to": map[string]any{"name": "In Progress"}, "fields": map[string]any{"assignee": map[string]any{}},
			}}})
		case r.URL.Path == "/rest/api/3/user/assignable/search":
			writeJSON(w, []any{map[string]any{"accountId": "acct", "emailAddress": "worker@example.com"}})
		case r.URL.Path == "/rest/api/3/issue/PAY-1/transitions" && r.Method == http.MethodPost:
			if !strings.Contains(string(body), `"assignee":{"accountId":"acct"}`) {
				t.Fatalf("transition did not include assignee: %s", body)
			}
			w.WriteHeader(http.StatusNoContent)
		default:
			http.NotFound(w, r)
		}
	})
	c := s.client(t)
	if _, err := c.Search(context.Background(), "key = PAY-1"); err != nil {
		t.Fatal(err)
	}
	if err := c.Transition(context.Background(), "PAY-1", "In Progress", "worker@example.com"); err != nil {
		t.Fatal(err)
	}
	if s.count(http.MethodPut, "/rest/api/3/issue/PAY-1/assignee") != 0 {
		t.Fatal("assignment used a separate call despite transition-screen support")
	}
}

func TestTransitionFallsBackToSeparateAssignment(t *testing.T) {
	s := newJiraServer(t, func(w http.ResponseWriter, r *http.Request, _ []byte) {
		switch {
		case r.URL.Path == "/rest/api/3/search/jql":
			writeJSON(w, map[string]any{"issues": []any{issue("PAY-1")}, "isLast": true})
		case r.URL.Path == "/rest/api/3/issue/PAY-1/transitions" && r.Method == http.MethodGet:
			writeJSON(w, map[string]any{"transitions": []any{map[string]any{
				"id": "21", "to": map[string]any{"name": "In Progress"}, "fields": map[string]any{},
			}}})
		case r.URL.Path == "/rest/api/3/user/assignable/search":
			writeJSON(w, []any{map[string]any{"accountId": "acct", "emailAddress": "worker@example.com"}})
		case r.URL.Path == "/rest/api/3/issue/PAY-1/assignee" && r.Method == http.MethodPut:
			w.WriteHeader(http.StatusNoContent)
		case r.URL.Path == "/rest/api/3/issue/PAY-1/transitions" && r.Method == http.MethodPost:
			w.WriteHeader(http.StatusNoContent)
		default:
			http.NotFound(w, r)
		}
	})
	c := s.client(t)
	if _, err := c.Search(context.Background(), "key = PAY-1"); err != nil {
		t.Fatal(err)
	}
	if err := c.Transition(context.Background(), "PAY-1", "In Progress", "worker@example.com"); err != nil {
		t.Fatal(err)
	}
	if s.count(http.MethodPut, "/rest/api/3/issue/PAY-1/assignee") != 1 {
		t.Fatal("transition without assignee field did not use assignment fallback")
	}
}

func TestUpdateMailboxIsOneCall(t *testing.T) {
	s := newJiraServer(t, func(w http.ResponseWriter, r *http.Request, body []byte) {
		if r.Method != http.MethodPut || r.URL.Path != "/rest/api/3/issue/PAY-2" {
			http.NotFound(w, r)
			return
		}
		if !strings.Contains(string(body), `"description"`) || !strings.Contains(string(body), `"labels"`) {
			t.Fatalf("combined update missing fields: %s", body)
		}
		w.WriteHeader(http.StatusNoContent)
	})
	if err := s.client(t).UpdateMailbox(context.Background(), "PAY-2", "Work:\nDo it", "wf:flow"); err != nil {
		t.Fatal(err)
	}
	if s.count(http.MethodPut, "/rest/api/3/issue/PAY-2") != 1 {
		t.Fatal("mailbox reconciliation was not one call")
	}
}

func TestEnsureLabelIsOneAddCall(t *testing.T) {
	s := newJiraServer(t, func(w http.ResponseWriter, r *http.Request, body []byte) {
		if r.Method != http.MethodPut || r.URL.Path != "/rest/api/3/issue/PAY-1" {
			http.NotFound(w, r)
			return
		}
		if !strings.Contains(string(body), `"add":"wf:flow"`) {
			t.Fatalf("label update = %s", body)
		}
		w.WriteHeader(http.StatusNoContent)
	})
	if err := s.client(t).EnsureLabel(context.Background(), "PAY-1", "wf:flow"); err != nil {
		t.Fatal(err)
	}
	if s.count(http.MethodPut, "/rest/api/3/issue/PAY-1") != 1 {
		t.Fatal("claim label was not one call")
	}
}

func TestTransitionAlreadyAtTargetMakesNoTransitionCall(t *testing.T) {
	s := newJiraServer(t, func(w http.ResponseWriter, r *http.Request, _ []byte) {
		if r.URL.Path == "/rest/api/3/search/jql" {
			item := issue("PAY-1")
			item["fields"].(map[string]any)["status"] = map[string]any{"name": "Done"}
			writeJSON(w, map[string]any{"issues": []any{item}, "isLast": true})
			return
		}
		http.NotFound(w, r)
	})
	c := s.client(t)
	if _, err := c.Search(context.Background(), "key = PAY-1"); err != nil {
		t.Fatal(err)
	}
	if err := c.Transition(context.Background(), "PAY-1", "Done", ""); err != nil {
		t.Fatal(err)
	}
	if s.count(http.MethodGet, "/rest/api/3/issue/PAY-1/transitions") != 0 {
		t.Fatal("already-completed issue queried transitions")
	}
}

func TestCommentsUseADFAndParseMarker(t *testing.T) {
	s := newJiraServer(t, func(w http.ResponseWriter, r *http.Request, body []byte) {
		if r.URL.Path != "/rest/api/3/issue/PAY-2/comment" {
			http.NotFound(w, r)
			return
		}
		if r.Method == http.MethodGet {
			writeJSON(w, map[string]any{"comments": []any{map[string]any{"body": ADF("summary\n\n<!-- visit:summary -->")}}, "startAt": 0, "total": 1})
			return
		}
		if !strings.Contains(string(body), `"type":"doc"`) {
			t.Fatalf("comment is not ADF: %s", body)
		}
		w.WriteHeader(http.StatusCreated)
	})
	c := s.client(t)
	comments, err := c.ListComments(context.Background(), "PAY-2")
	if err != nil || len(comments) != 1 || !strings.Contains(comments[0], "visit:summary") {
		t.Fatalf("comments=%v err=%v", comments, err)
	}
	if err := c.AddComment(context.Background(), "PAY-2", "SUMMARY:\nDone"); err != nil {
		t.Fatal(err)
	}
}

func TestRetries429WithoutExposingCredentials(t *testing.T) {
	calls := 0
	s := newJiraServer(t, func(w http.ResponseWriter, r *http.Request, _ []byte) {
		calls++
		if calls == 1 {
			w.Header().Set("Retry-After", "0")
			http.Error(w, "limited", http.StatusTooManyRequests)
			return
		}
		writeJSON(w, map[string]any{"accountId": "abc"})
	})
	if err := s.client(t).ValidateCredentials(context.Background()); err != nil {
		t.Fatal(err)
	}
	if calls != 2 {
		t.Fatalf("calls=%d, want one retry", calls)
	}
}

func TestErrorRedactsToken(t *testing.T) {
	s := newJiraServer(t, func(w http.ResponseWriter, _ *http.Request, _ []byte) {
		http.Error(w, "rejected secret for bot@example.com", http.StatusUnauthorized)
	})
	err := s.client(t).ValidateCredentials(context.Background())
	if err == nil || strings.Contains(err.Error(), "secret") || strings.Contains(err.Error(), "bot@example.com") {
		t.Fatalf("error did not redact Jira credentials: %v", err)
	}
}

func TestADFFormatsHeadingsListsAndRoundTripsMarker(t *testing.T) {
	doc := ADF("SUMMARY\n\nNode: implement\n\n- first\n- second\n\n```\ngo test ./...\n```\n\n<!-- visit:summary -->")
	raw, err := json.Marshal(doc)
	if err != nil {
		t.Fatal(err)
	}
	text := string(raw)
	for _, want := range []string{`"type":"heading"`, `"type":"bulletList"`, `"type":"codeBlock"`, `"type":"strong"`} {
		if !strings.Contains(text, want) {
			t.Fatalf("ADF %s missing %s", text, want)
		}
	}
	if got := ADFText(raw); !strings.Contains(got, "visit:summary") {
		t.Fatalf("ADF marker round trip = %q", got)
	}
}

func contains(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}
