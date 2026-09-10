package server

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"

	"github.com/rajpopat27/relay-flow/internal/config"
	"github.com/rajpopat27/relay-flow/internal/repo"
	"github.com/rajpopat27/relay-flow/internal/run"
	"github.com/rajpopat27/relay-flow/internal/runner"
	"github.com/rajpopat27/relay-flow/internal/task"
	"github.com/rajpopat27/relay-flow/internal/workflow"
)

// Client is the typed HTTP-over-Unix-socket client matching the routes in
// docs/structs-methods-interfaces.md. Commands use it; the CLI never
// speaks JSON itself.
type Client struct {
	Socket string
}

// NewClient returns a Client bound to the given socket path.
func NewClient(socket string) *Client { return &Client{Socket: socket} }

func (c *Client) httpClient() *http.Client {
	return &http.Client{
		Transport: &http.Transport{
			DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
				return (&net.Dialer{}).DialContext(ctx, "unix", c.Socket)
			},
		},
	}
}

// call performs one HTTP request against the socket server and decodes
// the standard envelope. Non-2xx responses return an error carrying the
// server-provided code and message.
func (c *Client) call(ctx context.Context, method, path string, body []byte, out any) error {
	var rdr io.Reader
	if body != nil {
		rdr = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, "http://unix"+path, rdr)
	if err != nil {
		return err
	}
	if body != nil {
		// Default to JSON; raw YAML workflow submits override below.
		ct := "application/json"
		if len(body) > 0 && body[0] != '{' && body[0] != '[' {
			ct = "application/x-yaml"
		}
		req.Header.Set("Content-Type", ct)
	}
	resp, err := c.httpClient().Do(req)
	if err != nil {
		return fmt.Errorf("server call %s %s: %w", method, path, err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	var env struct {
		OK    bool            `json:"ok"`
		Data  json.RawMessage `json:"data"`
		Error *struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(raw, &env); err != nil {
		return fmt.Errorf("server response not JSON envelope: %w", err)
	}
	if !env.OK {
		if env.Error != nil {
			return fmt.Errorf("server %s: %s", env.Error.Code, env.Error.Message)
		}
		return fmt.Errorf("server returned not-ok (HTTP %d)", resp.StatusCode)
	}
	if out != nil && len(env.Data) > 0 {
		if err := json.Unmarshal(env.Data, out); err != nil {
			return fmt.Errorf("decode response data: %w", err)
		}
	}
	return nil
}

// Stop asks the server to shut down gracefully.
func (c *Client) Stop(ctx context.Context) error {
	return c.call(ctx, http.MethodPost, "/stop", nil, nil)
}

// SubmitWorkflow creates or replaces a workflow definition from YAML.
// The request body is the raw YAML document, not a JSON wrapper.
func (c *Client) SubmitWorkflow(ctx context.Context, yaml []byte) (*workflow.Workflow, error) {
	var wf workflow.Workflow
	if err := c.call(ctx, http.MethodPost, "/workflows", yaml, &wf); err != nil {
		return nil, err
	}
	return &wf, nil
}

// RemoveWorkflow deletes a workflow by name.
func (c *Client) RemoveWorkflow(ctx context.Context, name string) error {
	return c.call(ctx, http.MethodDelete, "/workflows/"+url.PathEscape(name), nil, nil)
}

// ListWorkflows returns every registered workflow.
func (c *Client) ListWorkflows(ctx context.Context) ([]*workflow.Workflow, error) {
	var out []*workflow.Workflow
	if err := c.call(ctx, http.MethodGet, "/workflows", nil, &out); err != nil {
		return nil, err
	}
	return out, nil
}

// ListWorkflowSummaries returns one additive summary row per workflow. The
// server computes all rows in one query boundary; a server without the
// optional capability still returns definitions, which are decoded as rows
// with zero execution counts.
func (c *Client) ListWorkflowSummaries(ctx context.Context) ([]WorkflowSummary, error) {
	var out []WorkflowSummary
	if err := c.call(ctx, http.MethodGet, "/workflows?summary=1", nil, &out); err != nil {
		return nil, err
	}
	return out, nil
}

// GetWorkflow returns one workflow by name.
func (c *Client) GetWorkflow(ctx context.Context, name string) (*workflow.Workflow, error) {
	var wf workflow.Workflow
	if err := c.call(ctx, http.MethodGet, "/workflows/"+url.PathEscape(name), nil, &wf); err != nil {
		return nil, err
	}
	return &wf, nil
}

// GetWorkflowDetail returns a definition and a small recent-run summary.
func (c *Client) GetWorkflowDetail(ctx context.Context, name string) (WorkflowDetail, error) {
	var out WorkflowDetail
	if err := c.call(ctx, http.MethodGet, "/workflows/"+url.PathEscape(name)+"?detail=1", nil, &out); err != nil {
		return WorkflowDetail{}, err
	}
	return out, nil
}

// DiscoverRepos returns runner-visible registration candidates.
func (c *Client) DiscoverRepos(ctx context.Context) ([]runner.RepoCandidate, error) {
	var out []runner.RepoCandidate
	if err := c.call(ctx, http.MethodGet, "/repos/discover", nil, &out); err != nil {
		return nil, err
	}
	return out, nil
}

// EnsureRepo asks the configured runner to make a repository resource
// available without persisting a relay-flow repo entry.
func (c *Client) EnsureRepo(ctx context.Context, candidate runner.RepoCandidate) error {
	payload, err := json.Marshal(candidate)
	if err != nil {
		return err
	}
	return c.call(ctx, http.MethodPost, "/repos/ensure", payload, nil)
}

// RepoTaskFields returns the documented initial required-key metadata.
func (c *Client) RepoTaskFields(ctx context.Context) ([]string, error) {
	var out struct {
		Fields []string `json:"fields"`
	}
	if err := c.call(ctx, http.MethodGet, "/repos/task-fields", nil, &out); err != nil {
		return nil, err
	}
	return out.Fields, nil
}

// RepoRegistrationFields returns task-plugin-owned repository prompts. Values
// are flat registration inputs so plugins can discover dependent choices (for
// example project statuses after project is selected). Initial discovery uses
// the documented GET endpoint; dependent discovery uses POST.
func (c *Client) RepoRegistrationFields(ctx context.Context, values config.RawValues) (task.Registration, error) {
	if len(values) == 0 {
		var out struct {
			Registration task.Registration `json:"registration"`
		}
		if err := c.call(ctx, http.MethodGet, "/repos/task-fields", nil, &out); err != nil {
			return task.Registration{}, err
		}
		return out.Registration, nil
	}
	payload, err := json.Marshal(struct {
		Values config.RawValues `json:"values,omitempty"`
	}{Values: values})
	if err != nil {
		return task.Registration{}, err
	}
	var out task.Registration
	if err := c.call(ctx, http.MethodPost, "/repos/task-fields", payload, &out); err != nil {
		return task.Registration{}, err
	}
	return out, nil
}

// RegisterRepo registers a repo by name/path with optional task config.
func (c *Client) RegisterRepo(ctx context.Context, input repo.RegisterInput) (repo.Info, error) {
	payload, _ := json.Marshal(input)
	var out repo.Info
	if err := c.call(ctx, http.MethodPost, "/repos", payload, &out); err != nil {
		return repo.Info{}, err
	}
	return out, nil
}

// RemoveRepo unregisters a repo by name.
func (c *Client) RemoveRepo(ctx context.Context, name string) error {
	return c.call(ctx, http.MethodDelete, "/repos/"+url.PathEscape(name), nil, nil)
}

// ListRepos returns registered repo infos.
func (c *Client) ListRepos(ctx context.Context) ([]repo.Info, error) {
	var out []repo.Info
	if err := c.call(ctx, http.MethodGet, "/repos", nil, &out); err != nil {
		return nil, err
	}
	return out, nil
}

// GetRepo returns one repo by name.
func (c *Client) GetRepo(ctx context.Context, name string) (repo.Info, error) {
	var out repo.Info
	if err := c.call(ctx, http.MethodGet, "/repos/"+url.PathEscape(name), nil, &out); err != nil {
		return repo.Info{}, err
	}
	return out, nil
}

// SubmitReport delivers one structured node report.
func (c *Client) SubmitReport(ctx context.Context, report run.ReportRequest) (run.ReportAck, error) {
	payload, err := json.Marshal(report)
	if err != nil {
		return run.ReportAck{}, err
	}
	var ack run.ReportAck
	if err := c.call(ctx, http.MethodPost, "/reports", payload, &ack); err != nil {
		return run.ReportAck{}, err
	}
	return ack, nil
}

// RegisterNodeSession binds the OpenCode session emitted for a run/node.
func (c *Client) RegisterNodeSession(ctx context.Context, registration run.NodeRuntimeRegistration) (run.NodeRuntimeRegistrationAck, error) {
	payload, err := json.Marshal(registration)
	if err != nil {
		return run.NodeRuntimeRegistrationAck{}, err
	}
	var ack run.NodeRuntimeRegistrationAck
	if err := c.call(ctx, http.MethodPost, "/runtime/session", payload, &ack); err != nil {
		return run.NodeRuntimeRegistrationAck{}, err
	}
	return ack, nil
}

// RestartRun creates or returns the active fresh attempt for a canceled
// ticket. The server resolves the current repo/workflow and task-system
// boundaries; the client only transports the command.
func (c *Client) RestartRun(ctx context.Context, ticket string) (run.Run, error) {
	var out run.Run
	if err := c.call(ctx, http.MethodPost, "/runs/by-ticket/"+url.PathEscape(ticket)+"/restart", nil, &out); err != nil {
		return run.Run{}, err
	}
	return out, nil
}

// CancelRun cancels the active run for the given ticket with a reason.
func (c *Client) CancelRun(ctx context.Context, ticket, reason string) error {
	payload, _ := json.Marshal(map[string]string{"reason": reason})
	return c.call(ctx, http.MethodPost, "/runs/by-ticket/"+url.PathEscape(ticket)+"/cancel", payload, nil)
}

// ListRuns returns runs matching the filter.
func (c *Client) ListRuns(ctx context.Context, filter run.Filter) ([]run.Run, error) {
	q := url.Values{}
	if filter.Repo != "" {
		q.Set("repo", filter.Repo)
	}
	if filter.Workflow != "" {
		q.Set("workflow", filter.Workflow)
	}
	if filter.Ticket != "" {
		q.Set("ticket", filter.Ticket)
	}
	if filter.Active != nil {
		if *filter.Active {
			q.Set("active", "1")
		} else {
			q.Set("active", "0")
		}
	}
	path := "/runs"
	if s := q.Encode(); s != "" {
		path += "?" + s
	}
	var out []run.Run
	if err := c.call(ctx, http.MethodGet, path, nil, &out); err != nil {
		return nil, err
	}
	return out, nil
}

// GetRunByTicket returns the run for a ticket.
func (c *Client) GetRunByTicket(ctx context.Context, ticket string) (run.Run, error) {
	var out run.Run
	if err := c.call(ctx, http.MethodGet, "/runs/by-ticket/"+url.PathEscape(ticket), nil, &out); err != nil {
		return run.Run{}, err
	}
	return out, nil
}

// GetRunDetailByTicket returns the additive run inspection response. Existing
// callers should continue using GetRunByTicket when they only need lifecycle
// fields.
func (c *Client) GetRunDetailByTicket(ctx context.Context, ticket string) (run.RunDetail, error) {
	var out run.RunDetail
	if err := c.call(ctx, http.MethodGet, "/runs/by-ticket/"+url.PathEscape(ticket), nil, &out); err != nil {
		return run.RunDetail{}, err
	}
	return out, nil
}
