package sidecar

import (
	"bytes"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/crewship-ai/crewship/internal/memory"
)

// TestHandleMemorySearch_Hybrid_NoIPC_FallsBackToFTS asserts that
// when hybrid=true is requested but no IPC config is wired, the
// handler returns FTS-only results AND signals the degradation via
// X-Memory-Hybrid-Fallback so the caller knows what they got.
func TestHandleMemorySearch_Hybrid_NoIPC_FallsBackToFTS(t *testing.T) {
	base := t.TempDir()
	silent := slog.New(slog.NewTextHandler(io.Discard, nil))
	eng, err := memory.New(base, memory.DefaultConfig())
	if err != nil {
		t.Fatalf("memory.New: %v", err)
	}
	t.Cleanup(func() { _ = eng.Close() })
	s := &Server{
		memoryEngine:    eng,
		agentMemoryBase: base,
		logger:          silent,
		// ipc intentionally nil
	}

	body, _ := json.Marshal(map[string]any{"query": "x", "hybrid": true})
	req := httptest.NewRequest("POST", "/memory/search", bytes.NewReader(body))
	rr := httptest.NewRecorder()
	s.handleMemorySearch(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (FTS fallback)", rr.Code)
	}
	got := rr.Header().Get("X-Memory-Hybrid-Fallback")
	if got != "ipc_not_configured" {
		t.Errorf("fallback header = %q, want ipc_not_configured", got)
	}
}

// TestHandleMemorySearch_NonHybrid_NoFallbackHeader asserts the
// fallback header doesn't appear on the legacy non-hybrid path
// — operators eyeballing headers shouldn't see noise when nothing
// degraded.
func TestHandleMemorySearch_NonHybrid_NoFallbackHeader(t *testing.T) {
	base := t.TempDir()
	silent := slog.New(slog.NewTextHandler(io.Discard, nil))
	eng, err := memory.New(base, memory.DefaultConfig())
	if err != nil {
		t.Fatalf("memory.New: %v", err)
	}
	t.Cleanup(func() { _ = eng.Close() })
	s := &Server{
		memoryEngine:    eng,
		agentMemoryBase: base,
		logger:          silent,
	}

	body, _ := json.Marshal(map[string]any{"query": "x"}) // hybrid omitted (false)
	req := httptest.NewRequest("POST", "/memory/search", bytes.NewReader(body))
	rr := httptest.NewRecorder()
	s.handleMemorySearch(rr, req)

	if got := rr.Header().Get("X-Memory-Hybrid-Fallback"); got != "" {
		t.Errorf("non-hybrid request should not emit fallback header, got %q", got)
	}
}

// TestHandleMemorySearch_Hybrid_ForwardsToHost verifies the IPC
// forward path: when hybrid=true AND IPC is wired, the handler
// translates the request body for the host endpoint and proxies
// the call. We can't run a real host, so a httptest server stands
// in and the sidecar's ipc.BaseURL points at it.
func TestHandleMemorySearch_Hybrid_ForwardsToHost(t *testing.T) {
	// Stub host: echoes a known hybrid response so we can assert
	// the sidecar passed the body through unchanged.
	var receivedPath string
	var receivedBody map[string]any
	hostStub := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedPath = r.URL.Path
		_ = json.NewDecoder(r.Body).Decode(&receivedBody)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"query":"x","count":2,"hits":[{"source":"fts"},{"source":"episodic"}]}`))
	}))
	defer hostStub.Close()

	silent := slog.New(slog.NewTextHandler(io.Discard, nil))
	s := &Server{
		logger: silent,
		ipc: &IPCConfig{
			BaseURL: hostStub.URL,
			Token:   "test-token",
			CrewID:  "crew_x",
		},
	}

	body, _ := json.Marshal(map[string]any{
		"query":  "outlands thieving",
		"hybrid": true,
		"limit":  5,
		"scope":  "crew",
	})
	req := httptest.NewRequest("POST", "/memory/search", bytes.NewReader(body))
	rr := httptest.NewRecorder()
	s.handleMemorySearch(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%s", rr.Code, rr.Body.String())
	}
	if receivedPath != "/api/v1/memory/search/hybrid" {
		t.Errorf("host received path = %q, want /api/v1/memory/search/hybrid", receivedPath)
	}
	// scope translation: sidecar "crew" -> host "crew_shared"
	if receivedBody["scope"] != "crew_shared" {
		t.Errorf("host scope = %v, want crew_shared", receivedBody["scope"])
	}
	if receivedBody["query"] != "outlands thieving" {
		t.Errorf("host query = %v, want outlands thieving", receivedBody["query"])
	}
	if receivedBody["crew_id"] != "crew_x" {
		t.Errorf("host crew_id = %v, want crew_x", receivedBody["crew_id"])
	}

	var resp struct {
		Count int `json:"count"`
	}
	_ = json.Unmarshal(rr.Body.Bytes(), &resp)
	if resp.Count != 2 {
		t.Errorf("response count = %d, want 2 (passthrough from host stub)", resp.Count)
	}
}

// TestForwardHybridSearch_ScopeMapping locks the scope vocabulary
// translation so the host endpoint never sees a sidecar-shaped
// scope value.
func TestForwardHybridSearch_ScopeMapping(t *testing.T) {
	cases := []struct {
		sidecarScope, hostScope string
	}{
		{"agent", "own"},
		{"crew", "crew_shared"},
		{"both", ""}, // host falls back to ScopeOwn from "" — documented contract
	}
	for _, c := range cases {
		t.Run(c.sidecarScope, func(t *testing.T) {
			var receivedBody map[string]any
			hostStub := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				_ = json.NewDecoder(r.Body).Decode(&receivedBody)
				w.WriteHeader(http.StatusOK)
				_, _ = w.Write([]byte(`{"count":0,"hits":[]}`))
			}))
			defer hostStub.Close()
			s := &Server{
				logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
				ipc:    &IPCConfig{BaseURL: hostStub.URL, Token: "t"},
			}
			body, _ := json.Marshal(map[string]any{"query": "q", "hybrid": true, "scope": c.sidecarScope})
			req := httptest.NewRequest("POST", "/memory/search", bytes.NewReader(body))
			rr := httptest.NewRecorder()
			s.handleMemorySearch(rr, req)
			if rr.Code != http.StatusOK {
				t.Fatalf("status = %d", rr.Code)
			}
			if receivedBody["scope"] != c.hostScope {
				t.Errorf("sidecar scope %q -> host scope %v, want %v",
					c.sidecarScope, receivedBody["scope"], c.hostScope)
			}
		})
	}
}
