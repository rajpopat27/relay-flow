// Package server translates Unix-socket JSON to services. It contains no
// Jira, Orca, workflow graph, or SQLite logic; handlers call consumer
// services behind Deps and return the standard JSON envelope.
package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/rajpopat27/relay-flow/internal/repo"
	"github.com/rajpopat27/relay-flow/internal/run"
	"github.com/rajpopat27/relay-flow/internal/runner"
	"github.com/rajpopat27/relay-flow/internal/workflow"
)

// Deps are the consumer services the handlers call. The composition root
// (section 5) supplies the real implementations; tests supply fakes.
// Signatures match docs/structs-methods-interfaces.md (Client) exactly.
type Deps interface {
	// Workflows
	SubmitWorkflow(ctx context.Context, yaml []byte) (*workflow.Workflow, error)
	GetWorkflow(ctx context.Context, name string) (*workflow.Workflow, error)
	ListWorkflows(ctx context.Context) ([]*workflow.Workflow, error)
	RemoveWorkflow(ctx context.Context, name string) error

	// Runs
	ListRuns(ctx context.Context, filter run.Filter) ([]run.Run, error)
	GetRunByTicket(ctx context.Context, ticket string) (run.Run, error)
	CancelRun(ctx context.Context, ticket, reason string) error

	// Reports
	SubmitReport(ctx context.Context, report run.ReportRequest) (run.ReportAck, error)

	// Repos
	DiscoverRepos(ctx context.Context) ([]runner.RepoCandidate, error)
	TaskFields(ctx context.Context) ([]string, error)
	RegisterRepo(ctx context.Context, input repo.RegisterInput) (repo.Info, error)
	ListRepos(ctx context.Context) ([]repo.Info, error)
	GetRepo(ctx context.Context, name string) (repo.Info, error)
	RemoveRepo(ctx context.Context, name string) error

	// Shutdown requests graceful server shutdown; the serve command
	// supplies the concrete hook (signal the main loop to exit).
	Shutdown(ctx context.Context) error
}

// Error classification: services return typed errors (or errors wrapping
// them) so handlers map to stable HTTP status codes without any
// Jira/Orca/graph/SQLite knowledge.
var (
	// ErrNotFound maps to 404.
	ErrNotFound = errors.New("not found")
	// ErrConflict maps to 409.
	ErrConflict = errors.New("conflict")
	// ErrInvalid maps to 400.
	ErrInvalid = errors.New("invalid")
)

type envelope struct {
	OK    bool     `json:"ok"`
	Data  any      `json:"data,omitempty"`
	Error *errBody `json:"error,omitempty"`
}

type errBody struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

// New builds the HTTP handler over the given services.
func New(deps Deps) http.Handler {
	s := &server{deps: deps}
	mux := http.NewServeMux()
	mux.HandleFunc("/stop", s.handleStop)
	mux.HandleFunc("/workflows", s.handleWorkflows)
	mux.HandleFunc("/workflows/", s.handleWorkflowByName)
	mux.HandleFunc("/repos/discover", s.handleReposDiscover)
	mux.HandleFunc("/repos/task-fields", s.handleRepoTaskFields)
	mux.HandleFunc("/repos", s.handleRepos)
	mux.HandleFunc("/repos/", s.handleRepoByName)
	mux.HandleFunc("/reports", s.handleReports)
	mux.HandleFunc("/runs", s.handleRuns)
	mux.HandleFunc("/runs/by-ticket/", s.handleRunByTicket)
	return mux
}

type server struct {
	deps Deps
}

// --- envelope helpers ---

func writeOK(w http.ResponseWriter, status int, data any) {
	writeEnv(w, status, envelope{OK: true, Data: data})
}

func writeErr(w http.ResponseWriter, status int, code, msg string) {
	writeEnv(w, status, envelope{OK: false, Error: &errBody{Code: code, Message: msg}})
}

func writeEnv(w http.ResponseWriter, status int, env envelope) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(env)
}

// mapErr translates a service error into HTTP status + lowerCamel code.
// Services return errors wrapping ErrNotFound/ErrConflict/ErrInvalid;
// anything else is an unexpected 500.
func mapErr(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, ErrNotFound):
		writeErr(w, http.StatusNotFound, "notFound", err.Error())
	case errors.Is(err, ErrConflict):
		writeErr(w, http.StatusConflict, "conflict", err.Error())
	case errors.Is(err, ErrInvalid):
		writeErr(w, http.StatusBadRequest, "invalid", err.Error())
	default:
		writeErr(w, http.StatusInternalServerError, "internalError", err.Error())
	}
}

func methodOnly(w http.ResponseWriter, r *http.Request, allowed ...string) bool {
	for _, m := range allowed {
		if r.Method == m {
			return true
		}
	}
	writeErr(w, http.StatusMethodNotAllowed, "methodNotAllowed", "method not allowed")
	return false
}

// decodeStrict unmarshals body as JSON, rejecting unknown fields. Note:
// encoding/json matches keys case-insensitively, so DisallowUnknownFields
// alone does not reject wrong-cased keys; handlers that require strict
// lowerCamel keys must pre-check raw keys separately.
func decodeStrict(body []byte, dest any) error {
	dec := json.NewDecoder(strings.NewReader(string(body)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(dest); err != nil {
		return fmt.Errorf("malformed body: %w", err)
	}
	return nil
}

func readBody(r *http.Request) ([]byte, error) {
	if r.Body == nil {
		return nil, nil
	}
	defer r.Body.Close()
	return io.ReadAll(r.Body)
}

// --- /stop ---

func (s *server) handleStop(w http.ResponseWriter, r *http.Request) {
	if !methodOnly(w, r, http.MethodPost) {
		return
	}
	if err := s.deps.Shutdown(r.Context()); err != nil {
		mapErr(w, err)
		return
	}
	writeOK(w, http.StatusOK, map[string]string{"status": "stopping"})
}

// --- /workflows ---

func (s *server) handleWorkflows(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		wfs, err := s.deps.ListWorkflows(r.Context())
		if err != nil {
			mapErr(w, err)
			return
		}
		writeOK(w, http.StatusOK, wfs)
	case http.MethodPost:
		// The body IS the workflow YAML; no JSON wrapper. Content-Type is
		// not enforced because the route is the only writer and the body is
		// passed verbatim to the workflow parser.
		body, err := readBody(r)
		if err != nil {
			writeErr(w, http.StatusBadRequest, "invalid", err.Error())
			return
		}
		wf, err := s.deps.SubmitWorkflow(r.Context(), body)
		if err != nil {
			mapErr(w, err)
			return
		}
		writeOK(w, http.StatusOK, wf)
	default:
		writeErr(w, http.StatusMethodNotAllowed, "methodNotAllowed", "method not allowed")
	}
}

func (s *server) handleWorkflowByName(w http.ResponseWriter, r *http.Request) {
	name := strings.TrimPrefix(r.URL.Path, "/workflows/")
	if name == "" || strings.Contains(name, "/") {
		writeErr(w, http.StatusNotFound, "notFound", "workflow not found")
		return
	}
	switch r.Method {
	case http.MethodGet:
		wf, err := s.deps.GetWorkflow(r.Context(), name)
		if err != nil {
			mapErr(w, err)
			return
		}
		writeOK(w, http.StatusOK, wf)
	case http.MethodDelete:
		if err := s.deps.RemoveWorkflow(r.Context(), name); err != nil {
			mapErr(w, err)
			return
		}
		writeOK(w, http.StatusOK, map[string]string{"removed": name})
	default:
		writeErr(w, http.StatusMethodNotAllowed, "methodNotAllowed", "method not allowed")
	}
}

// --- /repos ---

func (s *server) handleReposDiscover(w http.ResponseWriter, r *http.Request) {
	if !methodOnly(w, r, http.MethodGet) {
		return
	}
	candidates, err := s.deps.DiscoverRepos(r.Context())
	if err != nil {
		mapErr(w, err)
		return
	}
	writeOK(w, http.StatusOK, candidates)
}

func (s *server) handleRepoTaskFields(w http.ResponseWriter, r *http.Request) {
	if !methodOnly(w, r, http.MethodGet) {
		return
	}
	fields, err := s.deps.TaskFields(r.Context())
	if err != nil {
		mapErr(w, err)
		return
	}
	writeOK(w, http.StatusOK, map[string]any{"fields": fields})
}

func (s *server) handleRepos(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		infos, err := s.deps.ListRepos(r.Context())
		if err != nil {
			mapErr(w, err)
			return
		}
		writeOK(w, http.StatusOK, infos)
	case http.MethodPost:
		body, err := readBody(r)
		if err != nil {
			writeErr(w, http.StatusBadRequest, "invalid", err.Error())
			return
		}
		var payload repo.RegisterInput
		if err := decodeStrict(body, &payload); err != nil {
			writeErr(w, http.StatusBadRequest, "invalid", err.Error())
			return
		}
		info, err := s.deps.RegisterRepo(r.Context(), payload)
		if err != nil {
			mapErr(w, err)
			return
		}
		writeOK(w, http.StatusOK, info)
	default:
		writeErr(w, http.StatusMethodNotAllowed, "methodNotAllowed", "method not allowed")
	}
}

func (s *server) handleRepoByName(w http.ResponseWriter, r *http.Request) {
	name := strings.TrimPrefix(r.URL.Path, "/repos/")
	if name == "" || strings.Contains(name, "/") {
		writeErr(w, http.StatusNotFound, "notFound", "repo not found")
		return
	}
	switch r.Method {
	case http.MethodGet:
		info, err := s.deps.GetRepo(r.Context(), name)
		if err != nil {
			mapErr(w, err)
			return
		}
		writeOK(w, http.StatusOK, info)
	case http.MethodDelete:
		if err := s.deps.RemoveRepo(r.Context(), name); err != nil {
			mapErr(w, err)
			return
		}
		writeOK(w, http.StatusOK, map[string]string{"removed": name})
	default:
		writeErr(w, http.StatusMethodNotAllowed, "methodNotAllowed", "method not allowed")
	}
}

// --- /reports ---

// ReportRequest wire keys are lowerCamel per docs. encoding/json matches
// keys case-insensitively, so the handler rejects any key that does not
// exactly match the contract at every nesting level.
var (
	reportTopKeys     = []string{"runId", "nodeVisitId", "report"}
	reportBodyKeys    = []string{"status", "nextStep", "summary", "feedback"}
	reportSummaryKeys = []string{"completed", "notCompleted", "issuesDiscovered", "verification", "notes"}
	reportFeedbackKeys = []string{"reasonForNextStep", "requiredActions", "relevantContext", "expectedResult"}
)

func rejectUnknownKeys(raw json.RawMessage, allowed []string, path string) error {
	var m map[string]json.RawMessage
	if err := json.Unmarshal(raw, &m); err != nil {
		return fmt.Errorf("%s: malformed object: %w", path, err)
	}
	allowedSet := make(map[string]bool, len(allowed))
	for _, k := range allowed {
		allowedSet[k] = true
	}
	for k := range m {
		if !allowedSet[k] {
			return fmt.Errorf("%s: unknown field %q", path, k)
		}
	}
	return nil
}

func (s *server) handleReports(w http.ResponseWriter, r *http.Request) {
	if !methodOnly(w, r, http.MethodPost) {
		return
	}
	body, err := readBody(r)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "invalid", err.Error())
		return
	}
	// Strict-case validation across every nested level.
	if err := rejectUnknownKeys(body, reportTopKeys, "report"); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid", err.Error())
		return
	}
	var top map[string]json.RawMessage
	_ = json.Unmarshal(body, &top)
	if rep, ok := top["report"]; ok {
		if err := rejectUnknownKeys(rep, reportBodyKeys, "report.report"); err != nil {
			writeErr(w, http.StatusBadRequest, "invalid", err.Error())
			return
		}
		var repBody map[string]json.RawMessage
		_ = json.Unmarshal(rep, &repBody)
		if sum, ok := repBody["summary"]; ok {
			if err := rejectUnknownKeys(sum, reportSummaryKeys, "report.report.summary"); err != nil {
				writeErr(w, http.StatusBadRequest, "invalid", err.Error())
				return
			}
		}
		if fb, ok := repBody["feedback"]; ok {
			if err := rejectUnknownKeys(fb, reportFeedbackKeys, "report.report.feedback"); err != nil {
				writeErr(w, http.StatusBadRequest, "invalid", err.Error())
				return
			}
		}
	}
	var req run.ReportRequest
	if err := decodeStrict(body, &req); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid", err.Error())
		return
	}
	ack, err := s.deps.SubmitReport(r.Context(), req)
	if err != nil {
		mapErr(w, err)
		return
	}
	writeOK(w, http.StatusOK, ack)
}

// --- /runs ---

func (s *server) handleRuns(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/runs" {
		writeErr(w, http.StatusNotFound, "notFound", "run not found")
		return
	}
	if !methodOnly(w, r, http.MethodGet) {
		return
	}
	var filter run.Filter
	q := r.URL.Query()
	filter.Repo = q.Get("repo")
	filter.Workflow = q.Get("workflow")
	filter.Ticket = q.Get("ticket")
	runs, err := s.deps.ListRuns(r.Context(), filter)
	if err != nil {
		mapErr(w, err)
		return
	}
	writeOK(w, http.StatusOK, runs)
}

func (s *server) handleRunByTicket(w http.ResponseWriter, r *http.Request) {
	// /runs/by-ticket/{key}           GET
	// /runs/by-ticket/{key}/cancel    POST
	rest := strings.TrimPrefix(r.URL.Path, "/runs/by-ticket/")
	parts := strings.Split(rest, "/")
	if len(parts) == 1 && parts[0] != "" {
		if !methodOnly(w, r, http.MethodGet) {
			return
		}
		rn, err := s.deps.GetRunByTicket(r.Context(), parts[0])
		if err != nil {
			mapErr(w, err)
			return
		}
		writeOK(w, http.StatusOK, rn)
		return
	}
	if len(parts) == 2 && parts[0] != "" && parts[1] == "cancel" {
		if !methodOnly(w, r, http.MethodPost) {
			return
		}
		var payload struct {
			Reason string `json:"reason,omitempty"`
		}
		body, err := readBody(r)
		if err != nil {
			writeErr(w, http.StatusBadRequest, "invalid", err.Error())
			return
		}
		if len(strings.TrimSpace(string(body))) > 0 {
			if err := decodeStrict(body, &payload); err != nil {
				writeErr(w, http.StatusBadRequest, "invalid", err.Error())
				return
			}
		}
		if err := s.deps.CancelRun(r.Context(), parts[0], payload.Reason); err != nil {
			mapErr(w, err)
			return
		}
		writeOK(w, http.StatusOK, map[string]string{"canceled": parts[0]})
		return
	}
	writeErr(w, http.StatusNotFound, "notFound", "run not found")
}
