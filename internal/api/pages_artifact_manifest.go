package api

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"unicode/utf8"
)

const (
	artifactStaticProfile = "static-html-v1"
	artifactFormatVersion = 1
	artifactHTMLMIME      = "text/html; charset=utf-8"
)

// Omission keeps the original single-file publication contract. Explicit null,
// empty, or unsupported values are rejected rather than guessed from HTML.
// Embed this in write requests and validate before touching blob storage.
type artifactFormatRequest struct {
	RuntimeProfile json.RawMessage `json:"runtime_profile,omitempty"`
	FormatVersion  json.RawMessage `json:"format_version,omitempty"`
}

func normalizeArtifactFormat(req artifactFormatRequest) error {
	if len(req.RuntimeProfile) > 0 {
		var profile string
		if err := json.Unmarshal(req.RuntimeProfile, &profile); err != nil || profile != artifactStaticProfile {
			return fmt.Errorf("runtime_profile must be %q; interactive profiles are unavailable", artifactStaticProfile)
		}
	}
	if len(req.FormatVersion) > 0 {
		var version int
		if err := json.Unmarshal(req.FormatVersion, &version); err != nil || version != artifactFormatVersion {
			return errors.New("format_version must be 1")
		}
	}
	return nil
}

type artifactManifestFile struct {
	Path      string `json:"path"`
	MIME      string `json:"mime"`
	SizeBytes int64  `json:"size_bytes"`
	SHA256    string `json:"sha256"`
}

type artifactManifest struct {
	FormatVersion  int                    `json:"format_version"`
	RuntimeProfile string                 `json:"runtime_profile"`
	Entrypoint     string                 `json:"entrypoint"`
	Files          []artifactManifestFile `json:"files"`
	// Published HTML is the build, not a promise of original React/TS sources.
	// No source-bundle upload/read contract is enabled by this single-file slice.
	Source json.RawMessage `json:"source"`
}

func newArtifactManifest(data []byte) (artifactManifest, error) {
	if len(data) == 0 || !utf8.Valid(data) {
		return artifactManifest{}, errors.New("HTML must be nonempty UTF-8")
	}
	if len(data) > maxArtifactHTMLBytes {
		return artifactManifest{}, errors.New("HTML exceeds 10 MiB")
	}
	return artifactManifest{FormatVersion: artifactFormatVersion, RuntimeProfile: artifactStaticProfile, Entrypoint: "index.html", Files: []artifactManifestFile{{Path: "index.html", MIME: artifactHTMLMIME, SizeBytes: int64(len(data)), SHA256: attachmentDigest(data)}}, Source: json.RawMessage("null")}, nil
}

func artifactManifestJSON(manifest artifactManifest) (string, error) {
	data, err := json.Marshal(manifest)
	return string(data), err
}

// Legacy revisions have no durable size metadata. Derive an honest read-only
// manifest from verified bytes; never invent a size or mutate historical rows.
// The existing revision SHA remains the blob/GC authority in both cases.
func resolveArtifactManifest(recorded sql.NullString, data []byte, sha string) (artifactManifest, string, error) {
	expected, err := newArtifactManifest(data)
	if err != nil {
		return artifactManifest{}, "", err
	}
	if expected.Files[0].SHA256 != sha {
		return artifactManifest{}, "", errors.New("artifact content digest mismatch")
	}
	if !recorded.Valid {
		return expected, "legacy_derived", nil
	}
	var manifest artifactManifest
	decoder := json.NewDecoder(bytes.NewBufferString(recorded.String))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&manifest); err != nil {
		return artifactManifest{}, "", fmt.Errorf("invalid artifact manifest: %w", err)
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		return artifactManifest{}, "", errors.New("artifact manifest must contain one JSON object")
	}
	// Comparing canonical values checks the one supported path, profile, MIME,
	// byte length and digest, and refuses unsupported sources or extra files.
	actualJSON, err := artifactManifestJSON(manifest)
	if err != nil {
		return artifactManifest{}, "", err
	}
	expectedJSON, err := artifactManifestJSON(expected)
	if err != nil {
		return artifactManifest{}, "", err
	}
	if actualJSON != expectedJSON {
		return artifactManifest{}, "", errors.New("artifact manifest does not match the supported single-file content")
	}
	// Only server-generated canonical manifests are stored. This also rejects
	// duplicate keys and missing fields that JSON decoding otherwise normalizes.
	if actualJSON != recorded.String {
		return artifactManifest{}, "", errors.New("artifact manifest is not canonical")
	}
	return manifest, "recorded", nil
}
