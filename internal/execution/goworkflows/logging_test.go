package goworkflows_test

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/rajpopat27/relay-flow/internal/execution/goworkflows"
	"github.com/rajpopat27/relay-flow/internal/workflow"
)

// 9.7: logging is observable behavior. Capture the slog default handler's
// output for one full node transition (run created → node entered → report
// persisted → summary written → feedback written → mailbox completed →
// run completed) and assert the ticket/runID/node attrs appear on each
// transition line. Uses the seam-(a) fakes and seam-(b) temp SQLite engine
// that every other engine test uses — no new production hooks.
func TestNodeTransitionLogsCarryTicketRunNodeAttrs(t *testing.T) {
	h := newCaptureHandler()
	prev := slog.Default()
	slog.SetDefault(slog.New(h))
	t.Cleanup(func() { slog.SetDefault(prev) })

	log := newEventLog()
	sys := newFakeTaskSystem(log)
	engine := newEngine(t, goworkflows.Dependencies{
		Repos:  repoRegistryWith("payments", sys),
		Runner: newFakeRunner(log), Harness: newFakeHarness(log),
	})
	rid, _ := startRun(engine, linearWorkflow(false))
	waitFor(t, 10*time.Second, func() bool {
		r, _ := engine.GetRun(context.Background(), rid)
		return r.CurrentNode == "coding" && r.CurrentNodeVisitID != ""
	})
	r, _ := engine.GetRun(context.Background(), rid)
	visit := r.CurrentNodeVisitID

	// coding → end with a feedback-carrying report (NextStep=end means
	// feedback is all None; use a coding→coding failure revisit first so
	// feedback is actually written to a selected next mailbox, then a
	// final coding→end success report).
	loop := successReport("coding")
	loop.Status = workflow.OutcomeFailure
	if _, err := engine.SubmitReport(context.Background(), reportRequest(rid, "coding", loop)); err != nil {
		t.Fatal(err)
	}
	waitFor(t, 10*time.Second, func() bool {
		r, _ := engine.GetRun(context.Background(), rid)
		return r.CurrentNodeVisitID != "" && r.CurrentNodeVisitID != visit
	})
	r, _ = engine.GetRun(context.Background(), rid)
	if _, err := engine.SubmitReport(context.Background(), reportRequest(rid, "coding", successReport("end"))); err != nil {
		t.Fatal(err)
	}
	// Wait for the terminal-state log line itself (not just the projection
	// state): the ProjectionUpdateState activity writes the state and the
	// log line in one call, and waiting on state alone races the log.
	waitFor(t, 10*time.Second, func() bool {
		for _, ln := range h.lines() {
			if ln["msg"] == "run completed" {
				return true
			}
		}
		return false
	})

	wantTicket := "PAY-101"
	wantRun := string(rid)

	// The transition lines that must carry the required attrs.
	type expectation struct {
		msg        string
		needsNode  bool
		needsVisit bool
	}
	expect := []expectation{
		{"run created", false, false},
		{"node entered", true, true},
		{"report persisted", true, true},
		{"summary written", false, true}, // summary carries nodeVisitID via marker
		{"feedback written", true, true}, // feedback carries the selected next node
		{"mailbox completed", true, false},
		{"run completed", false, false},
	}
	lines := h.lines()
	for _, e := range expect {
		var found map[string]string
		for _, ln := range lines {
			if ln["msg"] == e.msg {
				found = ln
				break
			}
		}
		if found == nil {
			t.Fatalf("missing log line %q; captured %d lines:\n%s", e.msg, len(lines), renderLines(lines))
		}
		if found["ticket"] != wantTicket {
			t.Errorf("%s: ticket = %q, want %q", e.msg, found["ticket"], wantTicket)
		}
		if found["runID"] != wantRun {
			t.Errorf("%s: runID = %q, want %q", e.msg, found["runID"], wantRun)
		}
		if e.needsNode && found["node"] == "" {
			t.Errorf("%s: missing node attr", e.msg)
		}
		if e.needsVisit && found["nodeVisitID"] == "" {
			t.Errorf("%s: missing nodeVisitID attr", e.msg)
		}
	}
}

func renderLines(lines []map[string]string) string {
	var b strings.Builder
	for _, ln := range lines {
		fmt.Fprintf(&b, "  %v\n", ln)
	}
	return b.String()
}

// captureHandler is an slog.Handler that records each record's message
// and attrs. Not safe for production use; tests only.
type captureHandler struct {
	mu   sync.Mutex
	recs []map[string]string
}

func newCaptureHandler() *captureHandler { return &captureHandler{} }

func (h *captureHandler) Enabled(context.Context, slog.Level) bool { return true }

func (h *captureHandler) Handle(_ context.Context, r slog.Record) error {
	m := map[string]string{"msg": r.Message, "level": r.Level.String()}
	r.Attrs(func(a slog.Attr) bool {
		m[a.Key] = fmt.Sprint(a.Value.Any())
		return true
	})
	h.mu.Lock()
	h.recs = append(h.recs, m)
	h.mu.Unlock()
	return nil
}

func (h *captureHandler) WithAttrs([]slog.Attr) slog.Handler { return h }
func (h *captureHandler) WithGroup(string) slog.Handler      { return h }

func (h *captureHandler) lines() []map[string]string {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]map[string]string(nil), h.recs...)
}
