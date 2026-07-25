package api

import (
	"bytes"
	"database/sql"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/keeper"
)

// seedCredentialEscalation stages the "agent proposed a credential, a human is
// about to approve it" state: an L3 credential sitting PENDING_APPROVAL and a
// PENDING CREDENTIAL escalation linking it. Returns the escalation + credential
// ids.
func seedCredentialEscalation(t *testing.T, db *sql.DB, userID, wsID, crewID, agentID string) (escID, credID string) {
	t.Helper()
	setTestEncryptionKey(t)

	chatID := generateCUID()
	execOrFatal(t, db,
		`INSERT INTO chats(id,agent_id,workspace_id,mode,status) VALUES (?, ?, ?, 'CHAT', 'ACTIVE')`,
		chatID, agentID, wsID)

	credID = generateCUID()
	execOrFatal(t, db, `
		INSERT INTO credentials (id, workspace_id, name, encrypted_value, type, provider, scope,
			security_level, status, created_by, created_by_actor_type, created_by_actor_id)
		VALUES (?, ?, 'Deploy Key', 'v1:aW52YWxpZA==', 'SECRET', 'NONE', 'WORKSPACE', 3, 'PENDING_APPROVAL', ?, 'agent', ?)`,
		credID, wsID, userID, agentID)

	escID = generateCUID()
	execOrFatal(t, db, `
		INSERT INTO escalations (id, workspace_id, crew_id, chat_id, from_agent_id, reason, status, type, credential_id, created_at)
		VALUES (?, ?, ?, ?, ?, 'need a deploy key', 'PENDING', 'CREDENTIAL', ?, datetime('now'))`,
		escID, wsID, crewID, chatID, agentID, credID)
	return escID, credID
}

func approveEscalation(t *testing.T, h *QueryHandler, userID, wsID, escID string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest("PATCH", "/", bytes.NewBufferString(`{"resolution":"granted","action":"approve"}`))
	req.SetPathValue("escalationId", escID)
	req = req.WithContext(withWorkspace(withUser(req.Context(), &AuthUser{ID: userID}), wsID, "OWNER"))
	rr := httptest.NewRecorder()
	h.ResolveEscalation(rr, req)
	return rr
}

// TestEscalationApprove_MintsLeasedGrant is the third minting site from #1373:
// "issue a lease on approval (Keeper ALLOW / escalation approve)".
//
// Approving an agent-proposed credential activates it but — before this change —
// created no agent_credentials row at all, so the proposing agent still could not
// reach the value through any delivery path. With the workspace opted into
// auto-lease, the approval now issues a TIME-BOXED grant.
func TestEscalationApprove_MintsLeasedGrant(t *testing.T) {
	h, userID, wsID, crewID, leadID, _ := newQueryHandler(t)
	escID, credID := seedCredentialEscalation(t, h.db, userID, wsID, crewID, leadID)
	enableAutoLease(t, h.db, wsID, 900)

	if rr := approveEscalation(t, h, userID, wsID, escID); rr.Code != http.StatusOK {
		t.Fatalf("approve: status = %d body=%s", rr.Code, rr.Body.String())
	}

	var expiresAt, source, reqID sql.NullString
	var envVar string
	err := h.db.QueryRow(
		`SELECT expires_at, lease_source, lease_request_id, env_var_name FROM agent_credentials
		  WHERE agent_id = ? AND credential_id = ?`, leadID, credID).
		Scan(&expiresAt, &source, &reqID, &envVar)
	if err == sql.ErrNoRows {
		t.Fatal("approve did not grant the proposing agent access to the credential it proposed")
	}
	if err != nil {
		t.Fatalf("read grant: %v", err)
	}
	if !expiresAt.Valid || expiresAt.String == "" {
		t.Fatal("the grant created on approve must be a LEASE, not a standing grant")
	}
	exp, perr := time.Parse(time.RFC3339, expiresAt.String)
	if perr != nil {
		t.Fatalf("expires_at %q is not RFC3339: %v", expiresAt.String, perr)
	}
	if d := time.Until(exp); d <= 10*time.Minute || d > 16*time.Minute {
		t.Errorf("lease expires in %v, want ~15m", d)
	}
	if source.String != leaseSourceEscalationApprove {
		t.Errorf("lease_source = %q, want %q", source.String, leaseSourceEscalationApprove)
	}
	if reqID.String != escID {
		t.Errorf("lease_request_id = %q, want the escalation id %q", reqID.String, escID)
	}
	// The env var must be derived with the same sanitizer /keeper/execute falls
	// back to, so "Deploy Key" resolves identically on both sides.
	if envVar != "DEPLOY_KEY" {
		t.Errorf("env_var_name = %q, want DEPLOY_KEY", envVar)
	}
}

// TestEscalationApprove_AutoLeaseOffGrantsNothing pins the opt-in boundary. With
// auto-lease off, approve must behave exactly as before — it activates the
// credential and creates no grant. Creating an untimed grant here would be a
// silent authorization change smuggled in with a lease PR.
func TestEscalationApprove_AutoLeaseOffGrantsNothing(t *testing.T) {
	h, userID, wsID, crewID, leadID, _ := newQueryHandler(t)
	escID, credID := seedCredentialEscalation(t, h.db, userID, wsID, crewID, leadID)
	// Deliberately NOT enabling auto-lease.

	if rr := approveEscalation(t, h, userID, wsID, escID); rr.Code != http.StatusOK {
		t.Fatalf("approve: status = %d body=%s", rr.Code, rr.Body.String())
	}

	var n int
	if err := h.db.QueryRow(
		`SELECT COUNT(*) FROM agent_credentials WHERE agent_id = ? AND credential_id = ?`,
		leadID, credID).Scan(&n); err != nil {
		t.Fatalf("count grants: %v", err)
	}
	if n != 0 {
		t.Fatalf("grants created with auto-lease off = %d, want 0 (unchanged behaviour)", n)
	}
	// The credential itself must still have been activated — the approval's
	// existing job is untouched.
	var status string
	if err := h.db.QueryRow(`SELECT status FROM credentials WHERE id = ?`, credID).Scan(&status); err != nil {
		t.Fatalf("read credential: %v", err)
	}
	if status != "ACTIVE" {
		t.Errorf("credential status = %q, want ACTIVE", status)
	}
}

// TestEscalationApprove_RejectGrantsNothing: only an approve issues a lease.
func TestEscalationApprove_RejectGrantsNothing(t *testing.T) {
	h, userID, wsID, crewID, leadID, _ := newQueryHandler(t)
	escID, credID := seedCredentialEscalation(t, h.db, userID, wsID, crewID, leadID)
	enableAutoLease(t, h.db, wsID, 900)

	req := httptest.NewRequest("PATCH", "/", bytes.NewBufferString(`{"resolution":"no","action":"reject"}`))
	req.SetPathValue("escalationId", escID)
	req = req.WithContext(withWorkspace(withUser(req.Context(), &AuthUser{ID: userID}), wsID, "OWNER"))
	rr := httptest.NewRecorder()
	h.ResolveEscalation(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("reject: status = %d body=%s", rr.Code, rr.Body.String())
	}

	var n int
	if err := h.db.QueryRow(
		`SELECT COUNT(*) FROM agent_credentials WHERE agent_id = ? AND credential_id = ?`,
		leadID, credID).Scan(&n); err != nil {
		t.Fatalf("count grants: %v", err)
	}
	if n != 0 {
		t.Fatalf("a rejected escalation granted %d credential(s), want 0", n)
	}
}

// TestEscalationApprove_L2CredentialNotGranted keeps the L3 floor consistent
// across all three minting sites: a low-tier credential is not auto-leased, so
// approve must not create a grant it cannot time-box either.
func TestEscalationApprove_L2CredentialNotGranted(t *testing.T) {
	h, userID, wsID, crewID, leadID, _ := newQueryHandler(t)
	escID, credID := seedCredentialEscalation(t, h.db, userID, wsID, crewID, leadID)
	if _, err := h.db.Exec(`UPDATE credentials SET security_level = ? WHERE id = ?`,
		int(keeper.SecurityLevelL2), credID); err != nil {
		t.Fatalf("downgrade security level: %v", err)
	}
	enableAutoLease(t, h.db, wsID, 900)

	if rr := approveEscalation(t, h, userID, wsID, escID); rr.Code != http.StatusOK {
		t.Fatalf("approve: status = %d body=%s", rr.Code, rr.Body.String())
	}

	var n int
	if err := h.db.QueryRow(
		`SELECT COUNT(*) FROM agent_credentials WHERE agent_id = ? AND credential_id = ?`,
		leadID, credID).Scan(&n); err != nil {
		t.Fatalf("count grants: %v", err)
	}
	if n != 0 {
		t.Fatalf("an L2 credential must not be auto-granted/leased on approve, got %d grants", n)
	}
}
