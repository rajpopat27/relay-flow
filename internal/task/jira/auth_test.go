package jira

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/rajpopat27/relay-flow/internal/config"
	"github.com/rajpopat27/relay-flow/internal/task"
)

func authServer(t *testing.T, valid bool) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		user, token, ok := r.BasicAuth()
		if !ok || user != "bot@example.com" || token != "secret" || !valid {
			http.Error(w, "invalid bot@example.com secret", http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/rest/api/3/myself":
			fmt.Fprint(w, `{"accountId":"bot"}`)
		case "/rest/api/3/user/assignable/search":
			fmt.Fprint(w, `[{"accountId":"bot","emailAddress":"bot@example.com","displayName":"Relay Bot"}]`)
		case "/rest/api/3/project/PAY/statuses":
			fmt.Fprint(w, `[{"id":"1","subtask":true,"statuses":[{"name":"To Do"},{"name":"In Progress"},{"name":"Done"}]}]`)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(server.Close)
	return server
}

func TestAuthWritesLoadsAndNewUsesJiraOwnedCredentials(t *testing.T) {
	root := t.TempDir()
	t.Setenv("RELAY_FLOW_HOME", root)
	configPath := filepath.Join(root, "config.yaml")
	if err := config.SaveMachine(configPath, &config.Machine{TaskPlugin: "jira"}); err != nil {
		t.Fatal(err)
	}
	server := authServer(t, true)
	if err := task.Auth(context.Background(), "jira", []string{
		"--site", server.URL, "--email", "bot@example.com", "--token", "secret",
	}, strings.NewReader("")); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, "credentials.yaml")
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("credentials mode = %o, want 600", info.Mode().Perm())
	}
	got, err := loadCredentials(path)
	if err != nil {
		t.Fatal(err)
	}
	if got.Site != server.URL || got.Email != "bot@example.com" || got.Token != "secret" {
		t.Fatalf("credentials = %+v", got)
	}
	machine, err := config.LoadMachine(configPath)
	if err != nil {
		t.Fatal(err)
	}
	if machine.TaskConfig["assignee"] != "bot@example.com" {
		t.Fatalf("default assignee = %v", machine.TaskConfig["assignee"])
	}
	if _, err := task.New(context.Background(), "jira", task.RepoSpec{
		Name: "payments", RootConfig: machine.TaskConfig,
		RepoConfig: config.RawValues{"project": "PAY", "component": "api"},
	}); err != nil {
		t.Fatalf("task.New did not load Jira-owned credentials: %v", err)
	}
}

func TestAuthPreservesConfiguredAndLaterRemovedAssignee(t *testing.T) {
	for _, tc := range []struct {
		name       string
		firstAuth  bool
		taskConfig config.RawValues
		want       string
		wantKey    bool
	}{
		{name: "configured on first auth", firstAuth: true, taskConfig: config.RawValues{"assignee": "configured@example.com"}, want: "configured@example.com", wantKey: true},
		{name: "configured on later auth", taskConfig: config.RawValues{"assignee": "configured@example.com"}, want: "configured@example.com", wantKey: true},
		{name: "removed on later auth", taskConfig: config.RawValues{}, wantKey: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			t.Setenv("RELAY_FLOW_HOME", root)
			if err := config.SaveMachine(filepath.Join(root, "config.yaml"), &config.Machine{TaskPlugin: "jira", TaskConfig: tc.taskConfig}); err != nil {
				t.Fatal(err)
			}
			if !tc.firstAuth {
				if err := saveCredentials(filepath.Join(root, "credentials.yaml"), credentials{Site: "https://old.example.com", Email: "old@example.com", Token: "old"}); err != nil {
					t.Fatal(err)
				}
			}
			server := authServer(t, true)
			if err := auth(context.Background(), []string{"--site", server.URL, "--email", "bot@example.com", "--token", "secret"}, strings.NewReader("")); err != nil {
				t.Fatal(err)
			}
			machine, err := config.LoadMachine(filepath.Join(root, "config.yaml"))
			if err != nil {
				t.Fatal(err)
			}
			got, exists := machine.TaskConfig["assignee"]
			if exists != tc.wantKey || (exists && got != tc.want) {
				t.Fatalf("assignee = %v, exists = %v; want %q, exists = %v", got, exists, tc.want, tc.wantKey)
			}
		})
	}
}

func TestAuthRejectsInvalidCredentialsWithoutWriting(t *testing.T) {
	root := t.TempDir()
	t.Setenv("RELAY_FLOW_HOME", root)
	server := authServer(t, false)
	err := auth(context.Background(), []string{
		"--site", server.URL, "--email", "bot@example.com", "--token", "secret",
	}, strings.NewReader(""))
	if err == nil {
		t.Fatal("invalid credentials accepted")
	}
	if strings.Contains(err.Error(), "secret") || strings.Contains(err.Error(), "bot@example.com") {
		t.Fatalf("credential error exposed a secret: %v", err)
	}
	if _, statErr := os.Stat(filepath.Join(root, "credentials.yaml")); !os.IsNotExist(statErr) {
		t.Fatalf("invalid auth wrote credentials: %v", statErr)
	}
}

func TestLoadCredentialsRejectsPermissionsAndMalformedSecrets(t *testing.T) {
	t.Run("permissions", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "credentials.yaml")
		if err := os.WriteFile(path, []byte("site: https://jira.example.com\nemail: bot@example.com\ntoken: secret\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		if _, err := loadCredentials(path); err == nil || !strings.Contains(err.Error(), "0600") {
			t.Fatalf("permission error = %v", err)
		}
	})
	t.Run("redaction", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "credentials.yaml")
		if err := os.WriteFile(path, []byte("token: [super-secret"), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := loadCredentials(path); err == nil || strings.Contains(err.Error(), "super-secret") {
			t.Fatalf("malformed credential error = %v", err)
		}
	})
}
