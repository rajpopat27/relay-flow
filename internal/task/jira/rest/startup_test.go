package rest

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestStartupCredentialsUsesOneAuthenticatedMyselfRead(t *testing.T) {
	s := newJiraServer(t, func(w http.ResponseWriter, r *http.Request, _ []byte) {
		if r.Method != http.MethodGet || r.URL.Path != "/rest/api/3/myself" || r.URL.RawQuery != "" {
			t.Errorf("unexpected startup request: %s %s", r.Method, r.URL)
		}
		writeJSON(w, map[string]any{"accountId": "account"})
	})
	if err := s.client(t).ValidateCredentialsStartup(context.Background()); err != nil {
		t.Fatal(err)
	}
	if s.count(http.MethodGet, "/rest/api/3/myself") != 1 {
		t.Fatal("startup did not make exactly one credential request")
	}
}

func TestStartupCredentialsNeverRetriesRateLimitsOrServerErrors(t *testing.T) {
	for _, status := range []int{http.StatusTooManyRequests, http.StatusInternalServerError, http.StatusServiceUnavailable} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			s := newJiraServer(t, func(w http.ResponseWriter, _ *http.Request, _ []byte) {
				w.Header().Set("Retry-After", "60")
				http.Error(w, "unavailable", status)
			})
			start := time.Now()
			if err := s.client(t).ValidateCredentialsStartup(context.Background()); err == nil {
				t.Fatal("startup accepted failed credentials check")
			}
			if s.count(http.MethodGet, "/rest/api/3/myself") != 1 || time.Since(start) > time.Second {
				t.Fatal("startup inherited runtime retry count or delay")
			}
		})
	}
}

func TestStartupCredentialFailureCategoriesAndRedaction(t *testing.T) {
	for _, tc := range []struct {
		name, body, want string
		status           int
	}{
		{"unauthorized", "secret bot@example.com", "authentication", http.StatusUnauthorized},
		{"forbidden", "secret bot@example.com", "authentication", http.StatusForbidden},
		{"invalid-json", "not-json", "malformed", http.StatusOK},
		{"missing-account", `{}`, "malformed", http.StatusOK},
		{"empty-response", "", "malformed", http.StatusOK},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := newJiraServer(t, func(w http.ResponseWriter, _ *http.Request, _ []byte) {
				w.WriteHeader(tc.status)
				_, _ = w.Write([]byte(tc.body))
			})
			err := s.client(t).ValidateCredentialsStartup(context.Background())
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("%s = %v, want category %s", tc.name, err, tc.want)
			}
			if strings.Contains(err.Error(), "secret") || strings.Contains(err.Error(), "bot@example.com") {
				t.Fatalf("credentials leaked: %v", err)
			}
		})
	}
	t.Run("connection", func(t *testing.T) {
		s := httptest.NewServer(http.NotFoundHandler())
		s.Close()
		c, err := New(s.URL, "bot@example.com", "secret")
		if err != nil {
			t.Fatal(err)
		}
		if err := c.ValidateCredentialsStartup(context.Background()); err == nil || !strings.Contains(err.Error(), "connection") {
			t.Fatalf("connection failure = %v", err)
		}
	})
}

func TestStartupCredentialsHonorsEarlierDeadlineAndCancellation(t *testing.T) {
	var calls atomic.Int32
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		<-r.Context().Done()
	}))
	defer s.Close()
	c, err := New(s.URL, "bot@example.com", "secret")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	start := time.Now()
	if err := c.ValidateCredentialsStartup(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("deadline = %v", err)
	}
	if time.Since(start) > time.Second || calls.Load() != 1 {
		t.Fatalf("timeout exceeded caller budget or retried: calls=%d", calls.Load())
	}
	ctx, cancelNow := context.WithCancel(context.Background())
	cancelNow()
	if err := c.ValidateCredentialsStartup(ctx); !errors.Is(err, context.Canceled) || calls.Load() != 1 {
		t.Fatalf("cancellation = %v, calls=%d", err, calls.Load())
	}
}

type deadlineTransport struct {
	deadline time.Time
}

func (d *deadlineTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	d.deadline, _ = r.Context().Deadline()
	return nil, errors.New("transport unavailable")
}

func TestStartupCredentialsCapsRequestDeadlineAtFiveSeconds(t *testing.T) {
	c, err := New("https://jira.example.invalid", "bot@example.com", "secret")
	if err != nil {
		t.Fatal(err)
	}
	transport := &deadlineTransport{}
	c.http.Transport = transport
	start := time.Now()
	_ = c.ValidateCredentialsStartup(context.Background())
	if transport.deadline.IsZero() || transport.deadline.After(start.Add(5*time.Second+time.Millisecond)) {
		t.Fatalf("request deadline = %v", transport.deadline)
	}
}
