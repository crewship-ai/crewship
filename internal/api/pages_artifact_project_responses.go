package api

import "github.com/crewship-ai/crewship/internal/artifacts"

// artifactProjectAvailabilityResponse is the body serialized by the workspace
// availability endpoint, including its explicit disabled state.
type artifactProjectAvailabilityResponse struct {
	Enabled bool `json:"enabled"`
}

// artifactProjectSnapshotResponse is the version-selected owner or shared view.
type artifactProjectSnapshotResponse struct {
	PageID        string                   `json:"page_id"`
	Name          string                   `json:"name"`
	Slug          string                   `json:"slug"`
	Origin        any                      `json:"origin"`
	Enabled       bool                     `json:"enabled"`
	CanWrite      bool                     `json:"can_write"`
	LatestVersion int64                    `json:"latest_version"`
	SharedVersion int64                    `json:"shared_version"`
	Manifest      map[string]any           `json:"manifest"`
	Versions      []map[string]any         `json:"versions"`
	Quota         artifacts.VersionQuota   `json:"quota"`
	Comments      []artifactProjectComment `json:"comments"`
}

type artifactProjectMutationResponse struct {
	PageID  string `json:"page_id"`
	Slug    string `json:"slug"`
	Version int64  `json:"version"`
	URL     string `json:"url"`
	Actor   string `json:"actor"`
}
