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
	"time"

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
	ex := newMemoryExecutor(silent)
	t.Cleanup(func() { ex.Close(time.Second) })
	return &Server{
		memoryEngine:    eng,
		agentMemoryBase: base,
		scrubber:        scrubber.New(),
		logger:          silent,
		memoryExec:      ex,
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

// TestHandleMemoryWrite_SlowReindexDoesNotBlock201 occupies the
// executor's single in-flight slot with a long-running task, then issues
// a write. The 201 must return promptly without waiting for the queued
// reindex to run — proving the reindex is off the request hot path.
func TestHandleMemoryWrite_SlowReindexDoesNotBlock201(t *testing.T) {
	s, _ := newWriteTestServer(t)

	// Wedge the worker with a slow task so any post-write reindex this
	// handler enqueues sits behind it. If the handler reindexed inline,
	// it would not even reach submit — but if it (incorrectly) blocked
	// waiting for the queue to clear, the 201 would be delayed by ~the
	// slow task's runtime.
	release := make(chan struct{})
	s.memoryExec.submit(func() { <-release })

	body, _ := json.Marshal(MemoryWriteRequest{File: "AGENT.md", Content: "fast path\n"})
	req := httptest.NewRequest("POST", "http://localhost/memory/write", bytes.NewReader(body))
	rr := httptest.NewRecorder()

	t0 := time.Now()
	s.handleMemoryWrite(rr, req)
	elapsed := time.Since(t0)

	if rr.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201; body=%s", rr.Code, rr.Body.String())
	}
	if elapsed > 500*time.Millisecond {
		t.Fatalf("handler blocked for %v; reindex is not off-thread", elapsed)
	}
	close(release)
}

// TestHandleMemoryWrite_OrderingPreserved issues several writes and
// confirms the file content reflects the LAST write after the executor
// drains — strict-FIFO reindexing means the final state is consistent
// with the final write, never an earlier turn winning a race.
func TestHandleMemoryWrite_OrderingPreserved(t *testing.T) {
	s, base := newWriteTestServer(t)

	for i := 0; i < 5; i++ {
		content := "turn " + string(rune('0'+i)) + "\n"
		body, _ := json.Marshal(MemoryWriteRequest{File: "AGENT.md", Content: content})
		req := httptest.NewRequest("POST", "http://localhost/memory/write", bytes.NewReader(body))
		rr := httptest.NewRecorder()
		s.handleMemoryWrite(rr, req)
		if rr.Code != http.StatusCreated {
			t.Fatalf("write %d status = %d, want 201", i, rr.Code)
		}
	}

	// Barrier: all enqueued reindexes have run in submission order.
	if !s.memoryExec.Flush(2 * time.Second) {
		t.Fatalf("executor Flush timed out")
	}

	got, _ := os.ReadFile(filepath.Join(base, "AGENT.md"))
	if !strings.Contains(string(got), "turn 4") {
		t.Errorf("final content = %q, want last write 'turn 4'", got)
	}
}

// TestHandleMemoryWrite_SyncFallbackNoExecutor verifies the handler still
// reindexes + emits synchronously when no executor is wired (degraded /
// legacy construction), so the index never silently diverges from disk.
func TestHandleMemoryWrite_SyncFallbackNoExecutor(t *testing.T) {
	s, base := newWriteTestServer(t)
	s.memoryExec.Close(time.Second)
	s.memoryExec = nil // force the synchronous fallback branch

	body, _ := json.Marshal(MemoryWriteRequest{File: "AGENT.md", Content: "sync fallback\n"})
	req := httptest.NewRequest("POST", "http://localhost/memory/write", bytes.NewReader(body))
	rr := httptest.NewRecorder()
	s.handleMemoryWrite(rr, req)
	if rr.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201; body=%s", rr.Code, rr.Body.String())
	}
	got, _ := os.ReadFile(filepath.Join(base, "AGENT.md"))
	if !strings.Contains(string(got), "sync fallback") {
		t.Errorf("file not persisted on sync fallback: %q", got)
	}
}

// TestHandleMemoryWrite_SyncFallbackReindexError exercises the synchronous
// fallback's reindex-error warning branch: WriteFile (disk-only) still
// succeeds, but the engine's DB is closed so ReindexContext fails. The handler
// logs and swallows the error — the write is already durable, so the 201
// still stands.
func TestHandleMemoryWrite_SyncFallbackReindexError(t *testing.T) {
	s, _ := newWriteTestServer(t)
	s.memoryExec.Close(time.Second)
	s.memoryExec = nil         // force the synchronous fallback branch
	_ = s.memoryEngine.Close() // make ReindexContext fail (closed DB)

	body, _ := json.Marshal(MemoryWriteRequest{File: "AGENT.md", Content: "x\n"})
	req := httptest.NewRequest("POST", "http://localhost/memory/write", bytes.NewReader(body))
	rr := httptest.NewRecorder()
	s.handleMemoryWrite(rr, req)
	if rr.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201 even on reindex error; body=%s", rr.Code, rr.Body.String())
	}
}

// TestHandleMemoryWrite_OffThreadReindexError exercises the off-thread
// closure's reindex-error warning branch: closing the engine underneath the
// executor makes the queued ReindexContext fail. The 201 is unaffected; the
// error is logged off-thread.
func TestHandleMemoryWrite_OffThreadReindexError(t *testing.T) {
	s, _ := newWriteTestServer(t)

	body, _ := json.Marshal(MemoryWriteRequest{File: "AGENT.md", Content: "x\n"})
	req := httptest.NewRequest("POST", "http://localhost/memory/write", bytes.NewReader(body))
	rr := httptest.NewRecorder()

	// Close the engine before the queued reindex runs so ReindexContext
	// errors inside the off-thread closure.
	_ = s.memoryEngine.Close()
	s.handleMemoryWrite(rr, req)
	if rr.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201; body=%s", rr.Code, rr.Body.String())
	}
	if !s.memoryExec.Flush(2 * time.Second) {
		t.Fatalf("executor Flush timed out")
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
