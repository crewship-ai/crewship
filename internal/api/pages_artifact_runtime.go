package api

import (
	"net/http"

	"github.com/crewship-ai/crewship/internal/pagebuild"
)

// Deliberately not configurable. Changing this is an activation decision, not
// something page_runtime_origin or its development exception may opt into.
const artifactInteractiveRuntimeEnabled = false

type artifactExecutionDescriptor struct {
	Enabled bool   `json:"enabled"`
	Reason  string `json:"reason"`
}

func artifactExecution() artifactExecutionDescriptor {
	return artifactExecutionDescriptor{Enabled: artifactInteractiveRuntimeEnabled, Reason: "feature_disabled"}
}

// ArtifactRuntimePreparation is the product routing seam for the canonical
// constant program. With the closed gate, no Host/configuration can serve it.
// It never authorizes a revision or accepts an interactive publication profile.
func (h *PageHandler) ArtifactRuntimePreparation(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	if !artifactInteractiveRuntimeEnabled {
		replyError(w, 404, "artifact runtime unavailable")
		return
	}
	pagebuild.ServeArtifactRuntimePreparation(w, r, h.pageRuntimeOrigin, h.pageStudioOrigin)
}
