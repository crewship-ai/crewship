package docker

import (
	"os"
	"path/filepath"
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
