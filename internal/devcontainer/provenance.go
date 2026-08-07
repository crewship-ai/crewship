package devcontainer

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// Provisioning provenance — what a crew's image is actually made of.
//
// A devcontainer is only "versioned" if you can find out what it contains. A
// ref like `common-utils:2` builds *some* 2.x, the tag keeps moving upstream,
// and configHash is computed from the ref as written — so a moved tag is a
// silent cache hit: the crew keeps an older image and nothing says so.
//
// Recording the resolved digest turns that from invisible into answerable. It
// is also what a pinned manifest can later be generated from.

// featureDigestFile sits inside a feature's cache directory and holds the
// digest the ref resolved to, so the answer survives a cache hit (where no
// registry call happens at all).
const featureDigestFile = ".crewship-digest"

var digestRe = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)

// FeatureRecord is one entry in a build's provenance: what was asked for, what
// it resolved to, and which version of the feature that turned out to be.
type FeatureRecord struct {
	// Ref is the reference as the operator wrote it.
	Ref string `json:"ref"`
	// ID is the feature's own id from devcontainer-feature.json.
	ID string `json:"id"`
	// Version is the feature's declared version (not the tool it installs).
	Version string `json:"version,omitempty"`
	// Digest is the OCI digest the ref resolved to. Empty when the cache
	// predates provenance recording.
	Digest string `json:"digest,omitempty"`
	// Pinned reports whether Ref itself names a digest — the only form that
	// cannot drift under the operator.
	Pinned bool `json:"pinned"`
}

// writeFeatureDigest records the resolved digest next to the extracted feature.
func writeFeatureDigest(dir, digest string) error {
	if !digestRe.MatchString(digest) {
		return nil // nothing worth recording; never a provisioning failure
	}
	return os.WriteFile(filepath.Join(dir, featureDigestFile), []byte(digest), 0o600)
}

// readFeatureDigest returns the recorded digest, or "" when it is absent or
// unreadable. Best-effort by contract: a cache populated before this existed
// must degrade to "unknown", never fail a build.
func readFeatureDigest(dir string) string {
	raw, err := os.ReadFile(filepath.Join(dir, featureDigestFile)) // #nosec G304 — cache path
	if err != nil {
		return ""
	}
	d := strings.TrimSpace(string(raw))
	if !digestRe.MatchString(d) {
		return ""
	}
	return d
}

// featureRecords renders the resolved features as records for storage and
// display.
func featureRecords(features []*ResolvedFeature) []FeatureRecord {
	out := make([]FeatureRecord, 0, len(features))
	for _, f := range features {
		if f == nil {
			continue
		}
		out = append(out, FeatureRecord{
			Ref:     f.Ref,
			ID:      f.Metadata.ID,
			Version: f.Metadata.Version,
			Digest:  f.Digest,
			Pinned:  strings.Contains(f.Ref, "@sha256:"),
		})
	}
	return out
}
