package cli

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

const testWorkspaceCUID = "cworkspace12345678901234"

func TestPipelineRunDetail_IsTerminal(t *testing.T) {
	terminal := []string{"completed", "failed", "cancelled", "interrupted", "dry_run", "COMPLETED", "Failed"}
	for _, s := range terminal {
		d := &PipelineRunDetail{Status: s}
		if !d.IsTerminal() {
			t.Errorf("IsTerminal(%q) = false, want true", s)
		}
	}
	nonTerminal := []string{"queued", "running", "waiting", "WAITING", ""}
	for _, s := range nonTerminal {
		d := &PipelineRunDetail{Status: s}
		if d.IsTerminal() {
			t.Errorf("IsTerminal(%q) = true, want false", s)
		}
	}
}

func TestGetPipelineRun_FetchesWorkspaceScopedRun(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		wantPath := "/api/v1/workspaces/" + testWorkspaceCUID + "/pipeline-runs/run_1"
		if r.URL.Path != wantPath {
			t.Errorf("path = %q, want %q", r.URL.Path, wantPath)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"id":            "run_1",
			"pipeline_slug": "summarize-text",
			"status":        "completed",
			"cost_usd":      0.0123,
			"duration_ms":   4200,
			"output":        "done",
		})
	}))
	defer srv.Close()

	c := NewClient(srv.URL, "tok", testWorkspaceCUID)
	detail, err := c.GetPipelineRun(context.Background(), "run_1")
	if err != nil {
		t.Fatalf("GetPipelineRun: %v", err)
	}
	if detail.Status != "completed" || detail.PipelineSlug != "summarize-text" {
		t.Errorf("detail = %+v", detail)
	}
	if detail.CostUSD != 0.0123 {
		t.Errorf("CostUSD = %v, want 0.0123", detail.CostUSD)
	}
}

func TestGetPipelineRun_NotFoundSurfacesAPIError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(404)
		_, _ = w.Write([]byte(`{"error":"run not found"}`))
	}))
	defer srv.Close()

	c := NewClient(srv.URL, "tok", testWorkspaceCUID)
	_, err := c.GetPipelineRun(context.Background(), "ghost")
	if err == nil {
		t.Fatal("expected error")
	}
	var apiErr *APIError
	if !errors.As(err, &apiErr) || apiErr.Status != 404 {
		t.Errorf("want wrapped *APIError 404, got %v", err)
	}
}

func TestPollPipelineRun_PollsUntilTerminal(t *testing.T) {
	var calls atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := calls.Add(1)
		status := "running"
		if n >= 3 {
			status = "completed"
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"id": "run_1", "status": status})
	}))
	defer srv.Close()

	c := NewClient(srv.URL, "tok", testWorkspaceCUID)
	var ticks int
	detail, err := c.PollPipelineRun(context.Background(), "run_1", 5*time.Millisecond, func(*PipelineRunDetail) {
		ticks++
	})
	if err != nil {
		t.Fatalf("PollPipelineRun: %v", err)
	}
	if detail.Status != "completed" {
		t.Errorf("Status = %q, want completed", detail.Status)
	}
	if calls.Load() < 3 {
		t.Errorf("calls = %d, want >= 3", calls.Load())
	}
	if ticks < 2 {
		t.Errorf("onTick ticks = %d, want >= 2 (one per non-terminal read)", ticks)
	}
}

func TestPollPipelineRun_ContextDeadline(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"id": "run_1", "status": "waiting"})
	}))
	defer srv.Close()

	c := NewClient(srv.URL, "tok", testWorkspaceCUID)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()

	_, err := c.PollPipelineRun(ctx, "run_1", 5*time.Millisecond, nil)
	if err == nil {
		t.Fatal("expected deadline error")
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("want DeadlineExceeded in chain, got %v", err)
	}
}
