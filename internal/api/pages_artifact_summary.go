package api

// pageArtifactSummary is metadata only. Publication does not assert that blob
// storage is currently available; the authorized detail read verifies that.
type pageArtifactSummary struct {
	HeadRev         int64  `json:"head_rev"`
	AuthorUserID    string `json:"author_user_id"`
	AuthorName      string `json:"author_name"`
	AuthorRunID     string `json:"author_run_id,omitempty"`
	AuthorAgentID   string `json:"author_agent_id,omitempty"`
	AuthorAgentName string `json:"author_agent_name,omitempty"`
	PublishedAt     string `json:"published_at"`
	LastActivityAt  string `json:"last_activity_at"`
}
