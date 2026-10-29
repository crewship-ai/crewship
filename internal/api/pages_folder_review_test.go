package api

import (
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"strings"
	"testing"
)

func folderReviewCall(t *testing.T, f *foldersFixture, folder, user, role, body string) *httptest.ResponseRecorder {
	t.Helper()
	r := artifactPolicyHumanRequest(t, f.h, "POST", "/api/v1/page-folders/"+folder+"/pages", f.wsID, user, role, body)
	r.SetPathValue("slug", folder)
	w := httptest.NewRecorder()
	f.h.AddFolderPage(w, r)
	return w
}

func TestFolderReviewWithholdsTargetACLAndPreservesOtherAccessPaths(t *testing.T) {
	f := newFoldersFixture(t)
	f.createFolder(t, "engine", "Engine", "crew/engine")
	f.share(t, "engine", "user", "alice", true)
	f.share(t, "engine", "user", "frank", false)
	body := fmt.Sprintf(`{"page":"fleet-201","pages_version":%d,"acl_version":%d,"dry_run":true}`, f.pagesVersion(t, "fleet-201"), f.aclVersion(t, "engine"))
	w := folderReviewCall(t, f, "engine", "alice", "MEMBER", body)
	expectStatus(t, w, 200)
	if strings.Contains(w.Body.String(), "frank") || strings.Contains(w.Body.String(), "mia") {
		t.Fatal("folder writer preview disclosed target ACL")
	}
	var p pageFolderReviewWire
	if err := json.Unmarshal(w.Body.Bytes(), &p); err != nil || !p.After.Withheld || len(p.After.Subjects) != 0 {
		t.Fatalf("missing explicit withheld %s %v", w.Body, err)
	}
	// The administrator sees the full union: a direct reader remains a reader.
	w = folderReviewCall(t, f, "engine", "erin", "ADMIN", body)
	expectStatus(t, w, 200)
	if err := json.Unmarshal(w.Body.Bytes(), &p); err != nil {
		t.Fatal(err)
	}
	for _, change := range p.Changes {
		if change.SubjectID == "dave" {
			t.Fatal("direct read was reported lost")
		}
	}
}

func TestFolderReviewRejectsAmbiguousTransportAndUnsupportedBatch(t *testing.T) {
	f := newFoldersFixture(t)
	f.createFolder(t, "engine", "Engine", "crew/engine")
	v := f.pagesVersion(t, "fleet-201")
	acl := f.aclVersion(t, "engine")
	for _, suffix := range []string{`"dry_run":false`, `"dry_run":null`, `"dry_run":true,"dry_run":false`, `"Dry_Run":true`, `"dry_run":true,"unknown":1`, `"confirm":false`} {
		body := fmt.Sprintf(`{"page":"fleet-201","pages_version":%d,"acl_version":%d,%s}`, v, acl, suffix)
		expectStatus(t, folderReviewCall(t, f, "engine", "erin", "ADMIN", body), 400)
		if f.pagesVersion(t, "fleet-201") != v {
			t.Fatal("invalid review changed page")
		}
	}
	body := fmt.Sprintf(`{"pages":[{"page":"fleet-201","pages_version":%d}],"acl_version":%d,"dry_run":true}`, v, acl)
	expectStatus(t, f.call(t, "POST", "/api/v1/page-folders/engine/pages:batch", "erin", "ADMIN", body), 400)
	if f.pagesVersion(t, "fleet-201") != v {
		t.Fatal("unsupported batch review mutated Page")
	}
}

func TestFolderReviewFinalWriterRejectsAuthorityABA(t *testing.T) {
	f := newFoldersFixture(t)
	f.createFolder(t, "engine", "Engine", "crew/engine")
	v := f.pagesVersion(t, "fleet-201")
	acl := f.aclVersion(t, "engine")
	w := folderReviewCall(t, f, "engine", "erin", "ADMIN", fmt.Sprintf(`{"page":"fleet-201","pages_version":%d,"acl_version":%d,"dry_run":true}`, v, acl))
	expectStatus(t, w, 200)
	p := decodeFoldersJSON(t, w)
	execOrFatal(t, f.h.db, `CREATE TRIGGER test_move_aba AFTER UPDATE OF folder_id ON pages BEGIN UPDATE workspace_members SET role='MEMBER' WHERE user_id='erin'; UPDATE workspace_members SET role='ADMIN' WHERE user_id='erin'; END`)
	body, _ := json.Marshal(map[string]any{"page": "fleet-201", "pages_version": v, "acl_version": acl, "confirm": true, "review_token": p["review_token"]})
	expectStatus(t, folderReviewCall(t, f, "engine", "erin", "ADMIN", string(body)), 409)
	if f.pagesVersion(t, "fleet-201") != v || f.folderOf(t, "fleet-201") != "" {
		t.Fatal("failed final fence did not roll back move")
	}
}

func TestFolderReviewExistingRelevanceGenerations(t *testing.T) {
	f := newFoldersFixture(t)
	f.createFolder(t, "engine", "Engine", "crew/engine")
	execOrFatal(t, f.h.db, `INSERT INTO mooring_authority_generations(workspace_id,scope_kind,scope_id) SELECT workspace_id,'page',id FROM pages WHERE slug='fleet-201'`)
	execOrFatal(t, f.h.db, `INSERT INTO mooring_authority_generations(workspace_id,scope_kind,scope_id) VALUES(?,'user','erin')`, f.wsID)
	v := f.pagesVersion(t, "fleet-201")
	acl := f.aclVersion(t, "engine")
	w := folderReviewCall(t, f, "engine", "erin", "ADMIN", fmt.Sprintf(`{"page":"fleet-201","pages_version":%d,"acl_version":%d,"dry_run":true}`, v, acl))
	expectStatus(t, w, 200)
	p := decodeFoldersJSON(t, w)
	body, _ := json.Marshal(map[string]any{"page": "fleet-201", "pages_version": v, "acl_version": acl, "confirm": true, "review_token": p["review_token"]})
	expectStatus(t, folderReviewCall(t, f, "engine", "erin", "ADMIN", string(body)), 200)
}

func TestFolderMoveReviewDryRunAndGrantABA(t *testing.T) {
	f := newFoldersFixture(t)
	f.createFolder(t, "engine", "Engine", "crew/engine")
	v := f.pagesVersion(t, "fleet-201")
	acl := f.aclVersion(t, "engine")
	body := fmt.Sprintf(`{"page":"fleet-201","pages_version":%d,"acl_version":%d,"dry_run":true}`, v, acl)
	w := folderReviewCall(t, f, "engine", "erin", "ADMIN", body)
	expectStatus(t, w, 200)
	if f.pagesVersion(t, "fleet-201") != v {
		t.Fatal("dry-run moved Page")
	}
	p := decodeFoldersJSON(t, w)
	if p["review_token"] == nil {
		t.Fatal("preview missing review token")
	}
	// Revoking then recreating the same grant must invalidate the old review.
	execOrFatal(t, f.h.db, `DELETE FROM page_grants WHERE subject_id='dave'`)
	pagesGrant(t, f.h, f.wsID, f.owner, "fleet-201", `{"subject_type":"user","subject":"dave","level":"read"}`)
	commit, _ := json.Marshal(map[string]any{"page": "fleet-201", "pages_version": v, "acl_version": acl, "confirm": true, "review_token": p["review_token"]})
	expectStatus(t, folderReviewCall(t, f, "engine", "erin", "ADMIN", string(commit)), 409)
	w = folderReviewCall(t, f, "engine", "erin", "ADMIN", body)
	expectStatus(t, w, 200)
	p = decodeFoldersJSON(t, w)
	commit, _ = json.Marshal(map[string]any{"page": "fleet-201", "pages_version": v, "acl_version": acl, "confirm": true, "review_token": p["review_token"]})
	expectStatus(t, folderReviewCall(t, f, "engine", "erin", "ADMIN", string(commit)), 200)
	if f.pagesVersion(t, "fleet-201") != v+1 {
		t.Fatal("confirmed move did not increment exactly once")
	}
}
