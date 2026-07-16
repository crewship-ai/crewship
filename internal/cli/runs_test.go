package cli

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestRunDetail_IsTerminal(t *testing.T) {
	cases := []struct {
		name   string
		status string
		want   bool
	}{
		{"completed uppercase", "COMPLETED", true},
		{"failed uppercase", "FAILED", true},
		{"cancelled uppercase", "CANCELLED", true},
		{"timeout uppercase", "TIMEOUT", true},
		{"completed lowercase", "completed", true},
		{"running not terminal", "RUNNING", false},
		{"empty not terminal", "", false},
		{"unknown not terminal", "queued", false},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			got := (&RunDetail{Status: tc.status}).IsTerminal()
			if got != tc.want {
				t.Errorf("IsTerminal(%q) = %v, want %v", tc.status, got, tc.want)
			}
		})
	}
}

func TestParsePRURL(t *testing.T) {
	cases := []struct {
		name  string
		in    string
		owner string
		repo  string
		num   int
		ok    bool
	}{
		{"github pull", "https://github.com/foo/bar/pull/123", "foo", "bar", 123, true},
		{"github pulls alias", "https://github.com/foo/bar/pulls/123", "foo", "bar", 123, true},
		{"gitlab mr flat", "https://gitlab.com/foo/bar/-/merge_requests/42", "foo", "bar", 42, true},
		{"gitlab mr subgroup", "https://gitlab.com/group/subgroup/repo/-/merge_requests/9", "group/subgroup", "repo", 9, true},
		{"gitlab mr deep subgroup", "https://gitlab.com/a/b/c/repo/-/merge_requests/77", "a/b/c", "repo", 77, true},
		{"bitbucket pull-requests", "https://bitbucket.org/foo/bar/pull-requests/7", "foo", "bar", 7, true},
		{"github issue rejected", "https://github.com/foo/bar/issues/1", "", "", 0, false},
		{"non-url rejected", "not a url", "", "", 0, false},
		{"empty rejected", "", "", "", 0, false},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			o, r, n, ok := ParsePRURL(tc.in)
			if ok != tc.ok || o != tc.owner || r != tc.repo || n != tc.num {
				t.Errorf("ParsePRURL(%q) = (%q,%q,%d,%v), want (%q,%q,%d,%v)",
					tc.in, o, r, n, ok, tc.owner, tc.repo, tc.num, tc.ok)
			}
		})
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

// TestGetRun_PipelineRunIDRejectedWithHint pins issue #1193: `crewship
// routine runs <slug>` surfaces run_-shaped pipeline run ids, but
// diff/resume (via GetRun) only ever resolve msg_-shaped chat-turn run
// ids. Feeding a run_ id in must produce a clear hint pointing at
// `routine logs`, not a bare "run not found" — and must not even hit the
// server, since a run_ id can never be found via /api/v1/runs/{id}.
func TestGetRun_PipelineRunIDRejectedWithHint(t *testing.T) {
	t.Parallel()
	called := false
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		called = true
		w.WriteHeader(http.StatusNotFound)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	c := NewClient(srv.URL, "tok", "ws")
	c.HTTPClient.Timeout = 2 * time.Second

	_, err := c.GetRun(context.Background(), "run_cmrm3xxzk0083de436e64")
	if err == nil {
		t.Fatal("expected error for a pipeline run_ id, got nil")
	}
	if called {
		t.Error("GetRun should reject a run_-shaped id before making any HTTP call")
	}
	for _, want := range []string{"pipeline run", "routine logs"} {
		if !strings.Contains(strings.ToLower(err.Error()), want) {
			t.Errorf("error %q should mention %q", err.Error(), want)
		}
	}
}

func TestIsPipelineRunID(t *testing.T) {
	cases := []struct {
		name string
		id   string
		want bool
	}{
		{"pipeline_run", "run_cmrm3xxzk0083de436e64", true},
		{"chat_turn_run", "msg_cmrm3xxzk0083de436e64", false},
		{"legacy_run", "r_abc123", false}, // legacy test fixture prefix, not the real pipeline shape
		{"empty", "", false},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			if got := IsPipelineRunID(tc.id); got != tc.want {
				t.Errorf("IsPipelineRunID(%q) = %v, want %v", tc.id, got, tc.want)
			}
		})
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
	// Bound the test so a regression that broke terminal-status
	// detection fails fast (within 500 ms) instead of stalling the
	// suite up to the global -timeout. The expected wall time at
	// 5 ms interval is ~15 ms across three polls.
	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()
	detail, err := c.PollRun(ctx, "r_x", 5*time.Millisecond, nil)
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
