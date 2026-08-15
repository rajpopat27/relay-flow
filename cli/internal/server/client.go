package server

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"

	"orca-jira-loop/internal/discovery"
)

// Client talks to a running `serve` process over its unix socket. Zero
// value is unusable; construct with NewClient (prod socket) or set Socket
// directly (tests).
type Client struct {
	Socket string
}

// NewClient returns a Client for the default socket path.
func NewClient() (*Client, error) {
	p, err := discovery.SocketPath()
	if err != nil {
		return nil, err
	}
	return &Client{Socket: p}, nil
}

func (c *Client) httpClient() *http.Client {
	return &http.Client{Transport: &http.Transport{
		DialContext: func(_ context.Context, _, _ string) (net.Conn, error) {
			return net.Dial("unix", c.Socket)
		},
	}}
}

func (c *Client) do(method, path string, body any, out any) error {
	var rdr *bytes.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rdr = bytes.NewReader(b)
	} else {
		rdr = bytes.NewReader(nil)
	}
	req, err := http.NewRequest(method, "http://unix"+path, rdr)
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.httpClient().Do(req)
	if err != nil {
		if _, statErr := os.Stat(c.Socket); os.IsNotExist(statErr) {
			return fmt.Errorf("no server at %s — is `orca-jira-loop serve` running?", c.Socket)
		}
		return fmt.Errorf("server call %s %s: %w (is `orca-jira-loop serve` running?)", method, path, err)
	}
	defer resp.Body.Close()
	var env struct {
		OK      bool            `json:"ok"`
		Err     string          `json:"error"`
		Configs []Info          `json:"configs"`
		Raw     json.RawMessage `json:"-"`
	}
	dec := json.NewDecoder(resp.Body)
	if err := dec.Decode(&env); err != nil {
		return fmt.Errorf("decode server reply: %w", err)
	}
	if resp.StatusCode >= 400 || !env.OK && env.Err != "" {
		if env.Err != "" {
			return fmt.Errorf("server: %s", env.Err)
		}
		return fmt.Errorf("server: status %d", resp.StatusCode)
	}
	if out != nil {
		if p, ok := out.(*[]Info); ok {
			*p = env.Configs
		}
	}
	return nil
}

// Submit sends a config to the server, which validates it and starts a
// poll loop for it. repoPath must be a directory inside the target repo.
// The config's name comes from the YAML's `name` field.
func (c *Client) Submit(repoPath string, yamlBytes []byte) error {
	return c.do("POST", "/submit", map[string]string{
		"repoPath": repoPath, "yaml": string(yamlBytes),
	}, nil)
}

// Remove stops the named config's daemon.
func (c *Client) Remove(name string) error {
	return c.do("POST", "/remove", map[string]string{"name": name}, nil)
}

// List returns all running configs.
func (c *Client) List() ([]Info, error) {
	var infos []Info
	if err := c.do("GET", "/list", nil, &infos); err != nil {
		return nil, err
	}
	return infos, nil
}
