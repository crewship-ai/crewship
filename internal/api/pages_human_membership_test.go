package api

import (
	"fmt"
	"net/http/httptest"
	"strings"
	"testing"
)

func revokeContentCeiling(t *testing.T, h *PageHandler, workspace, user, kind string) {
	t.Helper()
	switch kind {
	case "restricted":
		// This helper is also used by Board writes. Make the transition legal
		// under the account-wide Guest/admin invariant before restricting it.
		execOrFatal(t, h.db, `UPDATE workspace_members SET role='MEMBER' WHERE user_id=?`, user)
		execOrFatal(t, h.db, `INSERT INTO workspaces(id,name,slug) VALUES('ceiling-other','Other','ceiling-other')`)
		execOrFatal(t, h.db, `INSERT INTO workspace_members(id,workspace_id,user_id,role,access_mode) VALUES('ceiling-restricted','ceiling-other',?,'MEMBER','restricted')`, user)
	case "workspace-deleted":
		execOrFatal(t, h.db, `UPDATE workspaces SET deleted_at='2026-10-13T00:00:00Z' WHERE id=?`, workspace)
	case "member-removed":
		execOrFatal(t, h.db, `DELETE FROM workspace_members WHERE workspace_id=? AND user_id=?`, workspace, user)
	case "suspended":
		execOrFatal(t, h.db, `UPDATE users SET suspended_at='2026-10-13T00:00:00Z' WHERE id=?`, user)
	default:
		t.Fatal(kind)
	}
}
func TestArtifactWritesRecheckHumanCeilingAfterBodyRead(t *testing.T) {
	for _, operation := range []string{"publish", "comment"} {
		for _, change := range []string{"restricted", "workspace-deleted", "member-removed", "suspended"} {
			t.Run(operation+"/"+change, func(t *testing.T) {
				h, _, _, workspace, user, page := artifactFixture(t)
				if change == "restricted" {
					// Guest/admin roles are mutually exclusive. Publish first as a
					// MEMBER Page owner, proving demotion alone permits this write.
					execOrFatal(t, h.db, `UPDATE workspace_members SET role='MEMBER' WHERE user_id=?`, user)
				}
				artifactExpect(t, artifactSave(t, h, workspace, user, "original", "first", 0), 201)
				body := `{"html":"must not publish","base_rev":1,"mutation_id":"denied"}`
				if operation == "comment" {
					body = `{"revision":1,"anchor":{"schema_version":1,"type":"document"},"body":"must not comment","mutation_id":"denied"}`
				}
				r := pagesGrantRequest(t, "POST", "/", workspace, user, "OWNER", body)
				r.SetPathValue("slug", "report")
				r.Body = &artifactRevokingBody{reader: strings.NewReader(body), revoke: func() { revokeContentCeiling(t, h, workspace, user, change) }}
				w := httptest.NewRecorder()
				if operation == "comment" {
					h.CreateArtifactThread(w, r)
				} else {
					h.PublishArtifact(w, r)
				}
				artifactExpect(t, w, 403)
				var head, threads int
				if err := h.db.QueryRow(`SELECT head_rev FROM page_artifacts WHERE page_id=?`, page).Scan(&head); err != nil {
					t.Fatal(err)
				}
				if err := h.db.QueryRow(`SELECT COUNT(*) FROM page_artifact_threads WHERE page_id=?`, page).Scan(&threads); err != nil {
					t.Fatal(err)
				}
				if head != 1 || threads != 0 {
					t.Fatalf("revoked mutation committed head=%d threads=%d", head, threads)
				}
			})
		}
	}
}
func TestBoardWritesRecheckHumanCeilingAfterBodyRead(t *testing.T) {
	for _, change := range []string{"restricted", "workspace-deleted", "member-removed", "suspended"} {
		t.Run(change, func(t *testing.T) {
			h, _, _, workspace, user, page := artifactFixture(t)
			artifactExpect(t, boardCall(t, h, workspace, user, "OWNER", "report", "init", `{"mutation_id":"init"}`), 200)
			body := fmt.Sprintf(`{"mutation_id":"denied","base_seq":0,"upserts":[{"element":%s,"expected_rev":0}],"deletes":[]}`, boardAPIElement(t))
			r := pagesGrantRequest(t, "POST", "/", workspace, user, "OWNER", body)
			r.SetPathValue("slug", "report")
			r.Body = &artifactRevokingBody{reader: strings.NewReader(body), revoke: func() { revokeContentCeiling(t, h, workspace, user, change) }}
			w := httptest.NewRecorder()
			h.ApplyBoardBatch(w, r)
			artifactExpect(t, w, 403)
			var seq int
			if err := h.db.QueryRow(`SELECT seq FROM page_boards WHERE page_id=?`, page).Scan(&seq); err != nil || seq != 0 {
				t.Fatalf("revoked board committed seq=%d err=%v", seq, err)
			}
		})
	}
}
