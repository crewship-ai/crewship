package api

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/encryption"
	"github.com/crewship-ai/crewship/internal/keeper/governance"
)

// Binding a governance model credential needs OWNER/ADMIN, so changing what the
// bound credential holds needs it too (#5024): a MANAGER who may edit ordinary
// credentials must not repoint the workspace judge through PATCH or rotate.
func TestGovernanceBoundCredentialWrites(t *testing.T) {
	t.Parallel()
	setTestEncryptionKeyParallelSafe(t)

	cases := []struct {
		name       string
		role       string
		caps       string
		bound      bool
		op         string
		wantStatus int
	}{
		{"MANAGER updates bound", "MANAGER", `["chat"]`, true, "update", http.StatusForbidden},
		{"MANAGER updates ordinary", "MANAGER", `["chat"]`, false, "update", http.StatusOK},
		{"ADMIN updates bound", "ADMIN", `["chat"]`, true, "update", http.StatusOK},
		{"OWNER updates bound", "OWNER", `["chat"]`, true, "update", http.StatusOK},
		{"MANAGER with grant rotates bound", "MANAGER", `["chat","credential.rotate"]`, true, "rotate", http.StatusForbidden},
		{"MEMBER with grant rotates bound", "MEMBER", `["chat","credential.rotate"]`, true, "rotate", http.StatusForbidden},
		{"MANAGER with grant rotates ordinary", "MANAGER", `["chat","credential.rotate"]`, false, "rotate", http.StatusOK},
		{"ADMIN rotates bound", "ADMIN", `["chat"]`, true, "rotate", http.StatusOK},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			db := setupTestDB(t)
			ownerID := seedTestUser(t, db)
			wsID := seedTestWorkspace(t, db, ownerID)
			callerID := "gov-" + strings.ReplaceAll(tc.name, " ", "-")
			execOrFatal(t, db, `INSERT INTO users (id, email, full_name) VALUES (?, ?, 'T')`, callerID, callerID+"@x")
			execOrFatal(t, db, `INSERT INTO workspace_members (id, workspace_id, user_id, role, capabilities) VALUES (?, ?, ?, ?, ?)`,
				"m-"+callerID, wsID, callerID, tc.role, tc.caps)
			InvalidateCapabilityCache(wsID, callerID)

			credID := "cred-" + callerID
			seedCredentialEnc(t, db, wsID, ownerID, credID, "GOV_KEY", "old-value")
			if tc.bound {
				execOrFatal(t, db, `INSERT INTO keeper_governance_settings (workspace_id, gov_model_provider, gov_model_id, gov_model_credential_id) VALUES (?, 'anthropic', 'm', ?)`, wsID, credID)
			}

			h := NewCredentialHandler(db, quietLogger())
			method, path := "PATCH", "/api/v1/credentials/"+credID
			handle := h.Update
			if tc.op == "rotate" {
				method, path, handle = "POST", path+"/rotate", h.Rotate
			}
			body, _ := json.Marshal(map[string]any{"value": "new-value"})
			req := httptest.NewRequest(method, path, bytes.NewReader(body))
			ctx := withUser(req.Context(), &AuthUser{ID: callerID})
			req = req.WithContext(withWorkspace(ctx, wsID, tc.role))
			req.SetPathValue("credentialId", credID)

			rr := httptest.NewRecorder()
			handle(rr, req)
			if rr.Code != tc.wantStatus {
				t.Fatalf("status = %d, want %d: %s", rr.Code, tc.wantStatus, rr.Body.String())
			}

			var stored string
			if err := db.QueryRow(`SELECT encrypted_value FROM credentials WHERE id = ?`, credID).Scan(&stored); err != nil {
				t.Fatalf("read value: %v", err)
			}
			plain, err := encryption.DecryptIfEncrypted(stored)
			if err != nil {
				t.Fatalf("decrypt: %v", err)
			}
			want := "new-value"
			if tc.wantStatus == http.StatusForbidden {
				want = "old-value"
			}
			if plain != want {
				t.Fatalf("stored value = %q, want %q", plain, want)
			}
		})
	}
}

// The internal adapter stamps ADMIN before it calls Rotate, so the guard has to
// run in the adapter or the capability would still reach the bound credential.
func TestGovernanceBoundCredentialInternalRotate(t *testing.T) {
	setTestEncryptionKey(t)
	db := setupTestDB(t)
	ownerID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, ownerID)
	InvalidateCapabilityCache(wsID, ownerID)
	seedCredentialEnc(t, db, wsID, ownerID, "cred-bound", "API_KEY", "sk-old")
	seedCredentialEnc(t, db, wsID, ownerID, "cred-plain", "OTHER_KEY", "sk-old")
	execOrFatal(t, db, `INSERT INTO keeper_governance_settings (workspace_id, gov_model_provider, gov_model_id, gov_model_credential_id) VALUES (?, 'anthropic', 'm', 'cred-bound')`, wsID)

	adapter := NewCredentialInternalAdapter(&CredentialHandler{db: db, logger: quietLogger()})
	caller := ownerID
	rotate := func(credID string) int {
		r := httptest.NewRequest(http.MethodPost, "/?workspace_id="+wsID, strings.NewReader(`{"value":"sk-new"}`))
		r.SetPathValue("credentialId", credID)
		stampSignedCaller(r, forgedTestMaster, wsID, caller)
		w := httptest.NewRecorder()
		adapter.Rotate(w, r)
		return w.Code
	}
	// The signed caller is the OWNER, so the bound credential rotates.
	if got := rotate("cred-bound"); got != http.StatusOK {
		t.Fatalf("OWNER on bound credential: status = %d, want 200", got)
	}
	if got := rotate("cred-plain"); got != http.StatusOK {
		t.Fatalf("ordinary credential: status = %d, want 200", got)
	}

	const manager = "gov-internal-manager"
	execOrFatal(t, db, `INSERT INTO users (id, email, full_name) VALUES (?, ?, 'T')`, manager, manager+"@x")
	execOrFatal(t, db, `INSERT INTO workspace_members (id, workspace_id, user_id, role, capabilities) VALUES (?, ?, ?, 'MANAGER', ?)`,
		"m-"+manager, wsID, manager, `["chat","credential.rotate"]`)
	InvalidateCapabilityCache(wsID, manager)
	caller = manager
	if got := rotate("cred-bound"); got != http.StatusForbidden {
		t.Fatalf("MANAGER on bound credential: status = %d, want 403", got)
	}
	if got := rotate("cred-plain"); got != http.StatusOK {
		t.Fatalf("MANAGER on ordinary credential: status = %d, want 200", got)
	}
}

// A row waiting on an escalation cannot be bound: whoever answered the
// escalation would then choose the judge's value.
func TestGovernanceBindRefusesUnsettledCredential(t *testing.T) {
	t.Parallel()
	db := setupTestDB(t)
	ownerID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, ownerID)
	execOrFatal(t, db, `INSERT INTO credentials (id, workspace_id, name, encrypted_value, type, provider, scope, status, created_by, created_at, updated_at)
		VALUES ('cred-req', ?, 'REQ', '', 'API_KEY', 'ANTHROPIC', 'WORKSPACE', 'REQUESTED', ?, datetime('now'), datetime('now'))`, wsID, ownerID)
	id := "cred-req"
	body := keeperGovernancePutBody{GovModelCredentialID: &id}
	cur := governance.Settings{GovModelCredentialID: id}
	status, msg := checkGovernanceRefs(context.Background(), db, wsID, body, cur)
	if status != http.StatusBadRequest {
		t.Fatalf("status = %d (%s), want 400", status, msg)
	}

	// The Judge panel re-sends the stored id on every save.
	execOrFatal(t, db, `UPDATE credentials SET status = 'RATE_LIMITED' WHERE id = 'cred-req'`)
	execOrFatal(t, db, `INSERT INTO keeper_governance_settings (workspace_id, gov_model_provider, gov_model_id, gov_model_credential_id) VALUES (?, 'anthropic', 'm', 'cred-req')`, wsID)
	if status, msg := checkGovernanceRefs(context.Background(), db, wsID, body, cur); status != 0 {
		t.Fatalf("unchanged binding: status = %d (%s), want 0", status, msg)
	}
}

func TestStoreOAuthTokensRefusesGovernanceCredential(t *testing.T) {
	t.Parallel()
	setTestEncryptionKeyParallelSafe(t)
	db := setupTestDB(t)
	ownerID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, ownerID)
	seedCredentialEnc(t, db, wsID, ownerID, "cred-oauth", "OAUTH_KEY", "old-value")
	execOrFatal(t, db, `INSERT INTO keeper_governance_settings (workspace_id, gov_model_provider, gov_model_id, gov_model_credential_id) VALUES (?, 'anthropic', 'm', 'cred-oauth')`, wsID)

	h := &OAuthHandler{db: db}
	if err := h.storeOAuthTokens(context.Background(), "cred-oauth", &tokenResponse{AccessToken: "replacement"}); err == nil {
		t.Fatal("OAuth completion replaced the governance credential")
	}
	var stored string
	if err := db.QueryRow(`SELECT encrypted_value FROM credentials WHERE id = 'cred-oauth'`).Scan(&stored); err != nil {
		t.Fatal(err)
	}
	if plain, _ := encryption.DecryptIfEncrypted(stored); plain != "old-value" {
		t.Fatalf("stored value = %q", plain)
	}
}

func TestInternalCredentialPatchGovernance(t *testing.T) {
	t.Parallel()
	setTestEncryptionKeyParallelSafe(t)
	cases := []struct {
		name, status, payload string
		start                 string
		want                  int
	}{
		{"value", "ACTIVE", `{"status":"ACTIVE","access_token":"replacement"}`, "ACTIVE", http.StatusForbidden},
		{"refresh token", "ACTIVE", `{"status":"ACTIVE","refresh_token":"replacement"}`, "ACTIVE", http.StatusForbidden},
		{"reactivate revoked", "ACTIVE", `{"status":"ACTIVE"}`, "REVOKED", http.StatusForbidden},
		{"leave revoked through error", "ERROR", `{"status":"ERROR"}`, "REVOKED", http.StatusForbidden},
		{"activate pending approval", "ACTIVE", `{"status":"ACTIVE"}`, "PENDING_APPROVAL", http.StatusForbidden},
		{"leave pending through error", "ERROR", `{"status":"ERROR"}`, "PENDING_APPROVAL", http.StatusForbidden},
		{"leave requested", "ACTIVE", `{"status":"ACTIVE"}`, "REQUESTED", http.StatusForbidden},
		{"health report", "RATE_LIMITED", `{"status":"RATE_LIMITED"}`, "ACTIVE", http.StatusOK},
		{"recover after limit", "ACTIVE", `{"status":"ACTIVE"}`, "RATE_LIMITED", http.StatusOK},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			db := setupTestDB(t)
			ownerID := seedTestUser(t, db)
			wsID := seedTestWorkspace(t, db, ownerID)
			seedCredentialEnc(t, db, wsID, ownerID, "cred-int", "INT_KEY", "old-value")
			execOrFatal(t, db, `UPDATE credentials SET status = ? WHERE id = 'cred-int'`, tc.start)
			execOrFatal(t, db, `INSERT INTO keeper_governance_settings (workspace_id, gov_model_provider, gov_model_id, gov_model_credential_id) VALUES (?, 'anthropic', 'm', 'cred-int')`, wsID)

			h := &InternalHandler{db: db, logger: quietLogger()}
			r := httptest.NewRequest(http.MethodPatch, "/?workspace_id="+wsID, strings.NewReader(tc.payload))
			r.SetPathValue("credentialId", "cred-int")
			w := httptest.NewRecorder()
			h.UpdateCredentialStatus(w, r)
			if w.Code != tc.want {
				t.Fatalf("status = %d, want %d: %s", w.Code, tc.want, w.Body.String())
			}
			var stored, status string
			if err := db.QueryRow(`SELECT encrypted_value, status FROM credentials WHERE id = 'cred-int'`).Scan(&stored, &status); err != nil {
				t.Fatal(err)
			}
			if plain, _ := encryption.DecryptIfEncrypted(stored); plain != "old-value" {
				t.Fatalf("stored value = %q", plain)
			}
			if tc.want == http.StatusForbidden && status != tc.start {
				t.Fatalf("status changed to %q", status)
			}
		})
	}
}

// Rows bound before binding required an ACTIVE credential can still wait on
// their escalation. A MANAGER may not answer it.
func TestEscalationRefusesManagerOnBoundCredential(t *testing.T) {
	f := newSupplyFixture(t)
	escID, credID := f.ask(t, askMetadata)
	if credID == "" {
		t.Fatal("ask must stage a credential")
	}
	execOrFatal(t, f.h.db, `INSERT INTO keeper_governance_settings (workspace_id, gov_model_provider, gov_model_id, gov_model_credential_id) VALUES (?, 'anthropic', 'm', ?)`, f.wsID, credID)

	as := func(method string, call func(http.ResponseWriter, *http.Request), body map[string]any) int {
		req := httptest.NewRequest(method, "/", jsonBody(body))
		req.SetPathValue("escalationId", escID)
		req = req.WithContext(withWorkspace(withUser(req.Context(), &AuthUser{ID: "someone-else"}), f.wsID, "MANAGER"))
		rr := httptest.NewRecorder()
		call(rr, req)
		return rr.Code
	}
	if got := as("POST", f.h.SupplyEscalationCredential, map[string]any{"value": "replacement"}); got != http.StatusForbidden {
		t.Fatalf("supply: status = %d, want 403", got)
	}
	execOrFatal(t, f.h.db, `UPDATE credentials SET status = 'PENDING_APPROVAL' WHERE id = ?`, credID)
	if got := as("PATCH", f.h.ResolveEscalation, map[string]any{"action": "approve"}); got != http.StatusForbidden {
		t.Fatalf("resolve: status = %d, want 403", got)
	}
	var status string
	if err := f.h.db.QueryRow(`SELECT status FROM credentials WHERE id = ?`, credID).Scan(&status); err != nil {
		t.Fatal(err)
	}
	if status != "PENDING_APPROVAL" {
		t.Fatalf("credential status = %q", status)
	}
}
