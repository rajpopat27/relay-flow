package server_test

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"testing"

	"github.com/rajpopat27/relay-flow/internal/run"
	"github.com/rajpopat27/relay-flow/internal/task"
	"github.com/rajpopat27/relay-flow/internal/workflow"
)

// 3.35: server API per specs/workflow-repo-management "API responses and
// status codes are stable". Drives the real http.Handler (server.New) over
// in-process HTTP with fake services.

type envelope struct {
	OK    bool            `json:"ok"`
	Data  json.RawMessage `json:"data"`
	Error *struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

func do(t *testing.T, c *http.Client, method, url string, body []byte) (int, envelope) {
	t.Helper()
	var rdr io.Reader
	if body != nil {
		rdr = bytes.NewReader(body)
	}
	req, err := http.NewRequest(method, url, rdr)
	if err != nil {
		t.Fatal(err)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.Do(req)
	if err != nil {
		t.Fatalf("%s %s failed: %v", method, url, err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	var env envelope
	if err := json.Unmarshal(raw, &env); err != nil {
		t.Fatalf("response not a JSON envelope: %q", raw)
	}
	return resp.StatusCode, env
}

func TestSuccessEnvelope(t *testing.T) {
	c, cleanup := startHandler(t, &fakeServices{})
	defer cleanup()
	code, env := do(t, c, http.MethodGet, "http://relay/workflows", nil)
	if code != http.StatusOK || !env.OK {
		t.Fatalf("GET /workflows: code=%d env=%+v, want 200 ok", code, env)
	}
}

func TestErrorEnvelopeAndStatusMapping(t *testing.T) {
	c, cleanup := startHandler(t, &fakeServices{})
	defer cleanup()

	// 400 malformed JSON: drive a JSON endpoint (POST /repos) with malformed
	// input. POST /workflows takes raw YAML by design (docs routes).
	code, env := do(t, c, http.MethodPost, "http://relay/repos", []byte("{not json"))
	if code != http.StatusBadRequest || env.OK || env.Error == nil || env.Error.Code == "" || env.Error.Message == "" {
		t.Fatalf("malformed input: code=%d env=%+v, want 400 + lowerCamel error envelope", code, env)
	}
	if !isLowerCamel(env.Error.Code) {
		t.Fatalf("error code %q not lowerCamel", env.Error.Code)
	}

	// 404 missing resource.
	code, env = do(t, c, http.MethodGet, "http://relay/workflows/ghost", nil)
	if code != http.StatusNotFound || env.OK || env.Error == nil {
		t.Fatalf("missing resource: code=%d env=%+v, want 404", code, env)
	}

	// 405 wrong method.
	code, env = do(t, c, http.MethodPatch, "http://relay/workflows", []byte("{}"))
	if code != http.StatusMethodNotAllowed || env.OK {
		t.Fatalf("wrong method: code=%d env=%+v, want 405", code, env)
	}
}

func TestWorkflowConflictMapsTo409(t *testing.T) {
	// basicFlow is seeded with an active run, so replacement conflicts -> 409.
	c, cleanup := startHandler(t, &fakeServices{activeWorkflows: map[string]bool{"basicFlow": true}})
	defer cleanup()

	yamlBody := "name: basicFlow\nrepos: [payments]\nnodes:\n  start: {onSuccess: [{target: end}]}\n  end: {}\n"
	// POST /workflows body IS the raw YAML (docs routes; client sends raw).
	code, env := do(t, c, http.MethodPost, "http://relay/workflows", []byte(yamlBody))
	if code != http.StatusConflict {
		t.Fatalf("replacement during active run: code=%d, want 409", code)
	}
	if env.OK || env.Error == nil || !isLowerCamel(env.Error.Code) || env.Error.Message == "" {
		t.Fatalf("409 without standard error envelope: %+v", env)
	}
}

func TestUnexpectedFailureMapsTo500(t *testing.T) {
	c, cleanup := startHandler(t, &fakeServices{failRepos: true})
	defer cleanup()
	code, env := do(t, c, http.MethodGet, "http://relay/repos/any", nil)
	if code != http.StatusInternalServerError || env.OK || env.Error == nil {
		t.Fatalf("unexpected failure: code=%d env=%+v, want 500 + error envelope", code, env)
	}
}

func TestReportEndpointAcceptsJSON(t *testing.T) {
	c, cleanup := startHandler(t, &fakeServices{})
	defer cleanup()
	report := `{"runId":"payments/basicFlow/PAY-101","nodeVisitId":"abc","report":{"status":"success","nextStep":"end","summary":{"completed":"x","notCompleted":"None","issuesDiscovered":"None","verification":"x","notes":"None"},"feedback":{"reasonForNextStep":"None","requiredActions":"None","relevantContext":"None","expectedResult":"None"}}}`
	code, env := do(t, c, http.MethodPost, "http://relay/reports", []byte(report))
	if code != http.StatusOK || !env.OK {
		t.Fatalf("valid report: code=%d env=%+v, want 200 ok", code, env)
	}
	var ack struct {
		Accepted  bool `json:"accepted"`
		Duplicate bool `json:"duplicate"`
	}
	if err := json.Unmarshal(env.Data, &ack); err != nil {
		t.Fatalf("report ack data not the documented shape: %v", err)
	}
}

func TestReportRejectsWrongWireKeyCasing(t *testing.T) {
	c, cleanup := startHandler(t, &fakeServices{})
	defer cleanup()
	// runID/nodeVisitID (wrong casing) must be rejected by strict decoding.
	bad := `{"runID":"payments/basicFlow/PAY-101","nodeVisitID":"abc","report":{"status":"success","nextStep":"end","summary":{"completed":"x","notCompleted":"None","issuesDiscovered":"None","verification":"x","notes":"None"},"feedback":{"reasonForNextStep":"None","requiredActions":"None","relevantContext":"None","expectedResult":"None"}}}`
	code, env := do(t, c, http.MethodPost, "http://relay/reports", []byte(bad))
	if code != http.StatusBadRequest || env.OK {
		t.Fatalf("wrong-cased wire keys: code=%d env=%+v, want 400 not ok", code, env)
	}
}

func TestReportMultilineFieldsPreserved(t *testing.T) {
	c, cleanup := startHandler(t, &fakeServices{})
	defer cleanup()
	// Multiline summary/feedback arrive as one JSON object, never as flags.
	report := `{"runId":"payments/basicFlow/PAY-101","nodeVisitId":"abc","report":{"status":"success","nextStep":"end","summary":{"completed":"line1\nline2\n- bullet","notCompleted":"None","issuesDiscovered":"None","verification":"ran ` + "`go test ./...`" + `","notes":"None"},"feedback":{"reasonForNextStep":"None","requiredActions":"None","relevantContext":"None","expectedResult":"None"}}}`
	code, env := do(t, c, http.MethodPost, "http://relay/reports", []byte(report))
	if code != http.StatusOK || !env.OK {
		t.Fatalf("multiline report: code=%d env=%+v, want 200 ok", code, env)
	}
}

func TestRepoAndRunEndpoints(t *testing.T) {
	c, cleanup := startHandler(t, &fakeServices{})
	defer cleanup()

	// 200 repo list.
	code, env := do(t, c, http.MethodGet, "http://relay/repos", nil)
	if code != http.StatusOK || !env.OK {
		t.Fatalf("GET /repos: code=%d env=%+v, want 200 ok", code, env)
	}
	// 404 missing repo.
	code, env = do(t, c, http.MethodGet, "http://relay/repos/ghost", nil)
	if code != http.StatusNotFound || env.OK {
		t.Fatalf("GET /repos/ghost: code=%d, want 404", code)
	}
	// 200 run list.
	code, env = do(t, c, http.MethodGet, "http://relay/runs", nil)
	if code != http.StatusOK || !env.OK {
		t.Fatalf("GET /runs: code=%d env=%+v, want 200 ok", code, env)
	}
	// 404 run by ticket.
	code, env = do(t, c, http.MethodGet, "http://relay/runs/by-ticket/NOPE-1", nil)
	if code != http.StatusNotFound || env.OK {
		t.Fatalf("GET /runs/by-ticket/NOPE-1: code=%d, want 404", code)
	}
}

func TestRepoOperations(t *testing.T) {
	c, cleanup := startHandler(t, &fakeServices{})
	defer cleanup()

	// Discover returns candidates (200). Route is GET per docs.
	code, env := do(t, c, http.MethodGet, "http://relay/repos/discover", nil)
	if code != http.StatusOK || !env.OK {
		t.Fatalf("POST /repos/discover: code=%d env=%+v, want 200 ok", code, env)
	}
	// Register a repo (200), then get it (200).
	code, env = do(t, c, http.MethodPost, "http://relay/repos", []byte(`{"name":"payments","path":"/srv/payments"}`))
	if code != http.StatusOK && code != http.StatusCreated {
		t.Fatalf("POST /repos: code=%d env=%+v, want 200/201", code, env)
	}
	code, env = do(t, c, http.MethodGet, "http://relay/repos/payments", nil)
	if code != http.StatusOK || !env.OK {
		t.Fatalf("GET /repos/payments: code=%d env=%+v, want 200 ok", code, env)
	}
	// Remove it (200).
	code, env = do(t, c, http.MethodDelete, "http://relay/repos/payments", nil)
	if code != http.StatusOK || !env.OK {
		t.Fatalf("DELETE /repos/payments: code=%d env=%+v, want 200 ok", code, env)
	}
}

func TestWorkflowGetRemoveAndRunCancel(t *testing.T) {
	fake := &fakeServices{workflows: map[string]*workflow.Workflow{}, activeWorkflows: map[string]bool{}, runs: []run.Run{
		{ID: "payments/basicFlow/PAY-101", State: run.StateRunning, Ticket: task.TicketRef{Key: "PAY-101"}},
	}}
	c, cleanup := startHandler(t, fake)
	defer cleanup()

	// Submit then GET a workflow (200).
	wfYAML := "name: basicFlow\nrepos: [payments]\nnodes:\n  start: {onSuccess: [{target: coding}]}\n  coding: {type: agent, agent: build, description: work, onSuccess: [{target: end}], onFailure: [{target: coding}]}\n  end: {}\n"
	code, _ := do(t, c, http.MethodPost, "http://relay/workflows", []byte(wfYAML))
	if code != http.StatusOK && code != http.StatusCreated {
		t.Fatalf("POST /workflows: code=%d, want 200/201", code)
	}
	code, env := do(t, c, http.MethodGet, "http://relay/workflows/basicFlow", nil)
	if code != http.StatusOK || !env.OK {
		t.Fatalf("GET /workflows/basicFlow: code=%d env=%+v, want 200 ok", code, env)
	}
	// Cancel the run by ticket (200), then remove the workflow (200).
	// Route is /runs/by-ticket/{key}/cancel per docs.
	code, env = do(t, c, http.MethodPost, "http://relay/runs/by-ticket/PAY-101/cancel", []byte(`{"reason":"done"}`))
	if code != http.StatusOK || !env.OK {
		t.Fatalf("POST /runs/PAY-101/cancel: code=%d env=%+v, want 200 ok", code, env)
	}
	code, env = do(t, c, http.MethodDelete, "http://relay/workflows/basicFlow", nil)
	if code != http.StatusOK || !env.OK {
		t.Fatalf("DELETE /workflows/basicFlow: code=%d env=%+v, want 200 ok", code, env)
	}
}

func isLowerCamel(s string) bool {
	if s == "" || s[0] < 'a' || s[0] > 'z' {
		return false
	}
	for _, r := range s {
		if r == '_' || r == '-' || r == ' ' {
			return false
		}
	}
	return true
}
