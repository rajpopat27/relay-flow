// Package server runs the central relay process: one long-lived `serve`
// command hosting any number of submitted workflows, each polling in its
// own goroutine. Workflows arrive via `submit` over a unix socket.
package server

import (
	"encoding/json"
	"fmt"
	"log"
	"net"
	"net/http"
	"sync"
)

// Server is the central relay process. P6 fills in submit/report; P0 keeps
// only the socket lifecycle so the binary builds.
type Server struct {
	ln           net.Listener
	closed       chan struct{}
	shutdownOnce sync.Once
	dryRun       bool
}

// Deps injects side-effecting operations (populated in P6).
type Deps struct{}

// ProdDeps wires Deps to real implementations (populated in P6).
func ProdDeps(dryRun bool) Deps { return Deps{} }

func New(dryRun bool, deps Deps) *Server {
	return &Server{dryRun: dryRun, closed: make(chan struct{})}
}

// Serve accepts HTTP on ln (a unix socket) until Shutdown. Blocks.
func (s *Server) Serve(ln net.Listener) error {
	s.ln = ln
	mux := http.NewServeMux()
	mux.HandleFunc("/shutdown", methodGuard("POST", s.handleShutdown))
	hs := &http.Server{Handler: mux}
	err := hs.Serve(ln)
	select {
	case <-s.closed:
		return nil
	default:
	}
	return err
}

// Shutdown stops the HTTP listener. Idempotent.
func (s *Server) Shutdown() {
	s.shutdownOnce.Do(func() {
		close(s.closed)
		if s.ln != nil {
			s.ln.Close()
		}
	})
}

func (s *Server) handleShutdown(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, 200, map[string]any{"ok": true})
	log.Printf("shutdown requested via socket")
	go s.Shutdown()
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
