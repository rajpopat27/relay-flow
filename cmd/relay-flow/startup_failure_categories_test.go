package main

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/rajpopat27/relay-flow/internal/server"
)

func TestServeModesPropagateDependencyFailureCategoriesWithoutRetries(t *testing.T) {
	binary := buildCLIBinary(t)
	for _, tc := range []struct {
		name, stage, script, want string
		status                    int
		body                      string
	}{
		{name: "runner-launch", stage: "runner", script: "#!/nonexistent/interpreter\n", want: "launch failure"},
		{name: "runner-connection", stage: "runner", script: "#!/bin/sh\nprintf '%s' 'runtime connection refused' >&2\nexit 1\n", want: "connection refused"},
		{name: "runner-malformed", stage: "runner", script: "#!/bin/sh\nprintf '%s' '{\"result\":{}}'\n", want: "malformed response"},
		{name: "runner-timeout", stage: "runner", script: "#!/bin/sh\nexec /bin/sleep 30\n", want: "timeout"},
		{name: "task-authentication", stage: "task", status: 401, body: "fixture-token startup@example.invalid", want: "authentication"},
		{name: "task-connection", stage: "task", status: 503, body: "service unavailable", want: "HTTP 503"},
		{name: "task-malformed", stage: "task", status: 200, body: "not-json", want: "malformed response"},
		{name: "task-timeout", stage: "task", status: 0, want: "timeout"},
	} {
		for _, foreground := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/foreground=%v", tc.name, foreground), func(t *testing.T) {
				root := filepath.Join(t.TempDir(), "relay-flow")
				t.Setenv("RELAY_FLOW_HOME", root)
				init := exec.Command(binary, initArgs()...)
				init.Env = os.Environ()
				if out, err := init.CombinedOutput(); err != nil {
					t.Fatalf("init: %v\n%s", err, out)
				}
				installHealthyServePrerequisites(t, root)
				bin := strings.Split(os.Getenv("PATH"), string(os.PathListSeparator))[0]
				t.Setenv("PATH", bin)
				var calls atomic.Int32
				operation := "repo list"
				if tc.stage == "runner" {
					launcher := "orca"
					if runtime.GOOS == "linux" {
						launcher = "orca-ide"
					}
					if err := os.WriteFile(filepath.Join(bin, launcher), []byte(tc.script), 0o755); err != nil {
						t.Fatal(err)
					}
				} else {
					operation = "/rest/api/3/myself"
					s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
						calls.Add(1)
						user, token, ok := r.BasicAuth()
						if !ok || user != "startup@example.invalid" || token != "fixture-token" || r.Method != "GET" || r.URL.Path != "/rest/api/3/myself" {
							t.Errorf("unexpected probe: %s %s", r.Method, r.URL.Path)
						}
						if tc.status == 0 {
							<-r.Context().Done()
							return
						}
						w.Header().Set("Retry-After", "60")
						w.WriteHeader(tc.status)
						_, _ = w.Write([]byte(tc.body))
					}))
					t.Cleanup(s.Close)
					credentials := fmt.Sprintf("site: %q\nemail: startup@example.invalid\ntoken: fixture-token\n", s.URL)
					if err := os.WriteFile(filepath.Join(root, "credentials.yaml"), []byte(credentials), 0o600); err != nil {
						t.Fatal(err)
					}
				}
				// An earlier run's record must never escape into this diagnostic.
				logPath := filepath.Join(root, "server.log")
				if err := os.WriteFile(logPath, []byte("msg=\"server startup failed\" pid=1 error=\"STALE_PRIVATE_ERROR\"\n"), 0o600); err != nil {
					t.Fatal(err)
				}
				args := []string{"serve"}
				if foreground {
					args = append(args, "--foreground")
				}
				ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
				defer cancel()
				serve := exec.CommandContext(ctx, binary, args...)
				serve.Env = os.Environ()
				start := time.Now()
				out, err := serve.CombinedOutput()
				if err == nil || strings.Contains(string(out), "server started") || !strings.Contains(string(out), tc.want) || !strings.Contains(string(out), operation) || !strings.Contains(string(out), logPath) {
					t.Fatalf("failure category or safe identity lost: err=%v, output=%s", err, out)
				}
				for _, secret := range []string{"fixture-token", "startup@example.invalid", "STALE_PRIVATE_ERROR"} {
					if strings.Contains(string(out), secret) {
						t.Fatalf("startup diagnostic exposed secret/stale record %q: %s", secret, out)
					}
				}
				if tc.stage == "task" && calls.Load() != 1 {
					t.Fatalf("credential probe retried or queried project data: %d calls", calls.Load())
				}
				if elapsed := time.Since(start); elapsed > 7*time.Second {
					t.Fatalf("startup probe inherited retries or exceeded ceiling: %v", elapsed)
				}
				if serverResponding(server.NewClient(filepath.Join(root, "server.sock")), 50*time.Millisecond) {
					t.Fatal("failed probe left ready server")
				}
			})
		}
	}
}
