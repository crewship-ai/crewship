package api

// ID1 tripwire/regression: the internal credential create + rotate
// path must NOT trust a forgeable X-Caller-User-Id. A request that
// carries a forged (or unsigned) caller id of a privileged user is
// rejected; only a request whose caller id is HMAC-signed by a holder
// of the workspace-bound internal token is honoured.
//
// Threat: the agent process inside a crew container can reach the
// sidecar over loopback and set any X-Caller-User-Id it likes. Before
// the fix the backend trusted that header for credential mutation, so
// the agent could create/rotate credentials as ANY user (privilege
// escalation + audit forgery). The fix requires X-Caller-Signature =
// HMAC(internal_token, workspace_id || caller_id); the agent never
// holds the token and so cannot mint a valid signature.

import (
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/auth/internaltoken"
)

// stampSignedCaller wires a request the way the internal-auth
// middleware + sidecar would in production: it sets the validated
// workspace-bound internal token, the forwarded caller id, and a
// matching X-Caller-Signature. master is the server master secret the
// token derives from. Used by the secure-path assertions here and by
// the dual-path / coverage tests that drive the adapter directly.
func stampSignedCaller(r *http.Request, master, wsID, callerID string) {
	token := internaltoken.DeriveWorkspaceToken(master, wsID)
	r.Header.Set("X-Internal-Token", token)
	r.Header.Set("X-Caller-User-Id", callerID)
	r.Header.Set("X-Caller-Signature", internaltoken.SignCaller(token, wsID, callerID))
}

const forgedTestMaster = "forged-caller-test-master-secret"

// TestForgedCallerID_CredentialCreate_RejectedUnlessSigned is the core
// ID1 assertion for the create path.
func TestForgedCallerID_CredentialCreate_RejectedUnlessSigned(t *testing.T) {
	setTestEncryptionKey(t)
	db := setupTestDB(t)
	// ownerID is a privileged user (workspace OWNER → full capabilities).
	// This is precisely the identity an attacker would forge.
	ownerID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, ownerID)
	InvalidateCapabilityCache(wsID, ownerID)

	adapter := NewCredentialInternalAdapter(&CredentialHandler{db: db, logger: slog.Default()})

	t.Run("forged: privileged caller id with NO signature → 401", func(t *testing.T) {
		r := httptest.NewRequest(http.MethodPost,
			"/?workspace_id="+wsID, strings.NewReader(`{"name":"x","value":"y","type":"API_KEY","provider":"ANTHROPIC"}`))
		// The sidecar would attach the token; a forging agent can set
		// the caller id but cannot mint the signature.
		r.Header.Set("X-Internal-Token", internaltoken.DeriveWorkspaceToken(forgedTestMaster, wsID))
		r.Header.Set("X-Caller-User-Id", ownerID)
		w := httptest.NewRecorder()
		adapter.Create(w, r)
		if w.Code != http.StatusUnauthorized {
			t.Fatalf("status = %d, want 401 — an unsigned caller id must be rejected (forged-identity escalation)", w.Code)
		}
	})

	t.Run("forged: signature minted under a DIFFERENT token → 401", func(t *testing.T) {
		// An agent that knows neither the master nor the real workspace
		// token guesses a key. The signature won't verify against the
		// (validated) X-Internal-Token the request carries.
		token := internaltoken.DeriveWorkspaceToken(forgedTestMaster, wsID)
		r := httptest.NewRequest(http.MethodPost,
			"/?workspace_id="+wsID, strings.NewReader(`{"name":"x","value":"y","type":"API_KEY","provider":"ANTHROPIC"}`))
		r.Header.Set("X-Internal-Token", token)
		r.Header.Set("X-Caller-User-Id", ownerID)
		r.Header.Set("X-Caller-Signature", internaltoken.SignCaller("attacker-guessed-key", wsID, ownerID))
		w := httptest.NewRecorder()
		adapter.Create(w, r)
		if w.Code != http.StatusUnauthorized {
			t.Fatalf("status = %d, want 401 — a signature under the wrong key must be rejected", w.Code)
		}
	})

	t.Run("forged: signature bound to a DIFFERENT caller id → 401", func(t *testing.T) {
		// Replay a signature legitimately minted for another user onto
		// a request claiming to be the owner.
		token := internaltoken.DeriveWorkspaceToken(forgedTestMaster, wsID)
		r := httptest.NewRequest(http.MethodPost,
			"/?workspace_id="+wsID, strings.NewReader(`{"name":"x","value":"y","type":"API_KEY","provider":"ANTHROPIC"}`))
		r.Header.Set("X-Internal-Token", token)
		r.Header.Set("X-Caller-User-Id", ownerID)
		r.Header.Set("X-Caller-Signature", internaltoken.SignCaller(token, wsID, "some-other-user"))
		w := httptest.NewRecorder()
		adapter.Create(w, r)
		if w.Code != http.StatusUnauthorized {
			t.Fatalf("status = %d, want 401 — a signature bound to another caller id must not authorize this one", w.Code)
		}
	})

	t.Run("secure: correctly-signed caller id is accepted (201)", func(t *testing.T) {
		r := httptest.NewRequest(http.MethodPost,
			"/?workspace_id="+wsID, strings.NewReader(`{"name":"Signed Key","value":"sk-signed","type":"API_KEY","provider":"ANTHROPIC"}`))
		stampSignedCaller(r, forgedTestMaster, wsID, ownerID)
		w := httptest.NewRecorder()
		adapter.Create(w, r)
		if w.Code != http.StatusCreated {
			t.Fatalf("status = %d, want 201 — a correctly-signed privileged caller must be accepted; body=%s", w.Code, w.Body.String())
		}
		var n int
		if err := db.QueryRow(
			`SELECT COUNT(*) FROM credentials WHERE workspace_id = ? AND name = 'Signed Key'`, wsID).Scan(&n); err != nil {
			t.Fatalf("query credential: %v", err)
		}
		if n != 1 {
			t.Errorf("credential row count = %d, want 1 (signed create persisted)", n)
		}
	})
}

// TestForgedCallerID_CredentialRotate_RejectedUnlessSigned mirrors the
// create assertion for the rotate path — rotation is the higher
// blast-radius mutation (active sessions cut over), so the same gate
// must hold.
func TestForgedCallerID_CredentialRotate_RejectedUnlessSigned(t *testing.T) {
	setTestEncryptionKey(t)
	db := setupTestDB(t)
	ownerID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, ownerID)
	InvalidateCapabilityCache(wsID, ownerID)
	seedCredentialEnc(t, db, wsID, ownerID, "cred-rot", "API_KEY", "sk-old")

	adapter := NewCredentialInternalAdapter(&CredentialHandler{db: db, logger: slog.Default()})

	newReq := func() *http.Request {
		r := httptest.NewRequest(http.MethodPost,
			"/?workspace_id="+wsID, strings.NewReader(`{"value":"sk-new"}`))
		r.SetPathValue("credentialId", "cred-rot")
		return r
	}

	t.Run("forged: unsigned privileged caller id → 401", func(t *testing.T) {
		r := newReq()
		r.Header.Set("X-Internal-Token", internaltoken.DeriveWorkspaceToken(forgedTestMaster, wsID))
		r.Header.Set("X-Caller-User-Id", ownerID)
		w := httptest.NewRecorder()
		adapter.Rotate(w, r)
		if w.Code != http.StatusUnauthorized {
			t.Fatalf("status = %d, want 401 — unsigned rotate caller must be rejected", w.Code)
		}
	})

	t.Run("secure: correctly-signed caller id passes the identity gate", func(t *testing.T) {
		r := newReq()
		stampSignedCaller(r, forgedTestMaster, wsID, ownerID)
		w := httptest.NewRecorder()
		adapter.Rotate(w, r)
		// A correctly-signed, capable owner clears both the signature
		// gate (401) and the capability gate (403). Whatever the inner
		// rotate handler returns, it must not be either rejection.
		if w.Code == http.StatusUnauthorized || w.Code == http.StatusForbidden {
			t.Fatalf("status = %d — a correctly-signed owner must pass the identity + capability gates; body=%s", w.Code, w.Body.String())
		}
	})
}
