package api

import "github.com/crewship-ai/crewship/internal/artifacts"

// artifactProjectAvailabilityResponse is the body serialized by the workspace
// availability endpoint, including its explicit disabled state.
type artifactProjectAvailabilityResponse struct {
	Enabled bool `json:"enabled"`
	// CanCreate and Code are present only when the caller scoped the request
	// with agent_id. CanCreate is false with Code "artifact_crew_not_allowed"
	// when that agent's crew is outside the workspace creation policy.
	CanCreate *bool  `json:"can_create,omitempty"`
	Code      string `json:"code,omitempty"`
}

// artifactProjectSnapshotResponse is the version-selected owner or shared view.
type artifactProjectSnapshotResponse struct {
	ArtifactKind   string                   `json:"artifact_kind,omitempty"`
	PageID         string                   `json:"page_id"`
	Name           string                   `json:"name"`
	Slug           string                   `json:"slug"`
	Origin         any                      `json:"origin"`
	Enabled        bool                     `json:"enabled"`
	CanComment     bool                     `json:"can_comment"`
	CanShare       bool                     `json:"can_share"`
	CanRestore     bool                     `json:"can_restore"`
	CanSendToAgent bool                     `json:"can_send_to_agent"`
	CanWrite       bool                     `json:"can_write"`
	LatestVersion  int64                    `json:"latest_version"`
	SharedVersion  int64                    `json:"shared_version"`
	Manifest       map[string]any           `json:"manifest"`
	Versions       []map[string]any         `json:"versions"`
	Quota          artifacts.VersionQuota   `json:"quota"`
	Comments       []artifactProjectComment `json:"comments"`

	// RetentionPreview is owner-only and report-only: nothing it lists is deleted.
	RetentionPreview *artifacts.RetentionPreview `json:"retention_preview,omitempty"`
}

type artifactProjectMutationResponse struct {
	PageID  string `json:"page_id"`
	Slug    string `json:"slug"`
	Version int64  `json:"version"`
	URL     string `json:"url"`
	Actor   string `json:"actor"`
}
