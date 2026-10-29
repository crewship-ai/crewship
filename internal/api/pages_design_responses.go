package api

import (
	"encoding/json"

	"github.com/crewship-ai/crewship/internal/artifacts"
)

// These are the actual JSON bodies serialized by Design handlers. The contract
// gate derives required fields from their tags; Yjs node extensions remain
// opaque here and are validated by the standard engine before serialization.
type designCreateResponse struct {
	PageID  string `json:"page_id"`
	Slug    string `json:"slug"`
	Version int64  `json:"version"`
}
type designActorResponse struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}
type designOriginResponse struct {
	ChatID  string `json:"chat_id"`
	AgentID string `json:"agent_id"`
}
type designSnapshotResponse struct {
	AuthorityRevision string                `json:"authority_revision"`
	PageID            string                `json:"page_id"`
	Origin            *designOriginResponse `json:"origin"`
	CanEdit           bool                  `json:"can_edit"`
	CanComment        bool                  `json:"can_comment"`
	Nodes             []json.RawMessage     `json:"nodes"`
	Order             []string              `json:"order"`
	Version           int64                 `json:"version"`
	LatestVersion     int64                 `json:"latest_version"`
	SharedVersion     int64                 `json:"shared_version"`
	Actor             designActorResponse   `json:"actor"`
}
type designOperationsResponse struct {
	Nodes   []json.RawMessage `json:"nodes"`
	Order   []string          `json:"order"`
	Version int64             `json:"version"`
}
type designVersionListResponse struct {
	Versions      []artifacts.VersionInfo `json:"versions"`
	LatestVersion int64                   `json:"latest_version"`
	SharedVersion int64                   `json:"shared_version"`
}
type designCommentsResponse struct {
	Comments []artifactProjectComment `json:"comments"`
}
type designShareResponse struct {
	Version       int64 `json:"version"`
	SharedVersion int64 `json:"shared_version"`
}
type designAgentResponse struct {
	Origin designOriginResponse `json:"origin"`
}
