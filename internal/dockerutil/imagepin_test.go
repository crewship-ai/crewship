package dockerutil

import "testing"

const (
	pinDigestA = "sha256:1111111111111111111111111111111111111111111111111111111111111111"
	pinDigestB = "sha256:2222222222222222222222222222222222222222222222222222222222222222"
)

// TestPinnedRef covers the tag→digest rewrite that turns a mutable pull into
// an immutable one. The interesting cases are the ones where pinning must be
// REFUSED — an unparseable ref or a malformed digest has to fall back to the
// original ref rather than hand Docker a reference it will reject, because
// the caller uses ok=false as "pull by tag, and record that you could not
// pin" rather than as a hard failure.
func TestPinnedRef(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		ref     string
		digest  string
		want    string
		wantOK  bool
		comment string
	}{
		{
			name:   "tagged ref pins to digest",
			ref:    "ghcr.io/crewship-ai/agent-runtime:latest",
			digest: pinDigestA,
			want:   "ghcr.io/crewship-ai/agent-runtime@" + pinDigestA,
			wantOK: true,
		},
		{
			name:   "docker hub short ref normalizes then pins",
			ref:    "alpine:3.20",
			digest: pinDigestA,
			want:   "index.docker.io/library/alpine@" + pinDigestA,
			wantOK: true,
		},
		{
			name:   "untagged ref pins to digest",
			ref:    "ghcr.io/crewship-ai/agent-runtime",
			digest: pinDigestA,
			want:   "ghcr.io/crewship-ai/agent-runtime@" + pinDigestA,
			wantOK: true,
		},
		{
			name:   "already-pinned ref with the same digest is returned unchanged",
			ref:    "ghcr.io/crewship-ai/agent-runtime@" + pinDigestA,
			digest: pinDigestA,
			want:   "ghcr.io/crewship-ai/agent-runtime@" + pinDigestA,
			wantOK: true,
		},
		{
			name:   "already-pinned ref wins over a conflicting resolved digest",
			ref:    "ghcr.io/crewship-ai/agent-runtime@" + pinDigestA,
			digest: pinDigestB,
			want:   "ghcr.io/crewship-ai/agent-runtime@" + pinDigestA,
			wantOK: true,
		},
		{
			name:   "empty digest cannot pin",
			ref:    "ghcr.io/crewship-ai/agent-runtime:latest",
			digest: "",
			want:   "ghcr.io/crewship-ai/agent-runtime:latest",
			wantOK: false,
		},
		{
			name:   "malformed digest cannot pin",
			ref:    "ghcr.io/crewship-ai/agent-runtime:latest",
			digest: "sha256:nothex",
			want:   "ghcr.io/crewship-ai/agent-runtime:latest",
			wantOK: false,
		},
		{
			name:   "unparseable ref cannot pin",
			ref:    "NOT A REF",
			digest: pinDigestA,
			want:   "NOT A REF",
			wantOK: false,
		},
		{
			name:   "local-only cache tag has no registry to pin against",
			ref:    "crewship-cache:0d08da4b8ac3",
			digest: pinDigestA,
			// crewship-cache normalizes to docker.io/library/crewship-cache,
			// which is a real (if nonexistent) registry ref — pinning it is
			// harmless because the caller short-circuits these before ever
			// reaching PinnedRef.
			want:   "index.docker.io/library/crewship-cache@" + pinDigestA,
			wantOK: true,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, ok := PinnedRef(tt.ref, tt.digest)
			if ok != tt.wantOK {
				t.Fatalf("PinnedRef(%q, %q) ok = %v, want %v", tt.ref, tt.digest, ok, tt.wantOK)
			}
			if got != tt.want {
				t.Errorf("PinnedRef(%q, %q) = %q, want %q", tt.ref, tt.digest, got, tt.want)
			}
		})
	}
}

// TestIsDigestRef guards a guard. Callers use this to decide whether there is
// a local tag worth restoring after a digest-addressed pull, and the naive
// version of that check — comparing the pinned ref against the original
// string — is WRONG for any ref the registry normalizes. "alpine@sha256:…"
// pins to "index.docker.io/library/alpine@sha256:…", which differs as a
// string while being the very same already-pinned reference. Getting this
// backwards means asking Docker to tag a digest reference, which it refuses,
// on every container start for the users who pinned their image properly.
func TestIsDigestRef(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		ref  string
		want bool
	}{
		{"explicit registry digest ref", "ghcr.io/crewship-ai/agent-runtime@" + pinDigestA, true},
		{"docker hub short digest ref (normalizes)", "alpine@" + pinDigestA, true},
		{"tagged ref", "ghcr.io/crewship-ai/agent-runtime:latest", false},
		{"bare ref defaults to a tag", "alpine", false},
		{"local cache tag", "crewship-cache:0d08da4b8ac3", false},
		{"unparseable ref is not a digest ref", "NOT A REF", false},
		{"tag and digest together is still digest-addressed", "alpine:3.20@" + pinDigestA, true},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := IsDigestRef(tt.ref); got != tt.want {
				t.Errorf("IsDigestRef(%q) = %v, want %v", tt.ref, got, tt.want)
			}
		})
	}
}

// TestLocalRepoDigest is the read-back half: after a pull the digest we
// RECORD has to come from the image that is actually on disk, not from the
// HEAD we did beforehand. It is the same parse as RepoDigestsContain, run in
// the other direction.
func TestLocalRepoDigest(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		repoDigests []string
		ref         string
		want        string
	}{
		{
			name:        "single repo digest for the same repo",
			repoDigests: []string{"ghcr.io/crewship-ai/agent-runtime@" + pinDigestA},
			ref:         "ghcr.io/crewship-ai/agent-runtime:latest",
			want:        pinDigestA,
		},
		{
			name: "picks the entry matching the ref's repo, not the first entry",
			repoDigests: []string{
				"docker.io/other/thing@" + pinDigestB,
				"ghcr.io/crewship-ai/agent-runtime@" + pinDigestA,
			},
			ref:  "ghcr.io/crewship-ai/agent-runtime:latest",
			want: pinDigestA,
		},
		{
			name:        "no repo digests (locally built image) yields nothing",
			repoDigests: []string{},
			ref:         "crewship-cache:deadbeef",
			want:        "",
		},
		{
			name:        "no entry for this repo yields nothing rather than a wrong digest",
			repoDigests: []string{"docker.io/other/thing@" + pinDigestB},
			ref:         "ghcr.io/crewship-ai/agent-runtime:latest",
			want:        "",
		},
		{
			name:        "malformed entry is skipped",
			repoDigests: []string{"garbage-without-an-at-sign"},
			ref:         "ghcr.io/crewship-ai/agent-runtime:latest",
			want:        "",
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := LocalRepoDigest(tt.repoDigests, tt.ref); got != tt.want {
				t.Errorf("LocalRepoDigest(%v, %q) = %q, want %q", tt.repoDigests, tt.ref, got, tt.want)
			}
		})
	}
}
