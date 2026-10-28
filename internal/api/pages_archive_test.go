package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"testing"
)

func archiveCall(t *testing.T, f *foldersFixture, slug, user, role, op, body string) *httptest.ResponseRecorder {
	t.Helper()
	r := artifactPolicyHumanRequest(t, f.h, "POST", "/api/v1/pages/"+slug+"/"+op, f.wsID, user, role, body)
	r.SetPathValue("slug", slug)
	w := httptest.NewRecorder()
	if op == "archive" {
		f.h.ArchivePage(w, r)
	} else {
		f.h.UnarchivePage(w, r)
	}
	return w
}

func TestPageArchiveRejectsRunPrincipal(t *testing.T) {
	f := newFoldersFixture(t)
	r := artifactPolicyHumanRequest(t, f.h, "POST", "/api/v1/pages/fleet-201/archive", f.wsID, "alice", "MEMBER", fmt.Sprintf(`{"dry_run":true,"pages_version":%d}`, f.pagesVersion(t, "fleet-201")))
	r.SetPathValue("slug", "fleet-201")
	r = r.WithContext(context.WithValue(r.Context(), artifactRunKey{}, "delegated-run"))
	w := httptest.NewRecorder()
	f.h.ArchivePage(w, r)
	expectStatus(t, w, 403)
}

func archivePreview(t *testing.T, f *foldersFixture, op string) map[string]any {
	t.Helper()
	w := archiveCall(t, f, "fleet-201", "alice", "MEMBER", op, fmt.Sprintf(`{"dry_run":true,"pages_version":%d}`, f.pagesVersion(t, "fleet-201")))
	expectStatus(t, w, 200)
	return decodeFoldersJSON(t, w)
}

func TestPageArchiveReviewAndRestoreDiscoveryOnly(t *testing.T) {
	f := newFoldersFixture(t)
	v := f.pagesVersion(t, "fleet-201")
	preview := archivePreview(t, f, "archive")
	if f.pagesVersion(t, "fleet-201") != v {
		t.Fatal("dry-run changed page")
	}
	body, _ := json.Marshal(map[string]any{"confirm": true, "pages_version": v, "review_token": preview["review_token"]})
	w := archiveCall(t, f, "fleet-201", "alice", "MEMBER", "archive", string(body))
	expectStatus(t, w, 200)
	var archived string
	if err := f.h.db.QueryRow(`SELECT COALESCE(archived_at,'') FROM pages WHERE slug='fleet-201'`).Scan(&archived); err != nil || archived == "" {
		t.Fatalf("not archived: %q %v", archived, err)
	}
	if f.pagesVersion(t, "fleet-201") != v+1 {
		t.Fatal("missing one version bump")
	}
	// Archive is discovery state: direct readers and grants remain intact.
	read := pagesAccessCall(t, f.h, f.wsID, "alice", "MEMBER", "fleet-201", "")
	expectStatus(t, read, 200, "dave")
	p := archivePreview(t, f, "unarchive")
	body, _ = json.Marshal(map[string]any{"confirm": true, "pages_version": v + 1, "review_token": p["review_token"]})
	expectStatus(t, archiveCall(t, f, "fleet-201", "alice", "MEMBER", "unarchive", string(body)), 200)
}

func TestPageArchiveRejectsUnconfirmedAndAuthorityABA(t *testing.T) {
	f := newFoldersFixture(t)
	v := f.pagesVersion(t, "fleet-201")
	expectStatus(t, archiveCall(t, f, "fleet-201", "alice", "MEMBER", "archive", fmt.Sprintf(`{"pages_version":%d}`, v)), 400)
	p := archivePreview(t, f, "archive")
	// Same membership and resulting access, but a different canonical authority epoch.
	execOrFatal(t, f.h.db, `DELETE FROM workspace_members WHERE workspace_id=? AND user_id='dave'`, f.wsID)
	execOrFatal(t, f.h.db, `INSERT INTO workspace_members(id,workspace_id,user_id,role) VALUES('dave-return',?,'dave','MEMBER')`, f.wsID)
	body, _ := json.Marshal(map[string]any{"confirm": true, "pages_version": v, "review_token": p["review_token"]})
	expectStatus(t, archiveCall(t, f, "fleet-201", "alice", "MEMBER", "archive", string(body)), 409)
	expectStatus(t, archiveCall(t, f, "fleet-201", "dave", "ADMIN", "archive", fmt.Sprintf(`{"dry_run":true,"pages_version":%d}`, v)), 403)
}

func TestPageArchiveNoopAndBoundReview(t *testing.T) {
	f := newFoldersFixture(t)
	v := f.pagesVersion(t, "fleet-201")
	p := archivePreview(t, f, "unarchive")
	body, _ := json.Marshal(map[string]any{"confirm": true, "pages_version": v, "review_token": p["review_token"]})
	w := archiveCall(t, f, "fleet-201", "alice", "MEMBER", "unarchive", string(body))
	expectStatus(t, w, 200)
	if f.pagesVersion(t, "fleet-201") != v || decodeFoldersJSON(t, w)["changed"] != false {
		t.Fatal("no-op mutated Page")
	}
	p = archivePreview(t, f, "archive")
	body, _ = json.Marshal(map[string]any{"confirm": true, "pages_version": v, "review_token": p["review_token"]})
	// A second authorized actor cannot adopt somebody else's explicit review.
	expectStatus(t, archiveCall(t, f, "fleet-201", "erin", "ADMIN", "archive", string(body)), 409)
	expectStatus(t, archiveCall(t, f, "fleet-201", "alice", "MEMBER", "unarchive", string(body)), 409)
	for _, raw := range []string{`{"dry_run":true,"pages_version":0,"Pages_Version":0}`, `{"dry_run":true,"dry_run":false,"pages_version":0}`, `{"dry_run":true,"pages_version":null}`, `{"dry_run":true,"pages_version":0} {}`} {
		expectStatus(t, archiveCall(t, f, "fleet-201", "alice", "MEMBER", "archive", raw), 400)
	}
	if f.pagesVersion(t, "fleet-201") != v {
		t.Fatal("invalid confirmation changed Page")
	}
}

func TestPageArchiveFinalFenceRejectsABAAndRetainsArtifactHistory(t *testing.T) {
	h, ws, owner, call := artifactProjectFixture(t)
	created := call(owner, "POST", "artifacts", `{"slug":"three-screens","name":"Kept",`+artifactProjectBody(0, "Kept")[1:])
	expectStatus(t, created, 201)
	f := &foldersFixture{h: h, wsID: ws, owner: owner}
	var id string
	var head, shared, versions int
	if err := h.db.QueryRow(`SELECT page_id,latest_version,shared_version FROM artifact_version_heads`).Scan(&id, &head, &shared); err != nil {
		t.Fatal(err)
	}
	if err := h.db.QueryRow(`SELECT COUNT(*) FROM artifact_versions WHERE page_id=?`, id).Scan(&versions); err != nil {
		t.Fatal(err)
	}
	v := f.pagesVersion(t, "three-screens")
	w := archiveCall(t, f, "@"+id, owner, "OWNER", "archive", fmt.Sprintf(`{"dry_run":true,"pages_version":%d}`, v))
	expectStatus(t, w, 200)
	p := decodeFoldersJSON(t, w)
	// Canonical Page ownership is restored by the trigger; equality of the
	// final row must not revive a previously captured authorization epoch.
	execOrFatal(t, h.db, `CREATE TRIGGER test_archive_aba AFTER UPDATE OF archived_at ON pages BEGIN UPDATE workspace_members SET role='MEMBER' WHERE user_id='`+owner+`'; UPDATE workspace_members SET role='OWNER' WHERE user_id='`+owner+`'; END`)
	body, _ := json.Marshal(map[string]any{"confirm": true, "pages_version": v, "review_token": p["review_token"]})
	expectStatus(t, archiveCall(t, f, "@"+id, owner, "OWNER", "archive", string(body)), 409)
	if f.pagesVersion(t, "three-screens") != v {
		t.Fatal("failed archive did not roll back")
	}
	execOrFatal(t, h.db, `DROP TRIGGER test_archive_aba`)
	w = archiveCall(t, f, "@"+id, owner, "OWNER", "archive", fmt.Sprintf(`{"dry_run":true,"pages_version":%d}`, v))
	expectStatus(t, w, 200)
	p = decodeFoldersJSON(t, w)
	body, _ = json.Marshal(map[string]any{"confirm": true, "pages_version": v, "review_token": p["review_token"]})
	expectStatus(t, archiveCall(t, f, "@"+id, owner, "OWNER", "archive", string(body)), 200)
	var nextHead, nextShared, nextVersions int
	if err := h.db.QueryRow(`SELECT latest_version,shared_version FROM artifact_version_heads WHERE page_id=?`, id).Scan(&nextHead, &nextShared); err != nil {
		t.Fatal(err)
	}
	if err := h.db.QueryRow(`SELECT COUNT(*) FROM artifact_versions WHERE page_id=?`, id).Scan(&nextVersions); err != nil {
		t.Fatal(err)
	}
	if nextHead != head || nextShared != shared || nextVersions != versions {
		t.Fatal("archive altered VersionStore history/pins")
	}
	expectStatus(t, call(owner, "GET", "three-screens/artifact", ""), 200, "Kept")
}
