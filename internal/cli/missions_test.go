package cli

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestMissionDetail_IsTerminal(t *testing.T) {
	cases := []struct {
		name   string
		status string
		want   bool
	}{
		{"completed uppercase", "COMPLETED", true},
		{"failed uppercase", "FAILED", true},
		{"completed lowercase", "completed", true},
		{"in progress not terminal", "IN_PROGRESS", false},
		{"planning not terminal", "PLANNING", false},
		{"empty not terminal", "", false},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			got := (&MissionDetail{Status: tc.status}).IsTerminal()
			if got != tc.want {
				t.Errorf("IsTerminal(%q) = %v, want %v", tc.status, got, tc.want)
			}
		})
	}
}

func TestGetMission_OKAndNotFound(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/crews/crew1/missions/m_ok", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(MissionDetail{ID: "m_ok", Status: "IN_PROGRESS"})
	})
	mux.HandleFunc("/api/v1/crews/crew1/missions/m_missing", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"error":"mission not found"}`))
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	c := NewClient(srv.URL, "tok", "ws")
	c.HTTPClient.Timeout = 2 * time.Second

	got, err := c.GetMission(context.Background(), "crew1", "m_ok")
	if err != nil {
		t.Fatalf("GetMission ok: %v", err)
	}
	if got.Status != "IN_PROGRESS" {
		t.Fatalf("status = %s", got.Status)
	}

	if _, err := c.GetMission(context.Background(), "crew1", "m_missing"); err == nil {
		t.Fatal("expected error for missing mission, got nil")
	}

	if _, err := c.GetMission(context.Background(), "crew1", "  "); err == nil {
		t.Fatal("expected error for empty mission id, got nil")
	}
	if _, err := c.GetMission(context.Background(), "  ", "m_ok"); err == nil {
		t.Fatal("expected error for empty crew id, got nil")
	}
}

func TestPollMission_TerminatesOnTerminalStatus(t *testing.T) {
	hits := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		status := "IN_PROGRESS"
		if hits >= 3 {
			status = "COMPLETED"
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(MissionDetail{ID: "m_x", Status: status})
	}))
	defer srv.Close()

	c := NewClient(srv.URL, "tok", "ws")
	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()
	detail, err := c.PollMission(ctx, "crew1", "m_x", 5*time.Millisecond, nil)
	if err != nil {
		t.Fatalf("PollMission: %v", err)
	}
	if detail.Status != "COMPLETED" {
		t.Fatalf("status=%s", detail.Status)
	}
	if hits < 3 {
		t.Fatalf("expected >=3 polls, got %d", hits)
	}
}

func TestPollMission_ContextDeadline(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(MissionDetail{ID: "m_slow", Status: "IN_PROGRESS"})
	}))
	defer srv.Close()

	c := NewClient(srv.URL, "tok", "ws")
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()

	var ticks int
	_, err := c.PollMission(ctx, "crew1", "m_slow", 5*time.Millisecond, func(d *MissionDetail) { ticks++ })
	if err == nil {
		t.Fatal("expected deadline error, got nil")
	}
	if ticks == 0 {
		t.Error("expected at least one onTick call before the deadline")
	}
}
