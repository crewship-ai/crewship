//go:build !windows

// The tests in this file exercise Unix-only syscalls (Mkfifo, ELOOP) that
// don't exist on Windows. The build constraint keeps `go build`/`go test`
// on a Windows host from failing to compile — the runtime.GOOS skips
// inside each test only matter at test time and don't help the type
// checker. Memory indexer itself runs only on Unix sidecars
// (containers), so no Windows production path exercises the helper this
// test covers.

package memory

import (
	"errors"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

// TestReadRegularNoFollow_RegularFileOK confirms the helper still reads
// normal files — it's the success path the indexer relies on.
func TestReadRegularNoFollow_RegularFileOK(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "ok.md")
	if err := os.WriteFile(path, []byte("hello"), 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := readRegularNoFollow(path)
	if err != nil {
		t.Fatalf("expected success, got %v", err)
	}
	if string(got) != "hello" {
		t.Fatalf("got %q want %q", got, "hello")
	}
}

// TestReadRegularNoFollow_RejectsSymlink is the G-002 regression test.
// An agent inside a container can write a symlink under .memory/ — pre-fix
// readRegularNoFollow's predecessor (os.ReadFile) would happily follow it
// and read whatever target the symlink pointed at, then index those bytes
// into memory_chunks where they'd be queryable via /memory/search.
//
// O_NOFOLLOW makes the open syscall fail with ELOOP. We don't care which
// specific error wraps it — only that the open did not succeed.
func TestReadRegularNoFollow_RejectsSymlink(t *testing.T) {
	// Build constraint at the top of this file already excludes Windows
	// from compiling these tests; no runtime check needed.
	dir := t.TempDir()

	// Create the file the symlink will point at — content the attacker
	// would want to exfiltrate.
	target := filepath.Join(dir, "secret.txt")
	if err := os.WriteFile(target, []byte("attacker would steal this"), 0o600); err != nil {
		t.Fatal(err)
	}

	// Create the symlink under the simulated .memory/ path.
	link := filepath.Join(dir, "innocent.md")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}

	_, err := readRegularNoFollow(link)
	if err == nil {
		t.Fatalf("readRegularNoFollow followed a symlink — G-002 regression")
	}
	// Most platforms surface ELOOP. Some may wrap as a generic open
	// error. Either is fine; the test cares that we *didn't* read the
	// target bytes.
	var pathErr *os.PathError
	if !errors.As(err, &pathErr) && !errors.Is(err, syscall.ELOOP) {
		t.Logf("unexpected error type %T (%v) — accepting as non-success", err, err)
	}
}

// TestReadRegularNoFollow_RejectsFIFO_DoesNotHang is the CodeRabbit
// follow-up. Pre-fix the helper used `O_RDONLY|O_NOFOLLOW`, which
// blocks Open() forever on a FIFO with no writer. Reindex holds e.mu
// during the whole walk, so a single planted FIFO under .memory/ would
// soft-DoS every memory op in the process. The fix adds O_NONBLOCK so
// the FIFO opens immediately; the post-open Stat then rejects it as
// non-regular. This test exercises the actual readRegularNoFollow code
// path with a deadline — pre-fix it would block past the deadline and
// fail fast rather than hanging the whole suite.
func TestReadRegularNoFollow_RejectsFIFO_DoesNotHang(t *testing.T) {
	// Build constraint at the top of this file already excludes Windows.
	dir := t.TempDir()
	fifo := filepath.Join(dir, "pipe.md")
	if err := syscall.Mkfifo(fifo, 0o600); err != nil {
		t.Skipf("mkfifo not permitted in this sandbox: %v", err)
	}

	done := make(chan error, 1)
	go func() {
		_, err := readRegularNoFollow(fifo)
		done <- err
	}()

	select {
	case err := <-done:
		if err == nil {
			t.Fatalf("readRegularNoFollow returned no error on FIFO — must reject as non-regular")
		}
		// Either the post-open Stat rejected it ("not a regular file")
		// or the open itself failed (e.g. EOPNOTSUPP). Both acceptable.
	case <-time.After(2 * time.Second):
		t.Fatal("readRegularNoFollow blocked on FIFO open — O_NONBLOCK regression (Reindex would soft-DoS)")
	}
}
