package config_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/rajpopat27/relay-flow/internal/config"
)

// 3.30: WriteAtomic per specs/workflow-repo-management "Config and
// workflow writes are atomically replaced": sibling temp, write+fsync, set
// mode, rename over destination, fsync parent directory.

func TestWriteAtomicCreatesAndReplaces(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "workflow.yaml")

	if err := config.WriteAtomic(path, []byte("v1"), 0644); err != nil {
		t.Fatalf("WriteAtomic create failed: %v", err)
	}
	fi, _ := os.Stat(path)
	if fi.Mode().Perm() != 0644 {
		t.Fatalf("mode = %o, want 0644", fi.Mode().Perm())
	}

	if err := config.WriteAtomic(path, []byte("v2-longer-content"), 0644); err != nil {
		t.Fatalf("WriteAtomic replace failed: %v", err)
	}
	got, _ := os.ReadFile(path)
	if string(got) != "v2-longer-content" {
		t.Fatalf("content = %q, want complete replacement", got)
	}
}

func TestWriteAtomicSetsModeOnReplace(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	if err := config.WriteAtomic(path, []byte("a"), 0600); err != nil {
		t.Fatal(err)
	}
	// Replace with a different mode; the rename must carry the new mode.
	if err := config.WriteAtomic(path, []byte("b"), 0644); err != nil {
		t.Fatal(err)
	}
	fi, _ := os.Stat(path)
	if fi.Mode().Perm() != 0644 {
		t.Fatalf("mode after replace = %o, want 0644", fi.Mode().Perm())
	}
}

func TestWriteAtomicLeavesNoTempOnSuccess(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "f.yaml")
	if err := config.WriteAtomic(path, []byte("x"), 0600); err != nil {
		t.Fatal(err)
	}
	entries, _ := os.ReadDir(dir)
	if len(entries) != 1 {
		names := []string{}
		for _, e := range entries {
			names = append(names, e.Name())
		}
		t.Fatalf("leftover files after atomic write: %v", names)
	}
}

func TestWriteAtomicFailureLeavesPriorFileUsable(t *testing.T) {
	// A failed replacement leaves the previous complete file intact and
	// readable. Make the destination's parent non-writable by its owner so
	// the sibling temp file cannot be created; the existing destination is
	// never touched. (Deterministic for a non-root owner, which the test
	// environment is.)
	dir := t.TempDir()
	path := filepath.Join(dir, "workflow.yaml")
	if err := config.WriteAtomic(path, []byte("good-v1"), 0644); err != nil {
		t.Fatal(err)
	}

	if err := os.Chmod(dir, 0500); err != nil {
		t.Fatal(err)
	}
	err := config.WriteAtomic(path, []byte("bad-v2-partial"), 0644)
	if cerr := os.Chmod(dir, 0700); cerr != nil {
		t.Fatalf("restore dir perms: %v", cerr)
	}

	if err == nil {
		t.Fatal("WriteAtomic into a non-writable dir succeeded; want failure")
	}
	got, rerr := os.ReadFile(path)
	if rerr != nil {
		t.Fatalf("prior file unreadable after failed write: %v", rerr)
	}
	if string(got) != "good-v1" {
		t.Fatalf("prior file = %q, want intact good-v1", got)
	}
}
