package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/chatentities"
)

type deletionFixture struct {
	h                            *IssueHandler
	router                       *Router
	user, workspace, crew, issue string
}

func setupDeletionFixture(t *testing.T) *deletionFixture {
	t.Helper()
	h, user, ws, crew, lead, _ := newTestIssueHandler(t)
	issue := seedIssue(t, h.db, ws, crew, lead, "DEL-1", "BACKLOG")
	for _, chat := range []string{"delete-chat-one", "delete-chat-two"} {
		execOrFatal(t, h.db, `INSERT INTO chats(id,workspace_id,agent_id,created_by,visibility) VALUES(?,?,?,?,'private')`, chat, ws, lead, user)
	}
	tx, e := h.db.BeginTx(context.Background(), nil)
	if e != nil {
		t.Fatal(e)
	}
	defer tx.Rollback()
	s := chatentities.OperationSpec{ID: "delete-origin", WorkspaceID: ws, Key: "origin", RequestHash: "hash", ActorType: "agent", ActorID: lead, RunID: "original-run", Provenance: "verified", Kind: "issue", Verb: "created"}
	o, _, e := chatentities.BeginOperation(context.Background(), tx, s)
	if e != nil {
		t.Fatal(e)
	}
	if e = chatentities.CompleteOperation(context.Background(), tx, o.ID, chatentities.ResultSpec{EntityID: issue, Revision: 1, AggregateRevision: 1, Result: json.RawMessage(`{"private":"MUST_NOT_LEAK"}`)}); e != nil {
		t.Fatal(e)
	}
	for _, chat := range []string{"delete-chat-one", "delete-chat-two"} {
		if _, e = chatentities.AppendRef(context.Background(), tx, chatentities.RefSpec{EventID: "origin-" + chat, ChatID: chat, OperationID: o.ID, RunID: "original-run", TriggerMessageID: "trigger-" + chat}); e != nil {
			t.Fatal(e)
		}
	}
	if e = tx.Commit(); e != nil {
		t.Fatal(e)
	}
	return &deletionFixture{h: h, router: &Router{db: h.db, logger: newTestLogger()}, user: user, workspace: ws, crew: crew, issue: issue}
}
func (f *deletionFixture) request(method, path string) *http.Request {
	req := httptest.NewRequest(method, path, nil)
	return req.WithContext(withWorkspace(withUser(req.Context(), &AuthUser{ID: f.user}), f.workspace, "OWNER"))
}
func (f *deletionFixture) delete() *httptest.ResponseRecorder {
	req := f.request("DELETE", "/")
	req.SetPathValue("crewId", f.crew)
	req.SetPathValue("identifier", "DEL-1")
	rr := httptest.NewRecorder()
	f.h.Delete(rr, req)
	return rr
}
func (f *deletionFixture) refs(chat string) *httptest.ResponseRecorder {
	req := f.request("GET", "/api/v1/chats/"+chat+"/entity-refs")
	req.SetPathValue("chatId", chat)
	rr := httptest.NewRecorder()
	f.router.chatEntityRefs(rr, req)
	return rr
}
func (f *deletionFixture) operation(id string) *httptest.ResponseRecorder {
	req := f.request("GET", "/api/v1/operations/"+id)
	req.SetPathValue("operationId", id)
	rr := httptest.NewRecorder()
	f.router.chatEntityOperation(rr, req)
	return rr
}
func TestChatEntityDeletionHumanReceiptAcrossChats(t *testing.T) {
	f := setupDeletionFixture(t)
	if rr := f.delete(); rr.Code != 204 {
		t.Fatalf("delete %d %s", rr.Code, rr.Body.String())
	}
	var operationID string
	if e := f.h.db.QueryRow(`SELECT id FROM agent_operations WHERE verb='deleted' AND entity_id=?`, f.issue).Scan(&operationID); e != nil {
		t.Fatal(e)
	}
	for _, chat := range []string{"delete-chat-one", "delete-chat-two"} {
		rr := f.refs(chat)
		if rr.Code != 200 {
			t.Fatalf("refs %d %s", rr.Code, rr.Body.String())
		}
		var page EntityRefPage
		if e := json.Unmarshal(rr.Body.Bytes(), &page); e != nil {
			t.Fatal(e)
		}
		if len(page.Refs) != 1 || page.HasMore || !page.SnapshotComplete {
			t.Fatalf("wrong page %+v", page)
		}
		r := page.Refs[0]
		if r.Verb != "deleted" || !r.Entity.Deleted || r.Entity.Identifier != "" || r.Entity.ID != f.issue {
			t.Fatalf("wrong tombstone %+v", r)
		}
		if r.Provenance.Actor.Type != "human" || r.Provenance.Actor.ID != f.user {
			t.Fatalf("wrong deleter %+v", r.Provenance)
		}
		if r.Anchor.RunID != "original-run" || r.Anchor.TriggerMessageID != "trigger-"+chat {
			t.Fatalf("wrong origin %+v", r.Anchor)
		}
		if strings.Contains(rr.Body.String(), "MUST_NOT_LEAK") || strings.Contains(rr.Body.String(), "access_snapshot") {
			t.Fatal("private metadata leaked")
		}
	}
	if rr := f.operation(operationID); rr.Code != 200 || !strings.Contains(rr.Body.String(), `"deleted":true`) {
		t.Fatalf("operation %d %s", rr.Code, rr.Body.String())
	}
	if rr := f.operation("delete-origin"); rr.Code != 404 {
		t.Fatalf("old result %d", rr.Code)
	}
	execOrFatal(t, f.h.db, `DELETE FROM workspace_members WHERE workspace_id=? AND user_id=?`, f.workspace, f.user)
	if rr := f.refs("delete-chat-one"); rr.Code != 404 {
		t.Fatalf("access loss %d", rr.Code)
	}
	if rr := f.operation(operationID); rr.Code != 404 {
		t.Fatalf("operation access loss %d", rr.Code)
	}
}
func TestChatEntityDeletionReceiptFailureRollsBackIssue(t *testing.T) {
	f := setupDeletionFixture(t)
	execOrFatal(t, f.h.db, `CREATE TRIGGER reject_delete_ref BEFORE INSERT ON chat_entity_refs WHEN EXISTS(SELECT 1 FROM agent_operations WHERE id=NEW.operation_id AND verb='deleted') BEGIN SELECT RAISE(ABORT,'synthetic receipt failure');END`)
	if rr := f.delete(); rr.Code != 500 {
		t.Fatalf("delete %d %s", rr.Code, rr.Body.String())
	}
	var issues, operations, refs int
	f.h.db.QueryRow(`SELECT COUNT(*) FROM missions WHERE id=?`, f.issue).Scan(&issues)
	f.h.db.QueryRow(`SELECT COUNT(*) FROM agent_operations WHERE verb='deleted'`).Scan(&operations)
	f.h.db.QueryRow(`SELECT COUNT(*) FROM chat_entity_refs`).Scan(&refs)
	if issues != 1 || operations != 0 || refs != 2 {
		t.Fatalf("non-atomic delete issues=%d ops=%d refs=%d", issues, operations, refs)
	}
	rr := f.refs("delete-chat-one")
	if rr.Code != 200 || strings.Contains(rr.Body.String(), `"deleted":true`) {
		t.Fatalf("rollback view %d %s", rr.Code, rr.Body.String())
	}
}
func TestChatEntityDeletionUnknownAccessSnapshotFailsClosed(t *testing.T) {
	f := setupDeletionFixture(t)
	if rr := f.delete(); rr.Code != 204 {
		t.Fatalf("delete %d %s", rr.Code, rr.Body.String())
	}
	var id string
	if e := f.h.db.QueryRow(`SELECT id FROM agent_operations WHERE verb='deleted'`).Scan(&id); e != nil {
		t.Fatal(e)
	}
	snapshots := []string{`{}`, `{"version":2,"workspace_id":"` + f.workspace + `","visibility":"trusted_workspace"}`, `{"version":1,"workspace_id":"foreign","visibility":"trusted_workspace"}`, `{"version":1,"workspace_id":"` + f.workspace + `","visibility":"trusted_workspace","project_id":"private"}`, `{"version":1,"workspace_id":"` + f.workspace + `","visibility":"trusted_workspace","visibility":"trusted_workspace"}`}
	for _, snapshot := range snapshots {
		execOrFatal(t, f.h.db, `UPDATE agent_operations SET access_snapshot=? WHERE id=?`, snapshot, id)
		rr := f.refs("delete-chat-one")
		if rr.Code != 200 {
			t.Fatalf("list %d %s", rr.Code, rr.Body.String())
		}
		var page EntityRefPage
		if e := json.Unmarshal(rr.Body.Bytes(), &page); e != nil {
			t.Fatal(e)
		}
		if len(page.Refs) != 0 || page.HasMore {
			t.Fatalf("unsafe snapshot %s %+v", snapshot, page)
		}
		if rr := f.operation(id); rr.Code != 404 {
			t.Fatalf("unsafe operation snapshot %s: %d", snapshot, rr.Code)
		}
	}
}
