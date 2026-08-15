package discovery

import (
	"os"
	"path/filepath"
	"testing"
)

func TestSocketPath(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)
	p, err := SocketPath()
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(tmp, ".orca-jira-loop", "server.sock")
	if p != want {
		t.Fatalf("SocketPath=%q, want %q", p, want)
	}
}

func TestServerPidPath(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)
	p, err := ServerPidPath()
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(tmp, ".orca-jira-loop", "server.pid")
	if p != want {
		t.Fatalf("ServerPidPath=%q, want %q", p, want)
	}
	if _, err := os.Stat(filepath.Dir(p)); err != nil {
		t.Fatal("dir not created")
	}
}
