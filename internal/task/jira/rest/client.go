// Package rest is the small Jira REST API v3 boundary used by the Jira task
// adapter. It intentionally covers only relay-flow operations.
package rest

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math/rand/v2"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/rajpopat27/relay-flow/internal/retry"
)

const maxResponseBytes = 16 << 20

var requestSlots = make(chan struct{}, 20)

type SubtaskSpec struct {
	Title       string
	Description string
}

type CreatedSubtask struct {
	ID  string
	Key string
}

type Client interface {
	ValidateCredentials(context.Context) error
	Search(context.Context, string) ([]byte, error)
	ValidateAssignee(context.Context, string, string) error
	ValidateStatus(context.Context, string, string) error
	View(context.Context, string) ([]byte, error)
	CreateSubtasks(context.Context, string, string, string, []SubtaskSpec) ([]CreatedSubtask, error)
	UpdateMailbox(context.Context, string, string, string) error
	EnsureLabel(context.Context, string, string) error
	Transition(context.Context, string, string, string) error
	ListComments(context.Context, string) ([]string, error)
	AddComment(context.Context, string, string) error
}

type issueState struct {
	IssueType string
	Status    string
}

type transitionInfo struct {
	ID               string
	SupportsAssignee bool
}

type HTTPClient struct {
	base  string
	email string
	token string
	http  *http.Client

	mu           sync.Mutex
	issues       map[string]issueState
	transitions  map[string]transitionInfo
	accounts     map[string]string
	statuses     map[string]map[string]bool
	subtaskTypes map[string]string
}

func New(site, email, token string) (*HTTPClient, error) {
	site = strings.TrimSpace(site)
	if site == "" || strings.TrimSpace(email) == "" || token == "" {
		return nil, errors.New("Jira site, email, and API token are required")
	}
	if !strings.Contains(site, "://") {
		site = "https://" + site
	}
	u, err := url.Parse(site)
	if err != nil || u.Scheme == "" || u.Host == "" {
		return nil, fmt.Errorf("invalid Jira site %q", site)
	}
	if u.Scheme != "https" && u.Hostname() != "127.0.0.1" && u.Hostname() != "localhost" {
		return nil, errors.New("Jira site must use HTTPS")
	}
	return &HTTPClient{
		base:         strings.TrimRight(u.String(), "/"),
		email:        strings.TrimSpace(email),
		token:        token,
		http:         &http.Client{Timeout: 30 * time.Second},
		issues:       map[string]issueState{},
		transitions:  map[string]transitionInfo{},
		accounts:     map[string]string{},
		statuses:     map[string]map[string]bool{},
		subtaskTypes: map[string]string{},
	}, nil
}

func (c *HTTPClient) ValidateCredentials(ctx context.Context) error {
	var me struct {
		AccountID string `json:"accountId"`
	}
	if err := c.request(ctx, http.MethodGet, "/rest/api/3/myself", nil, nil, &me, true); err != nil {
		return fmt.Errorf("validate Jira credentials: %w", err)
	}
	if me.AccountID == "" {
		return errors.New("validate Jira credentials: Jira returned no account ID")
	}
	return nil
}

func (c *HTTPClient) Search(ctx context.Context, jql string) ([]byte, error) {
	issues := make([]json.RawMessage, 0)
	next := ""
	for {
		body := map[string]any{
			"jql":        jql,
			"maxResults": 100,
			"fields": []string{
				"key", "summary", "status", "issuetype", "labels", "assignee", "issuelinks",
			},
		}
		if next != "" {
			body["nextPageToken"] = next
		}
		var page struct {
			Issues        []json.RawMessage `json:"issues"`
			NextPageToken string            `json:"nextPageToken"`
			IsLast        bool              `json:"isLast"`
		}
		if err := c.request(ctx, http.MethodPost, "/rest/api/3/search/jql", nil, body, &page, true); err != nil {
			return nil, err
		}
		issues = append(issues, page.Issues...)
		for _, raw := range page.Issues {
			c.rememberIssue(raw)
		}
		if page.IsLast || page.NextPageToken == "" {
			break
		}
		next = page.NextPageToken
	}
	return json.Marshal(issues)
}

func (c *HTTPClient) ValidateAssignee(ctx context.Context, project, assignee string) error {
	_, err := c.accountID(ctx, project, assignee)
	return err
}

func (c *HTTPClient) accountID(ctx context.Context, project, assignee string) (string, error) {
	c.mu.Lock()
	account := c.accounts[project+"\x00"+assignee]
	c.mu.Unlock()
	if account != "" {
		return account, nil
	}
	q := url.Values{"project": {project}, "query": {assignee}, "maxResults": {"50"}}
	var users []struct {
		AccountID    string `json:"accountId"`
		EmailAddress string `json:"emailAddress"`
		DisplayName  string `json:"displayName"`
	}
	if err := c.request(ctx, http.MethodGet, "/rest/api/3/user/assignable/search", q, nil, &users, true); err != nil {
		return "", err
	}
	for _, user := range users {
		if strings.EqualFold(user.EmailAddress, assignee) || strings.EqualFold(user.AccountID, assignee) || strings.EqualFold(user.DisplayName, assignee) {
			c.mu.Lock()
			c.accounts[project+"\x00"+assignee] = user.AccountID
			c.mu.Unlock()
			return user.AccountID, nil
		}
	}
	if len(users) == 1 && users[0].AccountID != "" {
		c.mu.Lock()
		c.accounts[project+"\x00"+assignee] = users[0].AccountID
		c.mu.Unlock()
		return users[0].AccountID, nil
	}
	return "", fmt.Errorf("assignee %q is not assignable in project %s", assignee, project)
}

func (c *HTTPClient) ValidateStatus(ctx context.Context, project, status string) error {
	c.mu.Lock()
	statuses, ok := c.statuses[project]
	c.mu.Unlock()
	if !ok {
		var issueTypes []struct {
			ID       string `json:"id"`
			Subtask  bool   `json:"subtask"`
			Statuses []struct {
				Name string `json:"name"`
			} `json:"statuses"`
		}
		path := "/rest/api/3/project/" + url.PathEscape(project) + "/statuses"
		if err := c.request(ctx, http.MethodGet, path, nil, nil, &issueTypes, true); err != nil {
			return err
		}
		statuses = map[string]bool{}
		subtaskType := ""
		for _, issueType := range issueTypes {
			if issueType.Subtask && subtaskType == "" {
				subtaskType = issueType.ID
			}
			for _, candidate := range issueType.Statuses {
				statuses[strings.ToLower(candidate.Name)] = true
			}
		}
		c.mu.Lock()
		c.statuses[project] = statuses
		if subtaskType != "" {
			c.subtaskTypes[project] = subtaskType
		}
		c.mu.Unlock()
	}
	if !statuses[strings.ToLower(status)] {
		return fmt.Errorf("status %q is not valid in project %s", status, project)
	}
	return nil
}

func (c *HTTPClient) View(ctx context.Context, key string) ([]byte, error) {
	q := url.Values{"fields": {"summary,status,issuetype,labels,subtasks"}}
	var raw json.RawMessage
	if err := c.request(ctx, http.MethodGet, "/rest/api/3/issue/"+url.PathEscape(key), q, nil, &raw, true); err != nil {
		return nil, err
	}
	c.rememberIssue(raw)
	var issue struct {
		Fields struct {
			Subtasks []json.RawMessage `json:"subtasks"`
		} `json:"fields"`
	}
	if json.Unmarshal(raw, &issue) == nil {
		for _, subtask := range issue.Fields.Subtasks {
			c.rememberIssue(subtask)
		}
	}
	return raw, nil
}

func (c *HTTPClient) CreateSubtasks(ctx context.Context, parent, project, label string, specs []SubtaskSpec) ([]CreatedSubtask, error) {
	created := make([]CreatedSubtask, 0, len(specs))
	c.mu.Lock()
	subtaskType := c.subtaskTypes[project]
	c.mu.Unlock()
	if subtaskType == "" {
		if err := c.ValidateStatus(ctx, project, "To Do"); err != nil {
			return nil, err
		}
		c.mu.Lock()
		subtaskType = c.subtaskTypes[project]
		c.mu.Unlock()
		if subtaskType == "" {
			return nil, fmt.Errorf("no subtask issue type found for project %s", project)
		}
	}
	for start := 0; start < len(specs); start += 50 {
		end := min(start+50, len(specs))
		updates := make([]any, 0, end-start)
		for _, spec := range specs[start:end] {
			updates = append(updates, map[string]any{"fields": map[string]any{
				"project":     map[string]any{"key": project},
				"parent":      map[string]any{"key": parent},
				"issuetype":   map[string]any{"id": subtaskType},
				"summary":     spec.Title,
				"description": ADF(spec.Description),
				"labels":      []string{label},
			}})
		}
		var result struct {
			Issues []CreatedSubtask `json:"issues"`
			Errors []struct {
				FailedElementNumber int `json:"failedElementNumber"`
				Status              int `json:"status"`
			} `json:"errors"`
		}
		err := c.request(ctx, http.MethodPost, "/rest/api/3/issue/bulk", nil, map[string]any{"issueUpdates": updates}, &result, false)
		if err != nil {
			return nil, err
		}
		if len(result.Errors) > 0 || len(result.Issues) != end-start {
			return nil, fmt.Errorf("bulk create mailboxes: created %d of %d", len(result.Issues), end-start)
		}
		created = append(created, result.Issues...)
		for _, issue := range result.Issues {
			c.setIssueState(issue.Key, "Sub-task", "To Do")
		}
	}
	return created, nil
}

func (c *HTTPClient) UpdateMailbox(ctx context.Context, key, description, label string) error {
	body := map[string]any{
		"fields": map[string]any{"description": ADF(description)},
		"update": map[string]any{"labels": []any{map[string]any{"add": label}}},
	}
	return c.request(ctx, http.MethodPut, "/rest/api/3/issue/"+url.PathEscape(key), nil, body, nil, false)
}

func (c *HTTPClient) EnsureLabel(ctx context.Context, key, label string) error {
	body := map[string]any{"update": map[string]any{"labels": []any{map[string]any{"add": label}}}}
	return c.request(ctx, http.MethodPut, "/rest/api/3/issue/"+url.PathEscape(key), nil, body, nil, false)
}

func (c *HTTPClient) Transition(ctx context.Context, key, target, assignee string) error {
	state := c.issueState(key)
	if state.Status == "" || state.IssueType == "" {
		var err error
		state, err = c.refreshIssueState(ctx, key)
		if err != nil {
			return err
		}
	}
	if strings.EqualFold(state.Status, target) {
		if assignee == "" {
			return nil
		}
		project := strings.SplitN(key, "-", 2)[0]
		accountID, err := c.accountID(ctx, project, assignee)
		if err != nil {
			return err
		}
		return c.assign(ctx, key, accountID)
	}
	project := strings.SplitN(key, "-", 2)[0]
	cacheKey := project + "\x00" + state.IssueType + "\x00" + state.Status + "\x00" + target
	c.mu.Lock()
	info, ok := c.transitions[cacheKey]
	c.mu.Unlock()
	if !ok || info.ID == "" {
		var response struct {
			Transitions []struct {
				ID string `json:"id"`
				To struct {
					Name string `json:"name"`
				} `json:"to"`
				Fields map[string]json.RawMessage `json:"fields"`
			} `json:"transitions"`
		}
		q := url.Values{"expand": {"transitions.fields"}}
		if err := c.request(ctx, http.MethodGet, "/rest/api/3/issue/"+url.PathEscape(key)+"/transitions", q, nil, &response, true); err != nil {
			return err
		}
		for _, transition := range response.Transitions {
			if strings.EqualFold(transition.To.Name, target) {
				_, supportsAssignee := transition.Fields["assignee"]
				info = transitionInfo{ID: transition.ID, SupportsAssignee: supportsAssignee}
				break
			}
		}
		if info.ID == "" {
			current, err := c.refreshIssueState(ctx, key)
			if err == nil && strings.EqualFold(current.Status, target) {
				return nil
			}
			return fmt.Errorf("transition to %q is not available for %s", target, key)
		}
		if cacheKey != "\x00\x00"+target {
			c.mu.Lock()
			c.transitions[cacheKey] = info
			c.mu.Unlock()
		}
	}

	body := map[string]any{"transition": map[string]any{"id": info.ID}}
	if assignee != "" {
		accountID, err := c.accountID(ctx, project, assignee)
		if err != nil {
			return err
		}
		if info.SupportsAssignee {
			body["fields"] = map[string]any{"assignee": map[string]any{"accountId": accountID}}
		} else if err := c.assign(ctx, key, accountID); err != nil {
			return err
		}
	}
	if err := c.request(ctx, http.MethodPost, "/rest/api/3/issue/"+url.PathEscape(key)+"/transitions", nil, body, nil, false); err != nil {
		return err
	}
	c.setIssueState(key, state.IssueType, target)
	return nil
}

func (c *HTTPClient) refreshIssueState(ctx context.Context, key string) (issueState, error) {
	q := url.Values{"fields": {"status,issuetype"}}
	var raw json.RawMessage
	if err := c.request(ctx, http.MethodGet, "/rest/api/3/issue/"+url.PathEscape(key), q, nil, &raw, true); err != nil {
		return issueState{}, err
	}
	c.rememberIssue(raw)
	return c.issueState(key), nil
}

func (c *HTTPClient) assign(ctx context.Context, key, accountID string) error {
	return c.request(ctx, http.MethodPut, "/rest/api/3/issue/"+url.PathEscape(key)+"/assignee", nil, map[string]any{"accountId": accountID}, nil, false)
}

func (c *HTTPClient) ListComments(ctx context.Context, key string) ([]string, error) {
	start := 0
	comments := make([]string, 0)
	for {
		q := url.Values{"startAt": {strconv.Itoa(start)}, "maxResults": {"100"}, "orderBy": {"created"}}
		var page struct {
			Comments []struct {
				Body json.RawMessage `json:"body"`
			} `json:"comments"`
			StartAt    int `json:"startAt"`
			MaxResults int `json:"maxResults"`
			Total      int `json:"total"`
		}
		if err := c.request(ctx, http.MethodGet, "/rest/api/3/issue/"+url.PathEscape(key)+"/comment", q, nil, &page, true); err != nil {
			return nil, err
		}
		for _, comment := range page.Comments {
			comments = append(comments, ADFText(comment.Body))
		}
		start = page.StartAt + len(page.Comments)
		if start >= page.Total || len(page.Comments) == 0 {
			break
		}
	}
	return comments, nil
}

func (c *HTTPClient) AddComment(ctx context.Context, key, body string) error {
	return c.request(ctx, http.MethodPost, "/rest/api/3/issue/"+url.PathEscape(key)+"/comment", nil, map[string]any{"body": ADF(body)}, nil, false)
}

func (c *HTTPClient) rememberIssue(raw json.RawMessage) {
	var issue struct {
		Key    string `json:"key"`
		Fields struct {
			IssueType struct {
				ID   string `json:"id"`
				Name string `json:"name"`
			} `json:"issuetype"`
			Status struct {
				Name string `json:"name"`
			} `json:"status"`
		} `json:"fields"`
	}
	if json.Unmarshal(raw, &issue) == nil && issue.Key != "" {
		issueType := issue.Fields.IssueType.ID
		if issueType == "" {
			issueType = issue.Fields.IssueType.Name
		}
		c.setIssueState(issue.Key, issueType, issue.Fields.Status.Name)
	}
}

func (c *HTTPClient) setIssueState(key, issueType, status string) {
	c.mu.Lock()
	c.issues[key] = issueState{IssueType: issueType, Status: status}
	c.mu.Unlock()
}

func (c *HTTPClient) issueState(key string) issueState {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.issues[key]
}

func (c *HTTPClient) request(ctx context.Context, method, path string, query url.Values, body any, out any, safe bool) error {
	var payload []byte
	var err error
	if body != nil {
		payload, err = json.Marshal(body)
		if err != nil {
			return fmt.Errorf("encode Jira request: %w", err)
		}
	}
	endpoint := c.base + path
	if len(query) > 0 {
		endpoint += "?" + query.Encode()
	}
	for attempt := 0; ; attempt++ {
		select {
		case requestSlots <- struct{}{}:
		case <-ctx.Done():
			return ctx.Err()
		}
		req, reqErr := http.NewRequestWithContext(ctx, method, endpoint, bytes.NewReader(payload))
		if reqErr != nil {
			<-requestSlots
			return fmt.Errorf("build Jira request: %w", reqErr)
		}
		req.SetBasicAuth(c.email, c.token)
		req.Header.Set("Accept", "application/json")
		if body != nil {
			req.Header.Set("Content-Type", "application/json")
		}
		slog.Debug("jira call", "method", method, "path", path)
		resp, callErr := c.http.Do(req)
		<-requestSlots
		if callErr != nil {
			return fmt.Errorf("Jira %s %s: %s", method, path, redact(callErr.Error(), c.token, c.email))
		}
		raw, readErr := readBounded(resp.Body)
		resp.Body.Close()
		if readErr != nil {
			return fmt.Errorf("Jira %s %s: %w", method, path, readErr)
		}
		if resp.StatusCode >= 200 && resp.StatusCode < 300 {
			if out != nil && len(raw) > 0 {
				if err := json.Unmarshal(raw, out); err != nil {
					return fmt.Errorf("Jira %s %s: parse response: %w", method, path, err)
				}
			}
			slog.Info("jira outcome", "method", method, "path", path, "result", "ok")
			return nil
		}
		retryable := resp.StatusCode == http.StatusTooManyRequests || (safe && resp.StatusCode >= 500)
		if retryable && attempt < 4 {
			delay := retryDelay(resp.Header.Get("Retry-After"), attempt)
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(delay):
			}
			continue
		}
		message := strings.TrimSpace(string(raw))
		message = redact(message, c.token, c.email)
		if len(message) > 2048 {
			message = message[:2048]
		}
		if message == "" {
			message = resp.Status
		}
		slog.Info("jira outcome", "method", method, "path", path, "result", "error", "status", resp.StatusCode)
		return fmt.Errorf("Jira %s %s: HTTP %d: %s", method, path, resp.StatusCode, message)
	}
}

func redact(message string, secrets ...string) string {
	for _, secret := range secrets {
		if secret != "" {
			message = strings.ReplaceAll(message, secret, "[REDACTED]")
		}
	}
	return message
}

func readBounded(r io.Reader) ([]byte, error) {
	raw, err := io.ReadAll(io.LimitReader(r, maxResponseBytes+1))
	if err != nil {
		return nil, err
	}
	if len(raw) > maxResponseBytes {
		return nil, errors.New("response exceeds 16 MiB")
	}
	return raw, nil
}

func retryDelay(value string, attempt int) time.Duration {
	if seconds, err := strconv.Atoi(value); err == nil && seconds >= 0 {
		return time.Duration(seconds) * time.Second
	}
	if when, err := http.ParseTime(value); err == nil {
		if delay := time.Until(when); delay > 0 {
			return delay
		}
		return 0
	}
	return retry.DefaultBackoffPolicy.Delay(attempt, rand.Float64())
}
