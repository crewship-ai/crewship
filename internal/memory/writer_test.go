package memory

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/scrubber"
)

// TestWriteFile_VanillaWrite is the happy-path baseline: no scrubber,
// no cap, content lands at path with full byte-equality.
func TestWriteFile_VanillaWrite(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "AGENT.md")
	content := []byte("hello\n## section\nbody\n")

	res, err := WriteFile(context.Background(), path, content, WriteConfig{})
	if err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	if res.Rejected {
		t.Fatalf("vanilla write was rejected: %+v", res)
	}
	if res.BytesWritten != len(content) {
		t.Errorf("BytesWritten = %d, want %d", res.BytesWritten, len(content))
	}

	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if !bytes.Equal(got, content) {
		t.Errorf("on-disk content mismatch: got %q want %q", got, content)
	}
}

// TestWriteFile_CapOverflow asserts that writes exceeding MaxBytes are
// rejected before any disk I/O happens and a structured rejection is
// returned. The file on disk must remain untouched (creation or
// modification).
func TestWriteFile_CapOverflow(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "AGENT.md")

	// Pre-seed file with known content so we can prove rejection didn't touch it.
	preExisting := []byte("untouched\n")
	if err := os.WriteFile(path, preExisting, 0o644); err != nil {
		t.Fatalf("seed file: %v", err)
	}

	bigContent := []byte(strings.Repeat("x", 5000))
	res, err := WriteFile(context.Background(), path, bigContent, WriteConfig{MaxBytes: 4000})
	if err != nil {
		t.Fatalf("WriteFile returned error (should be nil with structured rejection): %v", err)
	}
	if !res.Rejected || res.RejectionKind != "cap" {
		t.Fatalf("expected Rejected=true RejectionKind=cap, got %+v", res)
	}
	if res.RejectionDetail["bytes_attempted"].(int) != 5000 {
		t.Errorf("bytes_attempted = %v, want 5000", res.RejectionDetail["bytes_attempted"])
	}
	if res.RejectionDetail["bytes_limit"].(int) != 4000 {
		t.Errorf("bytes_limit = %v, want 4000", res.RejectionDetail["bytes_limit"])
	}

	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile after rejection: %v", err)
	}
	if !bytes.Equal(got, preExisting) {
		t.Errorf("file was modified despite rejection: got %q", got)
	}
}

// TestWriteFile_ScrubberBlock asserts that a scrubber catch in
// ModeBlock returns a structured rejection (no error), leaves the file
// unchanged, and surfaces the hits to the caller.
func TestWriteFile_ScrubberBlock(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "AGENT.md")

	content := []byte("my key is sk-ant-api03-abcd1234efgh5678ijkl, don't share")
	res, err := WriteFile(context.Background(), path, content, WriteConfig{
		Scrubber:     scrubber.New(),
		ScrubberMode: scrubber.ModeBlock,
	})
	if err != nil {
		t.Fatalf("WriteFile returned error (should be nil with structured rejection): %v", err)
	}
	if !res.Rejected || res.RejectionKind != "scrubber" {
		t.Fatalf("expected Rejected=true RejectionKind=scrubber, got %+v", res)
	}
	if len(res.Hits) == 0 {
		t.Errorf("expected hits populated, got 0")
	}

	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Errorf("file should not exist after scrubber rejection, stat err=%v", err)
	}
}

// TestWriteFile_ScrubberWarn asserts the warn mode writes the original
// content to disk (the rejection is informational only) while still
// surfacing hits to the caller so it can journal a memory.write_rejected
// entry as a warning-only signal.
func TestWriteFile_ScrubberWarn(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "AGENT.md")

	content := []byte("AKIAIOSFODNN7EXAMPLE")
	res, err := WriteFile(context.Background(), path, content, WriteConfig{
		Scrubber:     scrubber.New(),
		ScrubberMode: scrubber.ModeWarn,
	})
	if err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	if res.Rejected {
		t.Fatalf("warn mode should not reject, got %+v", res)
	}
	if len(res.Hits) == 0 {
		t.Errorf("hits should be populated in warn mode")
	}

	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if !bytes.Equal(got, content) {
		t.Errorf("warn mode should write original content; got %q want %q", got, content)
	}
}

// TestWriteFile_ScrubberRedact asserts ModeRedact persists the cleaned
// (REDACTED) form of the content.
func TestWriteFile_ScrubberRedact(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "AGENT.md")

	content := []byte("anthropic sk-ant-api03-abcd1234efgh5678ijkl key")
	res, err := WriteFile(context.Background(), path, content, WriteConfig{
		Scrubber:     scrubber.New(),
		ScrubberMode: scrubber.ModeRedact,
	})
	if err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	if res.Rejected {
		t.Fatalf("redact mode should not reject, got %+v", res)
	}

	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if !strings.Contains(string(got), "[REDACTED:anthropic_key]") {
		t.Errorf("expected redacted marker in on-disk content; got %q", got)
	}
	if strings.Contains(string(got), "sk-ant-api03") {
		t.Errorf("redacted file still contains the key: %q", got)
	}
}

// TestWriteFile_AtomicReplace_UnderConcurrency runs N goroutines that
// each write a distinct payload to the same path. The invariant: a
// reader running concurrently must never observe a partial / torn
// payload — only one of the N writers' full content, or the empty
// initial state. We sample the file 50 times and assert every sample
// is byte-equal to one of the expected payloads (and never a prefix
// that does not equal a complete payload).
func TestWriteFile_AtomicReplace_UnderConcurrency(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "AGENT.md")

	const N = 8
	payloads := make([][]byte, N)
	want := map[string]bool{"": true}
	for i := 0; i < N; i++ {
		// Different sizes increase the chance any partial-write would be visible.
		payloads[i] = []byte(strings.Repeat("abcdef\n", 100+i*7))
		want[string(payloads[i])] = true
	}

	var wg sync.WaitGroup
	for i := 0; i < N; i++ {
		wg.Add(1)
		go func(p []byte) {
			defer wg.Done()
			_, err := WriteFile(context.Background(), path, p, WriteConfig{})
			if err != nil {
				t.Errorf("concurrent WriteFile: %v", err)
			}
		}(payloads[i])
	}

	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()

	// Sample the file while writers race.
	for {
		select {
		case <-done:
			final, _ := os.ReadFile(path)
			if !want[string(final)] {
				t.Errorf("final file content not one of the expected payloads (len=%d)", len(final))
			}
			return
		default:
			data, err := os.ReadFile(path)
			if err == nil {
				if !want[string(data)] {
					t.Errorf("torn-write observed: file content (len=%d) does not match any expected payload", len(data))
					return
				}
			}
		}
	}
}

// TestWriteFile_ContextCancellation asserts ctx cancellation is
// honoured before the file lands on disk (the writer must check ctx
// before fsync/rename, otherwise a hung shutdown path holds work
// indefinitely).
func TestWriteFile_ContextCancellation(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "AGENT.md")

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := WriteFile(ctx, path, []byte("hi"), WriteConfig{})
	if err == nil {
		t.Fatalf("expected error from cancelled ctx, got nil")
	}
	if _, statErr := os.Stat(path); !os.IsNotExist(statErr) {
		t.Errorf("file should not exist after cancelled write, stat err=%v", statErr)
	}
}

// TestWriteFile_ParentDirCreated asserts the writer creates parent
// directories if they don't exist (consolidator output dirs may not
// pre-exist; current appendRules path relies on the same implicit
// create). This locks the contract.
func TestWriteFile_ParentDirCreated(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "nested", "deep", "AGENT.md")
	content := []byte("ok")

	res, err := WriteFile(context.Background(), path, content, WriteConfig{})
	if err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	if res.Rejected {
		t.Fatalf("vanilla write was rejected: %+v", res)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if !bytes.Equal(got, content) {
		t.Errorf("nested write content mismatch: got %q", got)
	}
}

// TestWriteFile_NoLockfileLeak asserts the .lock sentinel still exists
// after the write but is closed (we can open it for read). Lockfile
// stays on disk by design — flock state is per-fd, not on the file —
// so re-opening it does not "stay locked".
func TestWriteFile_NoLockfileLeak(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "AGENT.md")
	if _, err := WriteFile(context.Background(), path, []byte("a"), WriteConfig{}); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	lockPath := path + ".lock"
	if _, err := os.Stat(lockPath); err != nil {
		t.Fatalf("lockfile should still exist: %v", err)
	}
	// A second writer must be able to proceed (lock is released).
	deadline := time.NewTimer(2 * time.Second)
	defer deadline.Stop()
	doneCh := make(chan error, 1)
	go func() {
		_, err := WriteFile(context.Background(), path, []byte("b"), WriteConfig{})
		doneCh <- err
	}()
	select {
	case err := <-doneCh:
		if err != nil {
			t.Fatalf("second writer failed: %v", err)
		}
	case <-deadline.C:
		t.Fatalf("second writer blocked on (presumably stale) lock — flock leak")
	}
}
