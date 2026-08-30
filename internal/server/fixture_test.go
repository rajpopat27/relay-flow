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

	"github.com/rajpopat27/relay-flow/internal/repo"
	"github.com/rajpopat27/relay-flow/internal/run"
	"github.com/rajpopat27/relay-flow/internal/runner"
	"github.com/rajpopat27/relay-flow/internal/server"
	"github.com/rajpopat27/relay-flow/internal/workflow"
)

// fakeServices backs the handler with fakes behind the documented service
// seams. activeWorkflows seeds workflows with an active run (drives 409);
// failRepos forces a 500; slowReport simulates a long-running report call.
type fakeServices struct {
	activeWorkflows  map[string]bool
	failRepos        bool
	slowReport       chan struct{}
	workflows        map[string]*workflow.Workflow
	repos            map[string]repo.Info
	runs             []run.Run
	shutdownCh       chan struct{}
	registrations    []run.NodeRuntimeRegistration
	runtimeAck       run.NodeRuntimeRegistrationAck
	processedReports map[string]bool
	submittedReports int
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
func (f *fakeServices) ListWorkflows(context.Context) ([]*workflow.Workflow, error) {
	out := []*workflow.Workflow{}
	for _, w := range f.workflows {
		out = append(out, w)
	}
	return out, nil
}
func (f *fakeServices) RemoveWorkflow(_ context.Context, name string) error {
	if f.activeWorkflows[name] {
		return errActiveRun{name}
	}
	if _, ok := f.workflows[name]; !ok {
		return errNotFound{name}
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
	f.submittedReports++
	if f.slowReport != nil {
		select {
		case <-f.slowReport:
		case <-ctx.Done():
		}
	}
	return run.ReportAck{Accepted: true}, nil
}

func (f *fakeServices) HasProcessedReport(_ context.Context, id run.ID, reportID string) (bool, error) {
	return f.processedReports[string(id)+":"+reportID], nil
}

func (f *fakeServices) RegisterNodeSession(_ context.Context, registration run.NodeRuntimeRegistration) (run.NodeRuntimeRegistrationAck, error) {
	f.registrations = append(f.registrations, registration)
	if f.runtimeAck != (run.NodeRuntimeRegistrationAck{}) {
		return f.runtimeAck, nil
	}
	return run.NodeRuntimeRegistrationAck{Accepted: true}, nil
}

func (f *fakeServices) GetRepo(_ context.Context, name string) (repo.Info, error) {
	if f.failRepos {
		return repo.Info{}, errUnexpected{}
	}
	if info, ok := f.repos[name]; ok {
		return info, nil
	}
	return repo.Info{}, errNotFound{name}
}

func (f *fakeServices) ListRepos(context.Context) ([]repo.Info, error) {
	out := []repo.Info{}
	for _, r := range f.repos {
		out = append(out, r)
	}
	return out, nil
}

func (f *fakeServices) DiscoverRepos(context.Context) ([]runner.RepoCandidate, error) {
	return []runner.RepoCandidate{{Name: "payments", Path: "/srv/payments"}}, nil
}

func (f *fakeServices) TaskFields(context.Context) ([]string, error) {
	return []string{"status", "labels"}, nil
}

func (f *fakeServices) RegisterRepo(_ context.Context, input repo.RegisterInput) (repo.Info, error) {
	if f.repos == nil {
		f.repos = map[string]repo.Info{}
	}
	info := repo.Info{Name: input.Name, Path: input.Path, TaskConfig: input.TaskConfig}
	f.repos[input.Name] = info
	return info, nil
}

func (f *fakeServices) RemoveRepo(_ context.Context, name string) error {
	if _, ok := f.repos[name]; !ok {
		return errNotFound{name}
	}
	delete(f.repos, name)
	return nil
}

func (f *fakeServices) Shutdown(context.Context) error {
	if f.shutdownCh != nil {
		select {
		case <-f.shutdownCh:
		default:
			close(f.shutdownCh)
		}
	}
	return nil
}

// errActiveRun wraps server.ErrConflict so the handler maps it to 409 per
// the documented typed-error mapping (docs/structs-methods-interfaces.md
// routes + 'API responses and status codes are stable').
type errActiveRun struct{ name string }

func (e errActiveRun) Error() string { return "workflow has active run: " + e.name }
func (e errActiveRun) Unwrap() error { return server.ErrConflict }

// errNotFound wraps server.ErrNotFound so the handler maps it to 404.
type errNotFound struct{ name string }

func (e errNotFound) Error() string { return "not found: " + e.name }
func (e errNotFound) Unwrap() error { return server.ErrNotFound }

type errUnexpected struct{}

func (errUnexpected) Error() string { return "unexpected failure" }

// startHandler builds the http.Handler via server.New(deps) and serves it on
// an in-process listener; returns a client and a shutdown func.
func startHandler(t *testing.T, svc *fakeServices) (*http.Client, func()) {
	t.Helper()
	if svc.workflows == nil {
		svc.workflows = map[string]*workflow.Workflow{}
	}
	if svc.repos == nil {
		svc.repos = map[string]repo.Info{}
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
	if svc.repos == nil {
		svc.repos = map[string]repo.Info{}
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
