package api

import (
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
)

func artifactCopyCall(t *testing.T, h *PageHandler, ws, user, role, body string) *httptest.ResponseRecorder {
	t.Helper()
	r := pagesGrantRequest(t, "POST", "/api/v1/pages/report/artifact/copies", ws, user, role, body)
	r.SetPathValue("slug", "report")
	rr := httptest.NewRecorder()
	h.CopyArtifact(rr, r)
	return rr
}
func TestArtifactCopyIndependentIdentityRetryAndImmutableSource(t *testing.T) {
	h, _, _, ws, user, source := artifactFixture(t)
	artifactExpect(t, artifactSave(t, h, ws, user, "<h1>Original</h1>", "original", 0), 201)
	artifactExpect(t, artifactSave(t, h, ws, user, "<h1>Changed</h1>", "changed", 1), 201)
	body := `{"source_revision":1,"slug":"report-copy","name":"My copy","mutation_id":"copy-once"}`
	rr := artifactCopyCall(t, h, ws, user, "OWNER", body)
	artifactExpect(t, rr, 201)
	var result struct {
		Slug     string `json:"page_slug"`
		Revision int64  `json:"revision"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if result.Slug != "report-copy" || result.Revision != 1 {
		t.Fatalf("copy receipt %s", rr.Body.String())
	}
	artifactExpect(t, artifactCopyCall(t, h, ws, user, "OWNER", body), 200)
	artifactExpect(t, artifactCopyCall(t, h, ws, user, "OWNER", `{"source_revision":2,"slug":"report-copy","name":"My copy","mutation_id":"copy-once"}`), 409)
	var target, owner, sha, sourceSHA string
	if err := h.db.QueryRow(`SELECT id,owner_user_id FROM pages WHERE slug='report-copy' AND workspace_id=?`, ws).Scan(&target, &owner); err != nil {
		t.Fatal(err)
	}
	if target == source || owner != user {
		t.Fatal("copy did not create independent human identity")
	}
	if err := h.db.QueryRow(`SELECT sha256 FROM page_artifact_revisions WHERE page_id=? AND revision=1`, target).Scan(&sha); err != nil {
		t.Fatal(err)
	}
	if err := h.db.QueryRow(`SELECT sha256 FROM page_artifact_revisions WHERE page_id=? AND revision=1`, source).Scan(&sourceSHA); err != nil {
		t.Fatal(err)
	}
	if sha != sourceSHA {
		t.Fatal("copied wrong revision")
	}
	for _, table := range []string{"page_grants", "page_artifact_run_delegations", "page_artifact_threads", "page_artifact_requests"} {
		var count int
		if err := h.db.QueryRow(`SELECT count(*) FROM `+table+` WHERE page_id=?`, target).Scan(&count); err != nil {
			t.Fatal(err)
		}
		if count != 0 {
			t.Fatalf("inherited %s", table)
		}
	}
	execOrFatal(t, h.db, `DELETE FROM page_artifact_revisions WHERE page_id=? AND revision=1`, source)
	artifactExpect(t, artifactCopyCall(t, h, ws, user, "OWNER", body), 200)
	execOrFatal(t, h.db, `DELETE FROM pages WHERE id=?`, target)
	artifactExpect(t, artifactCopyCall(t, h, ws, user, "OWNER", body), 409)
	var count int
	_ = h.db.QueryRow(`SELECT count(*) FROM pages WHERE slug='report-copy'`).Scan(&count)
	if count != 0 {
		t.Fatal("retry resurrected deleted copy")
	}
}
func TestArtifactCopyRequiresCurrentReadAndCreateAndRedactsOrigin(t *testing.T) {
	h, _, _, ws, user, source := artifactFixture(t)
	artifactExpect(t, artifactSave(t, h, ws, user, "private", "first", 0), 201)
	pagesSeedUser(t, h, ws, "copy-reader", "reader@example.com", "MEMBER")
	body := `{"source_revision":1,"slug":"reader-copy","name":"Reader copy","mutation_id":"reader-copy"}`
	artifactExpect(t, artifactCopyCall(t, h, ws, "copy-reader", "MEMBER", body), 404)
	pagesGrant(t, h, ws, user, "report", `{"subject_type":"user","subject":"copy-reader","level":"read"}`)
	artifactExpect(t, artifactCopyCall(t, h, ws, "copy-reader", "MEMBER", body), 403)
	execOrFatal(t, h.db, `UPDATE workspace_members SET role='MANAGER' WHERE workspace_id=? AND user_id='copy-reader'`, ws)
	artifactExpect(t, artifactCopyCall(t, h, ws, "copy-reader", "MANAGER", body), 201)
	var target string
	_ = h.db.QueryRow(`SELECT id FROM pages WHERE slug='reader-copy'`).Scan(&target)
	provenance := h.artifactCopyProvenance(pagesGrantRequest(t, "GET", "/", ws, "copy-reader", "MANAGER", "").Context(), ws, target, "copy-reader")
	if provenance["source"] == nil {
		t.Fatal("authorized origin missing")
	}
	execOrFatal(t, h.db, `DELETE FROM page_grants WHERE page_id=? AND subject_id='copy-reader'`, source)
	execOrFatal(t, h.db, `UPDATE workspace_members SET role='MEMBER' WHERE workspace_id=? AND user_id='copy-reader'`, ws)
	provenance = h.artifactCopyProvenance(pagesGrantRequest(t, "GET", "/", ws, "copy-reader", "MEMBER", "").Context(), ws, target, "copy-reader")
	if provenance["source"] != nil || provenance["source_revision"] != int64(1) {
		t.Fatalf("origin leaked %+v", provenance)
	}
	artifactExpect(t, artifactCopyCall(t, h, ws, "copy-reader", "MEMBER", body), 404)
}
func TestArtifactCopyRejectsMalformedAndMissingSource(t *testing.T) {
	h, _, _, ws, user, _ := artifactFixture(t)
	for _, body := range []string{`{}`, `{"source_revision":0,"slug":"copy","name":"Copy","mutation_id":"x"}`, `{"source_revision":1,"slug":"copy","name":"Copy","mutation_id":"x","owner":"forged"}`} {
		artifactExpect(t, artifactCopyCall(t, h, ws, user, "OWNER", body), 400)
	}
	artifactExpect(t, artifactCopyCall(t, h, ws, user, "OWNER", `{"source_revision":1,"slug":"copy","name":"Copy","mutation_id":"x"}`), 404)
}

func TestArtifactCopyProvenanceRejectsNullID(t *testing.T) {
	h, _, _, ws, user, _ := artifactFixture(t)
	_, err := h.db.Exec(`INSERT INTO page_artifact_copies(id,workspace_id,human_user_id,source_revision,source_sha256,mutation_id,request_digest,created_at) VALUES(NULL,?,?,1,?,'null-id','digest','2026-10-13T00:00:00Z')`, ws, user, "0000000000000000000000000000000000000000000000000000000000000000")
	if err == nil || !strings.Contains(err.Error(), "NOT NULL constraint failed: page_artifact_copies.id") {
		t.Fatalf("NULL provenance ID not rejected: %v", err)
	}
}
