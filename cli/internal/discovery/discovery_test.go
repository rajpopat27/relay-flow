package discovery

import (
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
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

func TestStopPidFile_NoProcess(t *testing.T) {
	tmp := t.TempDir()
	pidPath := filepath.Join(tmp, "x.pid")
	if err := StopPidFile(pidPath); err == nil {
		t.Fatal("expected error for missing pid file")
	}
}

func TestStopPidFile_StopsProcess(t *testing.T) {
	tmp := t.TempDir()
	pidPath := filepath.Join(tmp, "x.pid")
	// Sleep process as stand-in for a running server.
	cmd := exec.Command("sleep", "60")
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer cmd.Process.Kill()
	// Reap the child on exit so signal-0 liveness checks see it gone.
	go cmd.Wait()
	if err := os.WriteFile(pidPath, []byte(strconv.Itoa(cmd.Process.Pid)), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := StopPidFile(pidPath); err != nil {
		t.Fatalf("StopPidFile: %v", err)
	}
	if _, err := os.Stat(pidPath); !os.IsNotExist(err) {
		t.Fatal("pid file not released after stop")
	}
}

func TestServerLockPath(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)
	p, err := ServerLockPath()
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(tmp, ".orca-jira-loop", "server.lock")
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
