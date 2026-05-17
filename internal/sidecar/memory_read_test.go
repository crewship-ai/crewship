package sidecar

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/crewship-ai/crewship/internal/memory"
)

func newReadTestServer(t *testing.T) (*Server, string) {
	t.Helper()
	base := t.TempDir()
	silent := slog.New(slog.NewTextHandler(io.Discard, nil))
	eng, err := memory.New(base, memory.DefaultConfig())
	if err != nil {
		t.Fatalf("memory.New: %v", err)
	}
	t.Cleanup(func() { _ = eng.Close() })
	return &Server{
		memoryEngine:    eng,
		agentMemoryBase: base,
		logger:          silent,
	}, base
}

// seedFile drops content at base/relpath, creating parent dirs.
func seedFile(t *testing.T, base, relpath, body string) {
	t.Helper()
	full := filepath.Join(base, relpath)
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(full, []byte(body), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
}

func TestHandleMemoryRead_HappyPath(t *testing.T) {
	s, base := newReadTestServer(t)
	seedFile(t, base, "AGENT.md", "# Agent\nlong-term memory body\n")

	req := httptest.NewRequest("GET", "http://localhost/memory/read?file=AGENT.md", nil)
	rr := httptest.NewRecorder()
	s.handleMemoryRead(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%s", rr.Code, rr.Body.String())
	}
	var resp MemoryReadResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.Scope != "agent" {
		t.Errorf("scope = %q, want agent", resp.Scope)
	}
	if resp.Bytes != len("# Agent\nlong-term memory body\n") {
		t.Errorf("bytes = %d, want %d", resp.Bytes, len("# Agent\nlong-term memory body\n"))
	}
	if resp.Content != "# Agent\nlong-term memory body\n" {
		t.Errorf("content mismatch: got %q", resp.Content)
	}
}

func TestHandleMemoryRead_Subdir(t *testing.T) {
	s, base := newReadTestServer(t)
	seedFile(t, base, "daily/2026-05-17.md", "session notes\n")

	req := httptest.NewRequest("GET", "http://localhost/memory/read?file=daily/2026-05-17.md", nil)
	rr := httptest.NewRecorder()
	s.handleMemoryRead(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d", rr.Code)
	}
	var resp MemoryReadResponse
	_ = json.Unmarshal(rr.Body.Bytes(), &resp)
	if resp.Content != "session notes\n" {
		t.Errorf("content = %q, want %q", resp.Content, "session notes\n")
	}
}

func TestHandleMemoryRead_MissingFile_404(t *testing.T) {
	s, _ := newReadTestServer(t)
	req := httptest.NewRequest("GET", "http://localhost/memory/read?file=nope.md", nil)
	rr := httptest.NewRecorder()
	s.handleMemoryRead(rr, req)
	if rr.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", rr.Code)
	}
}

func TestHandleMemoryRead_MissingParam_400(t *testing.T) {
	s, _ := newReadTestServer(t)
	req := httptest.NewRequest("GET", "http://localhost/memory/read", nil)
	rr := httptest.NewRecorder()
	s.handleMemoryRead(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", rr.Code)
	}
}

func TestHandleMemoryRead_PathTraversal_403(t *testing.T) {
	s, _ := newReadTestServer(t)
	req := httptest.NewRequest("GET", "http://localhost/memory/read?file=../../../etc/passwd", nil)
	rr := httptest.NewRecorder()
	s.handleMemoryRead(rr, req)
	if rr.Code != http.StatusForbidden {
		t.Errorf("status = %d, want 403", rr.Code)
	}
}

func TestHandleMemoryRead_AbsolutePath_403(t *testing.T) {
	s, _ := newReadTestServer(t)
	req := httptest.NewRequest("GET", "http://localhost/memory/read?file=/etc/passwd", nil)
	rr := httptest.NewRecorder()
	s.handleMemoryRead(rr, req)
	if rr.Code != http.StatusForbidden {
		t.Errorf("status = %d, want 403", rr.Code)
	}
}

func TestHandleMemoryRead_InvalidScope_400(t *testing.T) {
	s, _ := newReadTestServer(t)
	req := httptest.NewRequest("GET", "http://localhost/memory/read?file=AGENT.md&scope=bogus", nil)
	rr := httptest.NewRecorder()
	s.handleMemoryRead(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", rr.Code)
	}
}

func TestHandleMemoryRead_NoEngineForScope_503(t *testing.T) {
	silent := slog.New(slog.NewTextHandler(io.Discard, nil))
	s := &Server{logger: silent}
	req := httptest.NewRequest("GET", "http://localhost/memory/read?file=AGENT.md", nil)
	rr := httptest.NewRecorder()
	s.handleMemoryRead(rr, req)
	if rr.Code != http.StatusServiceUnavailable {
		t.Errorf("status = %d, want 503", rr.Code)
	}
}

func TestHandleMemoryRead_EmptyFile_NotMissing(t *testing.T) {
	// An empty file is still a *present* file — must return 200 +
	// empty content, NOT 404. Mirrors the write path which allows
	// empty content on create.
	s, base := newReadTestServer(t)
	seedFile(t, base, "AGENT.md", "")

	req := httptest.NewRequest("GET", "http://localhost/memory/read?file=AGENT.md", nil)
	rr := httptest.NewRecorder()
	s.handleMemoryRead(rr, req)
	if rr.Code != http.StatusOK {
		t.Errorf("status = %d, want 200 for empty existing file", rr.Code)
	}
	var resp MemoryReadResponse
	_ = json.Unmarshal(rr.Body.Bytes(), &resp)
	if resp.Bytes != 0 {
		t.Errorf("bytes = %d, want 0", resp.Bytes)
	}
}
