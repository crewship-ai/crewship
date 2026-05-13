package cli

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestRunDetail_IsTerminal(t *testing.T) {
	cases := []struct {
		status string
		want   bool
	}{
		{"COMPLETED", true},
		{"FAILED", true},
		{"CANCELLED", true},
		{"TIMEOUT", true},
		{"completed", true},
		{"RUNNING", false},
		{"", false},
		{"queued", false},
	}
	for _, c := range cases {
		got := (&RunDetail{Status: c.status}).IsTerminal()
		if got != c.want {
			t.Errorf("IsTerminal(%q) = %v, want %v", c.status, got, c.want)
		}
	}
}

func TestParsePRURL(t *testing.T) {
	cases := []struct {
		in        string
		owner     string
		repo      string
		num       int
		ok        bool
	}{
		{"https://github.com/foo/bar/pull/123", "foo", "bar", 123, true},
		{"https://github.com/foo/bar/pulls/123", "foo", "bar", 123, true},
		{"https://gitlab.com/foo/bar/-/merge_requests/42", "foo", "bar", 42, true},
		{"https://bitbucket.org/foo/bar/pull-requests/7", "foo", "bar", 7, true},
		{"https://github.com/foo/bar/issues/1", "", "", 0, false},
		{"not a url", "", "", 0, false},
		{"", "", "", 0, false},
	}
	for _, c := range cases {
		o, r, n, ok := ParsePRURL(c.in)
		if ok != c.ok || o != c.owner || r != c.repo || n != c.num {
			t.Errorf("ParsePRURL(%q) = (%q,%q,%d,%v), want (%q,%q,%d,%v)",
				c.in, o, r, n, ok, c.owner, c.repo, c.num, c.ok)
		}
	}
}

func TestGetRun_OKAndNotFound(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/runs/r_ok", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(RunDetail{ID: "r_ok", Status: "COMPLETED"})
	})
	mux.HandleFunc("/api/v1/runs/r_missing", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"error":"run not found"}`))
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	c := NewClient(srv.URL, "tok", "ws")
	c.HTTPClient.Timeout = 2 * time.Second
	got, err := c.GetRun(context.Background(), "r_ok")
	if err != nil {
		t.Fatalf("GetRun ok: %v", err)
	}
	if got.Status != "COMPLETED" {
		t.Fatalf("status = %s", got.Status)
	}

	if _, err := c.GetRun(context.Background(), "r_missing"); err == nil {
		t.Fatal("expected error for missing run, got nil")
	}

	if _, err := c.GetRun(context.Background(), "  "); err == nil {
		t.Fatal("expected error for empty id, got nil")
	}
}

func TestPollRun_TerminatesOnTerminalStatus(t *testing.T) {
	hits := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		status := "RUNNING"
		if hits >= 3 {
			status = "COMPLETED"
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(RunDetail{ID: "r_x", Status: status})
	}))
	defer srv.Close()

	c := NewClient(srv.URL, "tok", "ws")
	detail, err := c.PollRun(context.Background(), "r_x", 5*time.Millisecond, nil)
	if err != nil {
		t.Fatalf("PollRun: %v", err)
	}
	if detail.Status != "COMPLETED" {
		t.Fatalf("status=%s", detail.Status)
	}
	if hits < 3 {
		t.Fatalf("expected ≥3 polls, got %d", hits)
	}
}
