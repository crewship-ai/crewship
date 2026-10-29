package api

import (
	"context"
	"database/sql"
	"encoding/json"

	"github.com/crewship-ai/crewship/internal/artifacts"
)

func artifactSelectedMetadataKind(contentType string, dashboard []byte, dashboardEnabled bool, manifest, index []byte, slidesEnabled bool) string {
	if contentType == "artifact_project" && slidesEnabled {
		var marker struct {
			Files map[string]json.RawMessage `json:"files"`
		}
		if len(manifest) <= 128<<10 && json.Unmarshal(manifest, &marker) == nil {
			if _, ok := marker.Files["deck.json"]; ok {
				if artifacts.IsSlidesArtifactIndex(manifest, index) {
					return "slides"
				}
				return "project"
			}
		}
	}
	return artifactMetadataKind(contentType, dashboard, dashboardEnabled)
}

func (h *PageHandler) artifactSelectedPageMetadata(ctx context.Context, workspace, page string) (string, int) {
	user := UserFromContext(ctx)
	if user == nil {
		return "", 0
	}
	var contentType string
	var shared int
	var dashboard, index, manifest []byte
	slidesEnabled := h.artifactSlidesEnabled(ctx)
	err := h.pageAuthReader(ctx).QueryRowContext(ctx, `SELECT h.content_type,coalesce(h.shared_version,0),coalesce(b.content,''),coalesce(sb.content,''),CASE WHEN ?=1 AND length(CAST(v.manifest_json AS BLOB))<=131072 THEN CAST(v.manifest_json AS BLOB) ELSE '' END
 FROM pages p JOIN artifact_version_heads h ON h.workspace_id=p.workspace_id AND h.page_id=p.id
 LEFT JOIN artifact_versions v ON v.workspace_id=h.workspace_id AND v.page_id=h.page_id AND v.version=CASE WHEN p.owner_user_id=? THEN h.latest_version ELSE h.shared_version END
 LEFT JOIN artifact_version_blobs b ON b.workspace_id=v.workspace_id AND b.page_id=v.page_id AND b.hash=json_extract(v.manifest_json,'$.files."dashboard.json".hash') AND length(b.content)<=16384
 LEFT JOIN artifact_version_blobs sb ON sb.workspace_id=v.workspace_id AND sb.page_id=v.page_id AND sb.hash=json_extract(v.manifest_json,'$.files."deck.json".hash') AND length(sb.content)<=16384 AND ?=1
 WHERE p.workspace_id=? AND p.id=?`, slidesEnabled, user.ID, slidesEnabled, workspace, page).Scan(&contentType, &shared, &dashboard, &index, &manifest)
	if err != nil {
		if err != sql.ErrNoRows {
			h.logger.Error("artifact metadata unavailable", "error", err)
		}
		return "", 0
	}
	return artifactSelectedMetadataKind(contentType, dashboard, h.artifactDataEnabled(ctx), manifest, index, slidesEnabled), shared
}
