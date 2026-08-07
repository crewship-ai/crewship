package devcontainer

import (
	"os"
	"path/filepath"
	"testing"
)

// A crew's devcontainer is only "versioned" if you can find out what it
// actually contains. A ref like `common-utils:2` builds *some* 2.x and the tag
// keeps moving, so without recording the digest the image is unauditable — and
// because configHash is computed from the ref as written, a moved tag is a
// silent cache hit that nobody can detect.
func TestFeatureDigest_RoundTripsThroughTheCache(t *testing.T) {
	dir := t.TempDir()
	const want = "sha256:1111111111111111111111111111111111111111111111111111111111111111"

	if err := writeFeatureDigest(dir, want); err != nil {
		t.Fatalf("writeFeatureDigest: %v", err)
	}
	if got := readFeatureDigest(dir); got != want {
		t.Errorf("digest = %q, want %q", got, want)
	}
}

// A cache directory populated before this existed has no digest file. That must
// degrade to "unknown", never to an error that fails provisioning.
func TestFeatureDigest_MissingIsEmptyNotAnError(t *testing.T) {
	if got := readFeatureDigest(t.TempDir()); got != "" {
		t.Errorf("a cache without a digest file must read empty, got %q", got)
	}
}

func TestFeatureDigest_IgnoresGarbage(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, featureDigestFile), []byte("not-a-digest\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := readFeatureDigest(dir); got != "" {
		t.Errorf("a malformed digest must be treated as unknown, got %q", got)
	}
}

// The record is what reaches the crew row, the API and the Builder popover, so
// it has to carry the three things a reader needs: what was asked for, what
// came back, and which version of the feature it is.
func TestFeatureProvenance_DescribesWhatWasBuilt(t *testing.T) {
	f := &ResolvedFeature{
		Ref:    "ghcr.io/devcontainers/features/github-cli:1",
		Digest: "sha256:2222222222222222222222222222222222222222222222222222222222222222",
	}
	f.Metadata.ID = "github-cli"
	f.Metadata.Version = "1.0.14"

	got := featureRecords([]*ResolvedFeature{f, nil})
	if len(got) != 1 {
		t.Fatalf("nil features must be skipped, got %d records", len(got))
	}
	if got[0].Ref != f.Ref || got[0].ID != "github-cli" || got[0].Version != "1.0.14" || got[0].Digest != f.Digest {
		t.Errorf("record does not describe the build: %+v", got[0])
	}
	// Pinned means the ref itself names a digest — the only form that cannot
	// drift under you.
	if got[0].Pinned {
		t.Error("a tag ref must not be reported as pinned")
	}
}

func TestFeatureProvenance_MarksDigestRefsPinned(t *testing.T) {
	f := &ResolvedFeature{Ref: "ghcr.io/devcontainers/features/github-cli@sha256:33333333333333333333333333333333333333333333333333333333333333333"}
	f.Metadata.ID = "github-cli"

	got := featureRecords([]*ResolvedFeature{f})
	if len(got) != 1 || !got[0].Pinned {
		t.Errorf("a digest ref must be reported as pinned: %+v", got)
	}
}
