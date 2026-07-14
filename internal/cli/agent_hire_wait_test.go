package cli

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestHireStatus_IsResolved(t *testing.T) {
	expiredAt := "2026-01-01T00:00:00Z"
	cases := []struct {
		name string
		hs   HireStatus
		want bool
	}{
		{"still pending review", HireStatus{Status: "PENDING_REVIEW"}, false},
		{"approved to idle", HireStatus{Status: "IDLE"}, true},
		{"ghosted while pending", HireStatus{Status: "PENDING_REVIEW", ExpiredAt: &expiredAt}, true},
		{"lowercase pending", HireStatus{Status: "pending_review"}, false},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.hs.IsResolved(); got != tc.want {
				t.Errorf("IsResolved() = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestGetHireStatus_OKAndNotFound(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/agents/a_ok", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(HireStatus{ID: "a_ok", Status: "PENDING_REVIEW"})
	})
	mux.HandleFunc("/api/v1/agents/a_missing", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"error":"agent not found"}`))
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	c := NewClient(srv.URL, "tok", "ws")
	c.HTTPClient.Timeout = 2 * time.Second

	got, err := c.GetHireStatus(context.Background(), "a_ok")
	if err != nil {
		t.Fatalf("GetHireStatus ok: %v", err)
	}
	if got.Status != "PENDING_REVIEW" {
		t.Fatalf("status = %s", got.Status)
	}

	if _, err := c.GetHireStatus(context.Background(), "a_missing"); err == nil {
		t.Fatal("expected error for missing agent, got nil")
	}
	if _, err := c.GetHireStatus(context.Background(), "  "); err == nil {
		t.Fatal("expected error for empty agent id, got nil")
	}
}

func TestPollHireApproval_TerminatesOnResolution(t *testing.T) {
	hits := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		status := "PENDING_REVIEW"
		if hits >= 3 {
			status = "IDLE"
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(HireStatus{ID: "a_x", Status: status})
	}))
	defer srv.Close()

	c := NewClient(srv.URL, "tok", "ws")
	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()
	got, err := c.PollHireApproval(ctx, "a_x", 5*time.Millisecond, nil)
	if err != nil {
		t.Fatalf("PollHireApproval: %v", err)
	}
	if got.Status != "IDLE" {
		t.Fatalf("status=%s", got.Status)
	}
	if hits < 3 {
		t.Fatalf("expected >=3 polls, got %d", hits)
	}
}

func TestPollHireApproval_ContextDeadline(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(HireStatus{ID: "a_slow", Status: "PENDING_REVIEW"})
	}))
	defer srv.Close()

	c := NewClient(srv.URL, "tok", "ws")
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()

	var ticks int
	_, err := c.PollHireApproval(ctx, "a_slow", 5*time.Millisecond, func(h *HireStatus) { ticks++ })
	if err == nil {
		t.Fatal("expected deadline error, got nil")
	}
	if ticks == 0 {
		t.Error("expected at least one onTick call before the deadline")
	}
}
