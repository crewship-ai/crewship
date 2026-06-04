package api

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/journal"
)

// ===========================================================================
// query_handler.go — ListPeerConversations filters/pagination + finishQuery
// ===========================================================================

func TestCovQMMListPeerConversations_PaginationAndEscalated(t *testing.T) {
	db := setupTestDB(t)
	logger := newTestLogger()

	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)

	execOrFatal(t, db, `INSERT INTO crews (id, workspace_id, name, slug) VALUES ('cqmm-crew', ?, 'Eng', 'eng')`, wsID)
	execOrFatal(t, db, `INSERT INTO agents (id, crew_id, workspace_id, name, slug) VALUES ('cqmm-a1', 'cqmm-crew', ?, 'Viktor', 'viktor')`, wsID)
	execOrFatal(t, db, `INSERT INTO agents (id, crew_id, workspace_id, name, slug) VALUES ('cqmm-a2', 'cqmm-crew', ?, 'Nela', 'nela')`, wsID)

	// Two rows; one escalated, one with a finished_at + duration set.
	execOrFatal(t, db, `INSERT INTO peer_conversations
		(id, workspace_id, crew_id, chat_id, from_agent_id, to_agent_id, question, response, status, duration_ms, escalated, created_at, finished_at)
		VALUES ('cqmm-pc1', ?, 'cqmm-crew', 'chat-x', 'cqmm-a1', 'cqmm-a2', 'Q1', 'A1', 'COMPLETED', 1200, 1, '2025-01-01T10:00:00Z', '2025-01-01T10:00:02Z')`, wsID)
	execOrFatal(t, db, `INSERT INTO peer_conversations
		(id, workspace_id, crew_id, chat_id, from_agent_id, to_agent_id, question, status, escalated, created_at)
		VALUES ('cqmm-pc2', ?, 'cqmm-crew', 'chat-x', 'cqmm-a1', 'cqmm-a2', 'Q2', 'RUNNING', 0, '2025-01-01T09:00:00Z')`, wsID)

	h := NewQueryHandler(db, nil, nil, "tok", logger)

	// Pagination: limit=1 offset=0 → newest first (pc1).
	req := httptest.NewRequest(http.MethodGet, "/api/v1/crews/cqmm-crew/peer-conversations?limit=1&offset=0", nil)
	req.SetPathValue("crewId", "cqmm-crew")
	req = req.WithContext(context.WithValue(req.Context(), ctxWorkspaceID, wsID))
	w := httptest.NewRecorder()
	h.ListPeerConversations(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body: %s", w.Code, w.Body.String())
	}
	var page1 []map[string]interface{}
	if err := json.Unmarshal(w.Body.Bytes(), &page1); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(page1) != 1 {
		t.Fatalf("page1 len = %d, want 1", len(page1))
	}
	if page1[0]["id"] != "cqmm-pc1" {
		t.Errorf("page1[0].id = %v, want cqmm-pc1", page1[0]["id"])
	}
	if page1[0]["escalated"] != true {
		t.Errorf("escalated = %v, want true", page1[0]["escalated"])
	}

	// Pagination page 2 → pc2.
	req2 := httptest.NewRequest(http.MethodGet, "/api/v1/crews/cqmm-crew/peer-conversations?limit=1&offset=1", nil)
	req2.SetPathValue("crewId", "cqmm-crew")
	req2 = req2.WithContext(context.WithValue(req2.Context(), ctxWorkspaceID, wsID))
	w2 := httptest.NewRecorder()
	h.ListPeerConversations(w2, req2)
	if w2.Code != http.StatusOK {
		t.Fatalf("page2 status = %d", w2.Code)
	}
	var page2 []map[string]interface{}
	if err := json.Unmarshal(w2.Body.Bytes(), &page2); err != nil {
		t.Fatalf("unmarshal page2: %v", err)
	}
	if len(page2) != 1 || page2[0]["id"] != "cqmm-pc2" {
		t.Errorf("page2 = %v, want single cqmm-pc2", page2)
	}
}

func TestCovQMMListPeerConversations_DBError(t *testing.T) {
	db := setupTestDB(t)
	logger := newTestLogger()
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	execOrFatal(t, db, `INSERT INTO crews (id, workspace_id, name, slug) VALUES ('cqmm-crew2', ?, 'Eng', 'eng')`, wsID)

	h := NewQueryHandler(db, nil, nil, "tok", logger)
	db.Close() // force the QueryContext to fail.

	req := httptest.NewRequest(http.MethodGet, "/api/v1/crews/cqmm-crew2/peer-conversations", nil)
	req.SetPathValue("crewId", "cqmm-crew2")
	req = req.WithContext(context.WithValue(req.Context(), ctxWorkspaceID, wsID))
	w := httptest.NewRecorder()
	h.ListPeerConversations(w, req)
	if w.Code != http.StatusInternalServerError {
		t.Errorf("status = %d, want 500", w.Code)
	}
}

func TestCovQMMFinishQuery_Completed(t *testing.T) {
	db := setupTestDB(t)
	logger := newTestLogger()
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	execOrFatal(t, db, `INSERT INTO crews (id, workspace_id, name, slug) VALUES ('cqmm-fc', ?, 'Eng', 'eng')`, wsID)
	execOrFatal(t, db, `INSERT INTO agents (id, crew_id, workspace_id, name, slug) VALUES ('cqmm-tgt', 'cqmm-fc', ?, 'Tgt', 'tgt')`, wsID)
	// Seed the RUNNING conversation finishQuery will update.
	execOrFatal(t, db, `INSERT INTO peer_conversations
		(id, workspace_id, crew_id, chat_id, from_agent_id, to_agent_id, question, status, created_at)
		VALUES ('cqmm-conv-c', ?, 'cqmm-fc', 'chat-c', 'cqmm-tgt', 'cqmm-tgt', 'Q', 'RUNNING', '2025-01-01T00:00:00Z')`, wsID)

	em := &emitRecorder{}
	h := NewQueryHandler(db, nil, nil, "tok", logger)
	h.SetJournal(em)

	h.finishQuery(context.Background(), "cqmm-conv-c", "run-c", "chat-c",
		"alice", "bob", wsID, "cqmm-fc", "cqmm-tgt", "the answer", "", time.Now().Add(-50*time.Millisecond))

	// peer_conversations marked COMPLETED with response persisted.
	var status, resp string
	if err := db.QueryRow(`SELECT status, COALESCE(response,'') FROM peer_conversations WHERE id='cqmm-conv-c'`).Scan(&status, &resp); err != nil {
		t.Fatalf("query conv: %v", err)
	}
	if status != "COMPLETED" || resp != "the answer" {
		t.Errorf("conv = %q/%q, want COMPLETED/the answer", status, resp)
	}

	// Expect both the answer entry and a terminal run.completed entry.
	var sawAnswer, sawTerminal bool
	for _, e := range em.entries {
		if e.Type == journal.EntryPeerConversation {
			if e.Payload["message_type"] == "answer" && e.Payload["state"] == "completed" {
				sawAnswer = true
			}
		}
		if e.TraceID == "run-c" {
			if code, ok := e.Payload["exit_code"]; ok && code == 0 {
				sawTerminal = true
			}
		}
	}
	if !sawAnswer {
		t.Errorf("missing completed answer journal entry; got %+v", em.entries)
	}
	if !sawTerminal {
		t.Errorf("missing terminal run.completed entry with exit_code=0")
	}
}

func TestCovQMMFinishQuery_Failed(t *testing.T) {
	db := setupTestDB(t)
	logger := newTestLogger()
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	execOrFatal(t, db, `INSERT INTO crews (id, workspace_id, name, slug) VALUES ('cqmm-ff', ?, 'Eng', 'eng')`, wsID)
	execOrFatal(t, db, `INSERT INTO agents (id, crew_id, workspace_id, name, slug) VALUES ('cqmm-tgt2', 'cqmm-ff', ?, 'Tgt', 'tgt2')`, wsID)
	execOrFatal(t, db, `INSERT INTO peer_conversations
		(id, workspace_id, crew_id, chat_id, from_agent_id, to_agent_id, question, status, created_at)
		VALUES ('cqmm-conv-f', ?, 'cqmm-ff', 'chat-f', 'cqmm-tgt2', 'cqmm-tgt2', 'Q', 'RUNNING', '2025-01-01T00:00:00Z')`, wsID)

	em := &emitRecorder{}
	h := NewQueryHandler(db, nil, nil, "tok", logger)
	h.SetJournal(em)

	h.finishQuery(context.Background(), "cqmm-conv-f", "run-f", "chat-f",
		"alice", "bob", wsID, "cqmm-ff", "cqmm-tgt2", "", "boom: execution error", time.Now().Add(-20*time.Millisecond))

	var status string
	if err := db.QueryRow(`SELECT status FROM peer_conversations WHERE id='cqmm-conv-f'`).Scan(&status); err != nil {
		t.Fatalf("query conv: %v", err)
	}
	if status != "FAILED" {
		t.Errorf("status = %q, want FAILED", status)
	}

	var sawFailedAnswer, sawTerminalFailed bool
	for _, e := range em.entries {
		if e.Type == journal.EntryPeerConversation &&
			e.Payload["message_type"] == "answer" &&
			e.Payload["state"] == "failed" &&
			e.Severity == journal.SeverityError {
			sawFailedAnswer = true
		}
		if e.TraceID == "run-f" {
			if _, ok := e.Payload["error_message"]; ok && e.Severity == journal.SeverityError {
				sawTerminalFailed = true
			}
		}
	}
	if !sawFailedAnswer {
		t.Errorf("missing failed answer entry with error severity; got %+v", em.entries)
	}
	if !sawTerminalFailed {
		t.Errorf("missing terminal failed run entry with error_message")
	}
}

func TestCovQMMFinishQuery_NoRunNoWorkspace(t *testing.T) {
	// runID == "" skips the terminal run entry; workspaceID == "" skips
	// the workspace broadcast — both branches the happy-path tests don't hit.
	db := setupTestDB(t)
	logger := newTestLogger()
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	execOrFatal(t, db, `INSERT INTO crews (id, workspace_id, name, slug) VALUES ('cqmm-nr', ?, 'Eng', 'eng')`, wsID)
	execOrFatal(t, db, `INSERT INTO agents (id, crew_id, workspace_id, name, slug) VALUES ('cqmm-tgt3', 'cqmm-nr', ?, 'Tgt', 'tgt3')`, wsID)
	execOrFatal(t, db, `INSERT INTO peer_conversations
		(id, workspace_id, crew_id, chat_id, from_agent_id, to_agent_id, question, status, created_at)
		VALUES ('cqmm-conv-nr', ?, 'cqmm-nr', 'chat-nr', 'cqmm-tgt3', 'cqmm-tgt3', 'Q', 'RUNNING', '2025-01-01T00:00:00Z')`, wsID)

	em := &emitRecorder{}
	h := NewQueryHandler(db, nil, nil, "tok", logger)
	h.SetJournal(em)

	h.finishQuery(context.Background(), "cqmm-conv-nr", "", "chat-nr",
		"alice", "bob", "", "cqmm-nr", "cqmm-tgt3", "ok", "", time.Now())

	for _, e := range em.entries {
		if e.TraceID != "" {
			t.Errorf("expected no terminal run entry when runID empty, got trace %q", e.TraceID)
		}
	}
}

// ===========================================================================
// crew_messaging.go — SendMessage validation/happy + ReadFile/WriteFile dirs
// ===========================================================================

func TestCovQMMSendMessage_HappyWithMetadataAndOversize(t *testing.T) {
	db := setupTestDB(t)
	logger := newTestLogger()
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	tmpDir := t.TempDir()
	h := NewCrewMessagingHandler(db, tmpDir, logger)

	seedCrewRow(t, db, "cqmm-mf", wsID, "MF", "mf")
	seedCrewRow(t, db, "cqmm-mt", wsID, "MT", "mt")
	execOrFatal(t, db, `INSERT INTO crew_connections (id, workspace_id, from_crew_id, to_crew_id, direction, status)
		VALUES ('cqmm-cc', ?, 'cqmm-mf', 'cqmm-mt', 'bidirectional', 'active')`, wsID)

	// Happy path WITH metadata (exercises metadataStr + ptrRawJSON non-nil arm).
	body := bytes.NewBufferString(`{"from_crew_id":"cqmm-mf","to_crew_id":"cqmm-mt","workspace_id":"` + wsID + `","content":"hi","metadata":{"k":"v"}}`)
	req := httptest.NewRequest("POST", "/x", body)
	rr := httptest.NewRecorder()
	h.SendMessage(rr, req)
	if rr.Code != http.StatusCreated {
		t.Fatalf("send status = %d, body: %s", rr.Code, rr.Body.String())
	}
	var resp messageResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if resp.Metadata == nil {
		t.Errorf("expected metadata echoed back")
	}

	// Content over 1MB → 400.
	huge := strings.Repeat("a", (1<<20)+1)
	over := bytes.NewBufferString(`{"from_crew_id":"cqmm-mf","to_crew_id":"cqmm-mt","workspace_id":"` + wsID + `","content":"` + huge + `"}`)
	rrOver := httptest.NewRecorder()
	h.SendMessage(rrOver, httptest.NewRequest("POST", "/x", over))
	if rrOver.Code != http.StatusBadRequest {
		t.Errorf("oversize status = %d, want 400", rrOver.Code)
	}
}

func TestCovQMMSendMessage_InsertError(t *testing.T) {
	db := setupTestDB(t)
	logger := newTestLogger()
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	tmpDir := t.TempDir()
	h := NewCrewMessagingHandler(db, tmpDir, logger)

	seedCrewRow(t, db, "cqmm-ef", wsID, "EF", "ef")
	seedCrewRow(t, db, "cqmm-et", wsID, "ET", "et")
	execOrFatal(t, db, `INSERT INTO crew_connections (id, workspace_id, from_crew_id, to_crew_id, direction, status)
		VALUES ('cqmm-ecc', ?, 'cqmm-ef', 'cqmm-et', 'bidirectional', 'active')`, wsID)

	// Closing the DB makes resolveWorkspaceID return "" → 403, not the
	// INSERT-error 500 we want. Dropping crew_messages lets validation +
	// canCommunicate pass, then fails the INSERT specifically.
	execOrFatal(t, db, `DROP TABLE crew_messages`)

	body := bytes.NewBufferString(`{"from_crew_id":"cqmm-ef","to_crew_id":"cqmm-et","workspace_id":"` + wsID + `","content":"hi"}`)
	rr := httptest.NewRecorder()
	h.SendMessage(rr, httptest.NewRequest("POST", "/x", body))
	if rr.Code != http.StatusInternalServerError {
		t.Errorf("insert-error status = %d, want 500, body: %s", rr.Code, rr.Body.String())
	}
}

func TestCovQMMReadFile_DirWithEntriesAndTooLarge(t *testing.T) {
	db := setupTestDB(t)
	logger := newTestLogger()
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	tmpDir := t.TempDir()
	h := NewCrewMessagingHandler(db, tmpDir, logger)

	seedCrewRow(t, db, "cqmm-ra", wsID, "RA", "ra")
	seedCrewRow(t, db, "cqmm-rb", wsID, "RB", "rb")
	execOrFatal(t, db, `INSERT INTO crew_connections (id, workspace_id, from_crew_id, to_crew_id, direction, status)
		VALUES ('cqmm-rcc', ?, 'cqmm-ra', 'cqmm-rb', 'bidirectional', 'active')`, wsID)

	sharedDir := filepath.Join(tmpDir, "crews", "cqmm-rb", "shared")
	subDir := filepath.Join(sharedDir, "docs")
	if err := os.MkdirAll(subDir, 0755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	// Files inside the directory so the entries loop has rows to scan.
	if err := os.WriteFile(filepath.Join(subDir, "a.txt"), []byte("aa"), 0644); err != nil {
		t.Fatalf("write a: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(subDir, "nested"), 0755); err != nil {
		t.Fatalf("mkdir nested: %v", err)
	}

	// Directory listing with non-empty entries.
	rDir := httptest.NewRequest("GET",
		"/api/v1/internal/crew-files/cqmm-rb?path=docs&requester_crew_id=cqmm-ra", nil)
	rDir.SetPathValue("crewId", "cqmm-rb")
	wDir := httptest.NewRecorder()
	h.ReadFile(wDir, rDir)
	if wDir.Code != http.StatusOK {
		t.Fatalf("dir list status = %d, body: %s", wDir.Code, wDir.Body.String())
	}
	var dirResp struct {
		Entries []map[string]interface{} `json:"entries"`
	}
	if err := json.Unmarshal(wDir.Body.Bytes(), &dirResp); err != nil {
		t.Fatalf("unmarshal dir: %v", err)
	}
	if len(dirResp.Entries) != 2 {
		t.Errorf("entries = %d, want 2 (file + nested dir)", len(dirResp.Entries))
	}

	// File larger than 10MB → 400.
	big := make([]byte, (10<<20)+16)
	if err := os.WriteFile(filepath.Join(sharedDir, "big.bin"), big, 0644); err != nil {
		t.Fatalf("write big: %v", err)
	}
	rBig := httptest.NewRequest("GET",
		"/api/v1/internal/crew-files/cqmm-rb?path=big.bin&requester_crew_id=cqmm-ra", nil)
	rBig.SetPathValue("crewId", "cqmm-rb")
	wBig := httptest.NewRecorder()
	h.ReadFile(wBig, rBig)
	if wBig.Code != http.StatusBadRequest {
		t.Errorf("too-large status = %d, want 400", wBig.Code)
	}
}

func TestCovQMMWriteFile_Traversal(t *testing.T) {
	db := setupTestDB(t)
	logger := newTestLogger()
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	tmpDir := t.TempDir()
	h := NewCrewMessagingHandler(db, tmpDir, logger)

	seedCrewRow(t, db, "cqmm-wa", wsID, "WA", "wa")
	seedCrewRow(t, db, "cqmm-wb", wsID, "WB", "wb")
	execOrFatal(t, db, `INSERT INTO crew_connections (id, workspace_id, from_crew_id, to_crew_id, direction, status)
		VALUES ('cqmm-wcc', ?, 'cqmm-wa', 'cqmm-wb', 'bidirectional', 'active')`, wsID)
	if err := os.MkdirAll(filepath.Join(tmpDir, "crews", "cqmm-wb", "shared"), 0755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	// destPath is joined under incoming/<requester>/ then Clean'd. Enough
	// "../" to escape both segments leaves a residual ".." → "invalid path".
	var buf bytes.Buffer
	mw := multipartFormBody(t, &buf, "cqmm-wa", "../../../../etc/escape.txt", "data", true)
	req := httptest.NewRequest("POST", "/api/v1/internal/crew-files/cqmm-wb", &buf)
	req.SetPathValue("crewId", "cqmm-wb")
	req.Header.Set("Content-Type", mw.FormDataContentType())
	rr := httptest.NewRecorder()
	h.WriteFile(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Errorf("traversal write status = %d, want 400, body: %s", rr.Code, rr.Body.String())
	}
}

// ===========================================================================
// mission_handler.go — List/Get/Create deeper branches + Metrics aggregates
// ===========================================================================

func TestCovQMMMissionList_PaginationWithTaskStats(t *testing.T) {
	db := setupTestDB(t)
	logger := newTestLogger()
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	crewID := seedMissionCrew(t, db, wsID)
	leadID := seedMissionAgent(t, db, wsID, crewID, "cqmm-lead", "LEAD")

	execOrFatal(t, db, `INSERT INTO missions (id, workspace_id, crew_id, lead_agent_id, trace_id, title, status, created_at, updated_at)
		VALUES ('cqmm-m1', ?, ?, ?, 'mission-cqmm1', 'M1', 'IN_PROGRESS', datetime('now','-1 second'), datetime('now'))`, wsID, crewID, leadID)
	execOrFatal(t, db, `INSERT INTO missions (id, workspace_id, crew_id, lead_agent_id, trace_id, title, status, created_at, updated_at)
		VALUES ('cqmm-m2', ?, ?, ?, 'mission-cqmm2', 'M2', 'IN_PROGRESS', datetime('now'), datetime('now'))`, wsID, crewID, leadID)
	// Tasks so getBatchTaskStats has rows to aggregate.
	execOrFatal(t, db, `INSERT INTO mission_tasks (id, mission_id, title, status, task_order, created_at, updated_at)
		VALUES ('cqmm-t1', 'cqmm-m1', 'T1', 'COMPLETED', 0, datetime('now'), datetime('now'))`)
	execOrFatal(t, db, `INSERT INTO mission_tasks (id, mission_id, title, status, task_order, created_at, updated_at)
		VALUES ('cqmm-t2', 'cqmm-m1', 'T2', 'PENDING', 1, datetime('now'), datetime('now'))`)

	h := NewMissionHandler(db, nil, nil, logger)

	// limit=1 → only the newest mission returned, with task stats loaded.
	req := httptest.NewRequest("GET", "/api/v1/crews/"+crewID+"/missions?limit=1&offset=0", nil)
	req.SetPathValue("crewId", crewID)
	req = req.WithContext(withWorkspace(withUser(req.Context(), &AuthUser{ID: userID}), wsID, "MEMBER"))
	rr := httptest.NewRecorder()
	h.List(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("list status = %d, body: %s", rr.Code, rr.Body.String())
	}
	var result []map[string]interface{}
	if err := json.Unmarshal(rr.Body.Bytes(), &result); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(result) != 1 {
		t.Fatalf("len = %d, want 1 (paginated)", len(result))
	}

	// Second page returns m1 (older) with populated task_stats.
	req2 := httptest.NewRequest("GET", "/api/v1/crews/"+crewID+"/missions?limit=1&offset=1", nil)
	req2.SetPathValue("crewId", crewID)
	req2 = req2.WithContext(withWorkspace(withUser(req2.Context(), &AuthUser{ID: userID}), wsID, "MEMBER"))
	rr2 := httptest.NewRecorder()
	h.List(rr2, req2)
	if rr2.Code != http.StatusOK {
		t.Fatalf("list page2 status = %d", rr2.Code)
	}
	var page2 []map[string]interface{}
	if err := json.Unmarshal(rr2.Body.Bytes(), &page2); err != nil {
		t.Fatalf("unmarshal page2: %v", err)
	}
	if len(page2) != 1 || page2[0]["id"] != "cqmm-m1" {
		t.Fatalf("page2 = %v, want single cqmm-m1", page2)
	}
	if _, ok := page2[0]["task_stats"]; !ok {
		t.Errorf("expected task_stats present on mission with tasks")
	}
}

func TestCovQMMMissionList_DBError(t *testing.T) {
	db := setupTestDB(t)
	logger := newTestLogger()
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	crewID := seedMissionCrew(t, db, wsID)

	h := NewMissionHandler(db, nil, nil, logger)
	db.Close()

	req := httptest.NewRequest("GET", "/api/v1/crews/"+crewID+"/missions", nil)
	req.SetPathValue("crewId", crewID)
	req = req.WithContext(withWorkspace(withUser(req.Context(), &AuthUser{ID: userID}), wsID, "MEMBER"))
	rr := httptest.NewRecorder()
	h.List(rr, req)
	if rr.Code != http.StatusInternalServerError {
		t.Errorf("status = %d, want 500", rr.Code)
	}
}

func TestCovQMMMissionGet_WithTaskStats(t *testing.T) {
	db := setupTestDB(t)
	logger := newTestLogger()
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	crewID := seedMissionCrew(t, db, wsID)
	leadID := seedMissionAgent(t, db, wsID, crewID, "cqmm-glead", "LEAD")

	execOrFatal(t, db, `INSERT INTO missions (id, workspace_id, crew_id, lead_agent_id, trace_id, title, status, created_at, updated_at)
		VALUES ('cqmm-gm', ?, ?, ?, 'mission-cqmmg', 'GetMe', 'IN_PROGRESS', datetime('now'), datetime('now'))`, wsID, crewID, leadID)
	execOrFatal(t, db, `INSERT INTO mission_tasks (id, mission_id, title, status, task_order, created_at, updated_at)
		VALUES ('cqmm-gt1', 'cqmm-gm', 'GT1', 'COMPLETED', 0, datetime('now'), datetime('now'))`)
	execOrFatal(t, db, `INSERT INTO mission_tasks (id, mission_id, title, status, task_order, created_at, updated_at)
		VALUES ('cqmm-gt2', 'cqmm-gm', 'GT2', 'IN_PROGRESS', 1, datetime('now'), datetime('now'))`)

	h := NewMissionHandler(db, nil, nil, logger)

	req := httptest.NewRequest("GET", "/api/v1/crews/"+crewID+"/missions/cqmm-gm", nil)
	req.SetPathValue("crewId", crewID)
	req.SetPathValue("missionId", "cqmm-gm")
	req = req.WithContext(withWorkspace(withUser(req.Context(), &AuthUser{ID: userID}), wsID, "MEMBER"))
	rr := httptest.NewRecorder()
	h.Get(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("get status = %d, body: %s", rr.Code, rr.Body.String())
	}
	var result map[string]interface{}
	if err := json.Unmarshal(rr.Body.Bytes(), &result); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	tasks, ok := result["tasks"].([]interface{})
	if !ok || len(tasks) != 2 {
		t.Errorf("tasks = %v, want 2", result["tasks"])
	}
	if _, ok := result["task_stats"]; !ok {
		t.Errorf("expected task_stats present")
	}
}

func TestCovQMMMissionGet_DBError(t *testing.T) {
	db := setupTestDB(t)
	logger := newTestLogger()
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	crewID := seedMissionCrew(t, db, wsID)

	h := NewMissionHandler(db, nil, nil, logger)
	db.Close()

	req := httptest.NewRequest("GET", "/api/v1/crews/"+crewID+"/missions/whatever", nil)
	req.SetPathValue("crewId", crewID)
	req.SetPathValue("missionId", "whatever")
	req = req.WithContext(withWorkspace(withUser(req.Context(), &AuthUser{ID: userID}), wsID, "MEMBER"))
	rr := httptest.NewRecorder()
	h.Get(rr, req)
	if rr.Code != http.StatusInternalServerError {
		t.Errorf("status = %d, want 500", rr.Code)
	}
}

func TestCovQMMMissionCreate_InvalidJSONAndLeadNotFound(t *testing.T) {
	db := setupTestDB(t)
	logger := newTestLogger()
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	crewID := seedMissionCrew(t, db, wsID)

	h := NewMissionHandler(db, nil, nil, logger)

	mkReq := func(b string) (*httptest.ResponseRecorder, *http.Request) {
		req := httptest.NewRequest("POST", "/api/v1/crews/"+crewID+"/missions", bytes.NewBufferString(b))
		req.SetPathValue("crewId", crewID)
		req = req.WithContext(withWorkspace(withUser(req.Context(), &AuthUser{ID: userID}), wsID, "MANAGER"))
		return httptest.NewRecorder(), req
	}

	// Invalid JSON → 400.
	rr, req := mkReq(`{not json`)
	h.Create(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Errorf("invalid json status = %d, want 400", rr.Code)
	}

	// lead_agent_id present but no such agent in crew → 400 "not found in crew".
	rr2, req2 := mkReq(`{"title":"X","lead_agent_id":"ghost-agent"}`)
	h.Create(rr2, req2)
	if rr2.Code != http.StatusBadRequest {
		t.Errorf("lead-not-found status = %d, want 400, body: %s", rr2.Code, rr2.Body.String())
	}
	if !strings.Contains(rr2.Body.String(), "not found in crew") {
		t.Errorf("body = %s, want 'not found in crew'", rr2.Body.String())
	}
}

func TestCovQMMMissionCreate_DBError(t *testing.T) {
	db := setupTestDB(t)
	logger := newTestLogger()
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	crewID := seedMissionCrew(t, db, wsID)
	leadID := seedMissionAgent(t, db, wsID, crewID, "cqmm-clead", "LEAD")

	h := NewMissionHandler(db, nil, nil, logger)
	db.Close() // lead lookup query fails → 500.

	req := httptest.NewRequest("POST", "/api/v1/crews/"+crewID+"/missions",
		bytes.NewBufferString(`{"title":"X","lead_agent_id":"`+leadID+`"}`))
	req.SetPathValue("crewId", crewID)
	req = req.WithContext(withWorkspace(withUser(req.Context(), &AuthUser{ID: userID}), wsID, "MANAGER"))
	rr := httptest.NewRecorder()
	h.Create(rr, req)
	if rr.Code != http.StatusInternalServerError {
		t.Errorf("status = %d, want 500", rr.Code)
	}
}

func TestCovQMMMissionMetrics_PopulatedAggregates(t *testing.T) {
	db := setupTestDB(t)
	logger := newTestLogger()
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	crewID := seedMissionCrew(t, db, wsID)
	leadID := seedMissionAgent(t, db, wsID, crewID, "cqmm-mlead", "LEAD")

	// A completed mission (within 24h) + a failed one + an active one.
	execOrFatal(t, db, `INSERT INTO missions (id, workspace_id, crew_id, lead_agent_id, trace_id, title, status, created_at, updated_at, completed_at)
		VALUES ('cqmm-done', ?, ?, ?, 'mtr-1', 'Done', 'COMPLETED', datetime('now','-1 hour'), datetime('now'), datetime('now'))`, wsID, crewID, leadID)
	execOrFatal(t, db, `INSERT INTO missions (id, workspace_id, crew_id, lead_agent_id, trace_id, title, status, created_at, updated_at)
		VALUES ('cqmm-fail', ?, ?, ?, 'mtr-2', 'Fail', 'FAILED', datetime('now','-2 hour'), datetime('now'))`, wsID, crewID, leadID)
	execOrFatal(t, db, `INSERT INTO missions (id, workspace_id, crew_id, lead_agent_id, trace_id, title, status, created_at, updated_at)
		VALUES ('cqmm-act', ?, ?, ?, 'mtr-3', 'Active', 'IN_PROGRESS', datetime('now'), datetime('now'))`, wsID, crewID, leadID)

	// Tasks with tokens/cost completed within the 24h window.
	execOrFatal(t, db, `INSERT INTO mission_tasks (id, mission_id, title, status, task_order, tokens_used, estimated_cost, completed_at, created_at, updated_at)
		VALUES ('cqmm-mt-a', 'cqmm-done', 'A', 'COMPLETED', 0, 500, 0.25, datetime('now'), datetime('now'), datetime('now'))`)
	execOrFatal(t, db, `INSERT INTO mission_tasks (id, mission_id, title, status, task_order, tokens_used, estimated_cost, completed_at, created_at, updated_at)
		VALUES ('cqmm-mt-b', 'cqmm-done', 'B', 'COMPLETED', 1, 300, 0.10, datetime('now'), datetime('now'), datetime('now'))`)
	execOrFatal(t, db, `INSERT INTO mission_tasks (id, mission_id, title, status, task_order, completed_at, created_at, updated_at)
		VALUES ('cqmm-mt-f', 'cqmm-fail', 'F', 'FAILED', 0, NULL, datetime('now'), datetime('now'))`)

	h := NewMissionHandler(db, nil, nil, logger)

	req := httptest.NewRequest("GET", "/api/v1/mission-metrics", nil)
	req = req.WithContext(withWorkspace(withUser(req.Context(), &AuthUser{ID: userID}), wsID, "MEMBER"))
	rr := httptest.NewRecorder()
	h.Metrics(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("metrics status = %d, body: %s", rr.Code, rr.Body.String())
	}
	var m map[string]interface{}
	if err := json.Unmarshal(rr.Body.Bytes(), &m); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if m["total_missions"].(float64) != 3 {
		t.Errorf("total_missions = %v, want 3", m["total_missions"])
	}
	if m["active_missions"].(float64) != 1 {
		t.Errorf("active_missions = %v, want 1", m["active_missions"])
	}
	if m["completed_24h"].(float64) != 1 {
		t.Errorf("completed_24h = %v, want 1", m["completed_24h"])
	}
	if m["failed_24h"].(float64) != 1 {
		t.Errorf("failed_24h = %v, want 1", m["failed_24h"])
	}
	if m["total_tokens_24h"].(float64) != 800 {
		t.Errorf("total_tokens_24h = %v, want 800", m["total_tokens_24h"])
	}
	if m["tasks_completed_24h"].(float64) != 2 {
		t.Errorf("tasks_completed_24h = %v, want 2", m["tasks_completed_24h"])
	}
	if m["tasks_failed_24h"].(float64) != 1 {
		t.Errorf("tasks_failed_24h = %v, want 1", m["tasks_failed_24h"])
	}
}

func TestCovQMMMissionMetrics_DBError(t *testing.T) {
	db := setupTestDB(t)
	logger := newTestLogger()
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)

	h := NewMissionHandler(db, nil, nil, logger)
	db.Close()

	req := httptest.NewRequest("GET", "/api/v1/mission-metrics", nil)
	req = req.WithContext(withWorkspace(withUser(req.Context(), &AuthUser{ID: userID}), wsID, "MEMBER"))
	rr := httptest.NewRecorder()
	h.Metrics(rr, req)
	if rr.Code != http.StatusInternalServerError {
		t.Errorf("status = %d, want 500", rr.Code)
	}
}
