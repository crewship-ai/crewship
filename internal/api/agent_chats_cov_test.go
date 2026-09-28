package api

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// agent_chats_cov_test.go covers remaining AgentHandler chat branches:
// ListChats DB/scan errors, CreateChat origin whitelisting + race
// fallbacks, and ListRuns error + optional-field enrichment.
// Helpers prefixed covACH.

func covACHSeed(t *testing.T, db *sql.DB) (wsID string) {
	t.Helper()
	userID := seedTestUser(t, db)
	wsID = seedTestWorkspace(t, db, userID)
	execOrFatal(t, db, `INSERT INTO crews (id, workspace_id, name, slug) VALUES ('covach-crew', ?, 'C', 'covach-c')`, wsID)
	seedAgentRow(t, db, "covach-ag", wsID, "covach-crew", "A", "covach-a", "AGENT")
	return wsID
}

func TestCovACH_ListChats_DBError(t *testing.T) {
	db := setupTestDB(t)
	h := NewAgentHandler(db, newTestLogger())
	db.Close()
	req := httptest.NewRequest("GET", "/api/v1/agents/a/chats", nil)
	req.SetPathValue("agentId", "a")
	req = req.WithContext(withWorkspace(req.Context(), "ws", "OWNER"))
	rr := httptest.NewRecorder()
	h.ListChats(rr, req)
	if rr.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", rr.Code)
	}
}

func TestCovACH_CreateChat_OriginWhitelistedAndPersisted(t *testing.T) {
	db := setupTestDB(t)
	wsID := covACHSeed(t, db)
	h := NewAgentHandler(db, newTestLogger())

	req := httptest.NewRequest("POST", "/api/v1/agents/covach-ag/chats",
		bytes.NewBufferString(`{"origin":"CLI"}`))
	req.SetPathValue("agentId", "covach-ag")
	ctx := withUser(req.Context(), &AuthUser{ID: "test-user-id"})
	ctx = withWorkspace(ctx, wsID, "OWNER")
	req = req.WithContext(ctx)
	rr := httptest.NewRecorder()
	h.CreateChat(rr, req)
	if rr.Code != http.StatusCreated {
		t.Fatalf("status = %d, body=%s", rr.Code, rr.Body.String())
	}
	var resp map[string]string
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	var origin sql.NullString
	if err := db.QueryRow(`SELECT origin FROM chats WHERE id = ?`, resp["id"]).Scan(&origin); err != nil {
		t.Fatalf("read chat: %v", err)
	}
	if origin.String != "CLI" {
		t.Errorf("origin = %q, want CLI", origin.String)
	}
}

func TestCreateChat_SessionIDCannotJoinAnotherCreatorsChat(t *testing.T) {
	db := setupTestDB(t)
	wsID := covACHSeed(t, db)
	otherID := "other-chat-user"
	execOrFatal(t, db, `INSERT INTO users (id, email, full_name) VALUES (?, 'other-chat@example.com', 'Other User')`, otherID)
	execOrFatal(t, db, `INSERT INTO workspace_members (id, workspace_id, user_id, role) VALUES ('m-other-chat', ?, ?, 'MEMBER')`, wsID, otherID)
	h := NewAgentHandler(db, newTestLogger())

	create := func(userID, chatID string) *httptest.ResponseRecorder {
		t.Helper()
		req := httptest.NewRequest("POST", "/api/v1/agents/covach-ag/chats",
			bytes.NewBufferString(`{"session_id":"`+chatID+`"}`))
		req.SetPathValue("agentId", "covach-ag")
		ctx := withUser(req.Context(), &AuthUser{ID: userID})
		req = req.WithContext(withWorkspace(ctx, wsID, "MEMBER"))
		rr := httptest.NewRecorder()
		h.CreateChat(rr, req)
		return rr
	}

	for _, tc := range []struct {
		name, userID string
		want         int
	}{
		{"owner creates", "test-user-id", http.StatusCreated},
		{"owner retries", "test-user-id", http.StatusCreated},
		{"other member cannot join", otherID, http.StatusNotFound},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rr := create(tc.userID, "private-chat")
			if rr.Code != tc.want {
				t.Fatalf("status = %d, want %d; body=%s", rr.Code, tc.want, rr.Body.String())
			}
		})
	}
	var owner string
	if err := db.QueryRow(`SELECT created_by FROM chats WHERE id = 'private-chat'`).Scan(&owner); err != nil {
		t.Fatal(err)
	}
	if owner != "test-user-id" {
		t.Fatalf("owner changed to %q", owner)
	}
}

func TestCovACH_CreateChat_AgentExistsDBError(t *testing.T) {
	db := setupTestDB(t)
	h := NewAgentHandler(db, newTestLogger())
	db.Close()
	req := httptest.NewRequest("POST", "/api/v1/agents/x/chats", bytes.NewBufferString(`{}`))
	req.SetPathValue("agentId", "x")
	ctx := withUser(req.Context(), &AuthUser{ID: "u"})
	ctx = withWorkspace(ctx, "ws", "OWNER")
	req = req.WithContext(ctx)
	rr := httptest.NewRecorder()
	h.CreateChat(rr, req)
	if rr.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", rr.Code)
	}
}

func TestCovACH_CreateChat_InsertDBError(t *testing.T) {
	db := setupTestDB(t)
	wsID := covACHSeed(t, db)
	execOrFatal(t, db, `CREATE TRIGGER covach_fail_chat BEFORE INSERT ON chats BEGIN SELECT RAISE(ABORT, 'covach boom'); END`)
	h := NewAgentHandler(db, newTestLogger())
	req := httptest.NewRequest("POST", "/api/v1/agents/covach-ag/chats", bytes.NewBufferString(`{}`))
	req.SetPathValue("agentId", "covach-ag")
	ctx := withUser(req.Context(), &AuthUser{ID: "test-user-id"})
	ctx = withWorkspace(ctx, wsID, "OWNER")
	req = req.WithContext(ctx)
	rr := httptest.NewRecorder()
	h.CreateChat(rr, req)
	if rr.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500; body=%s", rr.Code, rr.Body.String())
	}
}

func TestCovACH_CreateChat_RowVanishesAfterInsert(t *testing.T) {
	db := setupTestDB(t)
	wsID := covACHSeed(t, db)
	// Simulate the "agent deleted between preflight and INSERT" race:
	// the AFTER INSERT trigger removes the new row, so the verify
	// SELECT sees ErrNoRows -> 404.
	execOrFatal(t, db, `CREATE TRIGGER covach_eat_chat AFTER INSERT ON chats BEGIN DELETE FROM chats WHERE id = NEW.id; END`)
	h := NewAgentHandler(db, newTestLogger())
	req := httptest.NewRequest("POST", "/api/v1/agents/covach-ag/chats", bytes.NewBufferString(`{}`))
	req.SetPathValue("agentId", "covach-ag")
	ctx := withUser(req.Context(), &AuthUser{ID: "test-user-id"})
	ctx = withWorkspace(ctx, wsID, "OWNER")
	req = req.WithContext(ctx)
	rr := httptest.NewRecorder()
	h.CreateChat(rr, req)
	if rr.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404; body=%s", rr.Code, rr.Body.String())
	}
}

func TestCovACH_ListRuns_DBError(t *testing.T) {
	db := setupTestDB(t)
	h := NewAgentHandler(db, newTestLogger())
	db.Close()
	req := httptest.NewRequest("GET", "/api/v1/agents/a/runs", nil)
	req.SetPathValue("agentId", "a")
	req = req.WithContext(withWorkspace(req.Context(), "ws", "OWNER"))
	rr := httptest.NewRecorder()
	h.ListRuns(rr, req)
	if rr.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", rr.Code)
	}
}

func TestCovACH_ListRuns_ChatIDAndTriggeredBySurfaced(t *testing.T) {
	db := setupTestDB(t)
	wsID := covACHSeed(t, db)
	// A run.started entry carrying chat_id in the payload and an
	// actor_id (-> triggered_by).
	execOrFatal(t, db, `INSERT INTO journal_entries
		(id, workspace_id, agent_id, ts, entry_type, severity, actor_type, actor_id,
		 summary, payload, refs, trace_id, span_id, expires_at, priority)
		VALUES ('covach-je1', ?, 'covach-ag', strftime('%Y-%m-%dT%H:%M:%fZ','now'), 'run.started', 'info', 'user', 'user-9',
		        'run started', '{"trigger_type":"USER","chat_id":"covach-chat-1"}', '{}', 'covach-run-1', NULL, NULL, 'normal')`,
		wsID)

	h := NewAgentHandler(db, newTestLogger())
	req := httptest.NewRequest("GET", "/api/v1/agents/covach-ag/runs", nil)
	req.SetPathValue("agentId", "covach-ag")
	req = req.WithContext(withWorkspace(req.Context(), wsID, "OWNER"))
	rr := httptest.NewRecorder()
	h.ListRuns(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%s", rr.Code, rr.Body.String())
	}
	var runs []runResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &runs); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(runs) != 1 {
		t.Fatalf("runs = %d, want 1; body=%s", len(runs), rr.Body.String())
	}
	if runs[0].ChatID == nil || *runs[0].ChatID != "covach-chat-1" {
		t.Errorf("chat_id = %v, want covach-chat-1", runs[0].ChatID)
	}
	if runs[0].TriggeredBy == nil || *runs[0].TriggeredBy != "user-9" {
		t.Errorf("triggered_by = %v, want user-9", runs[0].TriggeredBy)
	}
}
