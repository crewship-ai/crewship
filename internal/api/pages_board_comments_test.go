package api

import (
	"encoding/json"
	"fmt"
	"github.com/crewship-ai/crewship/internal/backup"
	"net/http/httptest"
	"strings"
	"testing"
)

func boardDiscussionCall(t *testing.T, h *PageHandler, ws, user, op, id string, payload any) *httptest.ResponseRecorder {
	t.Helper()
	b, _ := json.Marshal(payload)
	r := pagesGrantRequest(t, "POST", "/", ws, user, "OWNER", string(b))
	r.SetPathValue("slug", "report")
	r.SetPathValue("threadId", id)
	w := httptest.NewRecorder()
	switch op {
	case "create":
		h.CreateBoardThread(w, r)
	case "reply":
		h.ReplyBoardThread(w, r)
	case "resolve":
		h.ResolveBoardThread(w, r)
	case "list":
		h.ListBoardThreads(w, r)
	}
	return w
}
func TestBoardComments_ImmutableAnchorCASAndRetry(t *testing.T) {
	h, _, _, ws, user, page := artifactFixture(t)
	artifactExpect(t, boardCall(t, h, ws, user, "OWNER", "report", "init", `{"mutation_id":"init"}`), 200)
	raw := boardAPIElement(t)
	var el struct {
		ID string `json:"id"`
	}
	json.Unmarshal(raw, &el)
	artifactExpect(t, boardCall(t, h, ws, user, "OWNER", "report", "batch", fmt.Sprintf(`{"mutation_id":"shape","base_seq":0,"upserts":[{"element":%s,"expected_rev":0}],"deletes":[]}`, raw)), 201)
	payload := map[string]any{"anchor": map[string]any{"schema_version": 1, "type": "board-element", "board_seq": 1, "element_id": el.ID, "element_rev": 1}, "body": "Please revise @agent (no dispatch)", "mutation_id": "comment"}
	w := boardDiscussionCall(t, h, ws, user, "create", "", payload)
	artifactExpect(t, w, 201)
	var thread boardThreadWire
	json.Unmarshal(w.Body.Bytes(), &thread)
	if thread.Anchor.Snapshot == nil || thread.Anchor.ElementID != el.ID || len(thread.Comments) != 1 {
		t.Fatal(w.Body.String())
	}
	// Deletion retains the original geometry and an exact retry never reanchors.
	if _, err := h.db.Exec(`UPDATE page_board_elements SET element_json=NULL,element_rev=2 WHERE board_page_id=? AND element_id=?`, page, el.ID); err != nil {
		t.Fatal(err)
	}
	artifactExpect(t, boardDiscussionCall(t, h, ws, user, "create", "", payload), 200)
	reply := map[string]any{"body": "Reply", "base_version": 1, "mutation_id": "reply"}
	artifactExpect(t, boardDiscussionCall(t, h, ws, user, "reply", thread.ID, reply), 201)
	reply["mutation_id"] = "stale"
	artifactExpect(t, boardDiscussionCall(t, h, ws, user, "reply", thread.ID, reply), 409)
	artifactExpect(t, boardDiscussionCall(t, h, ws, user, "resolve", thread.ID, map[string]any{"resolved": true, "base_version": 2, "mutation_id": "resolve"}), 200)
	artifactExpect(t, boardDiscussionCall(t, h, ws, user, "list", "", nil), 200)
	payload["mutation_id"] = "deleted"
	artifactExpect(t, boardDiscussionCall(t, h, ws, user, "create", "", payload), 409)
}
func TestBoardComments_StaleAndForgedAnchors(t *testing.T) {
	h, _, _, ws, user, _ := artifactFixture(t)
	artifactExpect(t, boardCall(t, h, ws, user, "OWNER", "report", "init", `{"mutation_id":"init"}`), 200)
	for _, anchor := range []any{map[string]any{"schema_version": 1, "type": "board", "board_seq": 0, "snapshot": map[string]any{"x": 1}}, map[string]any{"schema_version": 1, "type": "board-element", "board_seq": 0, "element_id": "missing", "element_rev": 0}} {
		artifactExpect(t, boardDiscussionCall(t, h, ws, user, "create", "", map[string]any{"anchor": anchor, "body": "x", "mutation_id": "bad"}), 422)
	}
	artifactExpect(t, boardDiscussionCall(t, h, ws, user, "create", "", map[string]any{"anchor": map[string]any{"schema_version": 1, "type": "board", "board_seq": 1}, "body": "x", "mutation_id": "stale"}), 409)
	artifactExpect(t, boardDiscussionCall(t, h, ws, "unknown", "list", "", nil), 403)
}

func TestBoardComments_CommentGrantResolveAndUseTimeRevocation(t *testing.T) {
	h, _, _, ws, owner, page := artifactFixture(t)
	artifactExpect(t, boardCall(t, h, ws, owner, "OWNER", "report", "init", `{"mutation_id":"init"}`), 200)
	execOrFatal(t, h.db, `INSERT INTO users(id,email) VALUES ('board-commenter','bc@example.invalid')`)
	execOrFatal(t, h.db, `INSERT INTO workspace_members(id,workspace_id,user_id,role) VALUES ('bc-member',?,'board-commenter','MEMBER')`, ws)
	execOrFatal(t, h.db, `INSERT INTO page_grants(page_id,subject_type,subject_id,level,granted_by_user_id) VALUES (?,'user','board-commenter','read',?)`, page, owner)
	create := map[string]any{"anchor": map[string]any{"schema_version": 1, "type": "board", "board_seq": 0}, "body": "review", "mutation_id": "same-per-actor"}
	// Caller context claims OWNER; live workspace role must control writes.
	artifactExpect(t, boardDiscussionCall(t, h, ws, "board-commenter", "create", "", create), 403)
	if _, err := h.db.Exec(`UPDATE page_grants SET level='comment' WHERE page_id=?`, page); err != nil {
		t.Fatal(err)
	}
	w := boardDiscussionCall(t, h, ws, "board-commenter", "create", "", create)
	artifactExpect(t, w, 201)
	var th boardThreadWire
	json.Unmarshal(w.Body.Bytes(), &th)
	artifactExpect(t, boardDiscussionCall(t, h, ws, "board-commenter", "resolve", th.ID, map[string]any{"resolved": true, "base_version": 1, "mutation_id": "resolve"}), 403)
	artifactExpect(t, boardDiscussionCall(t, h, ws, owner, "create", "", create), 201)
	raw := `{"body":"revoked","base_version":1,"mutation_id":"revoked"}`
	r := pagesGrantRequest(t, "POST", "/", ws, "board-commenter", "MEMBER", raw)
	r.SetPathValue("slug", "report")
	r.SetPathValue("threadId", th.ID)
	r.Body = &artifactRevokingBody{reader: strings.NewReader(raw), revoke: func() {
		if _, err := h.db.Exec(`DELETE FROM page_grants WHERE page_id=?`, page); err != nil {
			t.Fatal(err)
		}
	}}
	w = httptest.NewRecorder()
	h.ReplyBoardThread(w, r)
	artifactExpect(t, w, 404)
}

func TestBoardComments_BackupRestoreAnchorsAndAudit(t *testing.T) {
	for _, fork := range []string{"", "comments-fork"} {
		t.Run("fork="+fork, func(t *testing.T) {
			h, _, _, ws, user, _ := artifactFixture(t)
			artifactExpect(t, boardCall(t, h, ws, user, "OWNER", "report", "init", `{"mutation_id":"init"}`), 200)
			rr := boardDiscussionCall(t, h, ws, user, "create", "", map[string]any{"anchor": map[string]any{"schema_version": 1, "type": "board", "board_seq": 0}, "body": "Preserve original comment", "mutation_id": "first-comment"})
			artifactExpect(t, rr, 201)
			var thread boardThreadWire
			json.Unmarshal(rr.Body.Bytes(), &thread)
			artifactExpect(t, boardDiscussionCall(t, h, ws, user, "reply", thread.ID, map[string]any{"body": "Preserve reply", "base_version": 1, "mutation_id": "reply"}), 201)
			artifactExpect(t, boardDiscussionCall(t, h, ws, user, "resolve", thread.ID, map[string]any{"resolved": true, "base_version": 2, "mutation_id": "resolve"}), 200)
			const pass = "artifact-comments-roundtrip-password"
			created, err := backup.CreateBackup(t.Context(), h.db, backup.CreateOptions{Scope: backup.ScopeWorkspace, WorkspaceID: ws, OutputDir: t.TempDir(), Passphrase: pass, Actor: backup.Actor{UserID: user, Role: "ADMIN"}, AttachmentRoot: h.artifactStoragePath})
			if err != nil {
				t.Fatal(err)
			}
			target := setupTestDB(t)
			root := storageDir(t)
			restored, err := backup.RestoreBackup(t.Context(), target, backup.RestoreOptions{Path: created.Path, Passphrase: pass, AsWorkspace: fork, Actor: backup.Actor{UserID: user, Role: "ADMIN"}, AttachmentRoot: root})
			if err != nil {
				t.Fatal(err)
			}
			var id string
			if err := target.QueryRow(`SELECT t.id FROM page_board_threads t JOIN pages p ON p.id=t.page_id WHERE p.workspace_id=?`, restored.RestoredWorkspaceID).Scan(&id); err != nil {
				t.Fatal(err)
			}
			var pageID string
			if err := target.QueryRow(`SELECT page_id FROM page_board_threads WHERE id=?`, id).Scan(&pageID); err != nil {
				t.Fatal(err)
			}
			restoredHandler := NewPageHandler(target, nil, newTestLogger())
			v, err := restoredHandler.loadBoardThread(t.Context(), pageID, id)
			if err != nil {
				t.Fatal(err)
			}
			if v.Anchor.SchemaVersion != 1 || v.Anchor.Type != "board" || v.Anchor.BoardSeq == nil || *v.Anchor.BoardSeq != 0 || v.Status != "resolved" || v.Version != 3 || len(v.Comments) != 2 || v.Comments[0].Body != "Preserve original comment" || v.Comments[1].Body != "Preserve reply" {
				t.Fatalf("discussion restore lost anchor/content/state: %+v", v)
			}
			var mutations int
			if err := target.QueryRow(`SELECT COUNT(*) FROM page_board_comment_mutations WHERE page_id=?`, pageID).Scan(&mutations); err != nil || mutations != 3 {
				t.Fatalf("audit restored=%d %v", mutations, err)
			}
			if _, err := target.Exec(`DELETE FROM pages WHERE id=?`, pageID); err != nil {
				t.Fatal(err)
			}
			for _, table := range []string{"page_board_threads", "page_board_comments", "page_board_comment_mutations"} {
				var n int
				if err := target.QueryRow(`SELECT COUNT(*) FROM ` + table).Scan(&n); err != nil || n != 0 {
					t.Fatalf("cascade cleanup %s=%d %v", table, n, err)
				}
			}
		})
	}
}

func TestBoardCommentJSONRejectsAmbiguousFields(t *testing.T) {
	for _, raw := range []string{`{"body":"a","body":"b"}`, `{"Body":"a"}`, `null`, `[]`, `{} {}`} {
		if checkBoardCommentRequestJSON([]byte(raw)) == nil {
			t.Fatalf("accepted %s", raw)
		}
	}
	for _, raw := range []string{`{"schema_version":1,"type":"board","board_seq":0,"element_id":null}`, `{"schema_version":1,"type":"board-element","board_seq":0,"element_id":"id","element_rev":1,"snapshot":null}`, `{"schema_version":1,"type":"board","board_seq":0,"board_seq":1}`} {
		var anchor boardCommentAnchor
		err := json.Unmarshal([]byte(raw), &anchor)
		if err == nil && validateBoardCommentAnchor(&anchor) == nil {
			t.Fatalf("accepted anchor %s", raw)
		}
	}
}
