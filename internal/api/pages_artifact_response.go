package api

import "github.com/crewship-ai/crewship/internal/artifacts"

// These are the actual HTTP response envelopes. Their JSON tags also back
// the OpenAPI required-field contracts.
type artifactProjectAvailabilityResponse struct {
	Enabled bool `json:"enabled"`
}

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

type artifactProjectShareResponse struct {
	SharedVersion int64  `json:"shared_version"`
	URL           string `json:"url"`
}

type artifactProjectConsentResponse struct {
	RunID      string `json:"run_id"`
	ExpiresAt  string `json:"expires_at"`
	Authorized bool   `json:"authorized"`
	PageID     string `json:"page_id"`
}
