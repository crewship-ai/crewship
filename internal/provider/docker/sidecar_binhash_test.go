package docker

import (
	"bytes"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// TestExpectedSidecarHash locks #1008: the provider reports a stable content
// hash of the on-disk crewship-sidecar BINARY it bind-mounts, and the hash
// tracks the file's contents (so a redeploy that rewrites the binary changes
// it). It fails open (empty) when the path is unset or unreadable, so
// stale-sidecar detection never raises a false alarm on an unknown path.
func TestExpectedSidecarHash(t *testing.T) {
	dir := t.TempDir()
	bin := filepath.Join(dir, "crewship-sidecar")
	if err := os.WriteFile(bin, []byte("BINARY-V1"), 0o755); err != nil {
		t.Fatal(err)
	}

	p := &Provider{cfg: Config{SidecarBinaryPath: bin}}

	h1 := p.ExpectedSidecarHash()
	if h1 == "" {
		t.Fatal("ExpectedSidecarHash empty for a readable binary")
	}
	if h2 := p.ExpectedSidecarHash(); h2 != h1 {
		t.Errorf("hash not stable across calls: %q vs %q", h1, h2)
	}

	// A redeploy rewrites the binary → hash must change (mtime + size change
	// invalidate the memoized value).
	if err := os.WriteFile(bin, []byte("BINARY-V2-different-length"), 0o755); err != nil {
		t.Fatal(err)
	}
	if h3 := p.ExpectedSidecarHash(); h3 == h1 {
		t.Errorf("hash did not change after the binary contents changed: still %q", h3)
	}

	// Unset path → fail open (empty).
	if h := (&Provider{cfg: Config{SidecarBinaryPath: ""}}).ExpectedSidecarHash(); h != "" {
		t.Errorf("unset path should yield empty hash, got %q", h)
	}
	// Missing file → fail open (empty).
	if h := (&Provider{cfg: Config{SidecarBinaryPath: filepath.Join(dir, "nope")}}).ExpectedSidecarHash(); h != "" {
		t.Errorf("missing file should yield empty hash, got %q", h)
	}
}

// setBuildHash swaps the link-time injected hash for the test's lifetime and
// restores the previous value on cleanup. The var is package-global, so tests
// that touch it must not run in parallel.
func setBuildHash(t *testing.T, v string) {
	t.Helper()
	prev := buildExpectedSidecarHash
	buildExpectedSidecarHash = v
	t.Cleanup(func() { buildExpectedSidecarHash = prev })
}

// writeSidecarFixture writes a fake sidecar binary and returns its path plus
// the on-disk hash the provider computes for it.
func writeSidecarFixture(t *testing.T, content string) (path, onDiskHash string) {
	t.Helper()
	path = filepath.Join(t.TempDir(), "crewship-sidecar")
	if err := os.WriteFile(path, []byte(content), 0o755); err != nil {
		t.Fatal(err)
	}
	onDiskHash = (&Provider{cfg: Config{SidecarBinaryPath: path}}).ExpectedSidecarHash()
	if onDiskHash == "" {
		t.Fatal("fixture on-disk hash unexpectedly empty")
	}
	return path, onDiskHash
}

// TestExpectedSidecarHash_BuildTimeHashAuthoritative locks #1160 ask 2: when
// a build-time expected hash was injected via ldflags, it is authoritative —
// the provider reports IT (not the on-disk hash), so a deploy that updated
// the server but forgot `make build:sidecar` + copy (old-vs-old on disk)
// still trips stale detection against running sidecars. The on-disk/build
// divergence itself is surfaced as an error log exactly once per
// (path, on-disk hash) — restarting agents cannot fix a stale ARTIFACT, so
// operators need the distinct "redeploy the sidecar binary" message.
func TestExpectedSidecarHash_BuildTimeHashAuthoritative(t *testing.T) {
	bin, onDisk := writeSidecarFixture(t, "OLD-ARTIFACT-BYTES")
	const build = "abcdef012345"
	if onDisk == build {
		t.Fatalf("fixture hash collides with build hash: %q", onDisk)
	}
	setBuildHash(t, build)

	var logBuf bytes.Buffer
	p := &Provider{
		cfg:    Config{SidecarBinaryPath: bin},
		logger: slog.New(slog.NewTextHandler(&logBuf, nil)),
	}

	if got := p.ExpectedSidecarHash(); got != build {
		t.Fatalf("build-time hash not authoritative: got %q, want %q", got, build)
	}
	firstLog := logBuf.String()
	if !strings.Contains(firstLog, "stale") || !strings.Contains(firstLog, onDisk) || !strings.Contains(firstLog, build) {
		t.Errorf("artifact-staleness warning missing or incomplete, log: %q", firstLog)
	}

	// Second call: same result, but the warning must NOT repeat (warn-once
	// per path+hash pair; detection runs on every agent exec).
	logBuf.Reset()
	if got := p.ExpectedSidecarHash(); got != build {
		t.Fatalf("second call: got %q, want %q", got, build)
	}
	if logBuf.Len() != 0 {
		t.Errorf("artifact-staleness warning repeated: %q", logBuf.String())
	}
}

// TestExpectedSidecarHash_BuildHashMatchesOnDisk: healthy deploy — the
// on-disk artifact is exactly the one baked at build time. No warning.
func TestExpectedSidecarHash_BuildHashMatchesOnDisk(t *testing.T) {
	bin, onDisk := writeSidecarFixture(t, "FRESH-ARTIFACT-BYTES")
	setBuildHash(t, onDisk)

	var logBuf bytes.Buffer
	p := &Provider{
		cfg:    Config{SidecarBinaryPath: bin},
		logger: slog.New(slog.NewTextHandler(&logBuf, nil)),
	}
	if got := p.ExpectedSidecarHash(); got != onDisk {
		t.Fatalf("got %q, want %q", got, onDisk)
	}
	if logBuf.Len() != 0 {
		t.Errorf("unexpected warning for a matching artifact: %q", logBuf.String())
	}
}

// TestExpectedSidecarHash_BuildHashNormalized: the Makefile shell pipeline
// may leave a trailing newline or uppercase hex; the injected value is
// trimmed + lowercased before use.
func TestExpectedSidecarHash_BuildHashNormalized(t *testing.T) {
	bin, _ := writeSidecarFixture(t, "WHATEVER-BYTES")
	setBuildHash(t, "  ABCDEF012345\n")

	p := &Provider{cfg: Config{SidecarBinaryPath: bin}, logger: slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil))}
	if got := p.ExpectedSidecarHash(); got != "abcdef012345" {
		t.Fatalf("normalization failed: got %q, want %q", got, "abcdef012345")
	}
}

// TestExpectedSidecarHash_MalformedBuildHashIgnored: a garbage injection
// (wrong length, non-hex) must fail open to the on-disk hash — never turn a
// bad build flag into a fleet-wide false stale alarm.
func TestExpectedSidecarHash_MalformedBuildHashIgnored(t *testing.T) {
	bin, onDisk := writeSidecarFixture(t, "SOME-BYTES")
	for _, bad := range []string{"not-a-hash!", "abcdef", "abcdef0123456789", "ABCDEFG12345"} {
		setBuildHash(t, bad)
		p := &Provider{cfg: Config{SidecarBinaryPath: bin}}
		if got := p.ExpectedSidecarHash(); got != onDisk {
			t.Errorf("build hash %q: got %q, want on-disk fallback %q", bad, got, onDisk)
		}
	}
}

// TestExpectedSidecarHash_BuildHashWithUnreadableDisk: the build-time hash
// keeps stale detection alive even when the on-disk binary can't be read
// (today that returns "" and detection goes blind).
func TestExpectedSidecarHash_BuildHashWithUnreadableDisk(t *testing.T) {
	const build = "0123456789ab"
	setBuildHash(t, build)
	p := &Provider{cfg: Config{SidecarBinaryPath: filepath.Join(t.TempDir(), "nope")}}
	if got := p.ExpectedSidecarHash(); got != build {
		t.Fatalf("got %q, want build hash %q", got, build)
	}
}

// TestSidecarHashShellContract locks the Makefile↔Go contract: the shell
// pipeline the Makefile uses to compute SIDECAR_BUILD_HASH
// ((sha256sum || shasum -a 256) | cut -c1-12) must produce exactly the same
// 12-hex-char value as the provider's on-disk hash (and the sidecar's
// selfExeHash, which shares the format). If either side changes shape, the
// injected hash would mismatch every healthy deploy and alarm fleet-wide.
func TestSidecarHashShellContract(t *testing.T) {
	bin, onDisk := writeSidecarFixture(t, "CONTRACT-BYTES-#1160")
	cmd := exec.Command("sh", "-c",
		`(sha256sum "$1" 2>/dev/null || shasum -a 256 "$1" 2>/dev/null) | cut -c1-12`,
		"sh", bin)
	out, err := cmd.Output()
	if err != nil {
		t.Skipf("no sha256 tool available: %v", err)
	}
	got := strings.TrimSpace(string(out))
	if got != onDisk {
		t.Fatalf("shell pipeline hash %q != Go hash %q", got, onDisk)
	}
}
