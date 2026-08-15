// Package server runs the central orca-jira-loop process: one long-lived
// `serve` command hosting any number of submitted workflow configs, each
// polling in its own goroutine. Configs arrive via `submit` over a unix
// socket; Jira remains the only cross-process state.
package server

import (
	"encoding/json"
	"fmt"
	"log"
	"net"
	"net/http"
	"sort"
	"sync"
	"time"

	"orca-jira-loop/internal/acli"
	"orca-jira-loop/internal/config"
	"orca-jira-loop/internal/daemon"
	"orca-jira-loop/internal/discovery"
)

// Deps injects side-effecting operations so tests never call orca/acli or
// spawn real poll loops.
type Deps struct {
	// ResolveRepo maps a repo path to (repoID, displayName).
	ResolveRepo func(path string) (string, string, error)
	// ValidateConfig returns invalid Jira names (statuses and, when set,
	// the assignee user) in the config YAML (empty = all good).
	ValidateConfig func(yamlBytes []byte) ([]string, error)
	// StartDaemon launches one config's poll loop; the returned channel
	// must be closed to stop it.
	StartDaemon func(name string, yamlBytes []byte, repoID, repoName string) (stop chan struct{}, err error)
}

// ProdDeps wires Deps to the real implementations.
func ProdDeps(dryRun bool) Deps {
	return Deps{
		ResolveRepo: discovery.RepoFromPath,
		ValidateConfig: func(yamlBytes []byte) ([]string, error) {
			cfg, err := config.Parse("submit", yamlBytes)
			if err != nil {
				return nil, err
			}
			ac := acli.New()
			bad, err := daemon.ValidateConfigStatuses(cfg, ac)
			if err != nil {
				return nil, err
			}
			// Probe the assignee the same way statuses are probed: Jira's
			// JQL parser rejects unknown users, so a typo fails at submit
			// instead of silently matching zero tickets forever.
			if cfg.Assignee != "" {
				if err := ac.ValidateAssignee(cfg.Assignee); err != nil {
					bad = append(bad, "assignee: "+cfg.Assignee)
				}
			}
			return bad, nil
		},
		StartDaemon: func(name string, yamlBytes []byte, repoID, repoName string) (chan struct{}, error) {
			cfg, err := config.Parse(name, yamlBytes)
			if err != nil {
				return nil, err
			}
			d := daemon.New(name, cfg, repoID, repoName, dryRun)
			stop := make(chan struct{})
			go d.PollLoop(stop)
			return stop, nil
		},
	}
}

type Entry struct {
	ConfigName string    `json:"configName"`
	RepoPath   string    `json:"repoPath"`
	RepoID     string    `json:"repoId"`
	StartedAt  time.Time `json:"startedAt"`

	stop chan struct{}
}

// Info is the public, JSON-serializable view of an Entry.
type Info struct {
	ConfigName string    `json:"configName"`
	RepoPath   string    `json:"repoPath"`
	RepoID     string    `json:"repoId"`
	StartedAt  time.Time `json:"startedAt"`
}

type Server struct {
	mu      sync.Mutex
	entries map[string]*Entry
	deps    Deps
	dryRun  bool

	ln     net.Listener
	closed chan struct{}
}

func New(dryRun bool, deps Deps) *Server {
	return &Server{
		entries: map[string]*Entry{},
		deps:    deps,
		dryRun:  dryRun,
		closed:  make(chan struct{}),
	}
}

// Serve accepts HTTP on ln (a unix socket) until Shutdown. Blocks.
func (s *Server) Serve(ln net.Listener) error {
	s.ln = ln
	// go.mod targets 1.21, so no method-pattern routing; guard methods
	// inside each handler instead.
	mux := http.NewServeMux()
	mux.HandleFunc("/submit", methodGuard("POST", s.handleSubmit))
	mux.HandleFunc("/remove", methodGuard("POST", s.handleRemove))
	mux.HandleFunc("/list", methodGuard("GET", s.handleList))
	hs := &http.Server{Handler: mux}
	err := hs.Serve(ln)
	select {
	case <-s.closed:
		return nil
	default:
	}
	return err
}

// Shutdown stops the HTTP listener and every running config daemon.
func (s *Server) Shutdown() {
	close(s.closed)
	if s.ln != nil {
		s.ln.Close()
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for name, e := range s.entries {
		close(e.stop)
		delete(s.entries, name)
	}
}

// List returns a sorted snapshot of running configs.
func (s *Server) List() []Info {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]Info, 0, len(s.entries))
	for _, e := range s.entries {
		out = append(out, Info{ConfigName: e.ConfigName, RepoPath: e.RepoPath, RepoID: e.RepoID, StartedAt: e.StartedAt})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ConfigName < out[j].ConfigName })
	return out
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
	// 1. YAML must parse and validate structurally. The config's identity
	//    is the `name` field inside the YAML — single source of truth,
	//    never the CLI arg or filename.
	cfg, err := config.Parse("submit", []byte(req.YAML))
	if err != nil {
		writeError(w, 400, "invalid config: %v", err)
		return
	}
	name := cfg.Name
	// 2. Name must be free: two configs with the same name would share
	//    claim labels and double-dispatch/stall tickets. To update a
	//    config, remove it first, then submit again.
	s.mu.Lock()
	_, dup := s.entries[name]
	s.mu.Unlock()
	if dup {
		writeError(w, 409, "config %q already running; remove it first", name)
		return
	}
	// 3. Repo must resolve (submitted from a directory inside the repo).
	repoID, repoName, err := s.deps.ResolveRepo(req.RepoPath)
	if err != nil {
		writeError(w, 400, "resolve repo %s: %v", req.RepoPath, err)
		return
	}
	// 4. Every referenced Jira status (and the assignee user, when set)
	//    must exist.
	if bad, err := s.deps.ValidateConfig([]byte(req.YAML)); err != nil {
		writeError(w, 400, "status validation: %v", err)
		return
	} else if len(bad) > 0 {
		writeError(w, 400, "invalid Jira statuses: %v", bad)
		return
	}
	// 5. Start the daemon and register it under one lock hold. Nothing is
	//    persisted: the system is stateless — a server restart means
	//    resubmitting every config.
	stop, err := s.deps.StartDaemon(name, []byte(req.YAML), repoID, repoName)
	if err != nil {
		writeError(w, 500, "start daemon: %v", err)
		return
	}
	s.mu.Lock()
	s.entries[name] = &Entry{ConfigName: name, RepoPath: req.RepoPath, RepoID: repoID, StartedAt: time.Now(), stop: stop}
	s.mu.Unlock()
	log.Printf("submit %s: started (repo=%s)", name, repoID)
	writeJSON(w, 200, map[string]any{"ok": true, "configName": name})
}

func (s *Server) handleRemove(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name string `json:"name"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Name == "" {
		writeError(w, 400, "remove requires {name}")
		return
	}
	s.mu.Lock()
	e, ok := s.entries[req.Name]
	if ok {
		close(e.stop)
		delete(s.entries, req.Name)
	}
	s.mu.Unlock()
	if !ok {
		writeError(w, 404, "no running config %q", req.Name)
		return
	}
	log.Printf("remove %s: stopped", req.Name)
	writeJSON(w, 200, map[string]any{"ok": true})
}

func (s *Server) handleList(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, 200, map[string]any{"configs": s.List()})
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
