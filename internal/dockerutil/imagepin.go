package dockerutil

import (
	"strings"

	"github.com/google/go-containerregistry/pkg/name"
)

// PinnedRef rewrites a (possibly tagged) image reference into an immutable
// digest reference — "<registry>/<repo>@sha256:<hex>" — so the pull that
// follows can only ever land the manifest we resolved, never whatever the tag
// points at by the time the daemon gets around to fetching it.
//
// Why this exists at all: DigestResolver.Remote already HEADs the registry to
// answer "is my local :latest stale?", so the codebase has always KNOWN the
// digest at the moment it decides to pull — it just threw the answer away and
// pulled the tag again. That TOCTOU window is small but it is the entire
// supply-chain attack: a registry compromise (or a legitimate-but-unnoticed
// `docker push :latest`) between the HEAD and the pull swaps the binary the
// agent executes, and nothing downstream can tell. Pinning closes the window
// AND gives the audit trail something to record (#1825).
//
// ok=false means "could not pin, pull the original ref" — NOT an error. An
// unparseable reference or a malformed digest must not break a pull that would
// otherwise have worked; the caller degrades to a tag pull and records that
// the run is unpinned, which is strictly more honest than today's silence.
//
// A reference that already carries a digest is returned unchanged and its own
// digest WINS over the resolved one. An operator who wrote
// `image: repo@sha256:…` in a crew manifest has made a pinning statement, and
// a HEAD result — which resolves the tag, not their digest — must never be
// allowed to override it.
func PinnedRef(ref, digest string) (string, bool) {
	parsed, err := name.ParseReference(ref)
	if err != nil {
		return ref, false
	}
	// Already pinned: honour the caller's digest, ignore ours.
	if d, ok := parsed.(name.Digest); ok {
		return d.Name(), true
	}
	if digest == "" {
		return ref, false
	}
	pinned, err := name.NewDigest(parsed.Context().Name() + "@" + digest)
	if err != nil {
		// Digest failed the sha256:<64-hex> shape check. Something upstream
		// handed us a value that is not a manifest digest; pulling the tag is
		// the safe degradation, not pulling a reference Docker will reject.
		return ref, false
	}
	return pinned.Name(), true
}

// IsDigestRef reports whether ref is already digest-addressed
// ("repo@sha256:…", with or without a tag alongside it).
//
// Callers use it to decide whether a digest-pinned pull left a local TAG that
// needs restoring. The tempting shortcut — comparing PinnedRef's output against
// the input string — is wrong for every reference the registry normalizes:
// "alpine@sha256:…" pins to "index.docker.io/library/alpine@sha256:…", which
// differs as a string while being the same already-pinned reference. Acting on
// that difference means asking Docker to tag a digest reference, which it
// refuses outright, on every start for the users who pinned properly.
//
// An unparseable ref is not digest-addressed: it cannot be pinned either, so
// the caller's pull is tag-shaped and its tag handling should apply.
func IsDigestRef(ref string) bool {
	parsed, err := name.ParseReference(ref)
	if err != nil {
		return false
	}
	_, ok := parsed.(name.Digest)
	return ok
}

// LocalRepoDigest returns the manifest digest that the locally-present image
// for ref actually carries, read out of the daemon's RepoDigests list. It is
// RepoDigestsContain run in the other direction: instead of "does the local
// copy match the digest I expected?", it answers "which digest IS the local
// copy?" — the question the audit record needs.
//
// The repository is matched, not just the "@" suffix taken, because an image
// can be tagged into several repositories and RepoDigests then lists one entry
// per repository. Returning the first entry would happily record a digest that
// belongs to a different repo than the one this run pulled from. Returns ""
// when the repo has no entry, when the entry is malformed, or when the image
// was built locally and therefore has no registry digest at all.
func LocalRepoDigest(repoDigests []string, ref string) string {
	parsed, err := name.ParseReference(ref)
	if err != nil {
		return ""
	}
	repo := parsed.Context().Name()
	for _, rd := range repoDigests {
		if !strings.Contains(rd, "@") {
			continue
		}
		d, err := name.NewDigest(rd)
		if err != nil {
			continue
		}
		if d.Context().Name() == repo {
			return d.DigestStr()
		}
	}
	return ""
}
