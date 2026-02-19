package sidecar

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/crewship-ai/crewship/internal/memory"
)

func setupMemoryServer(t *testing.T) (*Server, string) {
	t.Helper()
	dir := t.TempDir()
	dailyDir := filepath.Join(dir, "daily")
	os.MkdirAll(dailyDir, 0o755)

	// Write test data
	os.WriteFile(filepath.Join(dir, "AGENT.md"), []byte("# Agent\n## Facts\nUser likes Go."), 0o644)
	os.WriteFile(filepath.Join(dailyDir, "2026-02-19.md"), []byte("# 2026-02-19\nFixed auth bug."), 0o644)

	srv := NewServer(ServerConfig{
		Addr:   "127.0.0.1:0",
		Logger: slog.Default(),
		Memory: &MemoryConfig{
			Enabled:   true,
			BasePath:  dir,
			AgentSlug: "test-agent",
		},
	})

	if srv.memoryEngine == nil {
		t.Fatal("memory engine should be initialized")
	}

	return srv, dir
}

func TestHandleMemorySearch_Success(t *testing.T) {
	srv, _ := setupMemoryServer(t)
	defer srv.memoryEngine.Close()

	body, _ := json.Marshal(map[string]interface{}{
		"query": "Go",
		"limit": 10,
	})

	req := httptest.NewRequest("POST", "http://localhost:9119/memory/search", bytes.NewReader(body))
	req.Host = "localhost:9119"
	w := httptest.NewRecorder()

	srv.handleMemorySearch(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	var resp map[string]interface{}
	json.Unmarshal(w.Body.Bytes(), &resp)

	results, ok := resp["results"]
	if !ok {
		t.Fatal("missing 'results' in response")
	}
	resultArr, ok := results.([]interface{})
	if !ok {
		t.Fatal("results should be an array")
	}
	if len(resultArr) == 0 {
		t.Error("expected at least one search result for 'Go'")
	}
}

func TestHandleMemorySearch_EmptyQuery(t *testing.T) {
	srv, _ := setupMemoryServer(t)
	defer srv.memoryEngine.Close()

	body, _ := json.Marshal(map[string]interface{}{
		"query": "",
	})

	req := httptest.NewRequest("POST", "http://localhost:9119/memory/search", bytes.NewReader(body))
	w := httptest.NewRecorder()

	srv.handleMemorySearch(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("expected 400 for empty query, got %d", w.Code)
	}
}

func TestHandleMemorySearch_InvalidJSON(t *testing.T) {
	srv, _ := setupMemoryServer(t)
	defer srv.memoryEngine.Close()

	req := httptest.NewRequest("POST", "http://localhost:9119/memory/search", bytes.NewReader([]byte("not json")))
	w := httptest.NewRecorder()

	srv.handleMemorySearch(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("expected 400 for invalid JSON, got %d", w.Code)
	}
}

func TestHandleMemorySearch_EngineNil(t *testing.T) {
	srv := NewServer(ServerConfig{
		Addr:   "127.0.0.1:0",
		Logger: slog.Default(),
	})

	body, _ := json.Marshal(map[string]interface{}{"query": "test"})
	req := httptest.NewRequest("POST", "http://localhost:9119/memory/search", bytes.NewReader(body))
	w := httptest.NewRecorder()

	srv.handleMemorySearch(w, req)

	if w.Code != http.StatusServiceUnavailable {
		t.Errorf("expected 503 when engine is nil, got %d", w.Code)
	}
}

func TestHandleMemorySearch_NoResults(t *testing.T) {
	srv, _ := setupMemoryServer(t)
	defer srv.memoryEngine.Close()

	body, _ := json.Marshal(map[string]interface{}{
		"query": "xyznonexistent",
		"limit": 10,
	})

	req := httptest.NewRequest("POST", "http://localhost:9119/memory/search", bytes.NewReader(body))
	w := httptest.NewRecorder()

	srv.handleMemorySearch(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", w.Code)
	}

	var resp map[string]interface{}
	json.Unmarshal(w.Body.Bytes(), &resp)
	count := int(resp["count"].(float64))
	if count != 0 {
		t.Errorf("expected 0 results, got %d", count)
	}
}

func TestHandleMemoryStatus_Success(t *testing.T) {
	srv, _ := setupMemoryServer(t)
	defer srv.memoryEngine.Close()

	req := httptest.NewRequest("GET", "http://localhost:9119/memory/status", nil)
	w := httptest.NewRecorder()

	srv.handleMemoryStatus(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	var status memory.Status
	if err := json.Unmarshal(w.Body.Bytes(), &status); err != nil {
		t.Fatalf("invalid status response: %v", err)
	}
	if status.TotalFiles == 0 {
		t.Error("expected at least one indexed file")
	}
	if !status.SearchReady {
		t.Error("expected search_ready to be true")
	}
}

func TestHandleMemoryStatus_EngineNil(t *testing.T) {
	srv := NewServer(ServerConfig{
		Addr:   "127.0.0.1:0",
		Logger: slog.Default(),
	})

	req := httptest.NewRequest("GET", "http://localhost:9119/memory/status", nil)
	w := httptest.NewRecorder()

	srv.handleMemoryStatus(w, req)

	if w.Code != http.StatusServiceUnavailable {
		t.Errorf("expected 503 when engine is nil, got %d", w.Code)
	}
}

func TestHandleMemoryReindex_Success(t *testing.T) {
	srv, dir := setupMemoryServer(t)
	defer srv.memoryEngine.Close()

	// Add a new file that wasn't indexed at startup
	os.WriteFile(filepath.Join(dir, "notes.md"), []byte("# New Notes\nImportant discovery."), 0o644)

	req := httptest.NewRequest("POST", "http://localhost:9119/memory/reindex", nil)
	w := httptest.NewRecorder()

	srv.handleMemoryReindex(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	// Verify new file is now searchable
	body, _ := json.Marshal(map[string]interface{}{"query": "discovery"})
	searchReq := httptest.NewRequest("POST", "http://localhost:9119/memory/search", bytes.NewReader(body))
	searchW := httptest.NewRecorder()

	srv.handleMemorySearch(searchW, searchReq)

	var resp map[string]interface{}
	json.Unmarshal(searchW.Body.Bytes(), &resp)
	count := int(resp["count"].(float64))
	if count == 0 {
		t.Error("expected search to find newly indexed content after reindex")
	}
}

func TestHandleMemoryReindex_EngineNil(t *testing.T) {
	srv := NewServer(ServerConfig{
		Addr:   "127.0.0.1:0",
		Logger: slog.Default(),
	})

	req := httptest.NewRequest("POST", "http://localhost:9119/memory/reindex", nil)
	w := httptest.NewRecorder()

	srv.handleMemoryReindex(w, req)

	if w.Code != http.StatusServiceUnavailable {
		t.Errorf("expected 503 when engine is nil, got %d", w.Code)
	}
}

func TestBuildHandler_MemoryRouting(t *testing.T) {
	srv, _ := setupMemoryServer(t)
	defer srv.memoryEngine.Close()

	handler := srv.buildHandler(srv.proxy)

	tests := []struct {
		name     string
		method   string
		path     string
		host     string
		wantCode int
	}{
		{"search via localhost", "POST", "/memory/search", "localhost:9119", http.StatusBadRequest}, // bad request (no body), but proves routing works
		{"status via localhost", "GET", "/memory/status", "localhost:9119", http.StatusOK},
		{"reindex via localhost", "POST", "/memory/reindex", "localhost:9119", http.StatusOK},
		{"status via 127.0.0.1", "GET", "/memory/status", "127.0.0.1:9119", http.StatusOK},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(tt.method, "http://"+tt.host+tt.path, nil)
			req.Host = tt.host
			w := httptest.NewRecorder()

			handler.ServeHTTP(w, req)

			if w.Code != tt.wantCode {
				t.Errorf("expected %d, got %d: %s", tt.wantCode, w.Code, w.Body.String())
			}
		})
	}
}

func TestNewServer_MemoryInitialization(t *testing.T) {
	t.Run("memory enabled with valid path", func(t *testing.T) {
		dir := t.TempDir()
		os.WriteFile(filepath.Join(dir, "AGENT.md"), []byte("test"), 0o644)

		srv := NewServer(ServerConfig{
			Addr:   "127.0.0.1:0",
			Logger: slog.Default(),
			Memory: &MemoryConfig{
				Enabled:   true,
				BasePath:  dir,
				AgentSlug: "test",
			},
		})

		if srv.memoryEngine == nil {
			t.Error("expected memory engine to be initialized")
		} else {
			srv.memoryEngine.Close()
		}
	})

	t.Run("memory disabled", func(t *testing.T) {
		srv := NewServer(ServerConfig{
			Addr:   "127.0.0.1:0",
			Logger: slog.Default(),
			Memory: &MemoryConfig{Enabled: false},
		})

		if srv.memoryEngine != nil {
			t.Error("expected nil memory engine when disabled")
		}
	})

	t.Run("memory nil config", func(t *testing.T) {
		srv := NewServer(ServerConfig{
			Addr:   "127.0.0.1:0",
			Logger: slog.Default(),
		})

		if srv.memoryEngine != nil {
			t.Error("expected nil memory engine with nil config")
		}
	})
}
