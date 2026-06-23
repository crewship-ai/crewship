package sidecar

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

// setupCovMemoryServer builds a server with agent + crew memory engines and
// some indexed content in each.
func setupCovMemoryServer(t *testing.T, ipc *IPCConfig) *Server {
	t.Helper()
	agentDir := t.TempDir()
	crewDir := t.TempDir()
	os.WriteFile(filepath.Join(agentDir, "AGENT.md"), []byte("# Agent\nPrefers tabs over spaces."), 0o644)
	os.WriteFile(filepath.Join(crewDir, "CREW.md"), []byte("# Crew\nDeploy on tabs Fridays."), 0o644)

	srv := NewServer(ServerConfig{
		Addr:   "127.0.0.1:0",
		Logger: covLogger(),
		IPC:    ipc,
		Memory: &MemoryConfig{
			Enabled:        true,
			BasePath:       agentDir,
			AgentRole:      "lead",
			CrewMemoryPath: crewDir,
		},
	})
	if srv.memoryEngine == nil || srv.crewMemoryEngine == nil {
		t.Fatal("both memory engines should be initialized")
	}
	return srv
}

func covMemorySearch(t *testing.T, srv *Server, payload map[string]interface{}) *httptest.ResponseRecorder {
	t.Helper()
	b, _ := json.Marshal(payload)
	req := httptest.NewRequest("POST", "http://localhost:9119/memory/search", bytes.NewReader(b))
	w := httptest.NewRecorder()
	srv.handleMemorySearch(w, req)
	return w
}

// --- hybrid forwarding ---

func TestCovMemorySearchHybridForwardsScopeVocabulary(t *testing.T) {
	tests := []struct {
		scope     string
		hostScope string
	}{
		{"agent", "own"},
		{"crew", "crew_shared"},
		{"both", ""},
	}

	for _, tt := range tests {
		t.Run(tt.scope, func(t *testing.T) {
			var gotPath string
			var gotBody map[string]interface{}
			mock := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				gotPath = r.URL.Path
				json.NewDecoder(r.Body).Decode(&gotBody)
				writeJSONResponse(w, http.StatusOK, map[string]interface{}{"results": []string{}, "count": 0})
			}))
			defer mock.Close()

			srv := setupCovMemoryServer(t, &IPCConfig{BaseURL: mock.URL, Token: "t", CrewID: "crew-42"})
			defer srv.memoryEngine.Close()
			defer srv.crewMemoryEngine.Close()

			w := covMemorySearch(t, srv, map[string]interface{}{
				"query": "tabs", "hybrid": true, "scope": tt.scope,
			})

			if w.Code != http.StatusOK {
				t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
			}
			if gotPath != "/api/v1/memory/search/hybrid" {
				t.Errorf("path = %q", gotPath)
			}
			if gotBody["scope"] != tt.hostScope {
				t.Errorf("host scope = %v, want %q", gotBody["scope"], tt.hostScope)
			}
			if gotBody["crew_id"] != "crew-42" {
				t.Errorf("crew_id = %v", gotBody["crew_id"])
			}
			if gotBody["query"] != "tabs" {
				t.Errorf("query = %v", gotBody["query"])
			}
			if gotBody["limit"] != float64(10) {
				t.Errorf("default limit = %v, want 10", gotBody["limit"])
			}
		})
	}
}

func TestCovMemorySearchHybridFallbackWithoutIPC(t *testing.T) {
	srv := setupCovMemoryServer(t, nil)
	defer srv.memoryEngine.Close()
	defer srv.crewMemoryEngine.Close()

	w := covMemorySearch(t, srv, map[string]interface{}{
		"query": "tabs", "hybrid": true,
	})

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200 FTS fallback, got %d: %s", w.Code, w.Body.String())
	}
	if got := w.Header().Get("X-Memory-Hybrid-Fallback"); got != "ipc_not_configured" {
		t.Errorf("X-Memory-Hybrid-Fallback = %q", got)
	}
}

func TestCovMemorySearchInvalidScope(t *testing.T) {
	srv := setupCovMemoryServer(t, nil)
	defer srv.memoryEngine.Close()
	defer srv.crewMemoryEngine.Close()

	w := covMemorySearch(t, srv, map[string]interface{}{
		"query": "tabs", "scope": "bogus",
	})
	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", w.Code)
	}
}

// --- ipcCrewID ---

func TestCovIPCCrewID(t *testing.T) {
	if got := (&Server{}).ipcCrewID(); got != "" {
		t.Errorf("nil-ipc ipcCrewID = %q, want empty", got)
	}
	s := &Server{ipc: &IPCConfig{CrewID: "crew-7"}}
	if got := s.ipcCrewID(); got != "crew-7" {
		t.Errorf("ipcCrewID = %q", got)
	}
}

// --- engine error paths via closed engines ---

func TestCovSearchSingleScopeEngineError(t *testing.T) {
	srv := setupCovMemoryServer(t, nil)
	srv.crewMemoryEngine.Close()
	defer srv.memoryEngine.Close()

	w := covMemorySearch(t, srv, map[string]interface{}{
		"query": "tabs", "scope": "crew",
	})
	if w.Code != http.StatusInternalServerError {
		t.Fatalf("expected 500 from closed engine, got %d: %s", w.Code, w.Body.String())
	}
}

func TestCovSearchBothScopesPartialFailure(t *testing.T) {
	srv := setupCovMemoryServer(t, nil)
	defer srv.memoryEngine.Close()
	srv.crewMemoryEngine.Close() // crew side fails, agent side still works

	w := covMemorySearch(t, srv, map[string]interface{}{
		"query": "tabs", "scope": "both",
	})
	if w.Code != http.StatusOK {
		t.Fatalf("partial failure should still 200, got %d: %s", w.Code, w.Body.String())
	}
	var resp struct {
		Results []scopedResult `json:"results"`
		Count   int            `json:"count"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.Count == 0 {
		t.Fatal("expected agent-scope hits despite crew engine failure")
	}
	for _, r := range resp.Results {
		if r.Source != "agent" {
			t.Errorf("result source = %q, want agent only", r.Source)
		}
	}
}

func TestCovSearchBothScopesAllFailed(t *testing.T) {
	srv := setupCovMemoryServer(t, nil)
	srv.memoryEngine.Close()
	srv.crewMemoryEngine.Close()

	w := covMemorySearch(t, srv, map[string]interface{}{
		"query": "tabs", "scope": "both",
	})
	if w.Code != http.StatusInternalServerError {
		t.Fatalf("expected 500 when all scopes fail, got %d: %s", w.Code, w.Body.String())
	}
	var body map[string]string
	json.Unmarshal(w.Body.Bytes(), &body)
	if body["error"] != "all memory scopes failed to search" {
		t.Errorf("error = %q", body["error"])
	}
}

func TestCovSearchBothScopesNoEngines(t *testing.T) {
	srv := NewServer(ServerConfig{Addr: "127.0.0.1:0", Logger: covLogger()})
	w := covMemorySearch(t, srv, map[string]interface{}{
		"query": "tabs", "scope": "both",
	})
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected 503, got %d", w.Code)
	}
}

func TestCovSearchBothScopesMergesAndSorts(t *testing.T) {
	srv := setupCovMemoryServer(t, nil)
	defer srv.memoryEngine.Close()
	defer srv.crewMemoryEngine.Close()

	w := covMemorySearch(t, srv, map[string]interface{}{
		"query": "tabs", "scope": "both", "limit": 1,
	})
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	var resp struct {
		Results []scopedResult `json:"results"`
		Count   int            `json:"count"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	// Both scopes match "tabs" but limit=1 must trim the merged list.
	if resp.Count != 1 || len(resp.Results) != 1 {
		t.Errorf("count = %d, results = %d, want 1 after limit trim", resp.Count, len(resp.Results))
	}
}

// --- status / reindex branches ---

func TestCovMemoryStatusBranches(t *testing.T) {
	srv := setupCovMemoryServer(t, nil)
	defer srv.memoryEngine.Close()

	// Invalid scope → 400.
	req := httptest.NewRequest("GET", "http://localhost:9119/memory/status?scope=weird", nil)
	w := httptest.NewRecorder()
	srv.handleMemoryStatus(w, req)
	if w.Code != http.StatusBadRequest {
		t.Errorf("invalid scope: expected 400, got %d", w.Code)
	}

	// Crew scope works while the engine is live.
	req = httptest.NewRequest("GET", "http://localhost:9119/memory/status?scope=crew", nil)
	w = httptest.NewRecorder()
	srv.handleMemoryStatus(w, req)
	if w.Code != http.StatusOK {
		t.Errorf("crew status: expected 200, got %d: %s", w.Code, w.Body.String())
	}

	// Closed engine → 500 from Status error.
	srv.crewMemoryEngine.Close()
	req = httptest.NewRequest("GET", "http://localhost:9119/memory/status?scope=crew", nil)
	w = httptest.NewRecorder()
	srv.handleMemoryStatus(w, req)
	if w.Code != http.StatusInternalServerError {
		t.Errorf("closed engine status: expected 500, got %d: %s", w.Code, w.Body.String())
	}
}

func TestCovMemoryReindexBranches(t *testing.T) {
	srv := setupCovMemoryServer(t, nil)
	defer srv.memoryEngine.Close()

	// Invalid scope → 400.
	req := httptest.NewRequest("POST", "http://localhost:9119/memory/reindex?scope=weird", nil)
	w := httptest.NewRecorder()
	srv.handleMemoryReindex(w, req)
	if w.Code != http.StatusBadRequest {
		t.Errorf("invalid scope: expected 400, got %d", w.Code)
	}

	// Crew reindex success returns the engine status.
	req = httptest.NewRequest("POST", "http://localhost:9119/memory/reindex?scope=crew", nil)
	w = httptest.NewRecorder()
	srv.handleMemoryReindex(w, req)
	if w.Code != http.StatusOK {
		t.Errorf("crew reindex: expected 200, got %d: %s", w.Code, w.Body.String())
	}

	// Closed engine → reindex error → 500.
	srv.crewMemoryEngine.Close()
	req = httptest.NewRequest("POST", "http://localhost:9119/memory/reindex?scope=crew", nil)
	w = httptest.NewRecorder()
	srv.handleMemoryReindex(w, req)
	if w.Code != http.StatusInternalServerError {
		t.Errorf("closed engine reindex: expected 500, got %d: %s", w.Code, w.Body.String())
	}
}

func TestCovResolveMemoryEngineScopes(t *testing.T) {
	srv := setupCovMemoryServer(t, nil)
	defer srv.memoryEngine.Close()
	defer srv.crewMemoryEngine.Close()

	if eng, ok := srv.resolveMemoryEngine(""); !ok || eng != srv.memoryEngine {
		t.Error("empty scope should resolve to agent engine")
	}
	if eng, ok := srv.resolveMemoryEngine("agent"); !ok || eng != srv.memoryEngine {
		t.Error("agent scope should resolve to agent engine")
	}
	if eng, ok := srv.resolveMemoryEngine("crew"); !ok || eng != srv.crewMemoryEngine {
		t.Error("crew scope should resolve to crew engine")
	}
	if _, ok := srv.resolveMemoryEngine("nope"); ok {
		t.Error("unknown scope must be invalid")
	}
}
