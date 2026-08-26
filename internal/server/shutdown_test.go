package server_test

import (
	"bytes"
	"encoding/json"
	"net/http"
	"testing"
	"time"
)

// 3.37: graceful shutdown per specs/workflow-repo-management "Graceful
// shutdown is bounded". Uses the handler fixture on a real Unix socket so a
// restart on the same dir resumes the same state.

func TestShutdownStopsAcceptingWithinBound(t *testing.T) {
	dir := t.TempDir()
	c, shutdown := startHandlerOnSocket(t, dir, &fakeServices{})

	if _, err := c.Get("http://unix/workflows"); err != nil {
		t.Fatalf("pre-shutdown request failed: %v", err)
	}

	start := time.Now()
	shutdown()
	if elapsed := time.Since(start); elapsed > 35*time.Second {
		t.Fatalf("shutdown took %v, exceeding the bounded 30s wait", elapsed)
	}

	if _, err := c.Get("http://unix/workflows"); err == nil {
		t.Fatal("server still accepting requests after shutdown")
	}
}

func TestShutdownWaitsForRunningCall(t *testing.T) {
	dir := t.TempDir()
	release := make(chan struct{})
	c, shutdown := startHandlerOnSocket(t, dir, &fakeServices{slowReport: release})

	done := make(chan error, 1)
	go func() {
		resp, err := c.Post("http://unix/reports", "application/json", slowReportBody(t))
		if err == nil {
			resp.Body.Close()
		}
		done <- err
	}()
	time.Sleep(100 * time.Millisecond) // let the slow request start

	stopped := make(chan struct{})
	go func() { shutdown(); close(stopped) }()

	// While the report is in flight, shutdown must be waiting.
	select {
	case <-stopped:
		t.Fatal("shutdown returned while a call was still running")
	case <-time.After(300 * time.Millisecond):
	}

	// Let the running call return; shutdown then completes within the bound.
	close(release)
	start := time.Now()
	select {
	case <-stopped:
	case <-time.After(35 * time.Second):
		t.Fatal("shutdown did not complete after the running call returned")
	}
	if err := <-done; err != nil {
		t.Fatalf("running call interrupted by shutdown: %v", err)
	}
	if elapsed := time.Since(start); elapsed > 35*time.Second {
		t.Fatalf("shutdown exceeded the bounded wait: %v", elapsed)
	}
}

func TestRestartOnSameStateResumes(t *testing.T) {
	// Same backing services across shutdown+restart resume the same state.
	// The fake services instance is shared so its store survives the restart.
	dir := t.TempDir()
	shared := &fakeServices{}
	c1, shutdown1 := startHandlerOnSocket(t, dir, shared)

	// Submit a workflow so there is state. POST /workflows body IS raw YAML.
	yamlBody := "name: basicFlow\nrepos: [payments]\nnodes:\n  start: {onSuccess: [{target: end}]}\n  end: {}\n"
	resp, err := c1.Post("http://unix/workflows", "application/yaml", bytes.NewReader([]byte(yamlBody)))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	shutdown1()

	// Restart on the same dir with the SAME services; the workflow persists.
	// Durable unfinished-run resume across restart is the engine's
	// responsibility and is covered by the goworkflows same-db restart tests
	// (TestVisitIDStableAcrossNormalRestart / crash tests); the server layer
	// resumes serving the same backing state.
	c2, shutdown2 := startHandlerOnSocket(t, dir, shared)
	defer shutdown2()
	code, env := do(t, c2, http.MethodGet, "http://unix/workflows", nil)
	if code != http.StatusOK || !env.OK {
		t.Fatalf("restart list: code=%d env=%+v", code, env)
	}
	var wfs []map[string]any
	if err := json.Unmarshal(env.Data, &wfs); err != nil || len(wfs) != 1 {
		t.Fatalf("state not resumed after restart: data=%s err=%v", env.Data, err)
	}
}

func slowReportBody(t *testing.T) *bytes.Reader {
	t.Helper()
	return bytes.NewReader([]byte(`{"runId":"r","node":"coding","reportId":"s:m","report":{"status":"success","nextStep":"end","summary":{"completed":"x","notCompleted":"None","issuesDiscovered":"None","verification":"x","notes":"None"},"feedback":{"reasonForNextStep":"None","requiredActions":"None","relevantContext":"None","expectedResult":"None"}}}`))
}

// The server.sock ownership/mode is set by the serve startup path (5.5), which
// binds and chmods the socket; server.New returns only an http.Handler and does
// not create sockets. The 0600 assertion lives in cmd/relay-flow/commands_test.go
// (TestServerSocketIsOwnerOnly) which exercises the serve fixture. See 3.29.
