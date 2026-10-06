package harness_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/rajpopat27/relay-flow/internal/harness"
	_ "github.com/rajpopat27/relay-flow/internal/harness/opencode"
	_ "github.com/rajpopat27/relay-flow/internal/harness/pi"
)

func TestSelectedHarnessStartupChecksOnlyLocalExecutable(t *testing.T) {
	for _, name := range []string{"pi", "opencode"} {
		t.Run(name, func(t *testing.T) {
			bin := t.TempDir()
			marker := filepath.Join(t.TempDir(), "invoked")
			t.Setenv("HARNESS_INVOKED", marker)
			if err := os.WriteFile(filepath.Join(bin, name), []byte("#!/bin/sh\nprintf '%s' invoked > \"$HARNESS_INVOKED\"\nexit 99\n"), 0o755); err != nil {
				t.Fatal(err)
			}
			t.Setenv("PATH", bin)
			h, err := harness.New(name, nil)
			if err != nil {
				t.Fatal(err)
			}
			if err := harness.ProbeStartup(context.Background(), h); err != nil {
				t.Fatal(err)
			}
			if _, err := os.Stat(marker); !os.IsNotExist(err) {
				t.Fatalf("startup invoked %s instead of resolving it: %v", name, err)
			}
			// No fallback to the other installed harness executable.
			other := "pi"
			if name == "pi" {
				other = "opencode"
			}
			missing, err := harness.New(other, nil)
			if err != nil {
				t.Fatal(err)
			}
			if err := harness.ProbeStartup(context.Background(), missing); err == nil || !strings.Contains(err.Error(), other) {
				t.Fatalf("missing selected harness = %v", err)
			}
			ctx, cancel := context.WithCancel(context.Background())
			cancel()
			if err := harness.ProbeStartup(ctx, h); !errors.Is(err, context.Canceled) {
				t.Fatalf("cancellation = %v", err)
			}
		})
	}
}

type startupHarness struct {
	*fakeHarness
	probe func(context.Context) error
}

func (h *startupHarness) ProbeStartup(ctx context.Context) error { return h.probe(ctx) }

func TestHarnessStartupDispatchRequiresSupportAndPreservesBudget(t *testing.T) {
	if err := harness.ProbeStartup(context.Background(), newFakeHarness()); err == nil {
		t.Fatal("silently skipped selected harness without startup support")
	}
	calls := 0
	failure := errors.New("executable unavailable")
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	deadline, _ := ctx.Deadline()
	h := &startupHarness{fakeHarness: newFakeHarness(), probe: func(ctx context.Context) error {
		calls++
		got, _ := ctx.Deadline()
		if !got.Equal(deadline) {
			t.Fatalf("startup extended caller deadline: %v", got)
		}
		return failure
	}}
	if err := harness.ProbeStartup(ctx, h); !errors.Is(err, failure) || calls != 1 {
		t.Fatalf("startup = %v, calls=%d", err, calls)
	}
}
