package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/encryption"
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
	rotate := func(credID string) int {
		r := httptest.NewRequest(http.MethodPost, "/?workspace_id="+wsID, strings.NewReader(`{"value":"sk-new"}`))
		r.SetPathValue("credentialId", credID)
		stampSignedCaller(r, forgedTestMaster, wsID, ownerID)
		w := httptest.NewRecorder()
		adapter.Rotate(w, r)
		return w.Code
	}
	if got := rotate("cred-bound"); got != http.StatusForbidden {
		t.Fatalf("bound credential: status = %d, want 403", got)
	}
	if got := rotate("cred-plain"); got != http.StatusOK {
		t.Fatalf("ordinary credential: status = %d, want 200", got)
	}
}
