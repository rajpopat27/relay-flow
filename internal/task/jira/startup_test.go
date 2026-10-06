package jira

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/rajpopat27/relay-flow/internal/config"
	"github.com/rajpopat27/relay-flow/internal/task"
)

func TestSelectedJiraStartupValidatesSystemCredentialsOnceEvenWithoutRepos(t *testing.T) {
	for _, withRepos := range []bool{false, true} {
		t.Run(map[bool]string{false: "no-repos", true: "many-repos"}[withRepos], func(t *testing.T) {
			root := t.TempDir()
			t.Setenv("RELAY_FLOW_HOME", root)
			var calls atomic.Int32
			s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				user, token, ok := r.BasicAuth()
				if !ok || user != "bot@example.com" || token != "secret" || r.Method != "GET" || r.URL.Path != "/rest/api/3/myself" {
					t.Errorf("unexpected credential request: %s %s", r.Method, r.URL.Path)
					http.Error(w, "invalid request", http.StatusBadRequest)
					return
				}
				_, _ = w.Write([]byte(`{"accountId":"account"}`))
			}))
			defer s.Close()
			path := filepath.Join(root, "credentials.yaml")
			if err := saveCredentials(path, credentials{Site: s.URL, Email: "bot@example.com", Token: "secret"}); err != nil {
				t.Fatal(err)
			}
			before, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			var repos map[string]config.Repo
			if withRepos {
				// Probe must not construct/validate dummy repo mappings or queries.
				repos = map[string]config.Repo{"one": {Path: "/absent/one"}, "two": {Path: "/absent/two"}}
			}
			if err := task.ProbeStartup(context.Background(), "jira", config.RawValues{"assignee": "unverified-assignee"}, repos); err != nil {
				t.Fatal(err)
			}
			if calls.Load() != 1 {
				t.Fatalf("system credential requests = %d", calls.Load())
			}
			after, err := os.ReadFile(path)
			if err != nil || string(after) != string(before) {
				t.Fatalf("startup changed credentials: %v", err)
			}
		})
	}
}

func TestSelectedJiraStartupRejectsMissingAndInvalidCredentials(t *testing.T) {
	root := t.TempDir()
	t.Setenv("RELAY_FLOW_HOME", root)
	if err := task.ProbeStartup(context.Background(), "jira", nil, nil); err == nil || !strings.Contains(err.Error(), "credentials") {
		t.Fatalf("missing credentials = %v", err)
	}
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "secret bot@example.com", http.StatusUnauthorized)
	}))
	defer s.Close()
	if err := saveCredentials(filepath.Join(root, "credentials.yaml"), credentials{Site: s.URL, Email: "bot@example.com", Token: "secret"}); err != nil {
		t.Fatal(err)
	}
	err := task.ProbeStartup(context.Background(), "jira", nil, nil)
	if err == nil || !strings.Contains(err.Error(), "authentication") || strings.Contains(err.Error(), "secret") || strings.Contains(err.Error(), "bot@example.com") {
		t.Fatalf("invalid credentials not safely rejected: %v", err)
	}
}
