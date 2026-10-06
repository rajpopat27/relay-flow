package bdcli

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestProbeDistinguishesSelectedBDLaunchFailure(t *testing.T) {
	bin := t.TempDir()
	path := filepath.Join(bin, "bd")
	if err := os.WriteFile(path, []byte("#!/nonexistent/interpreter\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin)
	if err := CheckExecutable(); err != nil {
		t.Fatalf("executable resolution unexpectedly launched bd: %v", err)
	}
	err := New(t.TempDir(), t.TempDir()).Probe(context.Background())
	var commandErr *CommandError
	if !errors.As(err, &commandErr) || !strings.Contains(err.Error(), "launch failure") || !strings.Contains(err.Error(), path) {
		t.Fatalf("launch failure = %v", err)
	}
}
