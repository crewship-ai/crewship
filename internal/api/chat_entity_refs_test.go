package api

import (
	"encoding/json"
	"fmt"
	"github.com/crewship-ai/crewship/internal/auth"
	"github.com/crewship-ai/crewship/internal/auth/sessions"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Fixtures come from the production handler, not separately maintained examples.
// Set UPDATE_CHAT_ENTITY_FIXTURES=1 to refresh deterministic response projections.
func TestChatEntityRefsPagingAuthorityAndFixtures(t *testing.T) {
	db := setupTestDB(t)
	user := seedTestUser(t, db)
	workspace := seedTestWorkspace(t, db, user)
	execOrFatal(t, db, `INSERT INTO crews(id,workspace_id,name,slug) VALUES('entity-crew',?,'Entity','entity')`, workspace)
	seedAgentRow(t, db, "entity-agent", workspace, "entity-crew", "Jamie", "jamie", "AGENT")
	execOrFatal(t, db, `INSERT INTO chats(id,workspace_id,agent_id,created_by,visibility) VALUES('entity-chat',?,'entity-agent',?,'private')`, workspace, user)
	for i := 1; i <= 3; i++ {
		id := fmt.Sprintf("entity-%d", i)
		op := fmt.Sprintf("op-%d", i)
		execOrFatal(t, db, `INSERT INTO missions(id,workspace_id,crew_id,lead_agent_id,trace_id,title,identifier) VALUES(?,?,'entity-crew','entity-agent',?,?,?)`, id, workspace, id, "Synthetic title", fmt.Sprintf("ENG-%d", i))
		execOrFatal(t, db, `INSERT INTO agent_operations(id,workspace_id,operation_key,request_hash,actor_type,actor_id,run_id,provenance,kind,verb,entity_id,entity_revision,entity_aggregate_revision,status,result,created_at) VALUES(?,?,?,'hash','agent','entity-agent','entity-run','verified','issue','created',?,1,1,'completed','{"secret":"NEVER_LEAK"}','2026-10-13T09:00:00Z')`, op, workspace, op, id)
		execOrFatal(t, db, `INSERT INTO chat_entity_refs(event_id,chat_id,operation_id,run_id,trigger_message_id,created_at) VALUES(?,'entity-chat',?,'entity-run','trigger-message','2026-10-13T09:00:00Z')`, fmt.Sprintf("event-%d", i), op)
	}
	router := &Router{db: db, logger: newTestLogger()}
	request := func(chat, cursor string, limit int) *httptest.ResponseRecorder {
		req := httptest.NewRequest("GET", fmt.Sprintf("/api/v1/chats/%s/entity-refs?limit=%d", chat, limit), nil)
		if cursor != "" {
			q := req.URL.Query()
			q.Set("cursor", cursor)
			req.URL.RawQuery = q.Encode()
		}
		req.SetPathValue("chatId", chat)
		req = req.WithContext(withWorkspace(withUser(req.Context(), &AuthUser{ID: user}), workspace, "OWNER"))
		rec := httptest.NewRecorder()
		router.chatEntityRefs(rec, req)
		return rec
	}
	decode := func(rec *httptest.ResponseRecorder) EntityRefPage {
		t.Helper()
		if rec.Code != 200 {
			t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
		}
		var page EntityRefPage
		if err := json.Unmarshal(rec.Body.Bytes(), &page); err != nil {
			t.Fatal(err)
		}
		if strings.Contains(rec.Body.String(), "NEVER_LEAK") || strings.Contains(rec.Body.String(), "chat_seq") || strings.Contains(rec.Body.String(), "human_authority") {
			t.Fatal("internal data disclosed")
		}
		if rec.Header().Get("Cache-Control") != "no-store" {
			t.Fatal("cacheable")
		}
		return page
	}
	first := decode(request("entity-chat", "", 2))
	if !first.Reset || !first.HasMore || first.SnapshotComplete || len(first.Refs) != 2 {
		t.Fatalf("first %+v", first)
	}
	second := decode(request("entity-chat", first.NextCursor, 2))
	if second.Reset || second.HasMore || !second.SnapshotComplete || len(second.Refs) != 1 || second.SnapshotID != first.SnapshotID {
		t.Fatalf("second %+v", second)
	}
	assertEntityHandlerFixture(t, "continuation.json", second)
	poll := decode(request("entity-chat", second.NextCursor, 2))
	if !poll.Reset || poll.SnapshotID == second.SnapshotID {
		t.Fatal("poll must replace bounded snapshot")
	}
	assertEntityHandlerFixture(t, "cursor-invalid.json", request("entity-chat", "invalid", 2).Body.Bytes())
	execOrFatal(t, db, `INSERT INTO chats(id,workspace_id,agent_id,created_by,visibility) VALUES('entity-other-chat',?,'entity-agent',?,'private')`, workspace, user)
	if rec := request("entity-other-chat", first.NextCursor, 2); rec.Code != 400 {
		t.Fatalf("cross chat cursor %d", rec.Code)
	}
	expired, err := router.decodeEntityCursor(first.NextCursor)
	if err != nil {
		t.Fatal(err)
	}
	expired.Expires = 1
	expiredCursor, err := router.encodeEntityCursor(expired)
	if err != nil {
		t.Fatal(err)
	}
	if rec := request("entity-chat", expiredCursor, 2); rec.Code != 400 {
		t.Fatalf("expired cursor %d", rec.Code)
	}
	assertEntityHandlerFixture(t, "empty.json", decode(request("entity-other-chat", "", 2)))
	for name, cursor := range map[string]string{"tampered": first.NextCursor + "x", "invalid": "garbage"} {
		if rec := request("entity-chat", cursor, 2); rec.Code != 400 {
			t.Fatalf("%s %d", name, rec.Code)
		}
	}
	if rec := request("missing-chat", "", 2); rec.Code != 404 {
		t.Fatalf("foreign chat %d", rec.Code)
	}
	execOrFatal(t, db, `UPDATE workspace_members SET access_revision=access_revision+1 WHERE user_id=?`, user)
	changed := decode(request("entity-chat", first.NextCursor, 2))
	assertEntityHandlerFixture(t, "access-reset.json", changed)
	if !changed.Reset || changed.Refs[0].EventID != "event-1" {
		t.Fatal("epoch change must reset")
	}
	normalized := first
	normalized.NextCursor = "<opaque cursor>"
	normalized.SnapshotID = "<snapshot id>"
	data, _ := json.MarshalIndent(normalized, "", "  ")
	data = append(data, '\n')
	path := filepath.Join("testdata", "chat-entity-refs", "page.json")
	if os.Getenv("UPDATE_CHAT_ENTITY_FIXTURES") == "1" {
		if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, data, 0644); err != nil {
			t.Fatal(err)
		}
	}
	expected, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(expected) != string(data) {
		t.Fatalf("handler fixture mismatch: %s", data)
	}

	// Terminal status is derived from the durable journal even when no final
	// assistant message was saved.
	execOrFatal(t, db, `INSERT INTO journal_entries(id,workspace_id,agent_id,entry_type,actor_type,summary,trace_id,ts) VALUES('entity-run-failed',?,'entity-agent','run.failed','system','Synthetic failure','entity-run','2026-10-13T09:01:00Z')`, workspace)
	failed := decode(request("entity-chat", "", 2))
	assertEntityHandlerFixture(t, "failed-run.json", failed)
	if failed.Refs[0].Anchor.RunStatus == nil || *failed.Refs[0].Anchor.RunStatus != "failed" {
		t.Fatalf("missing durable terminal status: %+v", failed.Refs[0])
	}
	// Viewer-bound cursors cannot be used by another user, even a workspace
	// owner with independent permission to read the same private chat.
	execOrFatal(t, db, `INSERT INTO users(id,email) VALUES('entity-other','entity-other@example.test')`)
	execOrFatal(t, db, `INSERT INTO workspace_members(id,user_id,workspace_id,role) VALUES('entity-other-member','entity-other',?,'OWNER')`, workspace)
	originalUser := user
	user = "entity-other"
	if rec := request("entity-chat", first.NextCursor, 2); rec.Code != 400 {
		t.Fatalf("foreign cursor %d %s", rec.Code, rec.Body.String())
	}
	user = originalUser
	// A deleted object fails closed rather than revealing a tombstone without
	// an exact access snapshot evaluator.
	execOrFatal(t, db, `DELETE FROM missions WHERE id='entity-3'`)
	deleted := decode(request("entity-chat", "", 100))
	if len(deleted.Refs) != 2 || deleted.HasMore {
		t.Fatalf("deleted metadata disclosed: %+v", deleted)
	}
	execOrFatal(t, db, `UPDATE workspace_members SET role='MEMBER',access_mode='restricted' WHERE user_id=?`, user)
	if rec := request("entity-chat", "", 2); rec.Code != 404 {
		t.Fatalf("restricted %d", rec.Code)
	}
}

func TestChatEntityOperationHidesRawResultAndRequiresChatAudience(t *testing.T) {
	// Reuse the cursor test's guarantees through a real operation fixture would
	// obscure failures; this independent fixture covers the read boundary.
	db := setupTestDB(t)
	user := seedTestUser(t, db)
	workspace := seedTestWorkspace(t, db, user)
	execOrFatal(t, db, `INSERT INTO crews(id,workspace_id,name,slug) VALUES('op-crew',?,'Operation','operation')`, workspace)
	seedAgentRow(t, db, "op-agent", workspace, "op-crew", "Jamie", "jamie", "AGENT")
	execOrFatal(t, db, `INSERT INTO missions(id,workspace_id,crew_id,lead_agent_id,trace_id,title,identifier) VALUES('op-issue',?,'op-crew','op-agent','op-trace','Synthetic','ENG-1')`, workspace)
	execOrFatal(t, db, `INSERT INTO chats(id,workspace_id,agent_id,created_by,visibility) VALUES('op-chat',?,'op-agent',?,'private')`, workspace, user)
	execOrFatal(t, db, `INSERT INTO agent_operations(id,workspace_id,operation_key,request_hash,actor_type,actor_id,provenance,kind,verb,entity_id,status,result,created_at) VALUES('operation',?,'KEY_SECRET','HASH_SECRET','agent','op-agent','verified','issue','created','op-issue','completed','{"sensitive":"RESULT_SECRET"}','2026-10-13T09:00:00Z')`, workspace)
	execOrFatal(t, db, `INSERT INTO chat_entity_refs(event_id,chat_id,operation_id,run_id,trigger_message_id,created_at) VALUES('op-event','op-chat','operation','op-run','trigger','2026-10-13T09:00:00Z')`)
	router := &Router{db: db}
	request := func() *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodGet, "/api/v1/operations/operation", nil)
		req.SetPathValue("operationId", "operation")
		req = req.WithContext(withWorkspace(withUser(req.Context(), &AuthUser{ID: user}), workspace, "OWNER"))
		rec := httptest.NewRecorder()
		router.chatEntityOperation(rec, req)
		return rec
	}
	rec := request()
	if rec.Code != 200 {
		t.Fatalf("%d %s", rec.Code, rec.Body.String())
	}
	assertEntityHandlerFixture(t, "operation.json", rec.Body.Bytes())
	for _, secret := range []string{"KEY_SECRET", "HASH_SECRET", "RESULT_SECRET", "human_authority"} {
		if strings.Contains(rec.Body.String(), secret) {
			t.Fatalf("leaked %s", secret)
		}
	}
	execOrFatal(t, db, `DELETE FROM chat_entity_refs WHERE operation_id='operation'`)
	if rec = request(); rec.Code != 404 {
		t.Fatalf("no origin audience %d %s", rec.Code, rec.Body.String())
	}
}

func TestChatEntityRoutesAuthenticationWorkspaceAndRestrictedMode(t *testing.T) {
	db := setupTestDB(t)
	user := seedTestUser(t, db)
	workspace := seedTestWorkspace(t, db, user)
	const secret = "synthetic-chat-entity-route-session-secret"
	router, err := NewRouter(db, secret, newTestLogger(), WithInternalToken("synthetic-chat-entity-host-token"))
	if err != nil {
		t.Fatal(err)
	}
	validator, err := auth.NewJWTValidator(secret)
	if err != nil {
		t.Fatal(err)
	}
	session, err := sessions.NewDBStore(db).Create(t.Context(), user, "test", "127.0.0.1", auth.RefreshTokenTTL)
	if err != nil {
		t.Fatal(err)
	}
	token, err := validator.IssueAccessToken(user, session.ID, user, "synthetic@example.test")
	if err != nil {
		t.Fatal(err)
	}
	paths := []string{"/api/v1/chats/nonexistent/entity-refs", "/api/v1/operations/nonexistent", "/api/v1/issues/nonexistent/revision"}
	for _, path := range paths {
		request := func(authenticated, withWorkspace bool) *httptest.ResponseRecorder {
			req := httptest.NewRequest("GET", path, nil)
			if authenticated {
				req.Header.Set("Authorization", "Bearer "+token)
			}
			if withWorkspace {
				req.Header.Set("X-Workspace-Id", workspace)
			}
			rec := httptest.NewRecorder()
			router.ServeHTTP(rec, req)
			return rec
		}
		if rec := request(false, true); rec.Code != 401 {
			t.Fatalf("unauthenticated %s %d %s", path, rec.Code, rec.Body.String())
		}
		if rec := request(true, false); rec.Code != 400 {
			t.Fatalf("missing workspace %s %d %s", path, rec.Code, rec.Body.String())
		}
		if rec := request(true, true); rec.Code != 404 {
			t.Fatalf("missing resource %s %d %s", path, rec.Code, rec.Body.String())
		}
	}
	execOrFatal(t, db, `UPDATE workspace_members SET role='MEMBER',access_mode='restricted',access_revision=access_revision+1 WHERE user_id=?`, user)
	for _, path := range paths {
		req := httptest.NewRequest("GET", path+"?workspace_id="+workspace, nil)
		req.Header.Set("Authorization", "Bearer "+token)
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)
		if rec.Code != 404 {
			t.Fatalf("restricted %s %d %s", path, rec.Code, rec.Body.String())
		}
	}
}

func TestChatEntityRevisionUsesLiveWorkspaceAndOrdinaryViewer(t *testing.T) {
	db := setupTestDB(t)
	user := seedTestUser(t, db)
	workspace := seedTestWorkspace(t, db, user)
	execOrFatal(t, db, `INSERT INTO crews(id,workspace_id,name,slug) VALUES('revision-crew',?,'Revision','revision')`, workspace)
	seedAgentRow(t, db, "revision-agent", workspace, "revision-crew", "Jamie", "jamie", "AGENT")
	execOrFatal(t, db, `INSERT INTO missions(id,workspace_id,crew_id,lead_agent_id,trace_id,title,identifier) VALUES('revision-issue',?,'revision-crew','revision-agent','revision-trace','Synthetic','ENG-1')`, workspace)
	router := &Router{db: db}
	request := func(identifier, pathWorkspace string) *httptest.ResponseRecorder {
		req := httptest.NewRequest("GET", "/api/v1/issues/"+identifier+"/revision", nil)
		req.SetPathValue("identifier", identifier)
		req.SetPathValue("workspaceId", pathWorkspace)
		req = req.WithContext(withWorkspace(withUser(req.Context(), &AuthUser{ID: user}), workspace, "OWNER"))
		rec := httptest.NewRecorder()
		router.issueEntityRevision(rec, req)
		return rec
	}
	rec := request("ENG-1", workspace)
	if rec.Code != 200 {
		t.Fatalf("%d %s", rec.Code, rec.Body.String())
	}
	var first EntityRefRevision
	if err := json.Unmarshal(rec.Body.Bytes(), &first); err != nil {
		t.Fatal(err)
	}
	execOrFatal(t, db, `UPDATE missions SET title='Changed' WHERE id='revision-issue'`)
	rec = request("revision-issue", workspace)
	if rec.Code != 200 {
		t.Fatalf("id resolution %d %s", rec.Code, rec.Body.String())
	}
	var changed EntityRefRevision
	_ = json.Unmarshal(rec.Body.Bytes(), &changed)
	if changed.Revision != first.Revision+1 || changed.AggregateRevision <= first.AggregateRevision {
		t.Fatalf("revision stale: %+v -> %+v", first, changed)
	}
	if rec = request("ENG-1", "other-workspace"); rec.Code != 404 {
		t.Fatal("path workspace mismatch disclosed revision")
	}
	execOrFatal(t, db, `UPDATE workspace_members SET role='MEMBER',access_mode='restricted' WHERE user_id=?`, user)
	if rec = request("ENG-1", workspace); rec.Code != 404 {
		t.Fatal("restricted revision leaked")
	}
}

func assertEntityHandlerFixture(t *testing.T, name string, value any) {
	t.Helper()
	switch page := value.(type) {
	case EntityRefPage:
		page.NextCursor = "<opaque cursor>"
		page.SnapshotID = "<snapshot id>"
		value = page
	case []byte:
		var decoded any
		if err := json.Unmarshal(page, &decoded); err != nil {
			t.Fatal(err)
		}
		value = decoded
	}
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	data = append(data, '\n')
	path := filepath.Join("testdata", "chat-entity-refs", name)
	if os.Getenv("UPDATE_CHAT_ENTITY_FIXTURES") == "1" {
		if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, data, 0644); err != nil {
			t.Fatal(err)
		}
	}
	expected, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(expected) != string(data) {
		t.Fatalf("fixture %s does not match handler: %s", name, data)
	}
}

func TestChatEntityOperationFindsReadableChatAfterLargeFanout(t *testing.T) {
	f := setupDeletionFixture(t)
	execOrFatal(t, f.h.db, `INSERT INTO users(id,email) VALUES('fanout-reader','fanout-reader@example.test')`)
	execOrFatal(t, f.h.db, `INSERT INTO workspace_members(id,user_id,workspace_id,role) VALUES('fanout-reader-member','fanout-reader',?,'MEMBER')`, f.workspace)
	var agent string
	if e := f.h.db.QueryRow(`SELECT agent_id FROM chats WHERE id='delete-chat-one'`).Scan(&agent); e != nil {
		t.Fatal(e)
	}
	// Two existing private chats plus 100 new ones: only chat 102 belongs
	// to this MEMBER. The old raw LIMIT 101 omitted their only readable chat.
	for i := 0; i < 100; i++ {
		chat := fmt.Sprintf("fanout-%03d", i)
		creator := f.user
		if i == 99 {
			creator = "fanout-reader"
		}
		execOrFatal(t, f.h.db, `INSERT INTO chats(id,workspace_id,agent_id,created_by,visibility) VALUES(?,?,?,?,'private')`, chat, f.workspace, agent, creator)
		execOrFatal(t, f.h.db, `INSERT INTO chat_entity_refs(event_id,chat_id,operation_id,run_id,trigger_message_id,created_at) VALUES(?,?,'delete-origin','original-run','trigger','now')`, "fanout-event-"+chat, chat)
	}
	request := func() *httptest.ResponseRecorder {
		req := httptest.NewRequest("GET", "/api/v1/operations/delete-origin", nil)
		req.SetPathValue("operationId", "delete-origin")
		req = req.WithContext(withWorkspace(withUser(req.Context(), &AuthUser{ID: "fanout-reader"}), f.workspace, "MEMBER"))
		rr := httptest.NewRecorder()
		f.router.chatEntityOperation(rr, req)
		return rr
	}
	if rr := request(); rr.Code != 200 || strings.Contains(rr.Body.String(), "MUST_NOT_LEAK") {
		t.Fatalf("readable source missed: %d %s", rr.Code, rr.Body.String())
	}
	execOrFatal(t, f.h.db, `UPDATE chats SET created_by=? WHERE id='fanout-099'`, f.user)
	if rr := request(); rr.Code != 404 {
		t.Fatalf("unreadable sources revealed operation: %d %s", rr.Code, rr.Body.String())
	}
}
