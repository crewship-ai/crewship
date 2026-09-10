// Package pagebuild compiles untrusted Page sources outside the server process.
package pagebuild

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"unicode/utf8"

	"github.com/crewship-ai/crewship/internal/pages"
)

const ArtifactFormat = "crewship-page-preview/v1"
const MaxArtifactBytes = 2 << 20
const MaxArtifactDocumentBytes = 8 << 20

// Builder is a separate infrastructure capability, never a crew runtime.
type Builder interface {
	Build(context.Context, *pages.SourceProject) (*Artifact, error)
}

// Artifact contains one self-contained JS bundle and CSS. No HTML, remote
// chunks, source maps, secrets or authority are accepted from a worker.
type Artifact struct {
	Format        string `json:"format"`
	JavaScript    string `json:"javascript"`
	CSS           string `json:"css"`
	Toolchain     string `json:"toolchain"`
	ProfileSHA256 string `json:"profile_sha256,omitempty"`
}

func (a *Artifact) Encode() ([]byte, string, error) {
	if a == nil || a.Format != ArtifactFormat || a.JavaScript == "" {
		return nil, "", errors.New("invalid Page preview artifact")
	}
	if !utf8.ValidString(a.JavaScript) || !utf8.ValidString(a.CSS) || len(a.JavaScript)+len(a.CSS) > MaxArtifactBytes {
		return nil, "", errors.New("Page artifact exceeds UTF-8 or size limits")
	}
	if a.ProfileSHA256 != "" && !digestPattern.MatchString(a.ProfileSHA256) {
		return nil, "", errors.New("invalid Page build profile fingerprint")
	}
	b, err := json.Marshal(a)
	if err != nil {
		return nil, "", err
	}
	if len(b) > MaxArtifactDocumentBytes {
		return nil, "", errors.New("encoded Page artifact exceeds limit")
	}
	return b, fmt.Sprintf("%x", sha256.Sum256(b)), nil
}
