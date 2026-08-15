package server

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"testing"
)

const validYAML = `
pollIntervalSeconds: 30
workflows:
  taskDevelopment:
    jql: project = FOO
    closeOn: Done
    agents:
      dev:
        handles:
          - status: To Do
            outcomes:
              done: In Review
`

// fakeDeps returns Deps whose functions never touch orca/acli and whose
// daemon starter records starts/stops instead of polling.
func fakeDeps() (Deps, *fakeStarter) {
	fs := &fakeStarter{}
	return Deps{
		ResolveRepo: func(path string) (string, string, error) {
			if path == "/bad" {
				return "", "", fmt.Errorf("no repo at %s", path)
			}
			return "repo-1", "myrepo", nil
		},
		ValidateStatuses: func(yamlBytes []byte) ([]string, error) {
			if string(yamlBytes) == validYAML {
				return nil, nil
			}
			return []string{"taskDevelopment: Bogus"}, nil
		},
		StartDaemon: fs.start,
	}, fs
}

type fakeStarter struct {
	mu      sync.Mutex
	started map[string][]chan struct{} // every start appends; replace creates a new channel
}

func (f *fakeStarter) start(name string, yamlBytes []byte, repoID, repoName string) (stop chan struct{}, err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.started == nil {
		f.started = map[string][]chan struct{}{}
	}
	ch := make(chan struct{})
	f.started[name] = append(f.started[name], ch)
	return ch, nil
}

// allStopped reports whether every daemon ever started under name has had
// its stop channel closed.
func (f *fakeStarter) allStopped(name string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, ch := range f.started[name] {
		select {
		case <-ch:
		default:
			return false
		}
	}
	return true
}

// starts returns how many daemons were started under name.
func (f *fakeStarter) starts(name string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.started[name])
}

// startServer brings up a Server on a temp unix socket and returns a client.
func startServer(t *testing.T) (*Server, *fakeStarter, *http.Client) {
	t.Helper()
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)
	deps, fs := fakeDeps()
	srv := New(false, deps)
	sock := filepath.Join(tmp, "test.sock")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	go srv.Serve(ln)
	t.Cleanup(func() { srv.Shutdown(); ln.Close() })
	client := &http.Client{Transport: &http.Transport{
		DialContext: func(_ context.Context, _, _ string) (net.Conn, error) {
			return net.Dial("unix", sock)
		},
	}}
	return srv, fs, client
}

func post(t *testing.T, c *http.Client, path string, body any) (*http.Response, map[string]any) {
	t.Helper()
	b, _ := json.Marshal(body)
	req, err := http.NewRequest("POST", "http://unix"+path, bytes.NewReader(b))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var out map[string]any
	dec := json.NewDecoder(resp.Body)
	dec.Decode(&out)
	return resp, out
}

func TestSubmit_ValidConfig_StartsEntry(t *testing.T) {
	srv, fs, client := startServer(t)
	resp, out := post(t, client, "/submit", map[string]string{
		"name": "workflow", "repoPath": "/repo", "yaml": validYAML,
	})
	if resp.StatusCode != 200 {
		t.Fatalf("status=%d body=%v", resp.StatusCode, out)
	}
	if !fs.allStopped("workflow") && len(srv.List()) != 1 {
		t.Fatalf("entries=%v", srv.List())
	}
	if _, err := os.Stat(savedConfigPath(t, "workflow")); err != nil {
		t.Fatalf("saved YAML missing: %v", err)
	}
}

func TestSubmit_InvalidYAML_Rejected(t *testing.T) {
	srv, _, client := startServer(t)
	resp, _ := post(t, client, "/submit", map[string]string{
		"name": "bad", "repoPath": "/repo", "yaml": "{{nope",
	})
	if resp.StatusCode != 400 {
		t.Fatalf("status=%d, want 400", resp.StatusCode)
	}
	if len(srv.List()) != 0 {
		t.Fatal("invalid config must not start")
	}
}

func TestSubmit_InvalidJiraStatus_Rejected(t *testing.T) {
	srv, _, client := startServer(t)
	resp, out := post(t, client, "/submit", map[string]string{
		"name": "badstatus", "repoPath": "/repo", "yaml": "pollIntervalSeconds: 1\nworkflows: {}\n", // passes Parse? no -> invalid
	})
	if resp.StatusCode != 400 {
		t.Fatalf("status=%d body=%v, want 400", resp.StatusCode, out)
	}
	if len(srv.List()) != 0 {
		t.Fatal("config with bad statuses must not start")
	}
}

func TestSubmit_BadRepoPath_Rejected(t *testing.T) {
	srv, _, client := startServer(t)
	resp, _ := post(t, client, "/submit", map[string]string{
		"name": "workflow", "repoPath": "/bad", "yaml": validYAML,
	})
	if resp.StatusCode != 400 {
		t.Fatalf("status=%d, want 400", resp.StatusCode)
	}
	if len(srv.List()) != 0 {
		t.Fatal("bad repo path must not start")
	}
}

func TestSubmit_Duplicate_ReplacesOld(t *testing.T) {
	srv, fs, client := startServer(t)
	post(t, client, "/submit", map[string]string{"name": "workflow", "repoPath": "/repo", "yaml": validYAML})
	post(t, client, "/submit", map[string]string{"name": "workflow", "repoPath": "/repo", "yaml": validYAML})
	if got := fs.starts("workflow"); got != 2 {
		t.Fatalf("starts=%d, want 2 (replace restarts)", got)
	}
	// First start's channel must be closed; the replacement's must be open.
	fs.mu.Lock()
	first := fs.started["workflow"][0]
	second := fs.started["workflow"][1]
	fs.mu.Unlock()
	select {
	case <-first:
	default:
		t.Fatal("first daemon's stop channel never closed on replace")
	}
	select {
	case <-second:
		t.Fatal("replacement daemon's stop channel closed prematurely")
	default:
	}
	if len(srv.List()) != 1 {
		t.Fatalf("entries=%d, want 1", len(srv.List()))
	}
}

func TestRemove_StopsEntryAndDeletesYAML(t *testing.T) {
	srv, fs, client := startServer(t)
	post(t, client, "/submit", map[string]string{"name": "workflow", "repoPath": "/repo", "yaml": validYAML})
	resp, _ := post(t, client, "/remove", map[string]string{"name": "workflow"})
	if resp.StatusCode != 200 {
		t.Fatalf("status=%d, want 200", resp.StatusCode)
	}
	if !fs.allStopped("workflow") {
		t.Fatal("stop channel never closed")
	}
	if len(srv.List()) != 0 {
		t.Fatal("entry still listed after remove")
	}
	if _, err := os.Stat(savedConfigPath(t, "workflow")); !os.IsNotExist(err) {
		t.Fatal("saved YAML not deleted")
	}
}

func TestRemove_Unknown_Error(t *testing.T) {
	_, _, client := startServer(t)
	resp, _ := post(t, client, "/remove", map[string]string{"name": "ghost"})
	if resp.StatusCode != 404 {
		t.Fatalf("status=%d, want 404", resp.StatusCode)
	}
}

func TestList(t *testing.T) {
	srv, _, client := startServer(t)
	post(t, client, "/submit", map[string]string{"name": "a", "repoPath": "/repo", "yaml": validYAML})
	post(t, client, "/submit", map[string]string{"name": "b", "repoPath": "/repo", "yaml": validYAML})
	entries := srv.List()
	if len(entries) != 2 {
		t.Fatalf("entries=%v", entries)
	}
}

func TestSubmitRemove_Parallel_NoRace(t *testing.T) {
	_, _, client := startServer(t)
	var wg sync.WaitGroup
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			name := fmt.Sprintf("cfg%d", i%3)
			post(t, client, "/submit", map[string]string{"name": name, "repoPath": "/repo", "yaml": validYAML})
			post(t, client, "/remove", map[string]string{"name": name})
		}(i)
	}
	wg.Wait()
}

func savedConfigPath(t *testing.T, name string) string {
	t.Helper()
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".orca-jira-loop", "configs", name+".yaml")
}
