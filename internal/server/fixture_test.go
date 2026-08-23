package server_test

// Test-local server fixture using the settled seam (d): a thin
// server.New(deps) http.Handler driven over in-process HTTP. Deps are the
// documented consumer services (workflow/repo/run/report), faked here.

import (
	"context"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"testing"

	"github.com/rajpopat27/relay-flow/internal/run"
	"github.com/rajpopat27/relay-flow/internal/server"
	"github.com/rajpopat27/relay-flow/internal/workflow"
)

// fakeServices backs the handler with fakes behind the documented service
// seams. activeWorkflows seeds workflows with an active run (drives 409);
// failRepos forces a 500; slowReport simulates a long-running report call.
type fakeServices struct {
	activeWorkflows map[string]bool
	failRepos       bool
	slowReport      chan struct{}
	workflows       map[string]*workflow.Workflow
	runs            []run.Run
}

func (f *fakeServices) SubmitWorkflow(_ context.Context, yaml []byte) (*workflow.Workflow, error) {
	wf, err := workflow.Parse("x", yaml)
	if err != nil {
		return nil, err
	}
	if f.activeWorkflows[wf.Name] {
		return nil, errActiveRun{wf.Name}
	}
	f.workflows[wf.Name] = wf
	return wf, nil
}
func (f *fakeServices) GetWorkflow(_ context.Context, name string) (*workflow.Workflow, error) {
	if wf, ok := f.workflows[name]; ok {
		return wf, nil
	}
	return nil, errNotFound{name}
}
func (f *fakeServices) ListWorkflows(context.Context) []*workflow.Workflow {
	out := []*workflow.Workflow{}
	for _, w := range f.workflows {
		out = append(out, w)
	}
	return out
}
func (f *fakeServices) RemoveWorkflow(_ context.Context, name string) error {
	if f.activeWorkflows[name] {
		return errActiveRun{name}
	}
	delete(f.workflows, name)
	return nil
}
func (f *fakeServices) ListRuns(context.Context, run.Filter) ([]run.Run, error) { return f.runs, nil }
func (f *fakeServices) GetRunByTicket(_ context.Context, ticket string) (run.Run, error) {
	for _, r := range f.runs {
		if r.Ticket.Key == ticket {
			return r, nil
		}
	}
	return run.Run{}, errNotFound{ticket}
}
func (f *fakeServices) CancelRun(_ context.Context, ticket, _ string) error {
	for i, r := range f.runs {
		if r.Ticket.Key == ticket {
			f.runs[i].State = run.StateCanceled
			return nil
		}
	}
	return errNotFound{ticket}
}

func (f *fakeServices) SubmitReport(ctx context.Context, _ run.ReportRequest) (run.ReportAck, error) {
	if f.slowReport != nil {
		select {
		case <-f.slowReport:
		case <-ctx.Done():
		}
	}
	return run.ReportAck{Accepted: true}, nil
}
func (f *fakeServices) GetRepo(_ context.Context, name string) error {
	if f.failRepos {
		return errUnexpected{}
	}
	return errNotFound{name}
}

func (f *fakeServices) ListRepos(context.Context) ([]string, error) { return []string{"payments"}, nil }
func (f *fakeServices) DiscoverRepos(context.Context) ([]string, error) {
	return []string{"payments"}, nil
}
func (f *fakeServices) RegisterRepo(_ context.Context, name string) error { return nil }
func (f *fakeServices) RemoveRepo(_ context.Context, name string) error {
	return errNotFound{name}
}

type errActiveRun struct{ name string }

func (e errActiveRun) Error() string { return "workflow has active run: " + e.name }

type errNotFound struct{ name string }

func (e errNotFound) Error() string { return "not found: " + e.name }

type errUnexpected struct{}

func (errUnexpected) Error() string { return "unexpected failure" }

// startHandler builds the http.Handler via server.New(deps) and serves it on
// an in-process listener; returns a client and a shutdown func.
func startHandler(t *testing.T, svc *fakeServices) (*http.Client, func()) {
	t.Helper()
	if svc.workflows == nil {
		svc.workflows = map[string]*workflow.Workflow{}
	}
	h := server.New(svc) // seam (d): thin http.Handler over the services

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	srv := &http.Server{Handler: h}
	go srv.Serve(ln)

	client := &http.Client{Transport: &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, "tcp", ln.Addr().String())
		},
	}}
	cleanup := func() {
		_ = srv.Shutdown(context.Background())
	}
	return client, cleanup
}

// startHandlerOnSocket serves the handler on a real Unix socket inside dir so
// the same dir can be reused across a restart (same-state resume). The server
// owns socket creation/mode; cleanup shuts down and unlinks the socket.
func startHandlerOnSocket(t *testing.T, dir string, svc *fakeServices) (*http.Client, func()) {
	t.Helper()
	if svc.workflows == nil {
		svc.workflows = map[string]*workflow.Workflow{}
	}
	h := server.New(svc)
	sock := filepath.Join(dir, "server.sock")
	_ = os.Remove(sock) // clear any stale socket before (re)binding
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	srv := &http.Server{Handler: h}
	go srv.Serve(ln)
	client := &http.Client{Transport: &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, "unix", sock)
		},
	}}
	cleanup := func() {
		_ = srv.Shutdown(context.Background())
		_ = ln.Close()
		_ = os.Remove(sock)
	}
	return client, cleanup
}
