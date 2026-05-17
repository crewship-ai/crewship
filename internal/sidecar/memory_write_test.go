package sidecar

import (
	"bytes"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/memory"
	"github.com/crewship-ai/crewship/internal/scrubber"
)

func newWriteTestServer(t *testing.T) (*Server, string) {
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
		scrubber:        scrubber.New(),
		logger:          silent,
	}, base
}

func TestHandleMemoryWrite_OK(t *testing.T) {
	s, base := newWriteTestServer(t)
	body, _ := json.Marshal(MemoryWriteRequest{File: "AGENT.md", Content: "hello\n## section\nbody\n"})
	req := httptest.NewRequest("POST", "http://localhost/memory/write", bytes.NewReader(body))
	rr := httptest.NewRecorder()
	s.handleMemoryWrite(rr, req)
	if rr.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201; body=%s", rr.Code, rr.Body.String())
	}
	got, _ := os.ReadFile(filepath.Join(base, "AGENT.md"))
	if !strings.Contains(string(got), "## section") {
		t.Errorf("file content not persisted as expected: %q", got)
	}
}

func TestHandleMemoryWrite_ScrubberRejects(t *testing.T) {
	s, base := newWriteTestServer(t)
	body, _ := json.Marshal(MemoryWriteRequest{
		File:    "AGENT.md",
		Content: "key sk-ant-api03-abcdefghijklmnopqrst",
	})
	req := httptest.NewRequest("POST", "http://localhost/memory/write", bytes.NewReader(body))
	rr := httptest.NewRecorder()
	s.handleMemoryWrite(rr, req)
	if rr.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want 422; body=%s", rr.Code, rr.Body.String())
	}
	var rej MemoryWriteRejection
	if err := json.NewDecoder(rr.Body).Decode(&rej); err != nil {
		t.Fatalf("decode rejection: %v", err)
	}
	if rej.Kind != "scrubber" {
		t.Errorf("rejection kind = %q, want scrubber", rej.Kind)
	}
	if len(rej.Hits) == 0 {
		t.Errorf("expected hits populated, got 0")
	}
	if _, err := os.Stat(filepath.Join(base, "AGENT.md")); !os.IsNotExist(err) {
		t.Errorf("file should not exist after scrubber rejection")
	}
}

func TestHandleMemoryWrite_CapOverflow(t *testing.T) {
	s, _ := newWriteTestServer(t)
	body, _ := json.Marshal(MemoryWriteRequest{
		File:    "AGENT.md",
		Content: strings.Repeat("x", 5000),
	})
	req := httptest.NewRequest("POST", "http://localhost/memory/write", bytes.NewReader(body))
	rr := httptest.NewRecorder()
	s.handleMemoryWrite(rr, req)
	if rr.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want 422", rr.Code)
	}
	var rej MemoryWriteRejection
	_ = json.NewDecoder(rr.Body).Decode(&rej)
	if rej.Kind != "cap" {
		t.Errorf("rejection kind = %q, want cap", rej.Kind)
	}
}

func TestHandleMemoryWrite_PathTraversal_Blocked(t *testing.T) {
	s, _ := newWriteTestServer(t)
	body, _ := json.Marshal(MemoryWriteRequest{File: "../../../etc/passwd", Content: "x"})
	req := httptest.NewRequest("POST", "http://localhost/memory/write", bytes.NewReader(body))
	rr := httptest.NewRecorder()
	s.handleMemoryWrite(rr, req)
	if rr.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", rr.Code)
	}
}

func TestHandleMemoryWrite_AbsolutePath_Blocked(t *testing.T) {
	s, _ := newWriteTestServer(t)
	body, _ := json.Marshal(MemoryWriteRequest{File: "/etc/passwd", Content: "x"})
	req := httptest.NewRequest("POST", "http://localhost/memory/write", bytes.NewReader(body))
	rr := httptest.NewRecorder()
	s.handleMemoryWrite(rr, req)
	if rr.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", rr.Code)
	}
}

func TestHandleMemoryWrite_DailyLog(t *testing.T) {
	s, base := newWriteTestServer(t)
	body, _ := json.Marshal(MemoryWriteRequest{
		File:    "daily/2026-05-16.md",
		Content: "session notes\n",
	})
	req := httptest.NewRequest("POST", "http://localhost/memory/write", bytes.NewReader(body))
	rr := httptest.NewRecorder()
	s.handleMemoryWrite(rr, req)
	if rr.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201; body=%s", rr.Code, rr.Body.String())
	}
	if _, err := os.Stat(filepath.Join(base, "daily", "2026-05-16.md")); err != nil {
		t.Errorf("daily log not persisted: %v", err)
	}
}

func TestHandleMemoryWrite_MissingFile_400(t *testing.T) {
	s, _ := newWriteTestServer(t)
	body, _ := json.Marshal(MemoryWriteRequest{Content: "x"})
	req := httptest.NewRequest("POST", "http://localhost/memory/write", bytes.NewReader(body))
	rr := httptest.NewRecorder()
	s.handleMemoryWrite(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rr.Code)
	}
}

func TestHandleMemoryWrite_BadJSON_400(t *testing.T) {
	s, _ := newWriteTestServer(t)
	req := httptest.NewRequest("POST", "http://localhost/memory/write", bytes.NewReader([]byte("not json")))
	rr := httptest.NewRecorder()
	s.handleMemoryWrite(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rr.Code)
	}
}

func TestHandleMemoryWrite_NoEngine_503(t *testing.T) {
	silent := slog.New(slog.NewTextHandler(io.Discard, nil))
	s := &Server{logger: silent, scrubber: scrubber.New()}
	body, _ := json.Marshal(MemoryWriteRequest{File: "AGENT.md", Content: "x"})
	req := httptest.NewRequest("POST", "http://localhost/memory/write", bytes.NewReader(body))
	rr := httptest.NewRecorder()
	s.handleMemoryWrite(rr, req)
	if rr.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", rr.Code)
	}
}
