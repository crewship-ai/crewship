package api

import (
	"net/http/httptest"
	"strings"
	"testing"
)

func TestArtifactAccountSuspensionDuringHumanUploadIsFenced(t *testing.T) {
	h, _, _, workspace, user, page := artifactFixture(t)
	r := pagesGrantRequest(t, "POST", "/", workspace, user, "OWNER", "")
	r.SetPathValue("slug", "report")
	r.Body = &artifactRevokingBody{reader: strings.NewReader(`{"html":"<p>must not publish</p>","base_rev":0,"mutation_id":"suspended-upload"}`), revoke: func() { execOrFatal(t, h.db, `UPDATE users SET suspended_at='2026-10-13T00:00:00Z' WHERE id=?`, user) }}
	rr := httptest.NewRecorder()
	h.PublishArtifact(rr, r)
	artifactExpect(t, rr, 403)
	var count int
	if err := h.db.QueryRow(`SELECT COUNT(*) FROM page_artifact_revisions WHERE page_id=?`, page).Scan(&count); err != nil || count != 0 {
		t.Fatalf("suspended human published: %d %v", count, err)
	}
}
func TestArtifactAccountSuspensionDuringCommentIsFenced(t *testing.T) {
	h, _, workspace, user, _ := discussionFixture(t)
	r := pagesGrantRequest(t, "POST", "/", workspace, user, "OWNER", "")
	r.SetPathValue("slug", "report")
	r.Body = &artifactRevokingBody{reader: strings.NewReader(`{"revision":1,"anchor":{"schema_version":1,"type":"document"},"body":"must not persist","mutation_id":"suspended-comment"}`), revoke: func() { execOrFatal(t, h.db, `UPDATE users SET suspended_at='2026-10-13T00:00:00Z' WHERE id=?`, user) }}
	rr := httptest.NewRecorder()
	h.CreateArtifactThread(rr, r)
	artifactExpect(t, rr, 403)
	var count int
	if err := h.db.QueryRow(`SELECT COUNT(*) FROM page_artifact_threads`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("suspendedhuman commented: %d %v", count, err)
	}
}
func TestArtifactAccountSuspendedGrantIssuerCannotAuthorizeAgent(t *testing.T) {
	h, workspace, user, page, token := artifactRunFixture(t)
	pagesSeedUser(t, h, workspace, "grant-admin", "grant-admin@example.com", "ADMIN")
	execOrFatal(t, h.db, `UPDATE page_grants SET granted_by_user_id='grant-admin' WHERE page_id=? AND subject_type='agent'`, page)
	execOrFatal(t, h.db, `UPDATE users SET suspended_at='2026-10-13T00:00:00Z' WHERE id='grant-admin'`)
	artifactExpect(t, artifactAgentSave(t, h, workspace, token, `{"html":"<p>must not publish</p>","base_rev":0,"mutation_id":"inactive-issuer"}`), 403)
	execOrFatal(t, h.db, `UPDATE page_grants SET granted_by_user_id=? WHERE page_id=? AND subject_type='agent'`, user, page)
	artifactExpect(t, artifactAgentSave(t, h, workspace, token, `{"html":"<p>active issuer</p>","base_rev":0,"mutation_id":"active-issuer"}`), 201)
}
