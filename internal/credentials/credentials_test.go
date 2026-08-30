package credentials_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/rajpopat27/relay-flow/internal/credentials"
)

func TestSaveLoadOwnerOnly(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "credentials.yaml")
	want := credentials.File{Jira: credentials.Jira{Email: "bot@example.com", Token: "secret"}}
	if err := credentials.Save(path, want); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("credentials mode = %o, want 600", info.Mode().Perm())
	}
	got, err := credentials.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Fatalf("credentials = %+v, want email/token", got)
	}
}

func TestLoadErrorDoesNotExposeSecretContent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "credentials.yaml")
	if err := os.WriteFile(path, []byte("jira: [super-secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := credentials.Load(path)
	if err == nil {
		t.Fatal("invalid credentials parsed")
	}
	if strings.Contains(err.Error(), "super-secret") {
		t.Fatalf("error exposed credential content: %v", err)
	}
}

func TestLoadRejectsCredentialsReadableByOthers(t *testing.T) {
	path := filepath.Join(t.TempDir(), "credentials.yaml")
	if err := os.WriteFile(path, []byte("jira:\n  email: bot@example.com\n  token: secret\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := credentials.Load(path); err == nil || !strings.Contains(err.Error(), "0600") {
		t.Fatalf("insecure credentials error = %v", err)
	}
}
