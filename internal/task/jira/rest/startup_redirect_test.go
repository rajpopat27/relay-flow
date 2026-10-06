package rest

import (
	"context"
	"net/http"
	"strings"
	"testing"
)

func TestStartupCredentialsDoesNotFollowRedirects(t *testing.T) {
	s := newJiraServer(t, func(w http.ResponseWriter, r *http.Request, _ []byte) {
		if r.URL.Path != "/rest/api/3/myself" {
			t.Errorf("startup followed redirect to %s", r.URL.Path)
			writeJSON(w, map[string]any{"accountId": "account"})
			return
		}
		http.Redirect(w, r, "/unrelated-endpoint", http.StatusFound)
	})
	err := s.client(t).ValidateCredentialsStartup(context.Background())
	if err == nil || !strings.Contains(err.Error(), "HTTP 302") || s.count(http.MethodGet, "/rest/api/3/myself") != 1 || s.count(http.MethodGet, "/unrelated-endpoint") != 0 {
		t.Fatalf("startup redirect = %v", err)
	}
}
