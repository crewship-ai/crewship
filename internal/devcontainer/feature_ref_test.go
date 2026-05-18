package devcontainer

import (
	"strings"
	"testing"
)

// ---------------------------------------------------------------------------
// features.go — FeatureRef.Reference + ToFeatureRef round-trip.
//
// FeatureRef.Reference is the canonical OCI reference string the
// downloader hits. The tag/digest disambiguation rule (digest wins
// when set, tag wins otherwise) is the load-bearing contract — getting
// it wrong sends downloads to the wrong upstream artefact.
// ---------------------------------------------------------------------------

func TestFeatureRef_Reference_DigestWinsOverTag(t *testing.T) {
	// Source: "Uses digest form if Digest is set, otherwise tag form."
	// When BOTH are set, digest wins. Pin that ordering so a reorder
	// of the if/else branch doesn't silently flip to "tag wins" and
	// start hitting mutable upstream tags when the caller pinned a
	// digest for reproducibility.
	f := FeatureRef{
		Registry: "ghcr.io",
		Repo:     "devcontainers/features/python",
		Tag:      "1",
		Digest:   "sha256:abc123",
	}
	got := f.Reference()
	want := "ghcr.io/devcontainers/features/python@sha256:abc123"
	if got != want {
		t.Errorf("Reference() = %q, want %q (digest must win when both set)", got, want)
	}
}

func TestFeatureRef_Reference_TagFormWhenNoDigest(t *testing.T) {
	f := FeatureRef{
		Registry: "ghcr.io",
		Repo:     "devcontainers/features/node",
		Tag:      "1",
	}
	got := f.Reference()
	want := "ghcr.io/devcontainers/features/node:1"
	if got != want {
		t.Errorf("Reference() = %q, want %q", got, want)
	}
}

func TestFeatureRef_Reference_DigestForm(t *testing.T) {
	f := FeatureRef{
		Registry: "ghcr.io",
		Repo:     "devcontainers/features/git",
		Digest:   "sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef",
	}
	got := f.Reference()
	want := "ghcr.io/devcontainers/features/git@sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	if got != want {
		t.Errorf("Reference() = %q, want %q", got, want)
	}
	// Sanity: contains "@sha256:" — the OCI distinguisher between
	// digest and tag form. A regression that emitted ":digest" (with
	// the colon) would silently break upstream resolution.
	if !strings.Contains(got, "@sha256:") {
		t.Errorf("Reference() = %q, missing \"@sha256:\" digest separator", got)
	}
}

func TestFeatureRef_Reference_CustomRegistryAndDeepRepoPath(t *testing.T) {
	// Non-ghcr.io registries are valid; deep repo paths too. Pin both
	// reconstruct cleanly via Reference.
	f := FeatureRef{
		Registry: "registry.example.com:5000",
		Repo:     "org/group/sub/feature",
		Tag:      "v2.3.1",
	}
	got := f.Reference()
	want := "registry.example.com:5000/org/group/sub/feature:v2.3.1"
	if got != want {
		t.Errorf("Reference() = %q, want %q", got, want)
	}
}

func TestFeatureRef_Reference_EmptyFieldsProduceDeterministicString(t *testing.T) {
	// Zero-value FeatureRef is unusual but must produce a deterministic
	// string (no panic), since callers may format it for error messages
	// before the field-population step.
	f := FeatureRef{}
	got := f.Reference()
	// No tag + no digest → falls into the tag branch, producing "/:":
	// not useful but stable. Pin that no panic + stable empty-ish
	// output happens.
	if got == "" {
		t.Errorf("Reference() on zero value = %q, want some deterministic non-panic string", got)
	}
	if !strings.Contains(got, ":") {
		t.Errorf("Reference() on zero value = %q, expected the tag-form colon separator", got)
	}
}

// ---- ToFeatureRef ----

func TestToFeatureRef_TagForm(t *testing.T) {
	// ToFeatureRef wraps ParseFeatureRef; the Reference round-trip
	// must match the original input string when parsing succeeds.
	got, err := ToFeatureRef("ghcr.io/devcontainers/features/python:1")
	if err != nil {
		t.Fatalf("ToFeatureRef: %v", err)
	}
	if got.Registry != "ghcr.io" {
		t.Errorf("Registry = %q", got.Registry)
	}
	if got.Repo != "devcontainers/features/python" {
		t.Errorf("Repo = %q", got.Repo)
	}
	if got.Tag != "1" {
		t.Errorf("Tag = %q", got.Tag)
	}
	if got.Digest != "" {
		t.Errorf("Digest = %q on tag-form input; want empty", got.Digest)
	}
	// Reference should round-trip the input exactly.
	if rt := got.Reference(); rt != "ghcr.io/devcontainers/features/python:1" {
		t.Errorf("round-trip = %q, want input", rt)
	}
}

func TestToFeatureRef_DigestForm(t *testing.T) {
	input := "ghcr.io/devcontainers/features/python@sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	got, err := ToFeatureRef(input)
	if err != nil {
		t.Fatalf("ToFeatureRef: %v", err)
	}
	if got.Tag != "" {
		t.Errorf("Tag = %q on digest-form input; want empty", got.Tag)
	}
	if got.Digest == "" {
		t.Errorf("Digest empty on digest-form input")
	}
	if !strings.HasPrefix(got.Digest, "sha256:") {
		t.Errorf("Digest = %q, want \"sha256:...\" prefix", got.Digest)
	}
	// Round-trip pin.
	if rt := got.Reference(); rt != input {
		t.Errorf("round-trip = %q, want %q", rt, input)
	}
}

func TestToFeatureRef_MalformedInput_Errors(t *testing.T) {
	// Inputs that ParseFeatureRef can't disambiguate must surface as
	// errors with a zero-value FeatureRef. Callers depend on the error
	// to refuse the download (silent zero value would hit "ghcr.io//"
	// and fail at the remote layer with a less-actionable error).
	for _, in := range []string{
		"",
		"not-a-feature",
	} {
		t.Run(in, func(t *testing.T) {
			got, err := ToFeatureRef(in)
			if err == nil {
				t.Errorf("expected error on %q", in)
			}
			if got.Registry != "" || got.Repo != "" || got.Tag != "" || got.Digest != "" {
				t.Errorf("err path returned non-zero FeatureRef: %+v", got)
			}
		})
	}
}
