package api

import (
	"context"
	"database/sql"

	"github.com/crewship-ai/crewship/internal/artifacts"
)

// Metadata is derived from the same immutable code selection as the viewer:
// current owner's latest, everyone else's shared pin. It never examines a
// reader's private latest version or guesses a kind from the title/address.
func (h *PageHandler) artifactPageMetadata(ctx context.Context, workspace, page string) (string, int) {
	user := UserFromContext(ctx)
	if user == nil {
		return "", 0
	}
	var contentType string
	var shared int
	var declaration []byte
	err := h.pageAuthReader(ctx).QueryRowContext(ctx, `SELECT h.content_type,coalesce(h.shared_version,0),coalesce(b.content,'')
 FROM pages p JOIN artifact_version_heads h ON h.workspace_id=p.workspace_id AND h.page_id=p.id
 LEFT JOIN artifact_versions v ON v.workspace_id=h.workspace_id AND v.page_id=h.page_id AND v.version=CASE WHEN p.owner_user_id=? THEN h.latest_version ELSE h.shared_version END
 LEFT JOIN artifact_version_blobs b ON b.workspace_id=v.workspace_id AND b.page_id=v.page_id AND b.hash=json_extract(v.manifest_json,'$.files."dashboard.json".hash') AND length(b.content)<=16384
 WHERE p.workspace_id=? AND p.id=?`, user.ID, workspace, page).Scan(&contentType, &shared, &declaration)
	if err != nil {
		if err != sql.ErrNoRows {
			h.logger.Error("artifact metadata unavailable", "error", err)
		}
		return "", 0
	}
	return artifactMetadataKind(contentType, declaration, h.artifactDataEnabled(ctx)), shared
}

func artifactMetadataKind(contentType string, declaration []byte, dashboardEnabled bool) string {
	if contentType == "artifact_project" && dashboardEnabled {
		if _, err := artifacts.ParseDashboardCollections(declaration); err == nil {
			return "dashboard"
		}
	}
	return artifactListKind(contentType)
}
