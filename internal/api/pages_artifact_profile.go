package api

import (
	"encoding/json"

	"github.com/crewship-ai/crewship/internal/artifacts"
)

// Artifact profiles: what the selected version of an artifact project IS
// beyond its storage kind, so the catalog, its facets and the Page detail
// name it the same way before anyone opens it (#5152).
const (
	artifactProfileInteractive  = "interactive"
	artifactProfileDesignSystem = "design_system"
)

// maxProfileManifestBytes bounds the manifest the catalog reads per row; the
// manifest names files and hashes, never their content.
const maxProfileManifestBytes = 128 << 10

// artifactSelectedProfile reads only the file names of the selected
// manifest. A deck decides itself (Slides); a design system wins over a
// runtime marker, as in the viewer; an interactive.json marker makes the
// project an App whatever data collections it declares. The marker is the
// author's declaration; whether its runtime may start is decided elsewhere
// (ParseInteractiveArtifact and the feature switches).
func artifactSelectedProfile(contentType string, manifest []byte) string {
	if contentType != "artifact_project" || len(manifest) == 0 || len(manifest) > maxProfileManifestBytes {
		return ""
	}
	var selected struct {
		Files map[string]json.RawMessage `json:"files"`
	}
	if json.Unmarshal(manifest, &selected) != nil {
		return ""
	}
	if _, deck := selected.Files["deck.json"]; deck {
		return ""
	}
	if _, ok := selected.Files["design-system.json"]; ok {
		return artifactProfileDesignSystem
	}
	if _, ok := selected.Files[artifacts.InteractiveMarkerPath]; ok {
		return artifactProfileInteractive
	}
	return ""
}
