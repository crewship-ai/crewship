package api

// Coverage for query_handler.go — Create's validation, lookup, insert
// and orchestrator-not-available tail (which drives finishQuery's FAILED
// branch synchronously). query_handler_cov_test.go already exists, hence
// the _cov2 suffix.

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/orchestrator"
)

func covQryRig(t *testing.T) (h *QueryHandler, wsID, crewID, fromID, targetID, chatID string) {
	t.Helper()
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID = seedTestWorkspace(t, db, userID)
	crewID = seedCrewRow(t, db, "crew-qry2", wsID, "QRY", "qry2")
	fromID = seedAgentRow(t, db, "agent-qry2-from", wsID, crewID, "From", "qry2-from", "LEAD")
	targetID = seedAgentRow(t, db, "agent-qry2-to", wsID, crewID, "To", "qry2-to", "AGENT")
	chatID = "chat-qry2"
	if _, err := db.Exec(`
		INSERT INTO chats (id, agent_id, workspace_id, title, mode, status, started_at, created_at, updated_at)
		VALUES (?, ?, ?, 'q', 'MISSION', 'ACTIVE', datetime('now'), datetime('now'), datetime('now'))`,
		chatID, fromID, wsID); err != nil {
		t.Fatalf("seed chat: %v", err)
	}
	h = NewQueryHandler(db, nil, nil, "internal-test-token", newTestLogger())
	return
}

func covQryPost(t *testing.T, h *QueryHandler, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest("POST", "/api/v1/internal/queries", strings.NewReader(body))
	rr := httptest.NewRecorder()
	h.Create(rr, req)
	return rr
}

func TestQueryCreateCov_Validation(t *testing.T) {
	h, _, _, _, _, _ := covQryRig(t)
	t.Run("invalid json", func(t *testing.T) {
		if rr := covQryPost(t, h, "{nope"); rr.Code != http.StatusBadRequest {
			t.Errorf("status = %d, want 400", rr.Code)
		}
	})
	t.Run("missing fields", func(t *testing.T) {
		if rr := covQryPost(t, h, `{"target_slug":"x","question":"q"}`); rr.Code != http.StatusBadRequest {
			t.Errorf("status = %d, want 400", rr.Code)
		}
	})
}

func TestQueryCreateCov_TargetNotFound(t *testing.T) {
	h, wsID, crewID, _, _, chatID := covQryRig(t)
	rr := covQryPost(t, h, `{"target_slug":"ghost","question":"q","crew_id":"`+crewID+`","workspace_id":"`+wsID+`","chat_id":"`+chatID+`"}`)
	if rr.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404; body=%s", rr.Code, rr.Body.String())
	}
}

func TestQueryCreateCov_NoOrchestrator_FailsQuery(t *testing.T) {
	h, wsID, crewID, fromID, targetID, chatID := covQryRig(t)
	rr := covQryPost(t, h, `{"target_slug":"qry2-to","from_slug":"qry2-from","question":"what is up?","crew_id":"`+crewID+`","workspace_id":"`+wsID+`","chat_id":"`+chatID+`","depth":1}`)
	if rr.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503; body=%s", rr.Code, rr.Body.String())
	}

	// The peer conversation row must exist and be FAILED via finishQuery.
	var from, to, question, status string
	if err := h.db.QueryRow(`
		SELECT COALESCE(from_agent_id,''), to_agent_id, question, status
		FROM peer_conversations WHERE chat_id = ?`, chatID).
		Scan(&from, &to, &question, &status); err != nil {
		t.Fatalf("query peer_conversations: %v", err)
	}
	if from != fromID || to != targetID {
		t.Errorf("from=%q to=%q, want %q/%q", from, to, fromID, targetID)
	}
	if question != "what is up?" {
		t.Errorf("question = %q", question)
	}
	if status != "FAILED" {
		t.Errorf("status = %q, want FAILED", status)
	}
	var finishedAt string
	if err := h.db.QueryRow(`
		SELECT COALESCE(finished_at,'') FROM peer_conversations WHERE chat_id = ?`, chatID).Scan(&finishedAt); err != nil {
		t.Fatalf("query finished_at: %v", err)
	}
	if finishedAt == "" {
		t.Error("finished_at not set")
	}
}

func TestQueryCreateCov_ContainerError500(t *testing.T) {
	// Real orchestrator without container provider: Create proceeds past
	// the nil-orch gate and fails at GetOrCreateContainer → 500 +
	// finishQuery(FAILED, "container error: ...").
	h, wsID, crewID, _, _, chatID := covQryRig(t)
	h.orch = orchestrator.New(nil, nil, newTestLogger())
	rr := covQryPost(t, h, `{"target_slug":"qry2-to","from_slug":"qry2-from","question":"q","crew_id":"`+crewID+`","workspace_id":"`+wsID+`","chat_id":"`+chatID+`"}`)
	if rr.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500; body=%s", rr.Code, rr.Body.String())
	}
	var status, response string
	if err := h.db.QueryRow(
		`SELECT status, COALESCE(response,'') FROM peer_conversations WHERE chat_id = ?`, chatID).
		Scan(&status, &response); err != nil {
		t.Fatalf("query: %v", err)
	}
	if status != "FAILED" {
		t.Errorf("status = %q, want FAILED", status)
	}
}

func TestQueryCreateCov_UnknownFromSlugStillWorks(t *testing.T) {
	// from_slug that resolves to no agent: fromAgentID stays empty but the
	// query still proceeds to the orchestrator gate.
	h, wsID, crewID, _, _, chatID := covQryRig(t)
	rr := covQryPost(t, h, `{"target_slug":"qry2-to","from_slug":"nobody","question":"q","crew_id":"`+crewID+`","workspace_id":"`+wsID+`","chat_id":"`+chatID+`"}`)
	if rr.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503; body=%s", rr.Code, rr.Body.String())
	}
	var from string
	if err := h.db.QueryRow(
		`SELECT COALESCE(from_agent_id,'') FROM peer_conversations WHERE chat_id = ?`, chatID).Scan(&from); err != nil {
		t.Fatalf("query: %v", err)
	}
	if from != "" {
		t.Errorf("from_agent_id = %q, want empty", from)
	}
}
