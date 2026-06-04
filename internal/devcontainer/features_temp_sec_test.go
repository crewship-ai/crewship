package devcontainer

import (
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestSecFeaturesTempDirRandom verifies that the temporary extraction dir is
// created with a crypto-random suffix (os.MkdirTemp) in the parent of destDir,
// rather than the old predictable name destDir+".tmp-"+sha256(ref+PID)[:12]
// which was vulnerable to a TOCTOU/symlink race.
//
// The extraction temp dir is created inline in pull(), which requires network
// access to fetch an OCI image, so we cannot exercise pull() end-to-end here.
// Instead we assert the security property of the temp-dir-creation step that
// the fix relies on: createExtractTempDir produces a path that (a) lives under
// destDir's parent, (b) is not equal to the old deterministic name, and (c)
// differs between calls for the same ref+PID.
func TestSecFeaturesTempDirRandom(t *testing.T) {
	base := t.TempDir()
	destDir := filepath.Join(base, "feature-cache")
	const ref = "ghcr.io/example/feature:1"

	// Reconstruct the old, predictable name for comparison.
	oldName := destDir + ".tmp-" + fmt.Sprintf("%x", sha256.Sum256([]byte(ref+fmt.Sprint(os.Getpid()))))[:12]

	tmp1, err := createExtractTempDir(destDir)
	if err != nil {
		t.Fatalf("createExtractTempDir: %v", err)
	}
	defer os.RemoveAll(tmp1)

	tmp2, err := createExtractTempDir(destDir)
	if err != nil {
		t.Fatalf("createExtractTempDir (2nd): %v", err)
	}
	defer os.RemoveAll(tmp2)

	if tmp1 == oldName {
		t.Errorf("temp dir uses old predictable name: %s", tmp1)
	}
	if tmp1 == tmp2 {
		t.Errorf("two temp dirs are identical (not random): %s", tmp1)
	}
	if got := filepath.Dir(tmp1); got != filepath.Dir(destDir) {
		t.Errorf("temp dir parent = %s, want %s", got, filepath.Dir(destDir))
	}
	if !strings.HasPrefix(filepath.Base(tmp1), filepath.Base(destDir)) {
		t.Errorf("temp dir base %q does not carry destDir prefix for discoverability", filepath.Base(tmp1))
	}
	// Created dir must be owner-only (MkdirTemp uses 0700).
	info, err := os.Stat(tmp1)
	if err != nil {
		t.Fatalf("stat temp dir: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0o700 {
		t.Errorf("temp dir mode = %#o, want 0700", perm)
	}
}
