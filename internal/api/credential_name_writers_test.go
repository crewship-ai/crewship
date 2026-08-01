package api

import (
	"bytes"
	"context"
	"database/sql"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/provider"
)

// #1657, the writer half. A binding's slot and a grant's env_var_name ARE
// environment variables — unlike a credential's display name, which is an
// account identity and stays one. Both endpoints are held to the shared rule
// and store what the container will actually see, so the row stops disagreeing
// with the delivery.

// TestAgentCred_Add_StoresTheCanonicalEnvVarName is the assign endpoint's half.
// It used to check env_var_name for non-emptiness and NOTHING else, while the
// reader accepted only uppercase — so `gh-token` was recorded happily and then
// either failed to arrive or took the run down with it.
func TestAgentCred_Add_StoresTheCanonicalEnvVarName(t *testing.T) {
	t.Parallel()
	db := setupTestDB(t)
	userID, wsID, agentID, credID := seedAgentCredEnv(t, db)
	h := newAgentHandlerForCred(t, db)

	body := bytes.NewBufferString(`{"credential_id":"` + credID + `","env_var_name":"gh-token"}`)
	req := httptest.NewRequest("POST", "/api/v1/agents/"+agentID+"/credentials", body)
	req.SetPathValue("agentId", agentID)
	ctx := withUser(req.Context(), &AuthUser{ID: userID})
	req = req.WithContext(withWorkspace(ctx, wsID, "OWNER"))
	rr := httptest.NewRecorder()
	h.AddCredential(rr, req)
	if rr.Code != http.StatusCreated && rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 2xx: %s", rr.Code, rr.Body.String())
	}

	var stored string
	if err := db.QueryRow(`SELECT env_var_name FROM agent_credentials WHERE agent_id = ?`,
		agentID).Scan(&stored); err != nil {
		t.Fatalf("read back: %v", err)
	}
	if stored != "GH_TOKEN" {
		t.Errorf("stored env_var_name = %q, want GH_TOKEN — the row must hold the variable "+
			"the container will see, not a spelling the reader rejects", stored)
	}
}

// TestAgentCred_Add_RejectsAnUnnameableEnvVarName puts the refusal on the
// request that chose the name, rather than on a run days later whose error
// message blames the Docker daemon.
func TestAgentCred_Add_RejectsAnUnnameableEnvVarName(t *testing.T) {
	t.Parallel()
	db := setupTestDB(t)
	userID, wsID, agentID, credID := seedAgentCredEnv(t, db)
	h := newAgentHandlerForCred(t, db)

	for _, name := range []string{"gh token", "2fa", "GH;rm -rf /"} {
		body := bytes.NewBufferString(`{"credential_id":"` + credID + `","env_var_name":"` + name + `"}`)
		req := httptest.NewRequest("POST", "/api/v1/agents/"+agentID+"/credentials", body)
		req.SetPathValue("agentId", agentID)
		ctx := withUser(req.Context(), &AuthUser{ID: userID})
		req = req.WithContext(withWorkspace(ctx, wsID, "OWNER"))
		rr := httptest.NewRecorder()
		h.AddCredential(rr, req)
		if rr.Code != http.StatusBadRequest {
			t.Errorf("env_var_name %q: status = %d, want 400", name, rr.Code)
		}
	}
}

// TestBinding_Create_StoresTheCanonicalSlot is the bindings half, and it closes
// a collision class rather than only reporting it: the UNIQUE index on
// (scope, slot) now sees one spelling per variable, so `gh_token` and
// `GH-TOKEN` cannot both be bound in one scope and then fight at delivery.
func TestBinding_Create_StoresTheCanonicalSlot(t *testing.T) {
	t.Parallel()
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	seedCredentialEnc(t, db, wsID, userID, "bind-cred", "github-acme", "v")
	h := NewCredentialBindingHandler(db, newTestLogger())

	rr := postBinding(t, h, wsID, userID, `{"credential_id":"bind-cred","scope":"WORKSPACE","slot":"gh-token"}`)
	if rr.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201: %s", rr.Code, rr.Body.String())
	}
	if !strings.Contains(rr.Body.String(), `"slot":"GH_TOKEN"`) {
		t.Errorf("response slot must be the delivered variable, got: %s", rr.Body.String())
	}

	var stored string
	if err := db.QueryRow(`SELECT slot FROM credential_bindings WHERE credential_id = 'bind-cred'`).
		Scan(&stored); err != nil {
		t.Fatalf("read back: %v", err)
	}
	if stored != "GH_TOKEN" {
		t.Errorf("stored slot = %q, want GH_TOKEN", stored)
	}

	// The second spelling of the same variable is now a 409 in the same scope,
	// which is the whole reason for storing the canonical form.
	rr2 := postBinding(t, h, wsID, userID, `{"credential_id":"bind-cred","scope":"WORKSPACE","slot":"GH_token"}`)
	if rr2.Code != http.StatusConflict {
		t.Errorf("second spelling: status = %d, want 409 — two spellings of one variable "+
			"were both bound in one scope: %s", rr2.Code, rr2.Body.String())
	}
}

// TestBinding_Create_RejectsAnUnnameableSlot keeps the refusal for names no
// variable can be derived from. The old regexp refused these too; what changed
// is that it also refused `gh-token`, which is now accepted and delivered.
func TestBinding_Create_RejectsAnUnnameableSlot(t *testing.T) {
	t.Parallel()
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	seedCredentialEnc(t, db, wsID, userID, "bind-cred", "github-acme", "v")
	h := NewCredentialBindingHandler(db, newTestLogger())

	for _, slot := range []string{"", "gh token", "1TOKEN", "GH=TOKEN"} {
		rr := postBinding(t, h, wsID, userID,
			`{"credential_id":"bind-cred","scope":"WORKSPACE","slot":"`+slot+`"}`)
		if rr.Code != http.StatusBadRequest {
			t.Errorf("slot %q: status = %d, want 400", slot, rr.Code)
		}
	}
}

func postBinding(t *testing.T, h *CredentialBindingHandler, wsID, userID, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest("POST", "/api/v1/credentials/bindings", bytes.NewBufferString(body))
	ctx := withUser(req.Context(), &AuthUser{ID: userID})
	req = req.WithContext(withWorkspace(ctx, wsID, "OWNER"))
	rr := httptest.NewRecorder()
	h.Create(rr, req)
	return rr
}

// TestRevokeReconcile_RemovesTheFileDeliveryActuallyWrote is the coupling the
// normalisation creates and must not break. The reconciler builds an `rm` from
// the STORED env_var_name; delivery writes the file under the DELIVERED one. A
// row written before this change holds whatever the assign endpoint accepted
// when it checked nothing but non-emptiness, so a revoke that spelled it the
// stored way would `rm -f` a path that does not exist, exit 0, and report a
// removal that never happened — the operator believes a live container has
// stopped reading a secret it is still reading.
func TestRevokeReconcile_RemovesTheFileDeliveryActuallyWrote(t *testing.T) {
	t.Parallel()
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	execOrFatal(t, db, `INSERT INTO crews (id, workspace_id, name, slug) VALUES ('rc-crew', ?, 'C', 'c')`, wsID)
	execOrFatal(t, db, `INSERT INTO agents (id, crew_id, workspace_id, name, slug) VALUES ('rc-agent', 'rc-crew', ?, 'A', 'writer')`, wsID)
	seedCredentialEncTyped(t, db, wsID, userID, "rc-cred", "legacy-token", "v", "CLI_TOKEN")
	// The legacy spelling: legal to write before #1657, never legal to read.
	assignCredToAgent(t, db, "rc-cred", "rc-agent", "gh-token", 0)

	var calls []provider.ExecConfig
	reconcileRevokedCredentialFiles(context.Background(), db, newTestLogger(),
		newRecordingCtr(&calls, nil), "rc-cred", wsID)

	if len(calls) != 1 {
		t.Fatalf("exec calls = %d, want 1", len(calls))
	}
	script := strings.Join(calls[0].Cmd, " ")
	if !strings.Contains(script, "/secrets/writer/GH_TOKEN") {
		t.Errorf("revoke removed %q — delivery wrote /secrets/writer/GH_TOKEN, so this "+
			"rm removes nothing and reports success", script)
	}
}

// seedCredentialEncTyped is seedCredentialEnc with the type left to the caller:
// the reconciler branches on it to decide whether the credential has an on-disk
// form at all, so a SECRET-typed fixture would pass a test about CLI_TOKEN paths
// for the wrong reason.
func seedCredentialEncTyped(t *testing.T, db *sql.DB, wsID, userID, credID, name, plainValue, credType string) {
	t.Helper()
	seedCredentialEnc(t, db, wsID, userID, credID, name, plainValue)
	execOrFatal(t, db, `UPDATE credentials SET type = ? WHERE id = ?`, credType, credID)
}
