package api

// Third coverage pass for query_handler.go — Create's bound-token gates,
// credential-decrypt failure, the execution path past container creation
// (fake provider + StopAccepting), the backup-guard 409, and truncate's
// multibyte rune branch. finishQuery's terminal-entry error branch rides
// along via the default noopEmitter (which rejects run.* entries).

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/orchestrator"
)

// covQH3Rig: workspace + crew + two agents + chat anchored on the asker.
func covQH3Rig(t *testing.T) (h *QueryHandler, wsID, crewID, askerID, targetID, chatID string) {
	t.Helper()
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID = seedTestWorkspace(t, db, userID)
	crewID = seedCrewRow(t, db, "crew-qh3", wsID, "QH3", "qh3")
	askerID = seedAgentRow(t, db, "agent-qh3-from", wsID, crewID, "Asker", "qh3-from", "LEAD")
	targetID = seedAgentRow(t, db, "agent-qh3-to", wsID, crewID, "Target", "qh3-to", "AGENT")
	chatID = "chat-qh3"
	if _, err := db.Exec(`
		INSERT INTO chats (id, agent_id, workspace_id, title, mode, status, started_at, created_at, updated_at)
		VALUES (?, ?, ?, 'qh3', 'CHAT', 'ACTIVE', datetime('now'), datetime('now'), datetime('now'))`,
		chatID, askerID, wsID); err != nil {
		t.Fatalf("seed chat: %v", err)
	}
	h = NewQueryHandler(db, nil, nil, "internal-test-token", newTestLogger())
	return
}

func covQH3Post(t *testing.T, h *QueryHandler, body, boundWS string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest("POST", "/api/v1/internal/queries", strings.NewReader(body))
	if boundWS != "" {
		req = req.WithContext(context.WithValue(req.Context(), ctxInternalTokenWS, boundWS))
	}
	rr := httptest.NewRecorder()
	h.Create(rr, req)
	return rr
}

func covQH3Body(wsID, crewID, chatID string) string {
	return `{"target_slug":"qh3-to","question":"q?","from_slug":"qh3-from","crew_id":"` + crewID +
		`","workspace_id":"` + wsID + `","chat_id":"` + chatID + `"}`
}

// ---- truncate ----

func TestQH3_TruncateRunes(t *testing.T) {
	// len(s) > n in bytes but <= n in runes → returned unchanged.
	s := "ééééé" // 5 runes, 10 bytes
	if got := truncate(s, 6); got != s {
		t.Errorf("truncate(%q, 6) = %q, want unchanged", s, got)
	}
	// Real cut appends the ellipsis.
	if got := truncate("ééééé", 3); got != "ééé…" {
		t.Errorf("truncate cut = %q", got)
	}
	if got := truncate("abc", 0); got != "abc" {
		t.Errorf("truncate n=0 = %q", got)
	}
}

// ---- Create: bound-token gates ----

func TestQH3_Create_BoundTokenGates(t *testing.T) {
	h, wsID, crewID, _, _, chatID := covQH3Rig(t)

	t.Run("workspace mismatch", func(t *testing.T) {
		rr := covQH3Post(t, h, covQH3Body(wsID, crewID, chatID), "ws-elsewhere")
		if rr.Code != http.StatusForbidden {
			t.Errorf("status = %d, want 403; body=%s", rr.Code, rr.Body.String())
		}
	})
	t.Run("foreign crew", func(t *testing.T) {
		if _, err := h.db.Exec(`INSERT INTO workspaces (id, name, slug) VALUES ('ws-qh3-b', 'B', 'ws-qh3-b')`); err != nil {
			t.Fatalf("seed ws: %v", err)
		}
		foreignCrew := seedCrewRow(t, h.db, "crew-qh3-foreign", "ws-qh3-b", "F", "qh3-foreign")
		rr := covQH3Post(t, h, covQH3Body(wsID, foreignCrew, chatID), wsID)
		if rr.Code != http.StatusForbidden || !strings.Contains(rr.Body.String(), "crew does not belong") {
			t.Errorf("status = %d body=%s", rr.Code, rr.Body.String())
		}
	})
	t.Run("unknown chat", func(t *testing.T) {
		rr := covQH3Post(t, h, covQH3Body(wsID, crewID, "chat-ghost"), wsID)
		if rr.Code != http.StatusForbidden || !strings.Contains(rr.Body.String(), "chat does not belong") {
			t.Errorf("status = %d body=%s", rr.Code, rr.Body.String())
		}
	})
}

// ---- Create: credential decrypt failure ----

func TestQH3_Create_CredentialDecryptError500(t *testing.T) {
	setTestEncryptionKeyParallelSafe(t)
	h, wsID, crewID, _, targetID, chatID := covQH3Rig(t)
	userID := "test-user-id"
	if _, err := h.db.Exec(`
		INSERT INTO credentials (id, workspace_id, name, type, encrypted_value, created_by, created_at, updated_at)
		VALUES ('cred-qh3-bad', ?, 'bad', 'API_KEY', 'not-encrypted-garbage', ?, datetime('now'), datetime('now'))`,
		wsID, userID); err != nil {
		t.Fatalf("seed credential: %v", err)
	}
	if _, err := h.db.Exec(`
		INSERT INTO agent_credentials (id, agent_id, credential_id, env_var_name, priority, created_at)
		VALUES ('ac-qh3', ?, 'cred-qh3-bad', 'BAD_KEY', 1, datetime('now'))`, targetID); err != nil {
		t.Fatalf("seed agent_credentials: %v", err)
	}

	rr := covQH3Post(t, h, covQH3Body(wsID, crewID, chatID), "")
	if rr.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500; body=%s", rr.Code, rr.Body.String())
	}
}

// ---- Create: execution path past container creation ----

func TestQH3_Create_ExecutionError(t *testing.T) {
	h, wsID, crewID, _, _, chatID := covQH3Rig(t)
	orch := orchestrator.New(covAsg2Provider{}, nil, newTestLogger())
	orch.StopAccepting()
	h.orch = orch

	rr := covQH3Post(t, h, covQH3Body(wsID, crewID, chatID), "")
	if rr.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500; body=%s", rr.Code, rr.Body.String())
	}
	if !strings.Contains(rr.Body.String(), "query execution failed") {
		t.Errorf("body = %q", rr.Body.String())
	}

	// The peer conversation must be FAILED with the execution error recorded.
	var status, errMsg string
	if err := h.db.QueryRow(`
		SELECT status, COALESCE(response, '') FROM peer_conversations WHERE chat_id = ?`, chatID).
		Scan(&status, &errMsg); err != nil {
		t.Fatalf("query peer_conversations: %v", err)
	}
	if status != "FAILED" {
		t.Errorf("peer conversation status = %q, want FAILED", status)
	}
}

func TestQH3_Create_BackupGuard409(t *testing.T) {
	h, wsID, crewID, _, _, chatID := covQH3Rig(t)
	orch := orchestrator.New(covAsg2Provider{}, nil, newTestLogger())
	orch.StopAccepting() // safety net: must not be reached when the guard fires
	h.orch = orch

	expires := time.Now().Add(10 * time.Minute).UTC().Format(time.RFC3339)
	if _, err := h.db.Exec(`
		INSERT INTO backup_locks (workspace_id, acquired_at, acquired_by, expires_at)
		VALUES (?, datetime('now'), 'test', ?)`, wsID, expires); err != nil {
		t.Fatalf("insert backup lock: %v", err)
	}

	rr := covQH3Post(t, h, covQH3Body(wsID, crewID, chatID), "")
	if rr.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409; body=%s", rr.Code, rr.Body.String())
	}
	if !strings.Contains(rr.Body.String(), "backed up") {
		t.Errorf("body = %q", rr.Body.String())
	}
}
