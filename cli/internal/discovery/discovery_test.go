package discovery

import (
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
	want := filepath.Join(tmp, ".relay", "server.sock")
	if p != want {
		t.Fatalf("SocketPath=%q, want %q", p, want)
	}
}

func TestServerLockPath(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)
	p, err := ServerLockPath()
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(tmp, ".relay", "server.lock")
	if p != want {
		t.Fatalf("ServerLockPath=%q, want %q", p, want)
	}
}

func TestAcquireServerLock_SingleInstance(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)
	release, err := AcquireServerLock()
	if err != nil {
		t.Fatalf("first AcquireServerLock: %v", err)
	}
	defer release()
	// Second acquire while first is held must fail immediately.
	if _, err := AcquireServerLock(); err == nil {
		t.Fatal("second AcquireServerLock should fail while first is held")
	}
}

func TestAcquireServerLock_ReacquireAfterRelease(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)
	release, err := AcquireServerLock()
	if err != nil {
		t.Fatal(err)
	}
	release()
	// After release (process exit), lock is free again — no stale state.
	release2, err := AcquireServerLock()
	if err != nil {
		t.Fatalf("reacquire after release: %v", err)
	}
	release2()
}
