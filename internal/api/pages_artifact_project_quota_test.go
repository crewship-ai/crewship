package api

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/artifacts"
)

// The owner snapshot reports a quota level and a report-only retention
// preview; a shared reader sees neither, and the preview deletes nothing.
func TestArtifactProjectSnapshotReportsQuotaLevelAndDryRunRetention(t *testing.T) {
	h, ws, owner, call := artifactProjectFixture(t)
	body := strings.TrimPrefix(artifactProjectBody(0, "Version one"), "{")
	if created := call(owner, "POST", "artifacts", `{"slug":"three-screens","name":"Three screens",`+body); created.Code != 201 {
		t.Fatalf("create %d %s", created.Code, created.Body)
	}
	if got := call(owner, "POST", "three-screens/artifact/versions", artifactProjectBody(1, "Version two")); got.Code != 200 {
		t.Fatalf("v2 %d %s", got.Code, got.Body)
	}
	if got := call(owner, "POST", "three-screens/artifact/versions", artifactProjectBody(2, "Version three")); got.Code != 200 {
		t.Fatalf("v3 %d %s", got.Code, got.Body)
	}
	var page string
	if err := h.db.QueryRow(`SELECT id FROM pages WHERE slug='three-screens'`).Scan(&page); err != nil {
		t.Fatal(err)
	}
	mustExec(t, h.db, `UPDATE artifact_versions SET created_at='2026-01-02T10:00:00.000000000Z' WHERE page_id=? AND version IN (1,2)`, page)

	got := call(owner, "GET", "three-screens/artifact", "")
	if got.Code != 200 {
		t.Fatalf("owner read %d %s", got.Code, got.Body)
	}
	var snap struct {
		Quota            artifacts.VersionQuota      `json:"quota"`
		RetentionPreview *artifacts.RetentionPreview `json:"retention_preview"`
		Versions         []map[string]any            `json:"versions"`
	}
	if err := json.Unmarshal(got.Body.Bytes(), &snap); err != nil {
		t.Fatal(err)
	}
	if snap.Quota.Level != artifacts.QuotaLevelNone || snap.Quota.Warning || snap.Quota.Retention != "keep-all" || snap.Quota.LimitBytes <= 0 {
		t.Fatalf("quota %+v", snap.Quota)
	}
	p := snap.RetentionPreview
	if p == nil || !p.DryRun || p.Total != 3 || len(p.WouldThin) != 1 || p.WouldThin[0] != 1 {
		t.Fatalf("preview %+v", p)
	}
	var count int
	if err := h.db.QueryRow(`SELECT count(*) FROM artifact_versions WHERE page_id=?`, page).Scan(&count); err != nil || count != 3 || len(snap.Versions) != 3 {
		t.Fatalf("history changed: rows %d listed %d %v", count, len(snap.Versions), err)
	}

	if got := call(owner, "POST", "three-screens/artifact/share", `{"base_version":3,"version":3}`); got.Code != 200 {
		t.Fatalf("share %d %s", got.Code, got.Body)
	}
	reader := "artifact-reader"
	mustExec(t, h.db, `INSERT INTO users(id,email,full_name) VALUES(?,'artifact-reader@example.com','Reader')`, reader)
	mustExec(t, h.db, `INSERT INTO workspace_members(id,workspace_id,user_id,role) VALUES(?,?,?,'MEMBER')`, generateCUID(), ws, reader)
	mustExec(t, h.db, `INSERT INTO page_grants(page_id,subject_type,subject_id,level,granted_by_user_id,granted_at) VALUES(?,'user',?,'read',?,datetime('now'))`, page, reader, owner)
	shared := call(reader, "GET", "three-screens/artifact", "")
	if shared.Code != 200 || strings.Contains(shared.Body.String(), "retention_preview") || strings.Contains(shared.Body.String(), `"level"`) {
		t.Fatalf("shared reader saw owner quota detail %d %s", shared.Code, shared.Body)
	}
}
