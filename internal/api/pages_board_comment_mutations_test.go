package api

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/crewship-ai/crewship/internal/tsformat"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func boardCommentChangeCall(t *testing.T, h *PageHandler, ws, user, op, thread, comment, emoji string, payload any) *httptest.ResponseRecorder {
	t.Helper()
	raw, _ := json.Marshal(payload)
	method := "PATCH"
	if op == "delete" {
		method = "DELETE"
	}
	if op == "react" {
		method = "PUT"
	}
	if op == "history" {
		method = "GET"
	}
	r := pagesGrantRequest(t, method, "/api/v1/pages/report/board/threads", ws, user, "OWNER", string(raw))
	r.SetPathValue("slug", "report")
	r.SetPathValue("threadId", thread)
	r.SetPathValue("commentId", comment)
	r.SetPathValue("emoji", emoji)
	w := httptest.NewRecorder()
	switch op {
	case "edit":
		h.EditBoardComment(w, r)
	case "delete":
		h.DeleteBoardComment(w, r)
	case "react":
		h.ReactBoardComment(w, r)
	case "history":
		h.GetBoardCommentHistory(w, r)
	}
	return w
}

func TestBoardCommentChanges_CASRetryTombstoneAndAudit(t *testing.T) {
	h, _, ws, user, _ := boardCommentLifecycleFixture(t)
	created := boardCommentLifecycleCreate(t, h, ws, user, "original", "create", 1)
	artifactExpect(t, created, 201)
	th := boardLifecycleThread(t, created)
	id := th.Comments[0].ID
	edit := map[string]any{"base_version": 1, "body": "revised", "mutation_id": "edit-one"}
	w := boardCommentChangeCall(t, h, ws, user, "edit", th.ID, id, "", edit)
	artifactExpect(t, w, 200)
	th = boardLifecycleThread(t, w)
	if th.Version != 2 || th.Comments[0].Version != 2 || th.Comments[0].Body != "revised" || !th.Comments[0].CanEdit {
		t.Fatalf("edit: %+v", th)
	}
	artifactExpect(t, boardCommentChangeCall(t, h, ws, user, "edit", th.ID, id, "", edit), 200)
	artifactExpect(t, boardCommentChangeCall(t, h, ws, user, "edit", th.ID, id, "", map[string]any{"base_version": 1, "body": "changed retry", "mutation_id": "edit-one"}), 409)
	artifactExpect(t, boardCommentChangeCall(t, h, ws, user, "edit", th.ID, id, "", map[string]any{"base_version": 1, "body": "stale", "mutation_id": "stale"}), 409)
	react := map[string]any{"base_version": 2, "active": true, "mutation_id": "react-one"}
	w = boardCommentChangeCall(t, h, ws, user, "react", th.ID, id, "👀", react)
	artifactExpect(t, w, 200)
	th = boardLifecycleThread(t, w)
	if th.Version != 3 || len(th.Comments[0].Reactions) != 1 || !th.Comments[0].Reactions[0].ReactedByMe {
		t.Fatalf("reaction: %+v", th)
	}
	artifactExpect(t, boardCommentChangeCall(t, h, ws, user, "react", th.ID, id, "👀", react), 200)
	del := map[string]any{"base_version": 3, "mutation_id": "delete-one"}
	w = boardCommentChangeCall(t, h, ws, user, "delete", th.ID, id, "", del)
	artifactExpect(t, w, 200)
	th = boardLifecycleThread(t, w)
	c := th.Comments[0]
	if th.Version != 4 || c.Version != 3 || c.Body != "" || c.DeletedAt == "" || c.CanEdit || c.CanDelete || c.CanReact || !c.CanViewHistory || len(c.Reactions) != 0 {
		t.Fatalf("tombstone: %+v", th)
	}
	artifactExpect(t, boardCommentChangeCall(t, h, ws, user, "delete", th.ID, id, "", del), 200)
	w = boardCommentChangeCall(t, h, ws, user, "history", th.ID, id, "", nil)
	artifactExpect(t, w, 200)
	var result struct {
		History []struct {
			Version      int64
			Body, Action string
		}
	}
	if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if len(result.History) != 3 || result.History[0].Action != "delete" || result.History[1].Body != "revised" || result.History[2].Body != "original" {
		t.Fatalf("history: %s", w.Body.String())
	}
	if _, err := h.db.Exec(`UPDATE page_board_comment_history SET body='tampered' WHERE comment_id=?`, id); err == nil {
		t.Fatal("audit must be immutable")
	}
}

func TestBoardCommentChanges_OtherHumanCannotRewriteAuthor(t *testing.T) {
	h, _, ws, user, page := boardCommentLifecycleFixture(t)
	pagesSeedUser(t, h, ws, "comment-other", "other@example.test", "MEMBER")
	if _, err := h.db.Exec(`INSERT INTO page_grants(page_id,subject_type,subject_id,level,granted_by_user_id) VALUES (?,'user','comment-other','comment',?)`, page, user); err != nil {
		t.Fatal(err)
	}
	w := boardCommentLifecycleCreate(t, h, ws, user, "owned", "create", 1)
	artifactExpect(t, w, 201)
	th := boardLifecycleThread(t, w)
	id := th.Comments[0].ID
	artifactExpect(t, boardCommentChangeCall(t, h, ws, "comment-other", "edit", th.ID, id, "", map[string]any{"base_version": 1, "body": "forged", "mutation_id": "foreign-edit"}), 403)
	artifactExpect(t, boardCommentChangeCall(t, h, ws, "comment-other", "delete", th.ID, id, "", map[string]any{"base_version": 1, "mutation_id": "foreign-delete"}), 403)
	artifactExpect(t, boardCommentChangeCall(t, h, ws, "comment-other", "history", th.ID, id, "", nil), 403)
	artifactExpect(t, boardCommentChangeCall(t, h, ws, "comment-other", "react", th.ID, id, "👍", map[string]any{"base_version": 1, "active": true, "mutation_id": "foreign-react"}), 200)
	if _, err := h.db.Exec(`DELETE FROM page_grants WHERE page_id=? AND subject_id='comment-other'`, page); err != nil {
		t.Fatal(err)
	}
	artifactExpect(t, boardCommentChangeCall(t, h, ws, "comment-other", "react", th.ID, id, "👍", map[string]any{"base_version": 2, "active": false, "mutation_id": "revoked-react"}), 404)
}

func boardCommentLifecycleFixture(t *testing.T) (*PageHandler, *pagesFakeClock, string, string, string) {
	t.Helper()
	h, _, clock, ws, user, page := artifactFixture(t)
	artifactExpect(t, boardCall(t, h, ws, user, "OWNER", "report", "init", `{"mutation_id":"init"}`), 200)
	return h, clock, ws, user, page
}
func boardCommentLifecycleCreate(t *testing.T, h *PageHandler, ws, user, body, mutation string, _ int64) *httptest.ResponseRecorder {
	t.Helper()
	return boardDiscussionCall(t, h, ws, user, "create", "", map[string]any{"anchor": map[string]any{"schema_version": 1, "type": "board", "board_seq": 0}, "body": body, "mutation_id": mutation})
}
func boardLifecycleThread(t *testing.T, w *httptest.ResponseRecorder) boardThreadWire {
	t.Helper()
	var thread boardThreadWire
	if err := json.Unmarshal(w.Body.Bytes(), &thread); err != nil {
		t.Fatal(err)
	}
	return thread
}

func TestBoardCommentChangesUseTimeRevocationAndNoAgentAuthority(t *testing.T) {
	h, _, ws, owner, page := boardCommentLifecycleFixture(t)
	pagesSeedUser(t, h, ws, "author-commenter", "commenter@example.test", "MEMBER")
	execOrFatal(t, h.db, `INSERT INTO page_grants(page_id,subject_type,subject_id,level,granted_by_user_id) VALUES (?,'user','author-commenter','comment',?)`, page, owner)
	created := boardCommentLifecycleCreate(t, h, ws, "author-commenter", "original", "commenter-create", 0)
	artifactExpect(t, created, 201)
	th := boardLifecycleThread(t, created)
	id := th.Comments[0].ID
	r := pagesGrantRequest(t, "PATCH", "/", ws, "author-commenter", "OWNER", "")
	r.SetPathValue("slug", "report")
	r.SetPathValue("threadId", th.ID)
	r.SetPathValue("commentId", id)
	r.Body = &artifactRevokingBody{reader: strings.NewReader(`{"base_version":1,"mutation_id":"revoked","body":"must not save"}`), revoke: func() {
		execOrFatal(t, h.db, `DELETE FROM page_grants WHERE page_id=? AND subject_id='author-commenter'`, page)
	}}
	w := httptest.NewRecorder()
	h.EditBoardComment(w, r)
	artifactExpect(t, w, 404)
	var body string
	var n int
	if err := h.db.QueryRow(`SELECT body FROM page_board_comments WHERE id=?`, id).Scan(&body); err != nil || body != "original" {
		t.Fatalf("revokedwrite %q %v", body, err)
	}
	if err := h.db.QueryRow(`SELECT COUNT(*) FROM page_board_comment_history WHERE comment_id=?`, id).Scan(&n); err != nil || n != 1 {
		t.Fatalf("revokedhistory %d %v", n, err)
	}
	r = pagesGrantRequest(t, "DELETE", "/", ws, owner, "OWNER", `{"base_version":1,"mutation_id":"agent-denied"}`)
	r.SetPathValue("slug", "report")
	r.SetPathValue("threadId", th.ID)
	r.SetPathValue("commentId", id)
	r = r.WithContext(context.WithValue(r.Context(), artifactRunKey{}, &artifactRunAuthority{AgentID: "claimed-agent"}))
	w = httptest.NewRecorder()
	h.DeleteBoardComment(w, r)
	artifactExpect(t, w, 401)
}

func TestBoardCommentChangesConcurrentCASAndBoundedHistory(t *testing.T) {
	h, _, ws, user, _ := boardCommentLifecycleFixture(t)
	created := boardCommentLifecycleCreate(t, h, ws, user, "original", "create", 0)
	artifactExpect(t, created, 201)
	th := boardLifecycleThread(t, created)
	id := th.Comments[0].ID
	results := make(chan int, 2)
	for i := 0; i < 2; i++ {
		go func(i int) {
			w := boardCommentChangeCall(t, h, ws, user, "edit", th.ID, id, "", map[string]any{"base_version": 1, "body": fmt.Sprintf("edit%d", i), "mutation_id": fmt.Sprintf("race%d", i)})
			results <- w.Code
		}(i)
	}
	a, b := <-results, <-results
	if !((a == 200 && b == 409) || (a == 409 && b == 200)) {
		t.Fatalf("CAS %d/%d", a, b)
	}
	// Seed the remaining body audit capacity; one final tombstone remains possible.
	execOrFatal(t, h.db, `WITH RECURSIVE n(v) AS (SELECT 3 UNION ALL SELECT v+1 FROM n WHERE v<49) INSERT INTO page_board_comment_history(id,page_id,thread_id,comment_id,version,body,action,actor_user_id,created_at) SELECT 'limit-'||v,t.page_id,t.id,?,v,'old','edit',?,'2000-01-01T00:00:00Z' FROM n,page_board_threads t WHERE t.id=?`, id, user, th.ID)
	execOrFatal(t, h.db, `UPDATE page_board_comments SET version=49 WHERE id=?`, id)
	execOrFatal(t, h.db, `UPDATE page_board_threads SET version=49 WHERE id=?`, th.ID)
	artifactExpect(t, boardCommentChangeCall(t, h, ws, user, "edit", th.ID, id, "", map[string]any{"base_version": 49, "body": "over limit", "mutation_id": "limit-edit"}), 409)
	artifactExpect(t, boardCommentChangeCall(t, h, ws, user, "delete", th.ID, id, "", map[string]any{"base_version": 49, "mutation_id": "final-delete"}), 200)
	var count int
	if err := h.db.QueryRow(`SELECT COUNT(*) FROM page_board_comment_history WHERE comment_id=?`, id).Scan(&count); err != nil || count != 50 {
		t.Fatalf("history %d %v", count, err)
	}
}

func TestBoardCommentChangesRejectAmbiguousJSON(t *testing.T) {
	for _, raw := range []string{`null`, `{"base_version":1,"base_version":2}`, `{"Base_Version":1}`, `{"body":null}`, `{"mutation_id":"x"} {}`} {
		if checkBoardCommentChangeJSON([]byte(raw)) == nil {
			t.Errorf("accepted %s", raw)
		}
	}
}

func TestBoardCommentChangesReceiptBudgetAndLiveRetry(t *testing.T) {
	h, _, ws, user, page := boardCommentLifecycleFixture(t)
	created := boardCommentLifecycleCreate(t, h, ws, user, "original", "create", 0)
	artifactExpect(t, created, 201)
	th := boardLifecycleThread(t, created)
	id := th.Comments[0].ID
	edit := map[string]any{"base_version": 1, "body": "saved", "mutation_id": "saved-edit"}
	artifactExpect(t, boardCommentChangeCall(t, h, ws, user, "edit", th.ID, id, "", edit), 200)
	execOrFatal(t, h.db, `WITH RECURSIVE n(v) AS(SELECT 1 UNION ALL SELECT v+1 FROM n WHERE v<9998) INSERT INTO page_board_comment_mutations(id,page_id,thread_id,comment_id,mutation_id,request_digest,actor_user_id,operation,result_version,created_at) SELECT 'budget-'||v,?,?,?,'budget-'||v,'digest',?,'react',2,? FROM n`, page, th.ID, id, user, tsformat.Format(h.clock.Now().Add(-2*time.Hour)))
	artifactExpect(t, boardCommentChangeCall(t, h, ws, user, "edit", th.ID, id, "", edit), 200)
	artifactExpect(t, boardCommentChangeCall(t, h, ws, user, "edit", th.ID, id, "", map[string]any{"base_version": 2, "body": "over", "mutation_id": "over"}), 409)
	execOrFatal(t, h.db, `UPDATE users SET suspended_at='suspended' WHERE id=?`, user)
	artifactExpect(t, boardCommentChangeCall(t, h, ws, user, "edit", th.ID, id, "", edit), 403)
	artifactExpect(t, boardCommentChangeCall(t, h, ws, user, "history", th.ID, id, "", nil), 403)
}
