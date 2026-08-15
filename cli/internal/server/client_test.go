package server

import (
	"net"
	"path/filepath"
	"strings"
	"testing"
)

func TestClient_SubmitListRemove_RoundTrip(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)
	sock := startRealServer(t, false)

	c := &Client{Socket: sock}
	if err := c.Submit("workflow", "/repo", []byte(validYAML)); err != nil {
		t.Fatalf("Submit: %v", err)
	}
	infos, err := c.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(infos) != 1 || infos[0].ConfigName != "workflow" {
		t.Fatalf("List=%v", infos)
	}
	if err := c.Remove("workflow"); err != nil {
		t.Fatalf("Remove: %v", err)
	}
	infos, _ = c.List()
	if len(infos) != 0 {
		t.Fatalf("after remove List=%v", infos)
	}
}

func TestClient_RemoveUnknown_Errors(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)
	sock := startRealServer(t, false)
	c := &Client{Socket: sock}
	if err := c.Remove("ghost"); err == nil {
		t.Fatal("expected error removing unknown config")
	}
}

func TestClient_ServerDown_FriendlyError(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)
	c := &Client{Socket: tmp + "/absent.sock"}
	_, err := c.List()
	if err == nil {
		t.Fatal("expected error when server not running")
	}
	if got := err.Error(); !contains(got, "serve") {
		t.Fatalf("error should hint at `serve`, got %q", got)
	}
}

// startRealServer runs a Server with fake deps on a temp socket, returns path.
func startRealServer(t *testing.T, dryRun bool) string {
	t.Helper()
	deps, _ := fakeDeps()
	srv := New(dryRun, deps)
	sock := filepath.Join(t.TempDir(), "srv.sock")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	go srv.Serve(ln)
	t.Cleanup(func() { srv.Shutdown(); ln.Close() })
	return sock
}

func contains(s, sub string) bool { return strings.Contains(s, sub) }
