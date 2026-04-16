package api

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"sync"
	"testing"
	"time"
)

func newQueryHandler(t *testing.T) (*QueryHandler, string, string, string, string, string) {
	t.Helper()
	db := setupTestDB(t)
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	userID, wsID, crewID, leadID, workerID := seedIssueFixtures(t, db)
	h := &QueryHandler{
		db:                db,
		hub:               nil,
		logger:            logger,
		internalToken:     "tok",
		escalationWaiters: make(map[string]chan escalationResult),
	}
	return h, userID, wsID, crewID, leadID, workerID
}

// ── Activity feed ─────────────────────────────────────────────────────

func TestActivity_ListAll(t *testing.T) {
	h, userID, wsID, crewID, leadID, workerID := newQueryHandler(t)
	// chat needed for assignments
	chatID := generateCUID()
	if _, err := h.db.Exec(`INSERT INTO chats(id,agent_id,workspace_id,mode,status) VALUES (?, ?, ?, 'CHAT', 'ACTIVE')`, chatID, leadID, wsID); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Format(time.RFC3339)
	h.db.Exec(`INSERT INTO assignments(id,workspace_id,chat_id,assigned_by_id,assigned_to_id,task,status,created_at)
		VALUES (?, ?, ?, ?, ?, 'do thing', 'PENDING', ?)`, "a1", wsID, chatID, leadID, workerID, now)
	h.db.Exec(`INSERT INTO peer_conversations(id,workspace_id,crew_id,chat_id,from_agent_id,to_agent_id,question,status,created_at)
		VALUES (?, ?, ?, ?, ?, ?, 'q?', 'COMPLETED', ?)`, "p1", wsID, crewID, chatID, leadID, workerID, now)
	h.db.Exec(`INSERT INTO escalations(id,workspace_id,crew_id,chat_id,from_agent_id,reason,status,created_at)
		VALUES (?, ?, ?, ?, ?, 'help', 'PENDING', ?)`, "e1", wsID, crewID, chatID, leadID, now)

	req := httptest.NewRequest("GET", "/?limit=10", nil)
	req = req.WithContext(withWorkspace(withUser(req.Context(), &AuthUser{ID: userID}), wsID, "OWNER"))
	rr := httptest.NewRecorder()
	h.ListAllActivity(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rr.Code, rr.Body.String())
	}
	var items []activityItem
	json.Unmarshal(rr.Body.Bytes(), &items)
	if len(items) != 3 {
		t.Errorf("got %d items want 3", len(items))
	}
}

func TestActivity_ListAll_Pagination(t *testing.T) {
	h, userID, wsID, _, _, _ := newQueryHandler(t)

	req := httptest.NewRequest("GET", "/?limit=10&offset=5000", nil)
	req = req.WithContext(withWorkspace(withUser(req.Context(), &AuthUser{ID: userID}), wsID, "OWNER"))
	rr := httptest.NewRecorder()
	h.ListAllActivity(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d", rr.Code)
	}
	if rr.Body.String() != "[]\n" {
		t.Errorf("expected [], got %q", rr.Body.String())
	}
}

// ── Escalation ────────────────────────────────────────────────────────

func TestEscalation_PendingCount(t *testing.T) {
	h, userID, wsID, crewID, leadID, _ := newQueryHandler(t)
	chatID := generateCUID()
	h.db.Exec(`INSERT INTO chats(id,agent_id,workspace_id,mode,status) VALUES (?, ?, ?, 'CHAT', 'ACTIVE')`, chatID, leadID, wsID)
	h.db.Exec(`INSERT INTO escalations(id,workspace_id,crew_id,chat_id,from_agent_id,reason,status,created_at)
		VALUES ('e1', ?, ?, ?, ?, 'h', 'PENDING', datetime('now'))`, wsID, crewID, chatID, leadID)

	req := httptest.NewRequest("GET", "/", nil)
	req = req.WithContext(withWorkspace(withUser(req.Context(), &AuthUser{ID: userID}), wsID, "OWNER"))
	rr := httptest.NewRecorder()
	h.PendingEscalationCount(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rr.Code, rr.Body.String())
	}
	var resp map[string]int
	json.Unmarshal(rr.Body.Bytes(), &resp)
	if resp["count"] != 1 {
		t.Errorf("count = %d want 1", resp["count"])
	}
}

func TestEscalation_Create_Validations(t *testing.T) {
	h, _, _, _, _, _ := newQueryHandler(t)

	cases := []struct {
		name string
		body string
		want int
	}{
		{"bad json", `bad`, 400},
		{"missing fields", `{}`, 400},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest("POST", "/", bytes.NewBufferString(tc.body))
			rr := httptest.NewRecorder()
			h.CreateEscalation(rr, req)
			if rr.Code != tc.want {
				t.Errorf("got %d want %d", rr.Code, tc.want)
			}
		})
	}
}

func TestEscalation_Create_Success(t *testing.T) {
	h, _, wsID, crewID, leadID, _ := newQueryHandler(t)
	chatID := generateCUID()
	h.db.Exec(`INSERT INTO chats(id,agent_id,workspace_id,mode,status) VALUES (?, ?, ?, 'CHAT', 'ACTIVE')`, chatID, leadID, wsID)

	body := bytes.NewBufferString(`{"from_slug":"lead","reason":"need help","crew_id":"` + crewID + `","workspace_id":"` + wsID + `","chat_id":"` + chatID + `"}`)
	req := httptest.NewRequest("POST", "/", body)
	rr := httptest.NewRecorder()
	h.CreateEscalation(rr, req)
	if rr.Code != http.StatusCreated {
		t.Fatalf("status = %d body=%s", rr.Code, rr.Body.String())
	}
}

func TestEscalation_Create_FromAgentNotFound(t *testing.T) {
	h, _, wsID, crewID, _, _ := newQueryHandler(t)

	body := bytes.NewBufferString(`{"from_slug":"missing","reason":"x","crew_id":"` + crewID + `","workspace_id":"` + wsID + `","chat_id":"c1"}`)
	req := httptest.NewRequest("POST", "/", body)
	rr := httptest.NewRecorder()
	h.CreateEscalation(rr, req)
	if rr.Code != http.StatusNotFound {
		t.Errorf("status = %d", rr.Code)
	}
}

func TestEscalation_Create_LinkType(t *testing.T) {
	h, _, wsID, crewID, leadID, _ := newQueryHandler(t)
	chatID := generateCUID()
	h.db.Exec(`INSERT INTO chats(id,agent_id,workspace_id,mode,status) VALUES (?, ?, ?, 'CHAT', 'ACTIVE')`, chatID, leadID, wsID)

	cases := []struct {
		name string
		body string
		want int
	}{
		{"missing metadata", `{"from_slug":"lead","reason":"r","crew_id":"` + crewID + `","workspace_id":"` + wsID + `","chat_id":"` + chatID + `","type":"LINK"}`, 400},
		{"bad url", `{"from_slug":"lead","reason":"r","crew_id":"` + crewID + `","workspace_id":"` + wsID + `","chat_id":"` + chatID + `","type":"LINK","metadata":"http://x"}`, 400},
		{"bad type", `{"from_slug":"lead","reason":"r","crew_id":"` + crewID + `","workspace_id":"` + wsID + `","chat_id":"` + chatID + `","type":"BOGUS"}`, 400},
		{"valid LINK", `{"from_slug":"lead","reason":"r","crew_id":"` + crewID + `","workspace_id":"` + wsID + `","chat_id":"` + chatID + `","type":"LINK","metadata":"https://example.com"}`, 201},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest("POST", "/", bytes.NewBufferString(tc.body))
			rr := httptest.NewRecorder()
			h.CreateEscalation(rr, req)
			if rr.Code != tc.want {
				t.Errorf("got %d want %d body=%s", rr.Code, tc.want, rr.Body.String())
			}
		})
	}
}

func TestEscalation_Resolve_Forbidden(t *testing.T) {
	h, userID, wsID, _, _, _ := newQueryHandler(t)

	req := httptest.NewRequest("PATCH", "/", bytes.NewBufferString(`{"resolution":"ok"}`))
	req.SetPathValue("escalationId", "x")
	req = req.WithContext(withWorkspace(withUser(req.Context(), &AuthUser{ID: userID}), wsID, "VIEWER"))
	rr := httptest.NewRecorder()
	h.ResolveEscalation(rr, req)
	if rr.Code != http.StatusForbidden {
		t.Errorf("status = %d", rr.Code)
	}
}

func TestEscalation_Resolve_Validations(t *testing.T) {
	h, userID, wsID, _, _, _ := newQueryHandler(t)

	cases := []struct {
		name string
		body string
		want int
	}{
		{"bad json", `bad`, 400},
		{"empty resolution", `{"resolution":""}`, 400},
		{"bad action", `{"resolution":"ok","action":"yolo"}`, 400},
		{"redirect missing target", `{"resolution":"ok","action":"redirect"}`, 400},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest("PATCH", "/", bytes.NewBufferString(tc.body))
			req.SetPathValue("escalationId", "x")
			req = req.WithContext(withWorkspace(withUser(req.Context(), &AuthUser{ID: userID}), wsID, "OWNER"))
			rr := httptest.NewRecorder()
			h.ResolveEscalation(rr, req)
			if rr.Code != tc.want {
				t.Errorf("got %d want %d body=%s", rr.Code, tc.want, rr.Body.String())
			}
		})
	}
}

func TestEscalation_Resolve_NotFound(t *testing.T) {
	h, userID, wsID, _, _, _ := newQueryHandler(t)

	req := httptest.NewRequest("PATCH", "/", bytes.NewBufferString(`{"resolution":"ok"}`))
	req.SetPathValue("escalationId", "missing")
	req = req.WithContext(withWorkspace(withUser(req.Context(), &AuthUser{ID: userID}), wsID, "OWNER"))
	rr := httptest.NewRecorder()
	h.ResolveEscalation(rr, req)
	if rr.Code != http.StatusNotFound {
		t.Errorf("status = %d", rr.Code)
	}
}

func TestEscalation_Resolve_Success(t *testing.T) {
	h, userID, wsID, crewID, leadID, _ := newQueryHandler(t)
	chatID := generateCUID()
	h.db.Exec(`INSERT INTO chats(id,agent_id,workspace_id,mode,status) VALUES (?, ?, ?, 'CHAT', 'ACTIVE')`, chatID, leadID, wsID)
	h.db.Exec(`INSERT INTO escalations(id,workspace_id,crew_id,chat_id,from_agent_id,reason,status,type,created_at)
		VALUES ('e-resolve', ?, ?, ?, ?, 'help', 'PENDING', 'TEXT', datetime('now'))`, wsID, crewID, chatID, leadID)

	req := httptest.NewRequest("PATCH", "/", bytes.NewBufferString(`{"resolution":"ok","action":"approve"}`))
	req.SetPathValue("escalationId", "e-resolve")
	req = req.WithContext(withWorkspace(withUser(req.Context(), &AuthUser{ID: userID}), wsID, "OWNER"))
	rr := httptest.NewRecorder()
	h.ResolveEscalation(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rr.Code, rr.Body.String())
	}

	// Resolve again -> 409 (use a fresh request because body is single-use)
	req2 := httptest.NewRequest("PATCH", "/", bytes.NewBufferString(`{"resolution":"ok","action":"approve"}`))
	req2.SetPathValue("escalationId", "e-resolve")
	req2 = req2.WithContext(withWorkspace(withUser(req2.Context(), &AuthUser{ID: userID}), wsID, "OWNER"))
	rr2 := httptest.NewRecorder()
	h.ResolveEscalation(rr2, req2)
	if rr2.Code != http.StatusConflict {
		t.Errorf("re-resolve status = %d body=%s", rr2.Code, rr2.Body.String())
	}
}

func TestEscalation_Resolve_RedirectInvalidAgent(t *testing.T) {
	h, userID, wsID, crewID, leadID, _ := newQueryHandler(t)
	chatID := generateCUID()
	h.db.Exec(`INSERT INTO chats(id,agent_id,workspace_id,mode,status) VALUES (?, ?, ?, 'CHAT', 'ACTIVE')`, chatID, leadID, wsID)
	h.db.Exec(`INSERT INTO escalations(id,workspace_id,crew_id,chat_id,from_agent_id,reason,status,type,created_at)
		VALUES ('e-red', ?, ?, ?, ?, 'help', 'PENDING', 'TEXT', datetime('now'))`, wsID, crewID, chatID, leadID)

	body := bytes.NewBufferString(`{"resolution":"go","action":"redirect","redirect_to":"missing-agent"}`)
	req := httptest.NewRequest("PATCH", "/", body)
	req.SetPathValue("escalationId", "e-red")
	req = req.WithContext(withWorkspace(withUser(req.Context(), &AuthUser{ID: userID}), wsID, "OWNER"))
	rr := httptest.NewRecorder()
	h.ResolveEscalation(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Errorf("status = %d", rr.Code)
	}
}

func TestEscalation_List(t *testing.T) {
	h, userID, wsID, crewID, leadID, _ := newQueryHandler(t)
	chatID := generateCUID()
	h.db.Exec(`INSERT INTO chats(id,agent_id,workspace_id,mode,status) VALUES (?, ?, ?, 'CHAT', 'ACTIVE')`, chatID, leadID, wsID)
	h.db.Exec(`INSERT INTO escalations(id,workspace_id,crew_id,chat_id,from_agent_id,reason,status,type,created_at)
		VALUES ('e1', ?, ?, ?, ?, 'help', 'PENDING', 'TEXT', datetime('now'))`, wsID, crewID, chatID, leadID)

	req := httptest.NewRequest("GET", "/?limit=10", nil)
	req.SetPathValue("crewId", crewID)
	req = req.WithContext(withWorkspace(withUser(req.Context(), &AuthUser{ID: userID}), wsID, "OWNER"))
	rr := httptest.NewRecorder()
	h.ListEscalations(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rr.Code, rr.Body.String())
	}
}

// ── Escalation Waiter ─────────────────────────────────────────────────

func TestEscalation_Waiter_RegisterNotifyRemove(t *testing.T) {
	h := &QueryHandler{
		escalationMu:      sync.Mutex{},
		escalationWaiters: make(map[string]chan escalationResult),
	}
	ch := h.registerEscalationWaiter("e1")
	go h.notifyEscalationWaiter("e1", escalationResult{Resolution: "ok"})

	select {
	case res := <-ch:
		if res.Resolution != "ok" {
			t.Errorf("resolution = %q", res.Resolution)
		}
	case <-time.After(200 * time.Millisecond):
		t.Fatal("waiter timed out")
	}

	// removeEscalationWaiter no-op when channel mismatch
	other := make(chan escalationResult)
	h.removeEscalationWaiter("e1", other)

	// notify when no waiter registered should not panic
	h.notifyEscalationWaiter("nope", escalationResult{})
}

func TestEscalation_Wait_ResolvedImmediate(t *testing.T) {
	h, _, wsID, crewID, leadID, _ := newQueryHandler(t)
	chatID := generateCUID()
	h.db.Exec(`INSERT INTO chats(id,agent_id,workspace_id,mode,status) VALUES (?, ?, ?, 'CHAT', 'ACTIVE')`, chatID, leadID, wsID)
	h.db.Exec(`INSERT INTO escalations(id,workspace_id,crew_id,chat_id,from_agent_id,reason,status,type,resolution,action,created_at)
		VALUES ('e-done', ?, ?, ?, ?, 'h', 'RESOLVED', 'TEXT', 'do it', 'approve', datetime('now'))`, wsID, crewID, chatID, leadID)

	req := httptest.NewRequest("GET", "/", nil)
	req.SetPathValue("escalationId", "e-done")
	rr := httptest.NewRecorder()
	h.WaitForEscalationResponse(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rr.Code, rr.Body.String())
	}
}

func TestEscalation_Wait_NotFound(t *testing.T) {
	h, _, _, _, _, _ := newQueryHandler(t)
	req := httptest.NewRequest("GET", "/", nil)
	req.SetPathValue("escalationId", "missing")
	rr := httptest.NewRecorder()
	h.WaitForEscalationResponse(rr, req)
	if rr.Code != http.StatusNotFound {
		t.Errorf("status = %d", rr.Code)
	}
}

func TestEscalation_Wait_Timeout(t *testing.T) {
	h, _, wsID, crewID, leadID, _ := newQueryHandler(t)
	chatID := generateCUID()
	h.db.Exec(`INSERT INTO chats(id,agent_id,workspace_id,mode,status) VALUES (?, ?, ?, 'CHAT', 'ACTIVE')`, chatID, leadID, wsID)
	h.db.Exec(`INSERT INTO escalations(id,workspace_id,crew_id,chat_id,from_agent_id,reason,status,type,created_at)
		VALUES ('e-w', ?, ?, ?, ?, 'h', 'PENDING', 'TEXT', datetime('now'))`, wsID, crewID, chatID, leadID)

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	req := httptest.NewRequest("GET", "/", nil).WithContext(ctx)
	req.SetPathValue("escalationId", "e-w")
	rr := httptest.NewRecorder()
	h.WaitForEscalationResponse(rr, req)
	if rr.Code != http.StatusRequestTimeout {
		t.Errorf("status = %d, want 408", rr.Code)
	}
}

// ── Confidence ────────────────────────────────────────────────────────

func TestConfidence_BadJSON(t *testing.T) {
	h, _, _, _, _, _ := newQueryHandler(t)
	req := httptest.NewRequest("POST", "/", bytes.NewBufferString(`bad`))
	rr := httptest.NewRecorder()
	h.ReportConfidence(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Errorf("status = %d", rr.Code)
	}
}

func TestConfidence_BadParams(t *testing.T) {
	h, _, _, _, _, _ := newQueryHandler(t)
	cases := []string{
		`{}`,
		`{"agent_id":"a","crew_id":"c","confidence":-1}`,
		`{"agent_id":"a","crew_id":"c","confidence":2}`,
	}
	for _, body := range cases {
		req := httptest.NewRequest("POST", "/", bytes.NewBufferString(body))
		rr := httptest.NewRecorder()
		h.ReportConfidence(rr, req)
		if rr.Code != http.StatusBadRequest {
			t.Errorf("body=%q status = %d", body, rr.Code)
		}
	}
}

func TestConfidence_NoActiveTask(t *testing.T) {
	h, _, _, crewID, _, workerID := newQueryHandler(t)

	body := bytes.NewBufferString(`{"agent_id":"` + workerID + `","crew_id":"` + crewID + `","confidence":0.9}`)
	req := httptest.NewRequest("POST", "/", body)
	rr := httptest.NewRecorder()
	h.ReportConfidence(rr, req)
	if rr.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", rr.Code)
	}
}

func TestConfidence_Success(t *testing.T) {
	h, _, wsID, crewID, leadID, workerID := newQueryHandler(t)
	missionID := generateCUID()
	h.db.Exec(`INSERT INTO chats(id,agent_id,workspace_id,mode,status) VALUES (?, ?, ?, 'MISSION', 'ACTIVE')`, missionID, leadID, wsID)
	h.db.Exec(`INSERT INTO missions(id,workspace_id,crew_id,lead_agent_id,trace_id,title,status,created_at,updated_at)
		VALUES (?, ?, ?, ?, ?, 'M', 'IN_PROGRESS', datetime('now'), datetime('now'))`, missionID, wsID, crewID, leadID, "trace-"+missionID)
	tID := generateCUID()
	h.db.Exec(`INSERT INTO mission_tasks(id,mission_id,assigned_agent_id,title,status,task_order,depends_on,created_at,updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, datetime('now'), datetime('now'))`,
		tID, missionID, workerID, "T", "IN_PROGRESS", 1, "[]")

	body := bytes.NewBufferString(`{"agent_id":"` + workerID + `","crew_id":"` + crewID + `","confidence":0.4,"reason":"unsure"}`)
	req := httptest.NewRequest("POST", "/", body)
	rr := httptest.NewRecorder()
	h.ReportConfidence(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rr.Code, rr.Body.String())
	}
}

func TestConfidence_AutoEscalate(t *testing.T) {
	h, _, wsID, crewID, leadID, workerID := newQueryHandler(t)
	missionID := generateCUID()
	h.db.Exec(`INSERT INTO chats(id,agent_id,workspace_id,mode,status) VALUES (?, ?, ?, 'MISSION', 'ACTIVE')`, missionID, leadID, wsID)
	h.db.Exec(`UPDATE crews SET escalation_config='{"require_approval_below":0.5,"notify_threshold":0.8}' WHERE id=?`, crewID)
	h.db.Exec(`INSERT INTO missions(id,workspace_id,crew_id,lead_agent_id,trace_id,title,status,created_at,updated_at)
		VALUES (?, ?, ?, ?, ?, 'M', 'IN_PROGRESS', datetime('now'), datetime('now'))`, missionID, wsID, crewID, leadID, "trace-"+missionID)
	tID := generateCUID()
	h.db.Exec(`INSERT INTO mission_tasks(id,mission_id,assigned_agent_id,title,status,task_order,depends_on,created_at,updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, datetime('now'), datetime('now'))`,
		tID, missionID, workerID, "T", "IN_PROGRESS", 1, "[]")

	body := bytes.NewBufferString(`{"agent_id":"` + workerID + `","crew_id":"` + crewID + `","confidence":0.2,"reason":"low"}`)
	req := httptest.NewRequest("POST", "/", body)
	rr := httptest.NewRecorder()
	h.ReportConfidence(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rr.Code, rr.Body.String())
	}
	var resp map[string]interface{}
	json.Unmarshal(rr.Body.Bytes(), &resp)
	if resp["action"] != "escalated" {
		t.Errorf("action = %v want escalated", resp["action"])
	}
}

// ── Standup ───────────────────────────────────────────────────────────

func newStandupHandler(t *testing.T) (*QueryHandler, string, string, string, string) {
	t.Helper()
	db := setupTestDB(t)
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	userID, wsID, crewID, leadID, _ := seedIssueFixtures(t, db)
	h := &QueryHandler{db: db, hub: nil, logger: logger, escalationWaiters: make(map[string]chan escalationResult)}
	return h, userID, wsID, crewID, leadID
}

func TestStandup_NoCrew(t *testing.T) {
	h, _, _, _, _ := newStandupHandler(t)
	req := httptest.NewRequest("GET", "/", nil)
	rr := httptest.NewRecorder()
	h.Standup(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Errorf("status = %d", rr.Code)
	}
}

func TestStandup_BadSince(t *testing.T) {
	h, userID, wsID, crewID, _ := newStandupHandler(t)
	req := httptest.NewRequest("GET", "/?since=not-rfc3339", nil)
	req.SetPathValue("crewId", crewID)
	req = req.WithContext(withWorkspace(withUser(req.Context(), &AuthUser{ID: userID}), wsID, "OWNER"))
	rr := httptest.NewRecorder()
	h.Standup(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Errorf("status = %d", rr.Code)
	}
}

func TestStandup_CrewNotInWorkspace(t *testing.T) {
	h, userID, wsID, _, _ := newStandupHandler(t)
	req := httptest.NewRequest("GET", "/", nil)
	req.SetPathValue("crewId", "missing")
	req = req.WithContext(withWorkspace(withUser(req.Context(), &AuthUser{ID: userID}), wsID, "OWNER"))
	rr := httptest.NewRecorder()
	h.Standup(rr, req)
	if rr.Code != http.StatusNotFound {
		t.Errorf("status = %d", rr.Code)
	}
}

func TestStandup_Success(t *testing.T) {
	h, userID, wsID, crewID, leadID := newStandupHandler(t)
	chatID := generateCUID()
	h.db.Exec(`INSERT INTO chats(id,agent_id,workspace_id,mode,status) VALUES (?, ?, ?, 'CHAT', 'ACTIVE')`, chatID, leadID, wsID)
	h.db.Exec(`INSERT INTO peer_conversations(id,workspace_id,crew_id,chat_id,from_agent_id,to_agent_id,question,response,status,escalated,created_at)
		VALUES ('p1', ?, ?, ?, ?, ?, 'q?', 'answer', 'COMPLETED', 0, datetime('now'))`, wsID, crewID, chatID, leadID, leadID)
	h.db.Exec(`INSERT INTO escalations(id,workspace_id,crew_id,chat_id,from_agent_id,reason,status,type,created_at)
		VALUES ('e1', ?, ?, ?, ?, 'help', 'PENDING', 'TEXT', datetime('now'))`, wsID, crewID, chatID, leadID)

	req := httptest.NewRequest("GET", "/", nil)
	req.SetPathValue("crewId", crewID)
	req = req.WithContext(withWorkspace(withUser(req.Context(), &AuthUser{ID: userID}), wsID, "OWNER"))
	rr := httptest.NewRecorder()
	h.Standup(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rr.Code, rr.Body.String())
	}
}

func TestStandup_Internal_NoWorkspace(t *testing.T) {
	h, _, _, crewID, _ := newStandupHandler(t)
	// internal route: no path value, uses query param
	req := httptest.NewRequest("GET", "/?crew_id="+crewID, nil)
	rr := httptest.NewRecorder()
	h.Standup(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rr.Code, rr.Body.String())
	}
}

func TestStandup_FormatTimestamp(t *testing.T) {
	out := formatStandupTimestamp("2030-01-02T15:04:00Z")
	if out != "15:04" {
		t.Errorf("got %q want 15:04", out)
	}
	if formatStandupTimestamp("not-a-date") != "not-a-date" {
		t.Error("invalid input must be returned as-is")
	}
}
