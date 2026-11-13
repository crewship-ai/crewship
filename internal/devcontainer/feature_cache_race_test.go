//go:build unix

package devcontainer

import (
	"bytes"
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
)

// The staged-copy hash must equal the hash the cache entry itself produced
// before this change, including for modes the process umask would strip and
// for the .crewship-digest provenance file, so existing crewship-feat tags
// stay valid and no running crew is forced to rebuild.
func TestStageBuildContext_StagedHashMatchesCacheHash(t *testing.T) {
	old := setUmask(0o022)
	defer setUmask(old)

	d := t.TempDir()
	mustWrite(t, filepath.Join(d, "install.sh"), "#!/bin/sh\necho hi", 0o775)
	mustWrite(t, filepath.Join(d, "devcontainer-feature.json"), `{"id":"apt-packages"}`, 0o664)
	mustWrite(t, filepath.Join(d, featureDigestFile), "sha256:"+strings.Repeat("a", 64), 0o600)
	features := []*ResolvedFeature{{Ref: "ghcr.io/x/apt-packages:1", Dir: d, Metadata: FeatureMetadata{ID: "apt-packages"}}}

	ctxDir, dockerfile, tag, err := stageBuildContext("ubuntu:22.04", features, nil, nil)
	if err != nil {
		t.Fatalf("stageBuildContext: %v", err)
	}
	defer os.RemoveAll(ctxDir)
	want, err := hashBuildInputs(dockerfile, features)
	if err != nil {
		t.Fatal(err)
	}
	if tag != FeatureImageTagPrefix+want[:12] {
		t.Fatalf("tag %q differs from the cache-entry hash %q: existing images would be rebuilt", tag, FeatureImageTagPrefix+want[:12])
	}
	info, err := os.Stat(filepath.Join(ctxDir, featureContextDir, "apt-packages", "install.sh"))
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o775 {
		t.Fatalf("staged mode = %o, want 0775 (umask must not leak into the snapshot)", info.Mode().Perm())
	}
}

// Once staged, the build no longer depends on the shared cache directory:
// a concurrent refill that replaces the entry (the old RemoveAll+Rename) can
// not break hashing of a build that already copied it.
func TestStageBuildContext_HashDoesNotReadCacheAfterCopy(t *testing.T) {
	d := t.TempDir()
	mustWrite(t, filepath.Join(d, "install.sh"), "#!/bin/sh\necho hi", 0o755)
	mustWrite(t, filepath.Join(d, featureDigestFile), "sha256:"+strings.Repeat("b", 64), 0o600)
	features := []*ResolvedFeature{{Ref: "ghcr.io/x/f:1", Dir: d, Metadata: FeatureMetadata{ID: "f"}}}
	ctxDir, _, _, err := stageBuildContext("ubuntu:22.04", features, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(ctxDir)
	staged := []stagedFeature{{id: "f", dir: filepath.Join(ctxDir, featureContextDir, "f")}}
	if err := os.RemoveAll(d); err != nil {
		t.Fatal(err)
	}
	if _, err := hashStagedInputs("FROM x", staged); err != nil {
		t.Fatalf("hashing the staged snapshot failed after the cache entry vanished: %v", err)
	}
}

// A provision that waits on another's pull of the same feature must re-check
// the cache under the lock and use the winner's entry instead of pulling and
// replacing it.
func TestDownload_WaiterUsesEntryFilledByLockHolder(t *testing.T) {
	cache := t.TempDir()
	var logs bytes.Buffer
	var logMu sync.Mutex
	logger := slog.New(slog.NewTextHandler(&lockedWriter{w: &logs, mu: &logMu}, &slog.HandlerOptions{Level: slog.LevelDebug}))
	dl := NewFeatureDownloader(cache, logger)
	const ref = "registry.invalid/features/apt-packages:1"
	dir := dl.cachePathFor(ref)

	unlock := lockFeatureEntry(dir) // the "other" provision is mid-pull
	done := make(chan error, 1)
	go func() {
		_, err := dl.Download(context.Background(), ref, nil)
		done <- err
	}()
	select {
	case err := <-done:
		t.Fatalf("Download returned (%v) while the entry lock was held", err)
	case <-time.After(150 * time.Millisecond):
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	mustWrite(t, filepath.Join(dir, "install.sh"), "#!/bin/sh", 0o755)
	mustWrite(t, filepath.Join(dir, "devcontainer-feature.json"), `{"id":"apt-packages","version":"1.0.0"}`, 0o644)
	unlock()

	if err := <-done; err != nil {
		t.Fatalf("waiter failed: %v", err)
	}
	logMu.Lock()
	out := logs.String()
	logMu.Unlock()
	if !strings.Contains(out, "feature cache hit after wait") || strings.Contains(out, "pulling feature") {
		t.Fatalf("waiter did not take the post-wait cache hit:\n%s", out)
	}
}

type lockedWriter struct {
	w  *bytes.Buffer
	mu *sync.Mutex
}

func (l *lockedWriter) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.w.Write(p)
}

func mustWrite(t *testing.T, path, body string, mode os.FileMode) {
	t.Helper()
	if err := os.WriteFile(path, []byte(body), mode); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, mode); err != nil {
		t.Fatal(err)
	}
}

func setUmask(m int) int { return syscall.Umask(m) }
