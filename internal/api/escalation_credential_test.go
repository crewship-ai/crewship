package api

import (
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/encryption"
)

// --- parseCredentialProposal (pure) ---

func TestParseCredentialProposal(t *testing.T) {
	cases := []struct {
		name     string
		metadata string
		wantOK   bool
		wantType string // checked only when wantOK
		wantProv string
	}{
		{"full", `{"name":"DB","type":"API_KEY","provider":"GITHUB","value":"v"}`, true, "API_KEY", "GITHUB"},
		{"defaults", `{"name":"DB","value":"v"}`, true, "SECRET", "NONE"},
		{"missing value", `{"name":"DB","type":"SECRET"}`, false, "", ""},
		{"missing name", `{"value":"v"}`, false, "", ""},
		{"not json", `PG_PASSWORD=v`, false, "", ""},
		{"empty", ``, false, "", ""},
		{"link url", `https://example.com`, false, "", ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			p, ok := parseCredentialProposal(c.metadata)
			if ok != c.wantOK {
				t.Fatalf("ok = %v, want %v", ok, c.wantOK)
			}
			if ok {
				if p.Type != c.wantType {
					t.Errorf("type = %q, want %q", p.Type, c.wantType)
				}
				if p.Provider != c.wantProv {
					t.Errorf("provider = %q, want %q", p.Provider, c.wantProv)
				}
			}
		})
	}
}

func TestRedactedMetadata_StripsValue(t *testing.T) {
	p := credentialProposal{Name: "DB", Type: "SECRET", Provider: "NONE", Value: "sup3r-s3cr3t"}
	red := p.redactedMetadata("cred-123")
	if strings.Contains(red, "sup3r-s3cr3t") {
		t.Fatalf("redacted metadata leaked the secret value: %s", red)
	}
	if !strings.Contains(red, "cred-123") || !strings.Contains(red, "DB") {
		t.Errorf("redacted metadata should keep name + credential_id: %s", red)
	}
}

// --- createPendingCredential helper ---

func credRow(t *testing.T, h *QueryHandler, credID string) (status, encVal, actorType, actorID, createdBy string, deleted bool) {
	t.Helper()
	var del *string
	err := h.db.QueryRow(`SELECT status, encrypted_value, created_by_actor_type,
		created_by_actor_id, created_by, deleted_at FROM credentials WHERE id = ?`, credID).
		Scan(&status, &encVal, &actorType, &actorID, &createdBy, &del)
	if err != nil {
		t.Fatalf("load credential %s: %v", credID, err)
	}
	return status, encVal, actorType, actorID, createdBy, del != nil
}

func TestCreatePendingCredential_Success(t *testing.T) {
	ensureEncryptionKey(t)
	h, ownerID, wsID, _, agentID := covEscFixture(t)
	p := credentialProposal{Name: "REDIS_URL", Type: "SECRET", Provider: "NONE", Value: "redis://:p@h:6379/0"}
	credID, ok := h.createPendingCredential(context.Background(), wsID, agentID, p)
	if !ok || credID == "" {
		t.Fatalf("createPendingCredential ok=%v id=%q, want success", ok, credID)
	}
	status, encVal, actorType, actorID, createdBy, deleted := credRow(t, h, credID)
	if status != "PENDING_APPROVAL" {
		t.Errorf("status = %q, want PENDING_APPROVAL", status)
	}
	if deleted {
		t.Error("pending credential should not be soft-deleted")
	}
	if actorType != "agent" || actorID != agentID {
		t.Errorf("attribution = (%s,%s), want (agent,%s)", actorType, actorID, agentID)
	}
	if createdBy != ownerID {
		t.Errorf("created_by = %q, want workspace owner %q", createdBy, ownerID)
	}
	if encVal == p.Value || strings.Contains(encVal, "redis://") {
		t.Errorf("value not encrypted at rest: %q", encVal)
	}
	if got, err := encryption.Decrypt(encVal); err != nil || got != p.Value {
		t.Errorf("decrypt = (%q,%v), want %q", got, err, p.Value)
	}
}

func TestCreatePendingCredential_InvalidType_Fallback(t *testing.T) {
	h, _, wsID, _, agentID := covEscFixture(t)
	_, ok := h.createPendingCredential(context.Background(), wsID, agentID,
		credentialProposal{Name: "X", Type: "BOGUS", Value: "v"})
	if ok {
		t.Fatal("invalid type should not create a pending credential")
	}
}

func TestCreatePendingCredential_NameCollision_Fallback(t *testing.T) {
	h, ownerID, wsID, _, agentID := covEscFixture(t)
	execOrFatal(t, h.db, `INSERT INTO credentials
		(id, workspace_id, name, encrypted_value, type, provider, scope, status, created_by, created_at, updated_at)
		VALUES ('existing', ?, 'DUP', 'enc', 'SECRET', 'NONE', 'WORKSPACE', 'ACTIVE', ?, datetime('now'), datetime('now'))`,
		wsID, ownerID)
	_, ok := h.createPendingCredential(context.Background(), wsID, agentID,
		credentialProposal{Name: "DUP", Type: "SECRET", Value: "v"})
	if ok {
		t.Fatal("name collision with a live credential should fall back (no pending row)")
	}
	var n int
	h.db.QueryRow(`SELECT COUNT(*) FROM credentials WHERE workspace_id=? AND name='DUP'`, wsID).Scan(&n)
	if n != 1 {
		t.Fatalf("credential count = %d, want 1 (no duplicate created)", n)
	}
}

// --- full CreateEscalation HTTP path: redaction + linkage ---

func seedChat(t *testing.T, h *QueryHandler, chatID, agentID, wsID string) {
	t.Helper()
	execOrFatal(t, h.db, `INSERT INTO chats(id,agent_id,workspace_id,mode,status) VALUES (?,?,?,'CHAT','ACTIVE')`,
		chatID, agentID, wsID)
}

func createEsc(h *QueryHandler, wsID string, body map[string]string) *httptest.ResponseRecorder {
	req := httptest.NewRequest("POST", "/api/v1/internal/escalations", jsonBody(body))
	req = req.WithContext(context.WithValue(req.Context(), ctxInternalTokenWS, wsID))
	rr := httptest.NewRecorder()
	h.CreateEscalation(rr, req)
	return rr
}

func TestCreateEscalation_CredentialProposal_RedactsAndLinks(t *testing.T) {
	ensureEncryptionKey(t)
	h, _, wsID, crewID, agentID := covEscFixture(t)
	seedChat(t, h, "covesc-chat", agentID, wsID)
	const canary = "redaction-canary-value-987" //gitleaks:allow — fake test fixture, asserts the value is redacted
	rr := createEsc(h, wsID, map[string]string{
		"from_slug": "covesc-ag", "reason": "store pg pw", "crew_id": crewID,
		"workspace_id": wsID, "chat_id": "covesc-chat", "type": "CREDENTIAL",
		"metadata": `{"name":"PG_PASSWORD","type":"SECRET","provider":"NONE","value":"` + canary + `"}`,
	})
	if rr.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201; body=%s", rr.Code, rr.Body.String())
	}
	var storedMeta, credID *string
	if err := h.db.QueryRow(`SELECT metadata, credential_id FROM escalations
		WHERE workspace_id=? AND type='CREDENTIAL'`, wsID).Scan(&storedMeta, &credID); err != nil {
		t.Fatalf("load escalation: %v", err)
	}
	if credID == nil || *credID == "" {
		t.Fatal("escalation.credential_id not linked")
	}
	if storedMeta == nil || strings.Contains(*storedMeta, canary) {
		t.Fatalf("escalation.metadata must be redacted, leaked value: %v", storedMeta)
	}
	// The proposed credential exists and is PENDING_APPROVAL.
	status, _, _, _, _, _ := credRow(t, h, *credID)
	if status != "PENDING_APPROVAL" {
		t.Errorf("linked credential status = %q, want PENDING_APPROVAL", status)
	}
}

func TestCreateEscalation_MalformedMetadata_Fallback(t *testing.T) {
	h, _, wsID, crewID, agentID := covEscFixture(t)
	seedChat(t, h, "covesc-chat", agentID, wsID)
	rr := createEsc(h, wsID, map[string]string{
		"from_slug": "covesc-ag", "reason": "need a key", "crew_id": crewID,
		"workspace_id": wsID, "chat_id": "covesc-chat", "type": "CREDENTIAL",
		"metadata": "PG_PASSWORD=not-json",
	})
	if rr.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201; body=%s", rr.Code, rr.Body.String())
	}
	var credID *string
	h.db.QueryRow(`SELECT credential_id FROM escalations WHERE workspace_id=? AND type='CREDENTIAL'`, wsID).Scan(&credID)
	if credID != nil && *credID != "" {
		t.Errorf("malformed metadata should NOT create a pending credential, got %q", *credID)
	}
	var n int
	h.db.QueryRow(`SELECT COUNT(*) FROM credentials WHERE workspace_id=?`, wsID).Scan(&n)
	if n != 0 {
		t.Errorf("credential count = %d, want 0 on fallback", n)
	}
}

// A malformed proposal that still carries a value (here: missing name) must NOT
// leak the secret into escalations.metadata — it is redacted even though no
// pending credential is created.
func TestCreateEscalation_MalformedProposalWithValue_Redacted(t *testing.T) {
	h, _, wsID, crewID, agentID := covEscFixture(t)
	seedChat(t, h, "covesc-chat", agentID, wsID)
	const leak = "leakme-canary-555" //gitleaks:allow — fake fixture, asserts redaction
	rr := createEsc(h, wsID, map[string]string{
		"from_slug": "covesc-ag", "reason": "bad proposal", "crew_id": crewID,
		"workspace_id": wsID, "chat_id": "covesc-chat", "type": "CREDENTIAL",
		"metadata": `{"type":"SECRET","value":"` + leak + `"}`, // no name → not a valid proposal
	})
	if rr.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201; body=%s", rr.Code, rr.Body.String())
	}
	var storedMeta, credID *string
	if err := h.db.QueryRow(`SELECT metadata, credential_id FROM escalations
		WHERE workspace_id=? AND type='CREDENTIAL'`, wsID).Scan(&storedMeta, &credID); err != nil {
		t.Fatalf("load escalation: %v", err)
	}
	if storedMeta != nil && strings.Contains(*storedMeta, leak) {
		t.Fatalf("malformed proposal leaked its value into escalations.metadata: %v", storedMeta)
	}
	if credID != nil && *credID != "" {
		t.Errorf("no pending credential should be created for a nameless proposal, got %q", *credID)
	}
}

// --- approve / reject ---

func seedLinkedEscalation(t *testing.T, h *QueryHandler, escID, wsID, crewID, agentID, credID string) {
	t.Helper()
	execOrFatal(t, h.db, `INSERT INTO escalations
		(id, workspace_id, crew_id, chat_id, from_agent_id, reason, type, credential_id, status, created_at)
		VALUES (?, ?, ?, 'covesc-chat', ?, 'store cred', 'CREDENTIAL', ?, 'PENDING', datetime('now'))`,
		escID, wsID, crewID, agentID, credID)
}

func TestApprovePendingCredential_Activates(t *testing.T) {
	ensureEncryptionKey(t)
	h, userID, wsID, crewID, agentID := covEscFixture(t)
	credID, ok := h.createPendingCredential(context.Background(), wsID, agentID,
		credentialProposal{Name: "REDIS_URL", Type: "SECRET", Value: "v"})
	if !ok {
		t.Fatal("setup: pending credential not created")
	}
	seedLinkedEscalation(t, h, "esc-app", wsID, crewID, agentID, credID)

	rr := covEscResolve(h, userID, wsID, "esc-app", map[string]string{
		"resolution": "Approved from inbox", "action": "approve",
	})
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rr.Code, rr.Body.String())
	}
	var status, approvedBy string
	var approvedAt *string
	h.db.QueryRow(`SELECT status, COALESCE(approved_by_user_id,''), approved_at FROM credentials WHERE id=?`, credID).
		Scan(&status, &approvedBy, &approvedAt)
	if status != "ACTIVE" {
		t.Errorf("status = %q, want ACTIVE after approve", status)
	}
	if approvedBy != userID {
		t.Errorf("approved_by_user_id = %q, want approver %q", approvedBy, userID)
	}
	if approvedAt == nil {
		t.Error("approved_at should be set")
	}
}

func TestRejectPendingCredential_SoftDeletes(t *testing.T) {
	ensureEncryptionKey(t)
	h, userID, wsID, crewID, agentID := covEscFixture(t)
	credID, ok := h.createPendingCredential(context.Background(), wsID, agentID,
		credentialProposal{Name: "REDIS_URL", Type: "SECRET", Value: "v"})
	if !ok {
		t.Fatal("setup: pending credential not created")
	}
	seedLinkedEscalation(t, h, "esc-rej", wsID, crewID, agentID, credID)

	rr := covEscResolve(h, userID, wsID, "esc-rej", map[string]string{
		"resolution": "Rejected from inbox", "action": "reject",
	})
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rr.Code, rr.Body.String())
	}
	_, _, _, _, _, deleted := credRow(t, h, credID)
	if !deleted {
		t.Error("rejected credential should be soft-deleted (deleted_at set)")
	}
}

// TestResolveEscalation_ThroughRequireWorkspace exercises the FULL middleware
// chain the dashboard hits (RequireWorkspace → ResolveEscalation), which the
// other resolve tests skip by injecting the workspace into the context directly.
// That gap let the inbox "workspace_id is required" bug ship: RequireWorkspace
// reads workspace_id from the URL, so a resolve request without it 400s before
// the handler runs. This guards both the rejection and the happy path.
func TestResolveEscalation_ThroughRequireWorkspace(t *testing.T) {
	ensureEncryptionKey(t)
	h, userID, wsID, crewID, agentID := covEscFixture(t)
	credID, ok := h.createPendingCredential(context.Background(), wsID, agentID,
		credentialProposal{Name: "REDIS_URL", Type: "SECRET", Value: "v"})
	if !ok {
		t.Fatal("setup: pending credential not created")
	}
	seedLinkedEscalation(t, h, "esc-mw", wsID, crewID, agentID, credID)

	am := NewAuthMiddleware(nil, nil, h.db, newTestLogger())
	handler := am.RequireWorkspace(http.HandlerFunc(h.ResolveEscalation))
	body := map[string]string{"action": "approve", "resolution": "Approved from inbox"}

	// 1) No workspace_id on the URL (the inbox bug) → 400 from the middleware.
	reqNo := httptest.NewRequest("PATCH", "/api/v1/escalations/esc-mw/resolve", jsonBody(body))
	reqNo = reqNo.WithContext(withUser(reqNo.Context(), &AuthUser{ID: userID}))
	reqNo.SetPathValue("escalationId", "esc-mw")
	rrNo := httptest.NewRecorder()
	handler.ServeHTTP(rrNo, reqNo)
	if rrNo.Code != http.StatusBadRequest {
		t.Fatalf("without workspace_id: status = %d, want 400; body=%s", rrNo.Code, rrNo.Body.String())
	}

	// 2) workspace_id on the query string → 200 and the credential activates.
	reqYes := httptest.NewRequest("PATCH", "/api/v1/escalations/esc-mw/resolve?workspace_id="+wsID, jsonBody(body))
	reqYes = reqYes.WithContext(withUser(reqYes.Context(), &AuthUser{ID: userID}))
	reqYes.SetPathValue("escalationId", "esc-mw")
	rrYes := httptest.NewRecorder()
	handler.ServeHTTP(rrYes, reqYes)
	if rrYes.Code != http.StatusOK {
		t.Fatalf("with workspace_id: status = %d, want 200; body=%s", rrYes.Code, rrYes.Body.String())
	}
	var status string
	h.db.QueryRow(`SELECT status FROM credentials WHERE id=?`, credID).Scan(&status)
	if status != "ACTIVE" {
		t.Errorf("credential status = %q, want ACTIVE after approve through middleware", status)
	}
}

// --- delivery exclusion: auto-assign must skip PENDING_APPROVAL ---

func TestAutoAssign_ExcludesPendingApproval(t *testing.T) {
	h, ownerID, wsID, crewID, agentID := covEscFixture(t)
	mk := func(id, name, status string) {
		execOrFatal(t, h.db, `INSERT INTO credentials
			(id, workspace_id, name, encrypted_value, type, provider, scope, status, created_by, created_at, updated_at)
			VALUES (?, ?, ?, 'enc', 'API_KEY', 'ANTHROPIC', 'WORKSPACE', ?, ?, datetime('now'), datetime('now'))`,
			id, wsID, name, status, ownerID)
	}
	mk("cred-active", "ACTIVE_KEY", "ACTIVE")
	mk("cred-pending", "PENDING_KEY", "PENDING_APPROVAL")

	autoAssignCredentials(context.Background(), h.db, slog.Default(), nil, wsID, agentID, "now")

	var assigned []string
	rows, err := h.db.Query(`SELECT credential_id FROM agent_credentials WHERE agent_id=?`, agentID)
	if err != nil {
		t.Fatalf("query agent_credentials: %v", err)
	}
	defer rows.Close()
	for rows.Next() {
		var c string
		rows.Scan(&c)
		assigned = append(assigned, c)
	}
	_ = crewID
	seenActive := false
	for _, c := range assigned {
		if c == "cred-pending" {
			t.Fatal("PENDING_APPROVAL credential must NOT be auto-assigned to an agent")
		}
		if c == "cred-active" {
			seenActive = true
		}
	}
	// Positive guard: without this the test would also pass if autoAssign
	// inserted nothing at all, so it wouldn't actually prove the status filter.
	if !seenActive {
		t.Fatalf("ACTIVE credential should have been auto-assigned; assigned=%v", assigned)
	}
}
