// Package server runs the central relayflow process: one long-lived `serve`
// command hosting any number of submitted workflows, each polling in its
// own goroutine. Workflows arrive via `submit`, agent outcomes arrive via
// `report` — both over a unix socket. The tracker remains the only
// cross-process state.
package server

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net"
	"net/http"
	"strings"
	"sync"

	"github.com/rajpopat27/relayflow/cli/internal/config"
	"github.com/rajpopat27/relayflow/cli/internal/daemon"
	"github.com/rajpopat27/relayflow/cli/internal/discovery"
	"github.com/rajpopat27/relayflow/cli/internal/runner"
	"github.com/rajpopat27/relayflow/cli/internal/acli"
	"github.com/rajpopat27/relayflow/cli/internal/tasks"
	"github.com/rajpopat27/relayflow/cli/internal/tasks/jira"

	_ "github.com/rajpopat27/relayflow/cli/internal/runner/orca" // built-in adapters self-register
)

// Deps injects side-effecting operations so tests never call orca/acli or
// spawn real poll loops.
type Deps struct {
	// ResolveRepo maps a repo path to (repoID, displayName).
	ResolveRepo func(path string) (string, string, error)
	// ValidateConfig probe-validates adapter-visible names (tracker
	// states, assignee) in the YAML; returns invalid names.
	ValidateConfig func(yamlBytes []byte) ([]string, error)
}

// ProdDeps wires Deps to the real implementations.
func ProdDeps(dryRun bool) Deps {
	return Deps{
		ResolveRepo:    discovery.RepoFromPath,
		ValidateConfig: validateConfigProd,
	}
}

type entry struct {
	cfg    *config.Config
	tk     tasks.Tasks
	d      *daemon.Daemon
	cancel context.CancelFunc
	repoID string
}

type Server struct {
	mu      sync.Mutex
	entries map[string]*entry
	deps    Deps
	dryRun  bool

	ln           net.Listener
	closed       chan struct{}
	shutdownOnce sync.Once
}

func New(dryRun bool, deps Deps) *Server {
	return &Server{
		entries: map[string]*entry{},
		deps:    deps,
		dryRun:  dryRun,
		closed:  make(chan struct{}),
	}
}

func (s *Server) handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/submit", methodGuard("POST", s.handleSubmit))
	mux.HandleFunc("/report", methodGuard("POST", s.handleReport))
	mux.HandleFunc("/shutdown", methodGuard("POST", s.handleShutdown))
	return mux
}

// Serve accepts HTTP on ln (a unix socket) until Shutdown. Blocks.
func (s *Server) Serve(ln net.Listener) error {
	s.ln = ln
	err := (&http.Server{Handler: s.handler()}).Serve(ln)
	select {
	case <-s.closed:
		return nil
	default:
	}
	return err
}

// Shutdown stops the HTTP listener and every workflow's poll loop.
// Idempotent: the /shutdown handler and process signal handlers may both
// invoke it.
func (s *Server) Shutdown() {
	s.shutdownOnce.Do(func() {
		close(s.closed)
		if s.ln != nil {
			s.ln.Close()
		}
		s.mu.Lock()
		defer s.mu.Unlock()
		for name, e := range s.entries {
			e.cancel()
			delete(s.entries, name)
		}
	})
}

type submitRequest struct {
	RepoPath string `json:"repoPath"`
	YAML     string `json:"yaml"`
}

func (s *Server) handleSubmit(w http.ResponseWriter, r *http.Request) {
	var req submitRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.YAML == "" || req.RepoPath == "" {
		writeError(w, 400, "submit requires {repoPath, yaml}")
		return
	}
	// 1. YAML must parse and validate structurally. The workflow's
	//    identity is the `name` field inside the YAML.
	cfg, err := config.Parse("submit", []byte(req.YAML))
	if err != nil {
		writeError(w, 400, "invalid config: %v", err)
		return
	}
	// 2. Name must be free: two workflows with the same name would share
	//    claim labels and double-dispatch tickets.
	s.mu.Lock()
	_, dup := s.entries[cfg.Name]
	s.mu.Unlock()
	if dup {
		writeError(w, 409, "workflow %q already running; stop serve and resubmit to update", cfg.Name)
		return
	}
	// 3. Repo must resolve (submitted from a directory inside the repo).
	repoID, repoName, err := s.deps.ResolveRepo(req.RepoPath)
	if err != nil {
		writeError(w, 400, "resolve repo %s: %v", req.RepoPath, err)
		return
	}
	// 4. Tracker-visible names (states, assignee) must probe-validate.
	if bad, err := s.deps.ValidateConfig([]byte(req.YAML)); err != nil {
		writeError(w, 400, "config validation: %v", err)
		return
	} else if len(bad) > 0 {
		writeError(w, 400, "invalid tracker names: %v", bad)
		return
	}
	// 5. Build adapters + daemon and start the poll loop. Stateless:
	//    restart means resubmit.
	e, err := s.buildEntry(cfg, repoID, repoName)
	if err != nil {
		writeError(w, 400, "start workflow: %v", err)
		return
	}
	s.mu.Lock()
	s.entries[cfg.Name] = e
	s.mu.Unlock()
	log.Printf("submit %s: started (repo=%s)", cfg.Name, repoID)
	writeJSON(w, 200, map[string]any{"ok": true, "name": cfg.Name})
}

// buildEntry wires one workflow: tasks adapter → runner adapter → daemon
// + poll goroutine.
func (s *Server) buildEntry(cfg *config.Config, repoID, repoName string) (*entry, error) {
	tk, err := buildTasks(cfg, repoName)
	if err != nil {
		return nil, err
	}
	rn, err := runner.New(cfg.Runner.Type, cfg.Runner.Config)
	if err != nil {
		return nil, err
	}
	if wr, ok := rn.(interface{ WithRepo(string, string, bool) }); ok {
		wr.WithRepo(repoID, repoName, s.dryRun)
	}
	d := daemon.New(cfg, tk, rn, repoID, repoName, s.dryRun)
	ctx, cancel := context.WithCancel(context.Background())
	go d.PollLoop(ctx)
	return &entry{cfg: cfg, tk: tk, d: d, cancel: cancel, repoID: repoID}, nil
}

// buildTasks constructs the tasks adapter, injecting the machine-config
// assignee for jira in distributed mode (centralized assigneeIsAgent
// skips it). Adapter-specific because only jira consumes an assignee.
func buildTasks(cfg *config.Config, repoName string) (tasks.Tasks, error) {
	assignee := ""
	if cfg.Tasks.Type == "jira" {
		jc, err := jira.UnmarshalConfigForValidation(cfg.Tasks.Config)
		if err != nil {
			return nil, err
		}
		if !jc.AssigneeIsAgent {
			mc, err := config.LoadMachineConfig()
			if err != nil {
				return nil, err
			}
			assignee = mc.Assignee
		}
	}
	return tasks.New(cfg.Tasks.Type, cfg.Tasks.Config, cfg.Name, cfg.Nodes, assignee, repoName)
}

// handleShutdown replies first, then stops the server (listener + every
// workflow's poll loop). Process exit releases the flock.
func (s *Server) handleShutdown(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, 200, map[string]any{"ok": true})
	log.Printf("shutdown requested via socket")
	go s.Shutdown()
}

type reportRequest struct {
	Workflow string `json:"workflow"`
	Ticket   string `json:"ticket"`
	Node     string `json:"node"`
	Outcome  string `json:"outcome"`
	Summary  string `json:"summary"`
}

func (s *Server) handleReport(w http.ResponseWriter, r *http.Request) {
	var req reportRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil ||
		req.Workflow == "" || req.Ticket == "" || req.Node == "" || req.Outcome == "" || req.Summary == "" {
		writeError(w, 400, "report requires {workflow, ticket, node, outcome, summary}")
		return
	}
	if req.Outcome != "success" && req.Outcome != "failure" {
		writeError(w, 400, "outcome must be success or failure, got %q", req.Outcome)
		return
	}
	s.mu.Lock()
	e, ok := s.entries[req.Workflow]
	s.mu.Unlock()
	if !ok {
		writeError(w, 404, "no running workflow %q", req.Workflow)
		return
	}
	node, ok := e.cfg.Nodes[req.Node]
	if !ok {
		writeError(w, 400, "workflow %q has no node %q", req.Workflow, req.Node)
		return
	}
	target := node.OnSuccess
	if req.Outcome == "failure" {
		target = node.OnFailure
	}
	tk := tasks.Ticket{Key: req.Ticket, Node: req.Node, ClaimedBy: req.Workflow}
	if err := e.tk.Report(tk, req.Outcome, target, req.Summary); err != nil {
		log.Printf("report %s/%s: %v", req.Workflow, req.Ticket, err)
		writeJSON(w, 200, map[string]any{"ok": true, "action": "error", "detail": err.Error()})
		return
	}
	// Report moved the ticket: re-arm the bounce nudge marker for the
	// next node visit.
	e.d.ClearNudged(req.Ticket)
	action := "transitioned"
	if e.cfg.Nodes[target].When != "" && stringsEqualFoldNode(e.cfg, req.Node, target) {
		action = "commented"
	}
	log.Printf("report %s/%s: node=%s outcome=%s → %s (%s)", req.Workflow, req.Ticket, req.Node, req.Outcome, target, action)
	writeJSON(w, 200, map[string]any{"ok": true, "action": action, "detail": target})
}

// stringsEqualFoldNode reports whether two nodes share the same tracker
// state (self-loop: comment only, no transition).
func stringsEqualFoldNode(cfg *config.Config, a, b string) bool {
	wa, wb := cfg.Nodes[a].When, cfg.Nodes[b].When
	if wa == "" || wb == "" {
		return false
	}
	return strings.EqualFold(wa, wb)
}

func methodGuard(method string, h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != method {
			writeError(w, 405, "method %s not allowed, use %s", r.Method, method)
			return
		}
		h(w, r)
	}
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, code int, format string, args ...any) {
	writeJSON(w, code, map[string]any{"ok": false, "error": fmt.Sprintf(format, args...)})
}


// validateConfigProd probe-validates tracker-visible names at submit:
// every node's `when` status against the project (jira), plus the machine
// assignee when in distributed mode.
func validateConfigProd(yamlBytes []byte) ([]string, error) {
	cfg, err := config.Parse("submit", yamlBytes)
	if err != nil {
		return nil, err
	}
	if cfg.Tasks.Type != "jira" {
		return nil, nil // only the jira adapter has probeable states today
	}
	jc, err := jiraConfigOf(cfg)
	if err != nil {
		return nil, err
	}
	projectKey, err := jira.ProjectKeyFromQuery(jc.Query)
	if err != nil {
		return nil, err
	}
	ac := acli.New()
	bad, err := jira.ValidateStates(ac, cfg.Nodes, projectKey)
	if err != nil {
		return nil, err
	}
	if !jc.AssigneeIsAgent {
		mc, err := config.LoadMachineConfig()
		if err != nil {
			return nil, err
		}
		if err := ac.ValidateAssignee(mc.Assignee); err != nil {
			bad = append(bad, "assignee: "+mc.Assignee)
		}
	}
	return bad, nil
}

func jiraConfigOf(cfg *config.Config) (jira.JiraConfig, error) {
	jcAny, err := jira.UnmarshalConfigForValidation(cfg.Tasks.Config)
	if err != nil {
		return jira.JiraConfig{}, err
	}
	return jcAny, nil
}
