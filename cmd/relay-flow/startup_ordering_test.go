package main

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/rajpopat27/relay-flow/internal/server"
)

func TestServeDoesNotOpenExecutionStateOrPublishReadinessWhileProbeIsPending(t *testing.T) {
	f := newPrerequisiteFixture(t, nil)
	entered, release := make(chan struct{}), make(chan struct{})
	f.hrn.probe = func(ctx context.Context) error {
		close(entered)
		select {
		case <-release:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	before, err := os.ReadFile(f.paths.Database)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- serveRoot(ctx, f.paths, false) }()
	finished := false
	t.Cleanup(func() {
		cancel()
		if !finished {
			select {
			case <-done:
			case <-time.After(10 * time.Second):
				t.Error("serveRoot did not stop")
			}
		}
	})
	select {
	case <-entered:
	case err := <-done:
		finished = true
		t.Fatalf("server exited before probe: %v", err)
	case <-time.After(time.Second):
		t.Fatal("startup probe did not begin")
	}
	client := server.NewClient(f.paths.Socket)
	if serverResponding(client, 100*time.Millisecond) {
		t.Fatal("pending prerequisite was mistaken for readiness")
	}
	if _, err := os.Stat(f.paths.Socket); !os.IsNotExist(err) {
		t.Fatalf("socket opened before prerequisite completion: %v", err)
	}
	after, err := os.ReadFile(f.paths.Database)
	if err != nil || string(after) != string(before) {
		t.Fatalf("execution state opened/migrated before prerequisites: %v", err)
	}
	close(release)
	waitForServerOrError(t, client, done)
	if err := client.Stop(context.Background()); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		finished = true
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("ready server did not stop")
	}
}
