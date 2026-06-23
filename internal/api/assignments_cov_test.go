package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/journal"
	"github.com/crewship-ai/crewship/internal/orchestrator"
)

// assignments_cov_test.go covers the remaining branches of
// assignments.go: setters, credential loading (incl. the decrypt-skip
// path), closed-DB 500s on List/Get, and the DispatchAssignment
// queued path. All helpers here are prefixed covAsg.

type covAsgMissionCB struct{ called bool }

func (c *covAsgMissionCB) OnAssignmentCompleted(_ context.Context, _, _, _, _ string) error {
	c.called = true
	return nil
}

func TestCovAsg_SetMissionCallback(t *testing.T) {
	db := setupTestDB(t)
	h := NewAssignmentHandler(db, nil, nil, "tok", newTestLogger())
	cb := &covAsgMissionCB{}
	h.SetMissionCallback(cb)
	if h.missionCallback != MissionCallback(cb) {
		t.Fatalf("missionCallback not stored")
	}
}

func TestCovAsg_SetJournal_NilMapsToNoop(t *testing.T) {
	db := setupTestDB(t)
	h := NewAssignmentHandler(db, nil, nil, "tok", newTestLogger())

	h.SetJournal(nil)
	if _, ok := h.journal.(noopEmitter); !ok {
		t.Fatalf("SetJournal(nil) = %T, want noopEmitter", h.journal)
	}

	// Non-nil emitter is stored as-is.
	var em journal.Emitter = noopEmitter{}
	h.SetJournal(em)
	if h.journal == nil {
		t.Fatalf("SetJournal(non-nil) left journal nil")
	}
}

func TestCovAsg_LoadAgentCredentials_DecryptSkipAndSuccess(t *testing.T) {
	ensureEncryptionKey(t)
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	execOrFatal(t, db, `INSERT INTO crews (id, workspace_id, name, slug) VALUES ('covasg-crew', ?, 'C', 'covasg-c')`, wsID)
	execOrFatal(t, db, `INSERT INTO agents (id, crew_id, workspace_id, name, slug) VALUES ('covasg-ag', 'covasg-crew', ?, 'A', 'covasg-a')`, wsID)

	// One credential that decrypts fine, one with garbage ciphertext.
	seedCredentialEnc(t, db, wsID, userID, "covasg-cred-ok", "covasg-ok", "plain-token-value")
	execOrFatal(t, db, `
		INSERT INTO credentials (id, workspace_id, name, encrypted_value, type, provider, scope, status, created_by, created_at, updated_at)
		VALUES ('covasg-cred-bad', ?, 'covasg-bad', 'not-a-ciphertext', 'SECRET', 'GITHUB', 'WORKSPACE', 'ACTIVE', ?, datetime('now'), datetime('now'))`,
		wsID, userID)
	execOrFatal(t, db, `INSERT INTO agent_credentials (id, agent_id, credential_id, env_var_name, priority, created_at)
		VALUES ('covasg-ac1', 'covasg-ag', 'covasg-cred-ok', 'TOKEN_OK', 0, datetime('now'))`)
	execOrFatal(t, db, `INSERT INTO agent_credentials (id, agent_id, credential_id, env_var_name, priority, created_at)
		VALUES ('covasg-ac2', 'covasg-ag', 'covasg-cred-bad', 'TOKEN_BAD', 1, datetime('now'))`)

	h := NewAssignmentHandler(db, nil, nil, "tok", newTestLogger())
	creds, err := h.loadAgentCredentials(context.Background(), "covasg-ag")
	if err != nil {
		t.Fatalf("loadAgentCredentials: %v", err)
	}
	// The bad-ciphertext credential is skipped, the good one survives
	// with its decrypted plaintext.
	if len(creds) != 1 {
		t.Fatalf("creds = %d, want 1 (bad ciphertext skipped)", len(creds))
	}
	if creds[0].EnvVarName != "TOKEN_OK" || creds[0].PlainValue != "plain-token-value" {
		t.Errorf("cred = %+v, want TOKEN_OK / plain-token-value", creds[0])
	}
}

func TestCovAsg_LoadAgentCredentials_QueryError(t *testing.T) {
	db := setupTestDB(t)
	h := NewAssignmentHandler(db, nil, nil, "tok", newTestLogger())
	db.Close()
	if _, err := h.loadAgentCredentials(context.Background(), "any"); err == nil {
		t.Fatalf("expected error on closed db")
	}
}

func TestCovAsg_List_DBError(t *testing.T) {
	db := setupTestDB(t)
	h := NewAssignmentHandler(db, nil, nil, "tok", newTestLogger())
	db.Close()

	req := httptest.NewRequest("GET", "/api/v1/crews/c1/assignments", nil)
	req.SetPathValue("crewId", "c1")
	req = req.WithContext(withWorkspace(req.Context(), "ws-x", "OWNER"))
	rr := httptest.NewRecorder()
	h.List(rr, req)
	if rr.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", rr.Code)
	}
}

func TestCovAsg_Get_DBError(t *testing.T) {
	db := setupTestDB(t)
	h := NewAssignmentHandler(db, nil, nil, "tok", newTestLogger())
	db.Close()

	req := httptest.NewRequest("GET", "/api/v1/internal/assignments/a1", nil)
	req.SetPathValue("assignmentId", "a1")
	rr := httptest.NewRecorder()
	h.Get(rr, req)
	if rr.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", rr.Code)
	}
}

func TestCovAsg_DispatchAssignment_UnknownAgent(t *testing.T) {
	db := setupTestDB(t)
	h := NewAssignmentHandler(db, nil, nil, "tok", newTestLogger())

	err := h.DispatchAssignment(context.Background(), orchestrator.DispatchRequest{
		AssignmentID: "as-x",
		AgentID:      "no-such-agent",
		Task:         "do things",
	})
	if err == nil || !strings.Contains(err.Error(), "lookup agent no-such-agent") {
		t.Fatalf("err = %v, want lookup agent error", err)
	}
}

func TestCovAsg_DispatchAssignment_QueuedWhenCrewAtBudget(t *testing.T) {
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	execOrFatal(t, db, `INSERT INTO crews (id, workspace_id, name, slug, max_concurrent_agents) VALUES ('covasg-q-crew', ?, 'Q', 'covasg-q', 1)`, wsID)
	execOrFatal(t, db, `INSERT INTO agents (id, crew_id, workspace_id, name, slug) VALUES ('covasg-busy', 'covasg-q-crew', ?, 'Busy', 'covasg-busy')`, wsID)
	execOrFatal(t, db, `INSERT INTO agents (id, crew_id, workspace_id, name, slug) VALUES ('covasg-tgt', 'covasg-q-crew', ?, 'Tgt', 'covasg-tgt')`, wsID)
	execOrFatal(t, db, `INSERT INTO chats (id, agent_id, workspace_id, mode, status) VALUES ('covasg-chat', 'covasg-busy', ?, 'CHAT', 'ACTIVE')`, wsID)
	// One RUNNING assignment exhausts the budget of 1.
	execOrFatal(t, db, `INSERT INTO assignments (id, workspace_id, chat_id, assigned_by_id, assigned_to_id, task, status, created_at)
		VALUES ('covasg-running', ?, 'covasg-chat', 'covasg-tgt', 'covasg-busy', 'busy work', 'RUNNING', datetime('now'))`, wsID)
	// The assignment being dispatched, still PENDING.
	execOrFatal(t, db, `INSERT INTO assignments (id, workspace_id, chat_id, assigned_by_id, assigned_to_id, task, status, created_at)
		VALUES ('covasg-new', ?, 'covasg-chat', 'covasg-busy', 'covasg-tgt', 'new work', 'PENDING', datetime('now'))`, wsID)

	h := NewAssignmentHandler(db, nil, nil, "tok", newTestLogger())
	err := h.DispatchAssignment(context.Background(), orchestrator.DispatchRequest{
		AssignmentID: "covasg-new",
		AgentID:      "covasg-tgt",
		CrewID:       "covasg-q-crew",
		WorkspaceID:  wsID,
		ChatID:       "covasg-chat",
		MissionID:    "m1",
		Task:         "new work",
		TraceID:      "trace-123", // exercises the trace-prefix branch
	})
	if err != nil {
		t.Fatalf("DispatchAssignment: %v", err)
	}

	var status string
	var queuedAt *string
	if qErr := db.QueryRow(`SELECT status, queued_at FROM assignments WHERE id = 'covasg-new'`).Scan(&status, &queuedAt); qErr != nil {
		t.Fatalf("read assignment: %v", qErr)
	}
	if status != "QUEUED" {
		t.Fatalf("status = %q, want QUEUED", status)
	}
	if queuedAt == nil {
		t.Fatalf("queued_at not stamped")
	}
}

func TestCovAsg_DispatchAssignment_BudgetFallbackOnScanError(t *testing.T) {
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	// max_concurrent_agents holds a non-integer value: computeCrewBudget's
	// Scan into NullInt64 fails -> fallback budget=1 branch.
	execOrFatal(t, db, `INSERT INTO crews (id, workspace_id, name, slug, max_concurrent_agents) VALUES ('covasg-b-crew', ?, 'B', 'covasg-b', 'bogus')`, wsID)
	execOrFatal(t, db, `INSERT INTO agents (id, crew_id, workspace_id, name, slug) VALUES ('covasg-b-busy', 'covasg-b-crew', ?, 'Busy', 'covasg-b-busy')`, wsID)
	execOrFatal(t, db, `INSERT INTO agents (id, crew_id, workspace_id, name, slug) VALUES ('covasg-b-tgt', 'covasg-b-crew', ?, 'Tgt', 'covasg-b-tgt')`, wsID)
	execOrFatal(t, db, `INSERT INTO chats (id, agent_id, workspace_id, mode, status) VALUES ('covasg-b-chat', 'covasg-b-busy', ?, 'CHAT', 'ACTIVE')`, wsID)
	execOrFatal(t, db, `INSERT INTO assignments (id, workspace_id, chat_id, assigned_by_id, assigned_to_id, task, status, created_at)
		VALUES ('covasg-b-running', ?, 'covasg-b-chat', 'covasg-b-tgt', 'covasg-b-busy', 'busy', 'RUNNING', datetime('now'))`, wsID)
	execOrFatal(t, db, `INSERT INTO assignments (id, workspace_id, chat_id, assigned_by_id, assigned_to_id, task, status, created_at)
		VALUES ('covasg-b-new', ?, 'covasg-b-chat', 'covasg-b-busy', 'covasg-b-tgt', 'new', 'PENDING', datetime('now'))`, wsID)

	h := NewAssignmentHandler(db, nil, nil, "tok", newTestLogger())
	err := h.DispatchAssignment(context.Background(), orchestrator.DispatchRequest{
		AssignmentID: "covasg-b-new",
		AgentID:      "covasg-b-tgt",
		CrewID:       "covasg-b-crew",
		WorkspaceID:  wsID,
		Task:         "new",
	})
	if err != nil {
		t.Fatalf("DispatchAssignment: %v", err)
	}
	var status string
	if qErr := db.QueryRow(`SELECT status FROM assignments WHERE id = 'covasg-b-new'`).Scan(&status); qErr != nil {
		t.Fatalf("read assignment: %v", qErr)
	}
	if status != "QUEUED" {
		t.Fatalf("status = %q, want QUEUED (budget fell back to 1, crew busy)", status)
	}
}

func TestCovAsg_LoadCrewMembers_QueryError(t *testing.T) {
	db := setupTestDB(t)
	h := NewAssignmentHandler(db, nil, nil, "tok", newTestLogger())
	db.Close()
	if members := h.loadCrewMembers(context.Background(), "crew-x", "ag-x"); members != nil {
		t.Fatalf("members = %v, want nil on query error", members)
	}
}
