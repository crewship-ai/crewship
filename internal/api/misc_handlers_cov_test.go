package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/crewship-ai/crewship/internal/encryption"
)

// ── escalation_handler.go: remaining branches ─────────────────────────────

func TestCovMisc_PendingEscalationCount_DBError(t *testing.T) {
	h, userID, wsID, _, _, _ := newQueryHandler(t)
	if err := h.db.Close(); err != nil {
		t.Fatalf("close db: %v", err)
	}
	req := httptest.NewRequest("GET", "/", nil)
	req = req.WithContext(withWorkspace(withUser(req.Context(), &AuthUser{ID: userID}), wsID, "OWNER"))
	rr := httptest.NewRecorder()
	h.PendingEscalationCount(rr, req)
	if rr.Code != http.StatusInternalServerError {
		t.Errorf("status = %d, want 500", rr.Code)
	}
}

func TestCovMisc_CreateEscalation_CredentialType(t *testing.T) {
	h, _, wsID, crewID, leadID, _ := newQueryHandler(t)
	chatID := generateCUID()
	if _, err := h.db.Exec(`INSERT INTO chats(id,agent_id,workspace_id,mode,status) VALUES (?, ?, ?, 'CHAT', 'ACTIVE')`, chatID, leadID, wsID); err != nil {
		t.Fatalf("insert chat: %v", err)
	}

	// CREDENTIAL type with context+metadata exercises the contextVal/metadataVal
	// non-nil branches plus the explicit-type acceptance path.
	body := bytes.NewBufferString(`{"from_slug":"lead","reason":"need token","context":"why","metadata":"hint","type":"CREDENTIAL","crew_id":"` + crewID + `","workspace_id":"` + wsID + `","chat_id":"` + chatID + `"}`)
	req := httptest.NewRequest("POST", "/", body)
	rr := httptest.NewRecorder()
	h.CreateEscalation(rr, req)
	if rr.Code != http.StatusCreated {
		t.Fatalf("status = %d body=%s", rr.Code, rr.Body.String())
	}
}

func TestCovMisc_CreateEscalation_InsertDBError(t *testing.T) {
	h, _, wsID, crewID, _, _ := newQueryHandler(t)
	// The from-agent lookup must succeed before the INSERT, so dropping only
	// the escalations table lets the agent lookup pass and the INSERT 500.
	if _, err := h.db.Exec(`DROP TABLE escalations`); err != nil {
		t.Fatalf("drop escalations: %v", err)
	}
	body := bytes.NewBufferString(`{"from_slug":"lead","reason":"x","crew_id":"` + crewID + `","workspace_id":"` + wsID + `","chat_id":"c1"}`)
	req := httptest.NewRequest("POST", "/", body)
	rr := httptest.NewRecorder()
	h.CreateEscalation(rr, req)
	if rr.Code != http.StatusInternalServerError {
		t.Errorf("status = %d, want 500 body=%s", rr.Code, rr.Body.String())
	}
}

func TestCovMisc_ResolveEscalation_RedirectSuccess(t *testing.T) {
	h, userID, wsID, crewID, leadID, _ := newQueryHandler(t)
	chatID := generateCUID()
	if _, err := h.db.Exec(`INSERT INTO chats(id,agent_id,workspace_id,mode,status) VALUES (?, ?, ?, 'CHAT', 'ACTIVE')`, chatID, leadID, wsID); err != nil {
		t.Fatalf("insert chat: %v", err)
	}
	if _, err := h.db.Exec(`INSERT INTO escalations(id,workspace_id,crew_id,chat_id,from_agent_id,reason,status,type,created_at)
		VALUES ('e-redir-ok', ?, ?, ?, ?, 'help', 'PENDING', 'TEXT', datetime('now'))`, wsID, crewID, chatID, leadID); err != nil {
		t.Fatalf("insert escalation: %v", err)
	}

	// redirect_to=worker exists in the same crew (seedIssueFixtures seeds slug "worker").
	body := bytes.NewBufferString(`{"resolution":"hand off","action":"redirect","redirect_to":"worker"}`)
	req := httptest.NewRequest("PATCH", "/", body)
	req.SetPathValue("escalationId", "e-redir-ok")
	req = req.WithContext(withWorkspace(withUser(req.Context(), &AuthUser{ID: userID}), wsID, "OWNER"))
	rr := httptest.NewRecorder()
	h.ResolveEscalation(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rr.Code, rr.Body.String())
	}
	var resp map[string]string
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if resp["action"] != "redirect" {
		t.Errorf("action = %q want redirect", resp["action"])
	}
}

func TestCovMisc_ResolveEscalation_CredentialEncrypts(t *testing.T) {
	setTestEncryptionKeyParallelSafe(t)
	h, userID, wsID, crewID, leadID, _ := newQueryHandler(t)
	chatID := generateCUID()
	if _, err := h.db.Exec(`INSERT INTO chats(id,agent_id,workspace_id,mode,status) VALUES (?, ?, ?, 'CHAT', 'ACTIVE')`, chatID, leadID, wsID); err != nil {
		t.Fatalf("insert chat: %v", err)
	}
	if _, err := h.db.Exec(`INSERT INTO escalations(id,workspace_id,crew_id,chat_id,from_agent_id,reason,status,type,created_at)
		VALUES ('e-cred', ?, ?, ?, ?, 'secret please', 'PENDING', 'CREDENTIAL', datetime('now'))`, wsID, crewID, chatID, leadID); err != nil {
		t.Fatalf("insert escalation: %v", err)
	}

	body := bytes.NewBufferString(`{"resolution":"s3cr3t-value","action":"approve"}`)
	req := httptest.NewRequest("PATCH", "/", body)
	req.SetPathValue("escalationId", "e-cred")
	req = req.WithContext(withWorkspace(withUser(req.Context(), &AuthUser{ID: userID}), wsID, "OWNER"))
	rr := httptest.NewRecorder()
	h.ResolveEscalation(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rr.Code, rr.Body.String())
	}

	// The stored resolution must be encrypted, not the plaintext.
	var stored string
	if err := h.db.QueryRow(`SELECT resolution FROM escalations WHERE id = 'e-cred'`).Scan(&stored); err != nil {
		t.Fatalf("read back: %v", err)
	}
	if stored == "s3cr3t-value" {
		t.Error("credential resolution stored in plaintext; expected encrypted-at-rest")
	}
	dec, err := encryption.Decrypt(stored)
	if err != nil {
		t.Fatalf("decrypt: %v", err)
	}
	if dec != "s3cr3t-value" {
		t.Errorf("decrypt = %q, want s3cr3t-value", dec)
	}
}

func TestCovMisc_ResolveEscalation_RedirectLookupDBError(t *testing.T) {
	h, userID, wsID, crewID, leadID, _ := newQueryHandler(t)
	chatID := generateCUID()
	if _, err := h.db.Exec(`INSERT INTO chats(id,agent_id,workspace_id,mode,status) VALUES (?, ?, ?, 'CHAT', 'ACTIVE')`, chatID, leadID, wsID); err != nil {
		t.Fatalf("insert chat: %v", err)
	}
	if _, err := h.db.Exec(`INSERT INTO escalations(id,workspace_id,crew_id,chat_id,from_agent_id,reason,status,type,created_at)
		VALUES ('e-redir-err', ?, ?, ?, ?, 'help', 'PENDING', 'TEXT', datetime('now'))`, wsID, crewID, chatID, leadID); err != nil {
		t.Fatalf("insert escalation: %v", err)
	}
	if err := h.db.Close(); err != nil {
		t.Fatalf("close db: %v", err)
	}
	body := bytes.NewBufferString(`{"resolution":"go","action":"redirect","redirect_to":"worker"}`)
	req := httptest.NewRequest("PATCH", "/", body)
	req.SetPathValue("escalationId", "e-redir-err")
	req = req.WithContext(withWorkspace(withUser(req.Context(), &AuthUser{ID: userID}), wsID, "OWNER"))
	rr := httptest.NewRecorder()
	h.ResolveEscalation(rr, req)
	if rr.Code != http.StatusInternalServerError {
		t.Errorf("status = %d, want 500", rr.Code)
	}
}

func TestCovMisc_ListEscalations_CredentialMaskedAndDBError(t *testing.T) {
	t.Run("masks credential resolution", func(t *testing.T) {
		setTestEncryptionKeyParallelSafe(t)
		h, userID, wsID, crewID, leadID, _ := newQueryHandler(t)
		chatID := generateCUID()
		if _, err := h.db.Exec(`INSERT INTO chats(id,agent_id,workspace_id,mode,status) VALUES (?, ?, ?, 'CHAT', 'ACTIVE')`, chatID, leadID, wsID); err != nil {
			t.Fatalf("insert chat: %v", err)
		}
		if _, err := h.db.Exec(`INSERT INTO escalations(id,workspace_id,crew_id,chat_id,from_agent_id,reason,status,type,resolution,created_at)
			VALUES ('e-mask', ?, ?, ?, ?, 'r', 'RESOLVED', 'CREDENTIAL', 'enc-blob', datetime('now'))`, wsID, crewID, chatID, leadID); err != nil {
			t.Fatalf("insert escalation: %v", err)
		}

		req := httptest.NewRequest("GET", "/?limit=10", nil)
		req.SetPathValue("crewId", crewID)
		req = req.WithContext(withWorkspace(withUser(req.Context(), &AuthUser{ID: userID}), wsID, "OWNER"))
		rr := httptest.NewRecorder()
		h.ListEscalations(rr, req)
		if rr.Code != http.StatusOK {
			t.Fatalf("status = %d body=%s", rr.Code, rr.Body.String())
		}
		if !bytes.Contains(rr.Body.Bytes(), []byte("[credential submitted]")) {
			t.Errorf("credential resolution not masked: %s", rr.Body.String())
		}
		if bytes.Contains(rr.Body.Bytes(), []byte("enc-blob")) {
			t.Error("raw credential blob leaked into list response")
		}
	})

	t.Run("db error 500", func(t *testing.T) {
		h, userID, wsID, crewID, _, _ := newQueryHandler(t)
		if err := h.db.Close(); err != nil {
			t.Fatalf("close db: %v", err)
		}
		req := httptest.NewRequest("GET", "/?limit=10", nil)
		req.SetPathValue("crewId", crewID)
		req = req.WithContext(withWorkspace(withUser(req.Context(), &AuthUser{ID: userID}), wsID, "OWNER"))
		rr := httptest.NewRecorder()
		h.ListEscalations(rr, req)
		if rr.Code != http.StatusInternalServerError {
			t.Errorf("status = %d, want 500", rr.Code)
		}
	})
}

// ── escalation_waiter.go: remaining branches ──────────────────────────────

func TestCovMisc_WaitForEscalation_DBError(t *testing.T) {
	h, _, _, _, _, _ := newQueryHandler(t)
	if err := h.db.Close(); err != nil {
		t.Fatalf("close db: %v", err)
	}
	req := httptest.NewRequest("GET", "/", nil)
	req.SetPathValue("escalationId", "anything")
	rr := httptest.NewRecorder()
	h.WaitForEscalationResponse(rr, req)
	if rr.Code != http.StatusInternalServerError {
		t.Errorf("status = %d, want 500", rr.Code)
	}
}

func TestCovMisc_WaitForEscalation_CredentialDecrypt(t *testing.T) {
	setTestEncryptionKeyParallelSafe(t)
	h, _, wsID, crewID, leadID, _ := newQueryHandler(t)
	chatID := generateCUID()
	if _, err := h.db.Exec(`INSERT INTO chats(id,agent_id,workspace_id,mode,status) VALUES (?, ?, ?, 'CHAT', 'ACTIVE')`, chatID, leadID, wsID); err != nil {
		t.Fatalf("insert chat: %v", err)
	}

	enc, err := encryption.Encrypt("the-secret")
	if err != nil {
		t.Fatalf("encrypt: %v", err)
	}
	if _, err := h.db.Exec(`INSERT INTO escalations(id,workspace_id,crew_id,chat_id,from_agent_id,reason,status,type,resolution,action,created_at)
		VALUES ('e-wcred', ?, ?, ?, ?, 'r', 'RESOLVED', 'CREDENTIAL', ?, 'approve', datetime('now'))`, wsID, crewID, chatID, leadID, enc); err != nil {
		t.Fatalf("insert escalation: %v", err)
	}

	req := httptest.NewRequest("GET", "/", nil)
	req.SetPathValue("escalationId", "e-wcred")
	rr := httptest.NewRecorder()
	h.WaitForEscalationResponse(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rr.Code, rr.Body.String())
	}
	var resp map[string]interface{}
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if resp["resolution"] != "the-secret" {
		t.Errorf("resolution = %v, want decrypted plaintext", resp["resolution"])
	}
}

func TestCovMisc_WaitForEscalation_ChannelDelivery(t *testing.T) {
	h, _, wsID, crewID, leadID, _ := newQueryHandler(t)
	chatID := generateCUID()
	if _, err := h.db.Exec(`INSERT INTO chats(id,agent_id,workspace_id,mode,status) VALUES (?, ?, ?, 'CHAT', 'ACTIVE')`, chatID, leadID, wsID); err != nil {
		t.Fatalf("insert chat: %v", err)
	}
	if _, err := h.db.Exec(`INSERT INTO escalations(id,workspace_id,crew_id,chat_id,from_agent_id,reason,status,type,created_at)
		VALUES ('e-chan', ?, ?, ?, ?, 'h', 'PENDING', 'TEXT', datetime('now'))`, wsID, crewID, chatID, leadID); err != nil {
		t.Fatalf("insert escalation: %v", err)
	}

	done := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		req := httptest.NewRequest("GET", "/", nil)
		req.SetPathValue("escalationId", "e-chan")
		rr := httptest.NewRecorder()
		h.WaitForEscalationResponse(rr, req)
		done <- rr
	}()

	// Wait until a waiter has registered, then notify it directly to drive the
	// `case result := <-ch` arm of the select.
	for {
		h.escalationMu.Lock()
		_, ok := h.escalationWaiters["e-chan"]
		h.escalationMu.Unlock()
		if ok {
			break
		}
	}
	h.notifyEscalationWaiter("e-chan", escalationResult{Resolution: "approved!", Action: "approve"})

	rr := <-done
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rr.Code, rr.Body.String())
	}
	var resp map[string]interface{}
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if resp["resolution"] != "approved!" {
		t.Errorf("resolution = %v, want approved!", resp["resolution"])
	}
}

// ── mcp_audit.go: DB-error branch ─────────────────────────────────────────

func TestCovMisc_MCPAudit_List_DBError(t *testing.T) {
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	if err := db.Close(); err != nil {
		t.Fatalf("close db: %v", err)
	}
	h := NewMCPAuditHandler(db, testLogger())
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req = req.WithContext(withWorkspace(req.Context(), wsID, "OWNER"))
	rr := httptest.NewRecorder()
	h.List(rr, req)
	if rr.Code != http.StatusInternalServerError {
		t.Errorf("status = %d, want 500", rr.Code)
	}
}

// ── confidence_handler.go: autoEscalateForConfidence dedup + notify ────────

func TestCovMisc_Confidence_AutoEscalate_DedupAndNotify(t *testing.T) {
	t.Run("dedup: second low report does not double-escalate", func(t *testing.T) {
		h, _, wsID, crewID, leadID, workerID := newQueryHandler(t)
		missionID := generateCUID()
		if _, err := h.db.Exec(`INSERT INTO chats(id,agent_id,workspace_id,mode,status) VALUES (?, ?, ?, 'MISSION', 'ACTIVE')`, missionID, leadID, wsID); err != nil {
			t.Fatalf("insert chat: %v", err)
		}
		if _, err := h.db.Exec(`UPDATE crews SET escalation_config='{"require_approval_below":0.5}' WHERE id=?`, crewID); err != nil {
			t.Fatalf("update crew: %v", err)
		}
		if _, err := h.db.Exec(`INSERT INTO missions(id,workspace_id,crew_id,lead_agent_id,trace_id,title,status,created_at,updated_at)
			VALUES (?, ?, ?, ?, ?, 'M', 'IN_PROGRESS', datetime('now'), datetime('now'))`, missionID, wsID, crewID, leadID, "trace-"+missionID); err != nil {
			t.Fatalf("insert mission: %v", err)
		}
		tID := generateCUID()
		if _, err := h.db.Exec(`INSERT INTO mission_tasks(id,mission_id,assigned_agent_id,title,status,task_order,depends_on,created_at,updated_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, datetime('now'), datetime('now'))`,
			tID, missionID, workerID, "T", "IN_PROGRESS", 1, "[]"); err != nil {
			t.Fatalf("insert mission_task: %v", err)
		}

		bodyStr := `{"agent_id":"` + workerID + `","crew_id":"` + crewID + `","confidence":0.2,"reason":"low"}`
		for i := 0; i < 2; i++ {
			req := httptest.NewRequest("POST", "/", bytes.NewBufferString(bodyStr))
			rr := httptest.NewRecorder()
			h.ReportConfidence(rr, req)
			if rr.Code != http.StatusOK {
				t.Fatalf("iter %d: status = %d body=%s", i, rr.Code, rr.Body.String())
			}
		}

		var n int
		if err := h.db.QueryRow(`SELECT COUNT(*) FROM escalations WHERE crew_id = ?
			AND json_extract(metadata,'$.source') = 'auto_confidence_gate'`, crewID).Scan(&n); err != nil {
			t.Fatalf("count escalations: %v", err)
		}
		if n != 1 {
			t.Errorf("auto-escalations = %d, want 1 (dedup must suppress the second)", n)
		}
	})

	t.Run("notify threshold only", func(t *testing.T) {
		h, _, wsID, crewID, leadID, workerID := newQueryHandler(t)
		missionID := generateCUID()
		if _, err := h.db.Exec(`INSERT INTO chats(id,agent_id,workspace_id,mode,status) VALUES (?, ?, ?, 'MISSION', 'ACTIVE')`, missionID, leadID, wsID); err != nil {
			t.Fatalf("insert chat: %v", err)
		}
		// notify at 0.8, require-approval at 0.5 — confidence 0.6 should notify, not escalate.
		if _, err := h.db.Exec(`UPDATE crews SET escalation_config='{"require_approval_below":0.5,"notify_threshold":0.8}' WHERE id=?`, crewID); err != nil {
			t.Fatalf("update crew: %v", err)
		}
		if _, err := h.db.Exec(`INSERT INTO missions(id,workspace_id,crew_id,lead_agent_id,trace_id,title,status,created_at,updated_at)
			VALUES (?, ?, ?, ?, ?, 'M', 'IN_PROGRESS', datetime('now'), datetime('now'))`, missionID, wsID, crewID, leadID, "trace-"+missionID); err != nil {
			t.Fatalf("insert mission: %v", err)
		}
		tID := generateCUID()
		if _, err := h.db.Exec(`INSERT INTO mission_tasks(id,mission_id,assigned_agent_id,title,status,task_order,depends_on,created_at,updated_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, datetime('now'), datetime('now'))`,
			tID, missionID, workerID, "T", "IN_PROGRESS", 1, "[]"); err != nil {
			t.Fatalf("insert mission_task: %v", err)
		}

		body := bytes.NewBufferString(`{"agent_id":"` + workerID + `","crew_id":"` + crewID + `","confidence":0.6,"reason":"meh"}`)
		req := httptest.NewRequest("POST", "/", body)
		rr := httptest.NewRecorder()
		h.ReportConfidence(rr, req)
		if rr.Code != http.StatusOK {
			t.Fatalf("status = %d body=%s", rr.Code, rr.Body.String())
		}
		var resp map[string]interface{}
		if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		if resp["action"] != "notified" {
			t.Errorf("action = %v, want notified", resp["action"])
		}
	})
}

// ── memory_health_handler.go ───────────────────────────────────────────────

func TestCovMisc_MemoryHealth_Unauthorized(t *testing.T) {
	db := setupTestDB(t)
	h := NewMemoryHealthHandler(db, newTestLogger())
	req := httptest.NewRequest(http.MethodGet, "/", nil) // no workspace in context
	rr := httptest.NewRecorder()
	h.Get(rr, req)
	if rr.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401", rr.Code)
	}
}

func TestCovMisc_MemoryHealth_CrewNotFound(t *testing.T) {
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	h := NewMemoryHealthHandler(db, newTestLogger())
	req := httptest.NewRequest(http.MethodGet, "/?crew_id=does-not-exist", nil)
	req = req.WithContext(withWorkspace(withUser(req.Context(), &AuthUser{ID: userID}), wsID, "OWNER"))
	rr := httptest.NewRecorder()
	h.Get(rr, req)
	if rr.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", rr.Code)
	}
}

func TestCovMisc_MemoryHealth_CrewLookupDBError(t *testing.T) {
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	if err := db.Close(); err != nil {
		t.Fatalf("close db: %v", err)
	}
	h := NewMemoryHealthHandler(db, newTestLogger())
	req := httptest.NewRequest(http.MethodGet, "/?crew_id=c1", nil)
	req = req.WithContext(withWorkspace(withUser(req.Context(), &AuthUser{ID: userID}), wsID, "OWNER"))
	rr := httptest.NewRecorder()
	h.Get(rr, req)
	if rr.Code != http.StatusInternalServerError {
		t.Errorf("status = %d, want 500", rr.Code)
	}
}

// ── setup_status.go ────────────────────────────────────────────────────────

func TestCovMisc_SetupStatus_NeedsBootstrap(t *testing.T) {
	db := setupTestDB(t)
	h := NewSetupStatusHandler(db, newTestLogger(), true)
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rr := httptest.NewRecorder()
	h.Status(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d", rr.Code)
	}
	var resp setupStatusResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if !resp.NeedsBootstrap {
		t.Error("empty users table must report needs_bootstrap=true")
	}
	if !resp.AllowSignup {
		t.Error("allow_signup must mirror the constructor flag (true)")
	}
}

func TestCovMisc_SetupStatus_AlreadyInitialized(t *testing.T) {
	db := setupTestDB(t)
	seedTestUser(t, db) // now one user exists
	h := NewSetupStatusHandler(db, newTestLogger(), false)
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rr := httptest.NewRecorder()
	h.Status(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d", rr.Code)
	}
	var resp setupStatusResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if resp.NeedsBootstrap {
		t.Error("non-empty users table must report needs_bootstrap=false")
	}
	if resp.AllowSignup {
		t.Error("allow_signup must mirror the constructor flag (false)")
	}
}

func TestCovMisc_SetupStatus_DBErrorFallsBackToFalse(t *testing.T) {
	db := setupTestDB(t)
	if err := db.Close(); err != nil {
		t.Fatalf("close db: %v", err)
	}
	h := NewSetupStatusHandler(db, newTestLogger(), true)
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rr := httptest.NewRecorder()
	h.Status(rr, req)
	// A DB blip must NOT bounce the user into the bootstrap flow: 200 + false.
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 even on DB error", rr.Code)
	}
	var resp setupStatusResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if resp.NeedsBootstrap {
		t.Error("DB error must fall back to needs_bootstrap=false")
	}
	if !resp.AllowSignup {
		t.Error("allow_signup must still mirror the flag on the error path")
	}
}

// ── system.go: Version ─────────────────────────────────────────────────────

func TestCovMisc_SystemVersion_Unauthorized(t *testing.T) {
	h := NewSystemHandler(newTestLogger(), "v1.2.3")
	req := httptest.NewRequest(http.MethodGet, "/", nil) // no user in context
	rr := httptest.NewRecorder()
	h.Version(rr, req)
	if rr.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401", rr.Code)
	}
}

func TestCovMisc_SystemVersion_Authenticated(t *testing.T) {
	h := NewSystemHandler(newTestLogger(), "v1.2.3")
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req = req.WithContext(withUser(req.Context(), &AuthUser{ID: "u1"}))
	rr := httptest.NewRecorder()
	h.Version(rr, req)
	// update.Check may hit the network or a warm cache; either way the handler
	// degrades gracefully to 200 with a "current" field. We only assert the
	// auth-passed contract and the always-present current version.
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d", rr.Code)
	}
	var resp map[string]interface{}
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if resp["current"] != "v1.2.3" {
		t.Errorf("current = %v, want v1.2.3", resp["current"])
	}
}
