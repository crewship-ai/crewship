package devcontainer

import (
	"context"
	"strings"
	"testing"
)

// TestPull_FailureLeavesCachedDigestIntact pins the invariant a failed pull
// must hold: it may not restamp the *existing* cache entry with the digest of
// the artifact it failed to install.
//
// The bug this covers wrote the digest with a deferred call registered right
// after the digest resolved, so every later failure path still ran it — and on
// those paths destDir is untouched, still holding the previously cached
// feature. The result is a cache entry whose content is version A and whose
// recorded provenance says version B, which is worse than no provenance: it
// answers "what is in this image" confidently and wrongly.
func TestPull_FailureLeavesCachedDigestIntact(t *testing.T) {
	t.Parallel()

	host := covStartRegistry(t)
	const repoTag = "features/restamped:1"

	goodRef, goodDigest := covPushFeature(t, host, repoTag, map[string]string{
		"install.sh":                "echo hi",
		"devcontainer-feature.json": `{"id":"restamped","version":"1"}`,
	})

	d := NewFeatureDownloader(t.TempDir(), testLogger())
	ctx := context.Background()
	dir := d.cachePathFor(goodRef)

	if err := d.pull(ctx, goodRef, dir); err != nil {
		t.Fatalf("first pull: %v", err)
	}
	if got := readFeatureDigest(dir); got != goodDigest {
		t.Fatalf("cached digest after a good pull = %q; want %q", got, goodDigest)
	}

	// Move the tag to an artifact that fails validation *after* its digest
	// has been resolved — the window the deferred write ran in.
	_, badDigest := covPushFeature(t, host, repoTag, map[string]string{
		"devcontainer-feature.json": `{"id":"restamped","version":"2"}`,
	})
	if badDigest == goodDigest {
		t.Fatal("fixture is inert: both artifacts have the same digest")
	}

	err := d.pull(ctx, goodRef, dir)
	if err == nil {
		t.Fatal("second pull succeeded; the fixture is missing install.sh and must fail")
	}
	if !strings.Contains(err.Error(), "missing install.sh") {
		t.Fatalf("second pull failed for the wrong reason: %v", err)
	}

	if got := readFeatureDigest(dir); got != goodDigest {
		t.Errorf("failed pull restamped the cache: digest = %q, want the still-installed %q", got, goodDigest)
	}
	if !d.IsCached(goodRef) {
		t.Error("failed pull destroyed the previously valid cache entry")
	}
}

// TestPull_DigestLandsWithTheContent is the positive half: the recorded digest
// must arrive as part of the atomic rename, not as a separate write that can
// be observed — or fail — independently of the content it describes.
func TestPull_DigestLandsWithTheContent(t *testing.T) {
	t.Parallel()

	host := covStartRegistry(t)
	ref, digest := covPushFeature(t, host, "features/atomic:1", map[string]string{
		"install.sh":                "echo hi",
		"devcontainer-feature.json": `{"id":"atomic","version":"1"}`,
	})

	d := NewFeatureDownloader(t.TempDir(), testLogger())
	dir := d.cachePathFor(ref)
	if err := d.pull(context.Background(), ref, dir); err != nil {
		t.Fatalf("pull: %v", err)
	}

	if got := readFeatureDigest(dir); got != digest {
		t.Errorf("recorded digest = %q; want %q", got, digest)
	}

	// Resolve() is what the provisioner reads, so the digest has to survive
	// the trip through it too.
	feat, err := d.Download(context.Background(), ref, nil)
	if err != nil {
		t.Fatalf("Download: %v", err)
	}
	if feat.Digest != digest {
		t.Errorf("Feature.Digest = %q; want %q", feat.Digest, digest)
	}
}
