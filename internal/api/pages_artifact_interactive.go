package api

import (
	"context"
	"net/http"
	"os"
	"strings"

	"github.com/crewship-ai/crewship/internal/artifacts"
	"github.com/crewship-ai/crewship/internal/featureflags"
	"github.com/crewship-ai/crewship/internal/pagebuild"
)

// artifactInteractiveRuntime tells the viewer where a Project's own scripts may
// run. It grants nothing: every data read and write still goes through the
// existing artifact data endpoints with the viewer's own session.
type artifactInteractiveRuntime struct {
	// RuntimeURL is the fixed Pages bootstrap on a separate site, or empty
	// when this server has no isolating runtime origin for the request host.
	RuntimeURL            string   `json:"runtime_url"`
	DevelopmentSameOrigin bool     `json:"development_same_origin"`
	Collections           []string `json:"collections"`
}

// artifactInteractiveEnabled requires the data collections it is built on,
// which in turn require artifact projects.
func (h *PageHandler) artifactInteractiveEnabled(ctx context.Context) bool {
	if !h.artifactDataEnabled(ctx) {
		return false
	}
	if os.Getenv("CREWSHIP_ARTIFACT_INTERACTIVE_ENABLED") == "true" {
		return true
	}
	enabled, err := featureflags.IsEnabledWith(ctx, h.pageAuthReader(ctx), WorkspaceIDFromContext(ctx), "artifact_interactive")
	return err == nil && enabled
}

// artifactInteractiveFor returns nil unless the flag is on and the selected
// version carries a valid interactive marker, so a flag-off snapshot keeps its
// exact previous shape.
func (h *PageHandler) artifactInteractiveFor(r *http.Request, files map[string]artifacts.VersionFile) *artifactInteractiveRuntime {
	if !h.artifactInteractiveEnabled(r.Context()) {
		return nil
	}
	collections, err := artifacts.ParseInteractiveArtifact(files)
	if err != nil {
		return nil
	}
	out := &artifactInteractiveRuntime{DevelopmentSameOrigin: h.pageRuntimeDevelopmentSameOrigin, Collections: collections}
	if origin := h.runtimeOriginFor(r); origin != "" {
		scheme := "http://"
		if strings.HasPrefix(origin, "https://") {
			scheme = "https://"
		}
		// The same request-host check as Page previews: a proxy alias must not
		// put author scripts back on the Studio site.
		if pagebuild.ValidateRuntimeOriginForDevelopment(origin, scheme+r.Host, h.pageRuntimeDevelopmentSameOrigin) == nil {
			out.RuntimeURL = strings.TrimSuffix(origin, "/") + pagebuild.RuntimePath
		}
	}
	return out
}
