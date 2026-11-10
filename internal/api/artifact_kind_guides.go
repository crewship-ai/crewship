package api

import (
	"net/http"

	"github.com/crewship-ai/crewship/internal/artifacts/kindguide"
)

// artifactKindGuideSummary is one row of the guide catalog. The content is
// served only by the per-kind route, so the list stays small.
type artifactKindGuideSummary struct {
	Kind         string   `json:"kind"`
	Title        string   `json:"title"`
	Version      string   `json:"version"`
	RulesVersion string   `json:"rules_version"`
	SHA256       string   `json:"sha256"`
	Bytes        int      `json:"bytes"`
	Modes        []string `json:"modes"`
}

// artifactKindGuideList is the catalog envelope.
type artifactKindGuideList struct {
	Guides []artifactKindGuideSummary `json:"guides"`
}

// ListArtifactKindGuides serves the static, embedded per-kind agent guide
// catalog. It reads no tenant rows; any signed-in caller may read it.
func ListArtifactKindGuides(w http.ResponseWriter, r *http.Request) {
	if UserFromContext(r.Context()) == nil {
		replyError(w, http.StatusUnauthorized, "Not authenticated")
		return
	}
	guides := kindguide.List()
	out := make([]artifactKindGuideSummary, 0, len(guides))
	for _, g := range guides {
		out = append(out, artifactKindGuideSummary{Kind: g.Kind, Title: g.Title, Version: g.Version, RulesVersion: g.RulesVersion, SHA256: g.SHA256, Bytes: g.Bytes, Modes: g.Modes})
	}
	writeJSON(w, http.StatusOK, artifactKindGuideList{Guides: out})
}

// GetArtifactKindGuide serves one kind's guide, resolving known aliases
// (design-system → library, project → app). Craft kinds and unknown words
// are 404: they have no artifact kind guide.
func GetArtifactKindGuide(w http.ResponseWriter, r *http.Request) {
	if UserFromContext(r.Context()) == nil {
		replyError(w, http.StatusUnauthorized, "Not authenticated")
		return
	}
	g, err := kindguide.Lookup(r.PathValue("kind"))
	if err != nil {
		replyError(w, http.StatusNotFound, "No agent guide for this artifact kind")
		return
	}
	writeJSON(w, http.StatusOK, g)
}
