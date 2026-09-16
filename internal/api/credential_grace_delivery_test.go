package api

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/encryption"
)

// Rotation grace delivery (#1882): a delivered credential carries its
// ACTIVE, unexpired rotation's previous value so the sidecar can replay a
// 401 once with it. The value takes the same road as the current one — the
// delivery snapshot, then the boot payload — and nothing else: an expired or
// cancelled rotation, a rotation with its value already scrubbed, or another
// credential's rotation delivers nothing.

func seedRotation(t *testing.T, db *sql.DB, id, credID, oldPlain, status string, expiresAt time.Time, rotatedAt time.Time, userID string) {
	t.Helper()
	enc := ""
	if oldPlain != "" {
		var err error
		enc, err = encryption.Encrypt(oldPlain)
		if err != nil {
			t.Fatalf("encrypt old value: %v", err)
		}
	}
	execOrFatal(t, db, `INSERT INTO credential_rotations (id, credential_id, old_value, grace_seconds, rotated_at, expires_at, rotated_by, status)
		VALUES (?, ?, ?, 3600, ?, ?, ?, ?)`,
		id, credID, enc, rotatedAt.UTC().Format(time.RFC3339), expiresAt.UTC().Format(time.RFC3339), userID, status)
}

func TestDeliveredCredentials_CarryRotationGrace(t *testing.T) {
	db := setupTestDB(t)
	ensureEncryptionKey(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)

	const (
		crewID  = "grace-crew"
		agentID = "grace-agent"
	)
	execOrFatal(t, db, `INSERT INTO crews (id, workspace_id, name, slug) VALUES (?, ?, 'C', 'grace-c')`, crewID, wsID)
	execOrFatal(t, db, `INSERT INTO agents (id, crew_id, workspace_id, name, slug) VALUES (?, ?, ?, 'A', 'grace-a')`,
		agentID, crewID, wsID)

	now := time.Now()
	grant := func(credID, envVar string) {
		execOrFatal(t, db, `INSERT INTO agent_credentials (id, agent_id, credential_id, env_var_name, priority, created_at)
			VALUES (?, ?, ?, ?, 0, datetime('now'))`, "ac-"+credID, agentID, credID, envVar)
	}

	// active: one ACTIVE unexpired rotation → its old value is delivered.
	seedCredentialEnc(t, db, wsID, userID, "cred-active", "ANTHROPIC_API_KEY", "sk-ant-new")
	execOrFatal(t, db, `UPDATE credentials SET provider = 'ANTHROPIC', type = 'API_KEY' WHERE id = 'cred-active'`)
	grant("cred-active", "ANTHROPIC_API_KEY")
	seedRotation(t, db, "rot-active", "cred-active", "sk-ant-old", "ACTIVE", now.Add(time.Hour), now.Add(-time.Minute), userID)

	// twice: two ACTIVE rotations → the most recent one's value wins.
	seedCredentialEnc(t, db, wsID, userID, "cred-twice", "OPENAI_API_KEY", "sk-oa-3")
	execOrFatal(t, db, `UPDATE credentials SET provider = 'OPENAI', type = 'API_KEY' WHERE id = 'cred-twice'`)
	grant("cred-twice", "OPENAI_API_KEY")
	seedRotation(t, db, "rot-twice-1", "cred-twice", "sk-oa-1", "ACTIVE", now.Add(time.Hour), now.Add(-2*time.Hour), userID)
	seedRotation(t, db, "rot-twice-2", "cred-twice", "sk-oa-2", "ACTIVE", now.Add(2*time.Hour), now.Add(-time.Minute), userID)

	// expired-row: ACTIVE in the table but expires_at has passed (the hourly
	// worker has not swept yet) → nothing.
	seedCredentialEnc(t, db, wsID, userID, "cred-expired", "OPENROUTER_API_KEY", "sk-or-new")
	execOrFatal(t, db, `UPDATE credentials SET provider = 'OPENROUTER', type = 'API_KEY' WHERE id = 'cred-expired'`)
	grant("cred-expired", "OPENROUTER_API_KEY")
	seedRotation(t, db, "rot-expired", "cred-expired", "sk-or-old", "ACTIVE", now.Add(-time.Minute), now.Add(-2*time.Hour), userID)

	// cancelled: status CANCELLED, value scrubbed → nothing.
	seedCredentialEnc(t, db, wsID, userID, "cred-cancelled", "GOOGLE_API_KEY", "AIza-new")
	execOrFatal(t, db, `UPDATE credentials SET provider = 'GOOGLE', type = 'API_KEY' WHERE id = 'cred-cancelled'`)
	grant("cred-cancelled", "GOOGLE_API_KEY")
	seedRotation(t, db, "rot-cancelled", "cred-cancelled", "", "CANCELLED", now.Add(time.Hour), now.Add(-time.Minute), userID)

	// plain: never rotated → nothing.
	seedCredentialEnc(t, db, wsID, userID, "cred-plain", "PLAIN_KEY", "plain-value")
	execOrFatal(t, db, `UPDATE credentials SET provider = 'ANTHROPIC', type = 'API_KEY' WHERE id = 'cred-plain'`)
	grant("cred-plain", "PLAIN_KEY")

	// foreign: an ACTIVE rotation on a credential this agent is NOT granted
	// must not attach to anything delivered.
	seedCredentialEnc(t, db, wsID, userID, "cred-foreign", "FOREIGN_KEY", "foreign-new")
	seedRotation(t, db, "rot-foreign", "cred-foreign", "foreign-old", "ACTIVE", now.Add(time.Hour), now.Add(-time.Minute), userID)

	delivered, _, err := loadDeliveredCredentials(context.Background(), db, agentID)
	if err != nil {
		t.Fatalf("loadDeliveredCredentials: %v", err)
	}
	byID := map[string]deliveredCredential{}
	for _, d := range delivered {
		byID[d.ID] = d
	}
	if _, ok := byID["cred-foreign"]; ok {
		t.Fatal("cred-foreign was delivered to an agent that holds no grant on it")
	}

	tests := []struct {
		credID      string
		wantGrace   string
		wantRotID   string
		wantExpires bool
	}{
		{"cred-active", "sk-ant-old", "rot-active", true},
		{"cred-twice", "sk-oa-2", "rot-twice-2", true},
		{"cred-expired", "", "", false},
		{"cred-cancelled", "", "", false},
		{"cred-plain", "", "", false},
	}
	for _, tc := range tests {
		t.Run(tc.credID, func(t *testing.T) {
			d, ok := byID[tc.credID]
			if !ok {
				t.Fatalf("%s not delivered", tc.credID)
			}
			got := deliveredGraceToken(d, "", decryptCredential)
			if got != tc.wantGrace {
				t.Errorf("grace token = %q, want %q", got, tc.wantGrace)
			}
			if d.GraceRotationID != tc.wantRotID {
				t.Errorf("GraceRotationID = %q, want %q", d.GraceRotationID, tc.wantRotID)
			}
			if (d.GraceExpiresAt != "") != tc.wantExpires {
				t.Errorf("GraceExpiresAt = %q, want set=%v", d.GraceExpiresAt, tc.wantExpires)
			}
			if tc.wantGrace == "" && d.GraceEncryptedValue != "" {
				t.Errorf("an unusable rotation still shipped ciphertext to the delivery: %q", d.GraceEncryptedValue)
			}
		})
	}
}

// An endpoint-shaped credential (OPENAI_COMPAT) stores {baseURL,apiKey,headers}
// as one object; the grace value is the previous object's apiKey, and only
// when that object pointed at the SAME upstream — a previous key for a
// different gateway is not a fallback for this one.
func TestDeliveredGraceToken_EndpointValueSplitsAndPinsBaseURL(t *testing.T) {
	ensureEncryptionKey(t)
	same, err := buildEndpointValue("https://gw.example.com/v1", "old-key", nil)
	if err != nil {
		t.Fatal(err)
	}
	other, err := buildEndpointValue("https://elsewhere.example.com/v1", "old-key", nil)
	if err != nil {
		t.Fatal(err)
	}
	encSame, _ := encryption.Encrypt(same)
	encOther, _ := encryption.Encrypt(other)
	encBare, _ := encryption.Encrypt("bare-old-key")
	encPending, _ := encryption.Encrypt(pendingSentinelOAuth)

	tests := []struct {
		name     string
		provider string
		enc      string
		baseURL  string
		want     string
	}{
		{"same upstream: the previous apiKey", "OPENAI_COMPAT", encSame, "https://gw.example.com/v1", "old-key"},
		{"different upstream: nothing", "OPENAI_COMPAT", encOther, "https://gw.example.com/v1", ""},
		{"malformed endpoint object: nothing", "OPENAI_COMPAT", encBare, "https://gw.example.com/v1", ""},
		{"bare provider: the value itself", "ANTHROPIC", encBare, "", "bare-old-key"},
		{"pending sentinel is never a grace value", "ANTHROPIC", encPending, "", ""},
		{"undecryptable ciphertext: nothing", "ANTHROPIC", "not-ciphertext", "", ""},
		{"no rotation: nothing", "ANTHROPIC", "", "", ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			d := deliveredCredential{ID: "c", Provider: tc.provider, GraceEncryptedValue: tc.enc}
			if got := deliveredGraceToken(d, tc.baseURL, decryptCredential); got != tc.want {
				t.Errorf("deliveredGraceToken = %q, want %q", got, tc.want)
			}
		})
	}
}

// The internal metadata listing tells the sidecar's reaper whether each
// credential still has an ACTIVE rotation (#1882): rotation_grace_until is
// the open window's end, and absent once the rotation is cancelled, expired,
// or was never there. It is metadata the public rotation listing already
// exposes — never the value — and the reaper drops its copy of the grace
// value for any credential listed without it.
func TestListCredentials_RotationGraceUntil(t *testing.T) {
	h, db, userID, wsID := covICRig(t)
	now := time.Now()
	for _, id := range []string{"grace-open", "grace-cancelled", "grace-expired", "grace-none"} {
		covICSeedCredScoped(t, db, wsID, userID, id, "WORKSPACE")
	}
	open := now.Add(time.Hour)
	seedRotation(t, db, "rot-open", "grace-open", "old", "ACTIVE", open, now.Add(-time.Minute), userID)
	seedRotation(t, db, "rot-cancelled", "grace-cancelled", "", "CANCELLED", now.Add(time.Hour), now.Add(-time.Minute), userID)
	seedRotation(t, db, "rot-expired", "grace-expired", "old", "ACTIVE", now.Add(-time.Minute), now.Add(-2*time.Hour), userID)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/internal/credentials?workspace_id="+wsID, nil)
	req.RemoteAddr = "127.0.0.1:9999"
	rec := httptest.NewRecorder()
	h.ListCredentials(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
	}
	var out []map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	got := map[string]any{}
	for _, row := range out {
		id, _ := row["id"].(string)
		got[id] = row["rotation_grace_until"]
		for _, k := range []string{"old_value", "grace_token", "access_token"} {
			if _, leaked := row[k]; leaked {
				t.Errorf("%s: listing carries %q", id, k)
			}
		}
	}
	if got["grace-open"] != open.UTC().Format(time.RFC3339) {
		t.Errorf("grace-open: rotation_grace_until = %v, want %s", got["grace-open"], open.UTC().Format(time.RFC3339))
	}
	for _, id := range []string{"grace-cancelled", "grace-expired", "grace-none"} {
		if v, ok := got[id]; !ok || v != nil {
			t.Errorf("%s: rotation_grace_until = %v (present=%v), want absent", id, v, ok)
		}
	}
}

// The API side of #1882's hop: the boot resolver opens the grace ciphertext
// and puts the token on mcpCredEntry under the tags chatbridge decodes. Bare
// and endpoint-shaped credentials both, since the endpoint one splits its
// object first.
func TestGrace_BootResolverCarriesRotationGrace(t *testing.T) {
	db := setupTestDB(t)
	ensureEncryptionKey(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	h := covCfgHandler(db)

	const (
		crewID  = "bg-crew"
		agentID = "bg-agent"
	)
	execOrFatal(t, db, `INSERT INTO crews (id, workspace_id, name, slug) VALUES (?, ?, 'C', 'bg-c')`, crewID, wsID)
	execOrFatal(t, db, `INSERT INTO agents (id, crew_id, workspace_id, name, slug) VALUES (?, ?, ?, 'A', 'bg-a')`, agentID, crewID, wsID)
	now := time.Now()

	seedCredentialEnc(t, db, wsID, userID, "bg-bare", "ANTHROPIC_API_KEY", "sk-new")
	execOrFatal(t, db, `UPDATE credentials SET provider = 'ANTHROPIC', type = 'API_KEY' WHERE id = 'bg-bare'`)
	execOrFatal(t, db, `INSERT INTO agent_credentials (id, agent_id, credential_id, env_var_name, priority, created_at)
		VALUES ('bg-ac-1', ?, 'bg-bare', 'ANTHROPIC_API_KEY', 0, datetime('now'))`, agentID)
	seedRotation(t, db, "bg-rot-1", "bg-bare", "sk-old", "ACTIVE", now.Add(time.Hour), now.Add(-time.Minute), userID)

	seedCredentialEnc(t, db, wsID, userID, "bg-compat", "COMPAT_KEY",
		`{"baseURL":"https://gw.example/v1","apiKey":"gw-new"}`)
	execOrFatal(t, db, `UPDATE credentials SET provider = 'OPENAI_COMPAT', type = 'API_KEY' WHERE id = 'bg-compat'`)
	execOrFatal(t, db, `INSERT INTO agent_credentials (id, agent_id, credential_id, env_var_name, priority, created_at)
		VALUES ('bg-ac-2', ?, 'bg-compat', 'COMPAT_KEY', 0, datetime('now'))`, agentID)
	seedRotation(t, db, "bg-rot-2", "bg-compat", `{"baseURL":"https://gw.example/v1","apiKey":"gw-old"}`, "ACTIVE",
		now.Add(time.Hour), now.Add(-time.Minute), userID)

	creds, err := h.resolveAgentCredentials(httptest.NewRequest("GET", "/", nil), agentID)
	if err != nil {
		t.Fatalf("resolveAgentCredentials: %v", err)
	}
	byID := map[string]mcpCredEntry{}
	for _, c := range creds {
		byID[c.ID] = c
	}
	bare, ok := byID["bg-bare"]
	if !ok {
		t.Fatal("bg-bare not delivered")
	}
	if bare.Value != "sk-new" || bare.GraceToken != "sk-old" || bare.GraceRotationID != "bg-rot-1" || bare.GraceExpiresAt == "" {
		t.Errorf("bare credential: %+v", bare)
	}
	compat, ok := byID["bg-compat"]
	if !ok {
		t.Fatal("bg-compat not delivered")
	}
	if compat.Value != "gw-new" || compat.GraceToken != "gw-old" || compat.BaseURL != "https://gw.example/v1" {
		t.Errorf("endpoint credential: the previous object was not split to its apiKey: %+v", compat)
	}

	blob, err := json.Marshal(bare)
	if err != nil {
		t.Fatal(err)
	}
	var wire map[string]any
	_ = json.Unmarshal(blob, &wire)
	for _, k := range []string{"grace_token", "grace_expires_at", "grace_rotation_id"} {
		if _, ok := wire[k]; !ok {
			t.Errorf("wire is missing %q — chatbridge decodes that tag", k)
		}
	}
}

func TestListCredentials_GraceIdentitiesTrackOverlappingRotations(t *testing.T) {
	h, db, userID, wsID := covICRig(t)
	covICSeedCredScoped(t, db, wsID, userID, "overlap", "WORKSPACE")
	now := time.Now()
	seedRotation(t, db, "older-active", "overlap", "old-a", "ACTIVE", now.Add(time.Hour), now.Add(-2*time.Minute), userID)
	seedRotation(t, db, "held-newer", "overlap", "old-b", "ACTIVE", now.Add(time.Hour), now.Add(-time.Minute), userID)
	readIDs := func() map[string]bool {
		t.Helper()
		req := httptest.NewRequest(http.MethodGet, "/api/v1/internal/credentials?workspace_id="+wsID, nil)
		req.RemoteAddr = "127.0.0.1:9999"
		rec := httptest.NewRecorder()
		h.ListCredentials(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("read: %d %s", rec.Code, rec.Body.String())
		}
		var rows []struct {
			ID        string   `json:"id"`
			Rotations []string `json:"rotation_grace_ids"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &rows); err != nil {
			t.Fatal(err)
		}
		ids := map[string]bool{}
		for _, row := range rows {
			if row.ID == "overlap" {
				for _, id := range row.Rotations {
					ids[id] = true
				}
			}
		}
		return ids
	}
	if ids := readIDs(); len(ids) != 2 || !ids["older-active"] || !ids["held-newer"] {
		t.Fatalf("active identities: %v", ids)
	}
	if _, err := db.Exec(`UPDATE credential_rotations SET status='CANCELLED',old_value='' WHERE id='held-newer'`); err != nil {
		t.Fatal(err)
	}
	if ids := readIDs(); len(ids) != 1 || !ids["older-active"] || ids["held-newer"] {
		t.Fatalf("cancelled identity retained: %v", ids)
	}
}
