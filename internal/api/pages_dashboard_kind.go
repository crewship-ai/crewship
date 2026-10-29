package api

import (
	"context"

	"github.com/crewship-ai/crewship/internal/artifacts"
)

// Metadata is derived from the same immutable code selection as the viewer:
// current owner's latest, everyone else's shared pin. It never examines a
// reader's private latest version or guesses a kind from the title/address.
func (h *PageHandler) artifactPageMetadata(ctx context.Context, workspace, page string) (string, int) {
	return h.artifactSelectedPageMetadata(ctx, workspace, page)
}

func artifactMetadataKind(contentType string, declaration []byte, dashboardEnabled bool) string {
	if contentType == "artifact_project" && dashboardEnabled {
		if _, err := artifacts.ParseDashboardCollections(declaration); err == nil {
			return "dashboard"
		}
	}
	return artifactListKind(contentType)
}
