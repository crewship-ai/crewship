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
