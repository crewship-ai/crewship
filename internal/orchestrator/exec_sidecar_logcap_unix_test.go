//go:build !windows

package orchestrator

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// -----------------------------------------------------------------------------
// Behavioural: run the shipped trim against a real file.
//
// The assertions in exec_sidecar_logcap_test.go are on the script's TEXT, which
// is tautological with respect to the constants: raising sidecarLogMaxBytes to
// 1<<62 leaves every one of them green while the cap stops capping anything.
// These tests execute the real /bin/sh the container runs and check what
// happens to the bytes.
//
// The build tag, rather than a runtime.GOOS check inside each test: the trim is
// /bin/sh and the crew container is Linux, so on Windows there is nothing to
// assert. A `t.Skip` would report the same "ok" as a pass and quietly hide the
// loss (scripts/skip-budget.sh). A build tag makes it a compile-time decision
// that shows up as a missing file, not a green test that never ran.
// -----------------------------------------------------------------------------

// overCapBytes is a fixed, absolute size — deliberately not derived from
// sidecarLogMaxBytes. A test that writes "cap + 1" moves with the constant and
// would keep passing at any cap; this one asserts that 2 MiB of sidecar stderr
// gets trimmed, which is a statement about what the cap has to be worth.
const overCapBytes = 2 << 20

// shellQuote wraps a path as a single shell word.
//
// sidecarLogTrimOnce takes a shell word, and in production that word is the
// literal /tmp/sidecar.log — no quoting needed, none applied. A test path comes
// from t.TempDir(), which on a CI runner can sit under a directory with a space
// in it, so quote it here rather than skipping the test when it does. The
// `.tail` suffix the trim appends outside the quotes still concatenates into
// one word ('/a b/c'.tail is /a b/c.tail to any POSIX shell), which is why this
// works without touching the generator.
func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// newLogWriter creates the log and returns an O_APPEND handle to it, mirroring
// the `2>>` the launch script gives the sidecar. Holding the handle across the
// trim is the point: the sidecar keeps one open file description for its whole
// life, so the trim has to survive that.
func newLogWriter(t *testing.T) (*os.File, string) {
	t.Helper()
	logPath := filepath.Join(t.TempDir(), "sidecar.log")
	f, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		t.Fatalf("create log: %v", err)
	}
	t.Cleanup(func() { _ = f.Close() })
	return f, logPath
}

func fill(t *testing.T, f *os.File, n int) {
	t.Helper()
	line := strings.Repeat("x", 63) + "\n"
	for written := 0; written < n; written += len(line) {
		if _, err := f.WriteString(line); err != nil {
			t.Fatalf("fill log: %v", err)
		}
	}
}

func runTrim(t *testing.T, logPath string) {
	t.Helper()
	out, err := exec.Command("/bin/sh", "-c", sidecarLogTrimOnce(shellQuote(logPath))).CombinedOutput()
	if err != nil {
		t.Fatalf("trim failed: %v\n%s", err, out)
	}
	if len(out) > 0 {
		t.Errorf("the trim is noisy; in the container it runs detached and this would be lost:\n%s", out)
	}
}

func sizeOf(t *testing.T, logPath string) int64 {
	t.Helper()
	fi, err := os.Stat(logPath)
	if err != nil {
		t.Fatalf("stat log: %v", err)
	}
	return fi.Size()
}

// The cap holds, the newest output survives it, and the file the sidecar has
// open is still the file it has open.
func TestSidecarLogTrim_BoundsTheFileAndKeepsTheNewestOutput(t *testing.T) {
	f, logPath := newLogWriter(t)

	fill(t, f, overCapBytes)
	const marker = "NEWEST-BEFORE-TRIM\n"
	if _, err := f.WriteString(marker); err != nil {
		t.Fatalf("write marker: %v", err)
	}

	before := sizeOf(t, logPath)
	if before <= sidecarLogMaxBytes {
		t.Fatalf("test wrote %d bytes but the cap is %d — nothing would be trimmed, so this "+
			"test proves nothing. The cap is too large to bound a crew's tmpfs charge.",
			before, sidecarLogMaxBytes)
	}

	runTrim(t, logPath)

	after := sizeOf(t, logPath)
	if after > sidecarLogKeepBytes {
		t.Errorf("the trim left %d bytes, above the %d-byte keep — the log is not bounded",
			after, sidecarLogKeepBytes)
	}
	if after == before {
		t.Errorf("the trim did not touch a %d-byte log; sidecar.log grows unbounded again", before)
	}

	body, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("read log: %v", err)
	}
	if !strings.HasSuffix(string(body), marker) {
		t.Error("the trim dropped the newest output — the half anyone debugging an OOM wants")
	}
}

// A log under the cap must be left completely alone. A trim that fired
// unconditionally would throw away an incident's entire context the moment the
// timer ticked.
func TestSidecarLogTrim_LeavesASmallLogAlone(t *testing.T) {
	f, logPath := newLogWriter(t)

	fill(t, f, 4<<10)
	before := sizeOf(t, logPath)

	runTrim(t, logPath)

	if got := sizeOf(t, logPath); got != before {
		t.Errorf("a %d-byte log (well under the %d-byte cap) was trimmed to %d",
			before, sidecarLogMaxBytes, got)
	}
}

// A path with a space in it must work. t.TempDir() usually produces a tame
// path, so without this the quoting in runTrim is untested on most machines and
// would rot the first time a CI runner's TMPDIR moved somewhere with a space.
func TestSidecarLogTrim_HandlesAPathNeedingShellQuoting(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "crew logs", "it's here")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	logPath := filepath.Join(dir, "sidecar.log")
	f, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		t.Fatalf("create log: %v", err)
	}
	defer f.Close()

	fill(t, f, overCapBytes)
	runTrim(t, logPath)

	if got := sizeOf(t, logPath); got > sidecarLogKeepBytes {
		t.Errorf("the trim left %d bytes on a path with a space and a quote in it", got)
	}
	if _, err := os.Stat(logPath + ".tail"); err == nil {
		t.Error("the trim left its .tail scratch file behind, doubling the tmpfs charge")
	}
}

// The trim must not rotate. The sidecar holds the log open for its whole life,
// so a rename would leave it writing into an unlinked inode: output unreadable,
// pages still charged to the cgroup. Checking the inode is the behavioural
// version of "no mv" — it catches any rotation, however it is spelled.
func TestSidecarLogTrim_RewritesInPlaceSoTheWritersFdSurvives(t *testing.T) {
	f, logPath := newLogWriter(t)

	fill(t, f, overCapBytes)

	beforeInfo, err := os.Stat(logPath)
	if err != nil {
		t.Fatalf("stat log: %v", err)
	}

	runTrim(t, logPath)

	afterInfo, err := os.Stat(logPath)
	if err != nil {
		t.Fatalf("stat log after trim: %v", err)
	}
	if !os.SameFile(beforeInfo, afterInfo) {
		t.Fatal("the trim replaced the log's inode; the sidecar is now writing into an " +
			"unlinked file — output invisible, pages still charged")
	}

	// The writer keeps writing through the fd it already had, and lands in the
	// trimmed file rather than past a hole. This is what `2>>` buys: an
	// appending writer reseeks to the new end. A non-appending one would keep
	// its pre-trim offset — see TestSidecarLogTrim_NonAppendingWriterDefeatsTheCap.
	const after = "AFTER-TRIM\n"
	if _, err := f.WriteString(after); err != nil {
		t.Fatalf("write after trim: %v", err)
	}
	if got := sizeOf(t, logPath); got > sidecarLogKeepBytes+int64(len(after)) {
		t.Errorf("the log is %d bytes after one post-trim write; the writer's offset did not "+
			"follow the trim, so the cap does not hold", got)
	}
	body, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("read log: %v", err)
	}
	if !strings.HasSuffix(string(body), after) {
		t.Error("post-trim output did not land at the end of the trimmed log")
	}
}

// The negative that justifies `2>>` in the launch script. Same trim, same file,
// only the writer's open mode differs — and the cap stops working.
//
// This is asserted, not skipped-if-absent. POSIX defines the file offset as a
// property of the open file description: another process truncating the file
// does not move it, and a write past EOF leaves a hole rather than being
// clamped. So a non-appending writer reliably restores the pre-trim size on
// every platform Go builds this file for. Without this test the append-mode
// requirement is a comment nobody can check.
func TestSidecarLogTrim_NonAppendingWriterDefeatsTheCap(t *testing.T) {
	logPath := filepath.Join(t.TempDir(), "sidecar.log")

	// os.O_WRONLY without os.O_APPEND — what `2>` would give the sidecar.
	f, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatalf("create log: %v", err)
	}
	defer f.Close()

	fill(t, f, overCapBytes)
	runTrim(t, logPath)

	trimmed := sizeOf(t, logPath)
	if trimmed > sidecarLogKeepBytes {
		t.Fatalf("precondition: trim left %d bytes", trimmed)
	}

	if _, err := f.WriteString("AFTER-TRIM\n"); err != nil {
		t.Fatalf("write after trim: %v", err)
	}

	if got := sizeOf(t, logPath); got <= sidecarLogKeepBytes {
		t.Fatalf("a non-appending writer wrote at %d bytes after a trim to %d — expected it "+
			"to restore the pre-trim size. If this ever holds, re-derive whether `2>>` is "+
			"still load-bearing in sidecarLaunchScript before relaxing it.",
			got, trimmed)
	}
}
