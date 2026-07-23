package api

import (
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/encryption"
)

// seedLeasedAICred inserts a CREW-scoped AI_CLI_TOKEN (provider-key) credential
// with an encrypted value, plus an agent_credentials grant whose expires_at is
// the given lease timestamp (empty string → standing grant / NULL). Returns
// nothing; the credID is the caller's.
func seedLeasedAICred(t *testing.T, db *sql.DB, wsID, userID, credID, agentID, plaintext, leaseExpiresAt string) {
	t.Helper()
	enc, err := encryption.Encrypt(plaintext)
	if err != nil {
		t.Fatalf("encrypt %s: %v", credID, err)
	}
	if _, err := db.Exec(`
		INSERT INTO credentials (id, workspace_id, name, encrypted_value,
			type, provider, scope, status, created_by, created_at, updated_at)
		VALUES (?, ?, ?, ?, 'AI_CLI_TOKEN', 'ANTHROPIC', 'CREW', 'ACTIVE', ?, datetime('now'), datetime('now'))`,
		credID, wsID, "ai-"+credID, enc, userID); err != nil {
		t.Fatalf("seed leased credential %s: %v", credID, err)
	}
	var expires any
	if leaseExpiresAt != "" {
		expires = leaseExpiresAt
	}
	if _, err := db.Exec(`
		INSERT INTO agent_credentials (id, agent_id, credential_id, env_var_name, priority, created_at, expires_at)
		VALUES (?, ?, ?, ?, 0, datetime('now'), ?)`,
		"ac-"+credID, agentID, credID, "ANTHROPIC_API_KEY", expires); err != nil {
		t.Fatalf("seed agent_credentials for %s: %v", credID, err)
	}
}

// TestListCredentials_ExpiredLease_NotDeliveredAtBoot is the #1373 core
// regression: a leased provider key (API_KEY / AI_CLI_TOKEN) whose
// agent_credentials lease has already lapsed must NOT be delivered on the
// boot-delivery include_values path (the same crew-scoped listing the sidecar
// credential reaper and the orchestrator boot payload consume). Before the fix
// the crew-scoped EXISTS subquery filtered on credential status only — so an
// expired-lease provider key was handed over as plaintext at boot and stayed
// usable for the whole container life regardless of TTL. A valid (future) lease
// and a standing (NULL) grant must still be delivered.
//
// Red on current head: credExpired's token IS returned (the EXISTS match
// ignores ac.expires_at), failing the "must be absent" assertion.
func TestListCredentials_ExpiredLease_NotDeliveredAtBoot(t *testing.T) {
	h, db, userID, wsID := covICRig(t)

	mustExec(t, db, `INSERT INTO crews (id, workspace_id, name, slug) VALUES ('crew-a', ?, 'A', 'crew-a')`, wsID)
	mustExec(t, db, `INSERT INTO agents (id, workspace_id, crew_id, name, slug) VALUES ('ag-a', ?, 'crew-a', 'AgA', 'ag-a')`, wsID)

	past := time.Now().UTC().Add(-1 * time.Minute).Format(time.RFC3339)
	future := time.Now().UTC().Add(1 * time.Hour).Format(time.RFC3339)

	seedLeasedAICred(t, db, wsID, userID, "credExpired", "ag-a", "sk-ant-expired", past)
	seedLeasedAICred(t, db, wsID, userID, "credValid", "ag-a", "sk-ant-valid", future)
	seedLeasedAICred(t, db, wsID, userID, "credStanding", "ag-a", "sk-ant-standing", "")

	req := httptest.NewRequest(http.MethodGet,
		"/api/v1/internal/credentials?include_values=true&workspace_id="+wsID+"&crew_id=crew-a", nil)
	req.RemoteAddr = "127.0.0.1:9999" // loopback: entitles include_values (boot-delivery / TokenSyncer hairpin)
	rec := httptest.NewRecorder()
	h.ListCredentials(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
	}

	var out []struct {
		ID          string  `json:"id"`
		AccessToken *string `json:"access_token"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	tokens := map[string]string{}
	for _, c := range out {
		if c.AccessToken != nil {
			tokens[c.ID] = *c.AccessToken
		} else {
			tokens[c.ID] = "" // present but withheld — still a leak of existence at boot
		}
	}

	if _, present := tokens["credExpired"]; present {
		t.Errorf("expired-lease provider key was delivered at boot: %v — an expired lease must be refused end-to-end", tokens)
	}
	if tokens["credValid"] != "sk-ant-valid" {
		t.Errorf("valid-lease provider key must be delivered, got %q", tokens["credValid"])
	}
	if tokens["credStanding"] != "sk-ant-standing" {
		t.Errorf("standing (NULL lease) grant must be delivered, got %q", tokens["credStanding"])
	}
}

// TestListCredentials_ExpiredLease_ReaperSourceOfTruth proves the metadata-only
// listing (values withheld) the sidecar credential reaper consumes ALSO drops an
// expired-lease credential — the reaper's source of truth returns only
// non-expired ACTIVE creds, so a key delivered before expiry is evicted from the
// in-memory store within one interval of its TTL lapsing (see
// internal/sidecar/credstore_reap.go).
func TestListCredentials_ExpiredLease_ReaperSourceOfTruth(t *testing.T) {
	h, db, userID, wsID := covICRig(t)

	mustExec(t, db, `INSERT INTO crews (id, workspace_id, name, slug) VALUES ('crew-a', ?, 'A', 'crew-a')`, wsID)
	mustExec(t, db, `INSERT INTO agents (id, workspace_id, crew_id, name, slug) VALUES ('ag-a', ?, 'crew-a', 'AgA', 'ag-a')`, wsID)

	past := time.Now().UTC().Add(-1 * time.Minute).Format(time.RFC3339)
	future := time.Now().UTC().Add(1 * time.Hour).Format(time.RFC3339)
	seedLeasedAICred(t, db, wsID, userID, "credExpired", "ag-a", "sk-ant-expired", past)
	seedLeasedAICred(t, db, wsID, userID, "credValid", "ag-a", "sk-ant-valid", future)

	// Metadata-only (no include_values): exactly what reapRevokedCredentials fetches.
	req := httptest.NewRequest(http.MethodGet,
		"/api/v1/internal/credentials?workspace_id="+wsID+"&crew_id=crew-a", nil)
	req.RemoteAddr = "203.0.113.9:5555" // a real crew sidecar (non-loopback)
	rec := httptest.NewRecorder()
	h.ListCredentials(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
	}
	var out []struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	got := map[string]bool{}
	for _, c := range out {
		got[c.ID] = true
	}
	if got["credExpired"] {
		t.Errorf("expired-lease cred still listed in reaper source-of-truth → never evicted; got %v", got)
	}
	if !got["credValid"] {
		t.Errorf("valid-lease cred must remain listed (else it'd be falsely reaped); got %v", got)
	}
}
