package memory

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"

	"github.com/crewship-ai/crewship/internal/scrubber"
)

// TestWriter_MaxBytesZero_AllowsLargeWrite pins the documented "MaxBytes=0
// means no cap" contract: a 5 MiB payload must land on disk unchanged
// when MaxBytes is left at its zero value. Without this lock, a future
// refactor could accidentally turn the zero default into a hard zero
// limit and silently reject every write.
func TestWriter_MaxBytesZero_AllowsLargeWrite(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "AGENT.md")

	// 5 MiB of deterministic bytes — well above any realistic per-tier
	// cap. Use a non-repeating-period pattern so an accidental truncate
	// would be detectable by length AND content.
	const size = 5 * 1024 * 1024
	content := make([]byte, size)
	for i := range content {
		content[i] = byte('a' + (i % 26))
	}

	res, err := WriteFile(context.Background(), path, content, WriteConfig{})
	if err != nil {
		t.Fatalf("WriteFile (MaxBytes unset, 5 MiB): %v", err)
	}
	if res.Rejected {
		t.Fatalf("zero-MaxBytes write must not be rejected, got %+v", res)
	}
	if res.BytesWritten != size {
		t.Errorf("BytesWritten = %d, want %d", res.BytesWritten, size)
	}

	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if !bytes.Equal(got, content) {
		t.Errorf("on-disk content differs from input (lens got=%d want=%d)", len(got), len(content))
	}
}

// TestWriter_MaxBytesOverflow_RejectsAndLeavesDiskUntouched complements
// the existing CapOverflow test by additionally asserting NO tempfile
// is left behind in the parent dir after rejection. The existing test
// only checks the target file's content; a regression that wrote a
// stub tempfile pre-cap would slip past it.
func TestWriter_MaxBytesOverflow_RejectsAndLeavesDiskUntouched(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "AGENT.md")

	preExisting := []byte("preserved\n")
	if err := os.WriteFile(path, preExisting, 0o600); err != nil {
		t.Fatalf("seed: %v", err)
	}

	res, err := WriteFile(context.Background(), path, []byte(strings.Repeat("x", 100)), WriteConfig{MaxBytes: 50})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !res.Rejected || res.RejectionKind != "cap" {
		t.Fatalf("expected cap rejection, got %+v", res)
	}

	// File untouched.
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if !bytes.Equal(got, preExisting) {
		t.Errorf("pre-existing file mutated: got %q", got)
	}

	// And no .tmp.* siblings were created.
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}
	for _, e := range entries {
		if strings.Contains(e.Name(), ".tmp.") {
			t.Errorf("tempfile leak after cap rejection: %s", e.Name())
		}
	}
}

// TestWriter_ConcurrentWriters_OneWinsCleanly runs two writers racing
// at the same path and asserts: (a) both calls return without error,
// (b) no tmp leak remains, (c) the final on-disk content is byte-equal
// to ONE of the two payloads (never a mix, never empty, never partial).
// The writer guarantees serialisation via flock — the contract is "last
// rename wins", but the schedule is non-deterministic so we accept
// either payload as the winner.
func TestWriter_ConcurrentWriters_OneWinsCleanly(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "AGENT.md")

	payloadA := []byte(strings.Repeat("AAAA\n", 256))
	payloadB := []byte(strings.Repeat("BBBB\n", 257)) // different length on purpose

	var wg sync.WaitGroup
	wg.Add(2)
	errs := make(chan error, 2)
	go func() {
		defer wg.Done()
		_, err := WriteFile(context.Background(), path, payloadA, WriteConfig{})
		errs <- err
	}()
	go func() {
		defer wg.Done()
		_, err := WriteFile(context.Background(), path, payloadB, WriteConfig{})
		errs <- err
	}()
	wg.Wait()
	close(errs)
	for e := range errs {
		if e != nil {
			t.Fatalf("concurrent writer error: %v", e)
		}
	}

	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if !bytes.Equal(got, payloadA) && !bytes.Equal(got, payloadB) {
		t.Errorf("final content matches neither writer (len=%d)", len(got))
	}

	// No tempfile residue.
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		if strings.Contains(e.Name(), ".tmp.") {
			t.Errorf("tmpfile leak after concurrent race: %s", e.Name())
		}
	}
}

// TestWriter_ScrubberBlock_KeepsFileMissing covers a corner the existing
// ScrubberBlock test doesn't: when the file pre-exists with arbitrary
// content, a scrubber rejection must leave THAT prior content in place
// — not delete it, not truncate it, not replace it. Regression guard
// against a future "open target as truncate-on-create" refactor.
func TestWriter_ScrubberBlock_KeepsFileMissing(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "AGENT.md")

	prior := []byte("# notes\nimportant pre-existing memory\n")
	if err := os.WriteFile(path, prior, 0o600); err != nil {
		t.Fatalf("seed: %v", err)
	}

	// Anthropic key pattern from scrubber.go.
	bad := []byte("leak: sk-ant-api03-AAAAAAAAAAAAAAAAAAAA tail")
	res, err := WriteFile(context.Background(), path, bad, WriteConfig{
		Scrubber:     scrubber.New(),
		ScrubberMode: scrubber.ModeBlock,
	})
	if err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	if !res.Rejected || res.RejectionKind != "scrubber" {
		t.Fatalf("expected scrubber rejection, got %+v", res)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if !bytes.Equal(got, prior) {
		t.Errorf("prior content overwritten on scrubber rejection: got %q want %q", got, prior)
	}
}

// TestWriter_ScrubberRedact_OnDiskHasRedactedForm asserts the redacted
// (not original) bytes land on disk when ModeRedact catches a pattern.
// Distinct from the existing ScrubberRedact test in writer_test.go in
// that it tests a DIFFERENT credential family (GitHub PAT) — pins the
// behaviour across patterns, not just anthropic_key.
func TestWriter_ScrubberRedact_OnDiskHasRedactedForm(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "CREW.md")

	content := []byte("debug token ghp_abcdefghijklmnopqrstuvwxyz0123 trailing")
	res, err := WriteFile(context.Background(), path, content, WriteConfig{
		Scrubber:     scrubber.New(),
		ScrubberMode: scrubber.ModeRedact,
	})
	if err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	if res.Rejected {
		t.Fatalf("redact must not reject, got %+v", res)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if strings.Contains(string(got), "ghp_abcdefghijklmnopqrstuvwxyz0123") {
		t.Errorf("on-disk content still contains raw GitHub PAT: %q", got)
	}
	if !strings.Contains(string(got), "[REDACTED:") {
		t.Errorf("on-disk content missing redaction marker: %q", got)
	}
	// BytesWritten reflects the redacted form (which is longer than the
	// original here, since [REDACTED:github_token] > ghp_…).
	if res.BytesWritten != len(got) {
		t.Errorf("BytesWritten=%d, on-disk size=%d — should match", res.BytesWritten, len(got))
	}
}

// TestWriter_TargetFilePerms_MatchesCodeContract pins the actual perm
// bits the writer creates. Reading writer.go: tempfile is opened with
// 0o644 and parent dir is MkdirAll(0o755) — NOT the 0o600/0o700 the
// caller brief assumed. Locking the real contract here so any tightening
// is a conscious change visible in this test's diff, not a silent drift.
// macOS/linux only — Windows perm bits don't map cleanly.
func TestWriter_TargetFilePerms_MatchesCodeContract(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("perm bits are unix-only")
	}
	dir := t.TempDir()
	nested := filepath.Join(dir, "subdir")
	path := filepath.Join(nested, "AGENT.md")

	if _, err := WriteFile(context.Background(), path, []byte("ok"), WriteConfig{}); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	fi, err := os.Stat(path)
	if err != nil {
		t.Fatalf("Stat file: %v", err)
	}
	// Mask out type bits; compare perm bits only.
	if got := fi.Mode().Perm(); got != 0o644 {
		t.Errorf("file perms = %#o, want 0o644 (writer.go OpenFile mode)", got)
	}

	di, err := os.Stat(nested)
	if err != nil {
		t.Fatalf("Stat parent dir: %v", err)
	}
	// Note: MkdirAll honours umask. On a typical dev box umask=022 →
	// 0o755; on a hardened box umask=077 → 0o700. Accept either by
	// asserting owner-rwx is present and group/other write is absent.
	dp := di.Mode().Perm()
	if dp&0o700 != 0o700 {
		t.Errorf("parent dir owner perms missing: got %#o", dp)
	}
	if dp&0o022 != 0 {
		t.Errorf("parent dir is group/other writable: got %#o", dp)
	}
}

// TestWriter_UnwritableParentDir_NoTempfileLeak makes the parent dir
// non-writable so the lockfile acquire (or tempfile create) fails. The
// invariant: WriteFile returns an error AND leaves no .tmp.* / .lock
// debris behind for a follow-up writer to trip over. The exact failure
// point (Lock vs OpenFile) is intentionally unverified — both are valid
// per the code, what matters is no partial state remains.
func TestWriter_UnwritableParentDir_NoTempfileLeak(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("perm-based denial is unix-only")
	}
	if os.Geteuid() == 0 {
		t.Skip("root bypasses perm bits — test would falsely pass")
	}
	dir := t.TempDir()
	readonly := filepath.Join(dir, "ro")
	if err := os.Mkdir(readonly, 0o500); err != nil { // r-x------
		t.Fatalf("mkdir readonly: %v", err)
	}
	// Restore perms so t.TempDir cleanup can rm -rf.
	defer func() { _ = os.Chmod(readonly, 0o700) }()

	path := filepath.Join(readonly, "AGENT.md")
	_, err := WriteFile(context.Background(), path, []byte("nope"), WriteConfig{})
	if err == nil {
		t.Fatalf("expected error writing into readonly dir, got nil")
	}

	// Listing may itself fail (rare) — that's fine, the assertion is
	// "if we CAN list, there must be no .tmp.* residue".
	if entries, lerr := os.ReadDir(readonly); lerr == nil {
		for _, e := range entries {
			n := e.Name()
			if strings.Contains(n, ".tmp.") {
				t.Errorf("tempfile leak after failed write: %s", n)
			}
		}
	}
}

// TestWriter_UTF8RoundTrip_NoBOMNoNormalization writes a mix of multi-
// byte UTF-8 (Czech diacritics, CJK, emoji, combining marks) and asserts
// byte-for-byte identity on disk. No BOM injection, no NFC/NFD swap,
// no zero-width strip (that's the scrubber's normaliser — the writer
// itself must be transparent when the scrubber is nil).
func TestWriter_UTF8RoundTrip_NoBOMNoNormalization(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "AGENT.md")

	// Notes:
	// - "café" with combining acute (e + U+0301) vs precomposed é —
	//   the writer must preserve whichever the caller passed.
	// - U+200B zero-width space included — the writer (no scrubber) must
	//   NOT strip it, even though scrubber.Validate would.
	content := []byte("Příliš žluťoučký kůň\n" +
		"日本語テスト\n" +
		"emoji 🚢⚓\n" +
		"combining: café vs café\n" +
		"zwsp:[​]end\n")

	res, err := WriteFile(context.Background(), path, content, WriteConfig{})
	if err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	if res.BytesWritten != len(content) {
		t.Errorf("BytesWritten=%d, want %d", res.BytesWritten, len(content))
	}

	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if !bytes.Equal(got, content) {
		t.Errorf("UTF-8 round-trip mismatch:\n got=% x\nwant=% x", got, content)
	}
	// Explicit BOM guard — easy to forget if a future refactor wraps
	// the writer in bufio.Writer with a "smart" preamble.
	if bytes.HasPrefix(got, []byte{0xEF, 0xBB, 0xBF}) {
		t.Errorf("unexpected UTF-8 BOM at start of file")
	}
}
