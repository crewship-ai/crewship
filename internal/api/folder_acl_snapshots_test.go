package api

import (
	"net/http/httptest"
	"testing"
)

func TestFolderACLCanonicalSnapshots(t *testing.T) {
	f := newFoldersFixture(t)
	f.createFolder(t, "snapshots", "Snapshots", "crew/engine")
	for i, step := range []struct {
		method, path, body string
		before, after      int
	}{
		{"PUT", "/api/v1/page-folders/snapshots/acl", `{"subject_type":"user","subject_id":"frank","can_write":false}`, 0, 1},
		{"PUT", "/api/v1/page-folders/snapshots/acl", `{"subject_type":"user","subject_id":"frank","can_write":true}`, 1, 1},
		{"DELETE", "/api/v1/page-folders/snapshots/acl/user/frank", "", 1, 0},
	} {
		rr := f.call(t, step.method, step.path, "erin", "ADMIN", step.body)
		if rr.Code != 200 && rr.Code != 204 {
			t.Fatalf("mutation: %d", rr.Code)
		}
		var before, after, version int
		if err := f.h.db.QueryRow(`SELECT json_array_length(json_extract(payload,'$.previous_acl')),json_array_length(json_extract(payload,'$.acl')),json_extract(payload,'$.previous_acl_version') FROM journal_entries WHERE entry_type='page.folder_acl_changed' ORDER BY seq DESC LIMIT 1`).Scan(&before, &after, &version); err != nil {
			t.Fatal(err)
		}
		if before != step.before || after != step.after || version != i {
			t.Fatalf("snapshots=%d/%d version=%d", before, after, version)
		}
		if i == 1 {
			var oldWrite, newWrite bool
			if err := f.h.db.QueryRow(`SELECT json_extract(payload,'$.previous_acl[0].can_write'),json_extract(payload,'$.acl[0].can_write') FROM journal_entries WHERE entry_type='page.folder_acl_changed' ORDER BY seq DESC LIMIT 1`).Scan(&oldWrite, &newWrite); err != nil {
				t.Fatal(err)
			}
			if oldWrite || !newWrite {
				t.Fatal("reissue lost old/new permissions")
			}
		}
	}
}

func TestWorkspaceDeletionAuditMemberCount(t *testing.T) {
	h, user, ws := deleteRig(t)
	seedOtherWorkspace(t, h, "ws-other", "other", user)
	rr := httptest.NewRecorder()
	h.Delete(rr, deleteReq(t, user, ws, "OWNER", "test"))
	if rr.Code != 200 {
		t.Fatal(rr.Code)
	}
	var count int
	var actor string
	if err := h.db.QueryRow(`SELECT actor_id,json_extract(payload,'$.member_count') FROM journal_entries WHERE workspace_id=? AND entry_type='workspace.deleted'`, ws).Scan(&actor, &count); err != nil {
		t.Fatal(err)
	}
	if actor != user || count != 1 {
		t.Fatalf("actor=%s members=%d", actor, count)
	}
}
