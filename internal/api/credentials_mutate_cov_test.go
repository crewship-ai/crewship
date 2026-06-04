package api

// Additional branch coverage for credentials_mutate.go (Create + Update).
// Mirrors the setup style in credentials_test.go: newCredHandler wires a
// fresh DB + a parallel-safe encryption key, so the encrypt/decrypt paths
// in Create/Update work. New helpers are prefixed covCM per the task spec.

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/encryption"
)

// covCMCreate fires a POST /api/v1/credentials with the given body and
// context (user + workspace + role) and returns the recorder.
func covCMCreate(t *testing.T, h *CredentialHandler, userID, wsID, role, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest("POST", "/api/v1/credentials", bytes.NewBufferString(body))
	req = withWorkspaceUser(req, userID, wsID, role)
	rr := httptest.NewRecorder()
	h.Create(rr, req)
	return rr
}

// covCMUpdate fires a PATCH /api/v1/credentials/{id}.
func covCMUpdate(t *testing.T, h *CredentialHandler, userID, wsID, role, credID, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest("PATCH", "/api/v1/credentials/"+credID, bytes.NewBufferString(body))
	req.SetPathValue("credentialId", credID)
	req = withWorkspaceUser(req, userID, wsID, role)
	rr := httptest.NewRecorder()
	h.Update(rr, req)
	return rr
}

// covCMSeedCrew inserts a crew row so crew-id validation passes.
func covCMSeedCrew(t *testing.T, db *sql.DB, wsID, crewID string) {
	t.Helper()
	if _, err := db.Exec(`INSERT INTO crews (id, workspace_id, name, slug, created_at, updated_at)
		VALUES (?, ?, ?, ?, datetime('now'), datetime('now'))`,
		crewID, wsID, crewID, crewID); err != nil {
		t.Fatalf("seed crew %s: %v", crewID, err)
	}
}

// ---- Create: per-type payload validation (422-style 400s) ----

func TestCovCMCreate_UserPassMissingUsername(t *testing.T) {
	t.Parallel()
	h, db := newCredHandler(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)

	rr := covCMCreate(t, h, userID, wsID, "OWNER", `{"name":"up","value":"pw","type":"USERPASS"}`)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body: %s", rr.Code, rr.Body.String())
	}
	if !strings.Contains(rr.Body.String(), "username is required") {
		t.Errorf("body missing reason: %s", rr.Body.String())
	}
}

func TestCovCMCreate_UserPassSuccessStoresUsernamePlaintext(t *testing.T) {
	t.Parallel()
	h, db := newCredHandler(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)

	rr := covCMCreate(t, h, userID, wsID, "OWNER",
		`{"name":"up","value":"s3cret","type":"USERPASS","username":"user@gmail.com"}`)
	if rr.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201; body: %s", rr.Code, rr.Body.String())
	}
	// username stored cleartext, password encrypted.
	var username, encVal string
	if err := db.QueryRow(`SELECT username, encrypted_value FROM credentials WHERE name='up'`).
		Scan(&username, &encVal); err != nil {
		t.Fatalf("load row: %v", err)
	}
	if username != "user@gmail.com" {
		t.Errorf("username = %q, want user@gmail.com", username)
	}
	plain, err := encryption.Decrypt(encVal)
	if err != nil {
		t.Fatalf("decrypt: %v", err)
	}
	if plain != "s3cret" {
		t.Errorf("decrypted password = %q, want s3cret", plain)
	}
}

func TestCovCMCreate_SSHKeyNonPEM(t *testing.T) {
	t.Parallel()
	h, db := newCredHandler(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)

	rr := covCMCreate(t, h, userID, wsID, "OWNER",
		`{"name":"k","value":"ssh-rsa AAAAB3","type":"SSH_KEY"}`)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body: %s", rr.Code, rr.Body.String())
	}
	if !strings.Contains(rr.Body.String(), "PEM-encoded private key") {
		t.Errorf("body missing reason: %s", rr.Body.String())
	}
}

func TestCovCMCreate_CertificateNonPEM(t *testing.T) {
	t.Parallel()
	h, db := newCredHandler(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)

	rr := covCMCreate(t, h, userID, wsID, "OWNER",
		`{"name":"c","value":"notpem","type":"CERTIFICATE"}`)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body: %s", rr.Code, rr.Body.String())
	}
	if !strings.Contains(rr.Body.String(), "certificate must be PEM") {
		t.Errorf("body missing reason: %s", rr.Body.String())
	}
}

func TestCovCMCreate_UnknownType(t *testing.T) {
	t.Parallel()
	h, db := newCredHandler(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)

	rr := covCMCreate(t, h, userID, wsID, "OWNER",
		`{"name":"x","value":"v","type":"BANANA"}`)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body: %s", rr.Code, rr.Body.String())
	}
}

func TestCovCMCreate_SSHKeyPEMSuccess(t *testing.T) {
	t.Parallel()
	h, db := newCredHandler(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)

	pem := pemFixture("OPENSSH PRIVATE KEY", "abc123")
	body, _ := json.Marshal(map[string]any{
		"name": "sshk", "value": pem, "type": "SSH_KEY",
	})
	rr := covCMCreate(t, h, userID, wsID, "OWNER", string(body))
	if rr.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201; body: %s", rr.Code, rr.Body.String())
	}
	var typ string
	db.QueryRow(`SELECT type FROM credentials WHERE name='sshk'`).Scan(&typ)
	if typ != "SSH_KEY" {
		t.Errorf("type = %q, want SSH_KEY", typ)
	}
}

// ---- Create: name-length validation ----

func TestCovCMCreate_NameTooLong(t *testing.T) {
	t.Parallel()
	h, db := newCredHandler(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)

	longName := strings.Repeat("a", 256)
	body, _ := json.Marshal(map[string]any{"name": longName, "value": "v"})
	rr := covCMCreate(t, h, userID, wsID, "OWNER", string(body))
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body: %s", rr.Code, rr.Body.String())
	}
	if !strings.Contains(rr.Body.String(), "name is required") {
		t.Errorf("body missing reason: %s", rr.Body.String())
	}
}

// ---- Create: manifest pending slot (value omitted + pending=true) ----

func TestCovCMCreate_ManifestPending(t *testing.T) {
	t.Parallel()
	h, db := newCredHandler(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)

	rr := covCMCreate(t, h, userID, wsID, "OWNER",
		`{"name":"slot","type":"SECRET","pending":true}`)
	if rr.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201; body: %s", rr.Code, rr.Body.String())
	}
	var status string
	db.QueryRow(`SELECT status FROM credentials WHERE name='slot'`).Scan(&status)
	if status != "PENDING" {
		t.Errorf("status = %q, want PENDING", status)
	}
	var resp credentialResponse
	json.Unmarshal(rr.Body.Bytes(), &resp)
	if resp.Status != "PENDING" {
		t.Errorf("resp.Status = %q, want PENDING", resp.Status)
	}
}

func TestCovCMCreate_ManifestPendingUnknownTypeRejected(t *testing.T) {
	t.Parallel()
	h, db := newCredHandler(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)

	rr := covCMCreate(t, h, userID, wsID, "OWNER",
		`{"name":"slot","type":"BANANA","pending":true}`)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body: %s", rr.Code, rr.Body.String())
	}
}

// ---- Create: security_level clamping (valid 1..3) ----

func TestCovCMCreate_SecurityLevelHonored(t *testing.T) {
	t.Parallel()
	h, db := newCredHandler(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)

	rr := covCMCreate(t, h, userID, wsID, "OWNER",
		`{"name":"sl","value":"v","security_level":3}`)
	if rr.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201; body: %s", rr.Code, rr.Body.String())
	}
	var lvl int
	db.QueryRow(`SELECT security_level FROM credentials WHERE name='sl'`).Scan(&lvl)
	if lvl != 3 {
		t.Errorf("security_level = %d, want 3", lvl)
	}
}

func TestCovCMCreate_SecurityLevelOutOfRangeDefaultsTo1(t *testing.T) {
	t.Parallel()
	h, db := newCredHandler(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)

	// 9 is out of [1,3]; Create silently keeps the default of 1.
	rr := covCMCreate(t, h, userID, wsID, "OWNER",
		`{"name":"sl2","value":"v","security_level":9}`)
	if rr.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201; body: %s", rr.Code, rr.Body.String())
	}
	var lvl int
	db.QueryRow(`SELECT security_level FROM credentials WHERE name='sl2'`).Scan(&lvl)
	if lvl != 1 {
		t.Errorf("security_level = %d, want 1 (out-of-range default)", lvl)
	}
}

// ---- Create: tags + description round-trip ----

func TestCovCMCreate_TagsAndDescription(t *testing.T) {
	t.Parallel()
	h, db := newCredHandler(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)

	rr := covCMCreate(t, h, userID, wsID, "OWNER",
		`{"name":"tagged","value":"v","description":"d","tags":["prod","db"]}`)
	if rr.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201; body: %s", rr.Code, rr.Body.String())
	}
	var resp credentialResponse
	json.Unmarshal(rr.Body.Bytes(), &resp)
	if len(resp.Tags) != 2 {
		t.Errorf("resp.Tags = %v, want 2 entries", resp.Tags)
	}
	var tagsCol sql.NullString
	db.QueryRow(`SELECT tags FROM credentials WHERE name='tagged'`).Scan(&tagsCol)
	if !tagsCol.Valid || !strings.Contains(tagsCol.String, "prod") {
		t.Errorf("tags column = %v, want JSON containing prod", tagsCol)
	}
}

// ---- Create: attribution branches (resolveCreateAttribution via handler) ----

func TestCovCMCreate_InvalidActorType(t *testing.T) {
	t.Parallel()
	h, db := newCredHandler(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)

	rr := covCMCreate(t, h, userID, wsID, "OWNER",
		`{"name":"a","value":"v","created_by_actor_type":"robot"}`)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body: %s", rr.Code, rr.Body.String())
	}
	if !strings.Contains(rr.Body.String(), "user|agent|system") {
		t.Errorf("body missing reason: %s", rr.Body.String())
	}
}

func TestCovCMCreate_AgentActorRequiresPrivilege(t *testing.T) {
	t.Parallel()
	h, db := newCredHandler(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)

	// MANAGER passes the create role-gate but is NOT OWNER/ADMIN, so a
	// non-self actor attribution is forbidden.
	rr := covCMCreate(t, h, userID, wsID, "MANAGER",
		`{"name":"a","value":"v","created_by_actor_type":"agent","created_by_actor_id":"ag1"}`)
	if rr.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403; body: %s", rr.Code, rr.Body.String())
	}
}

func TestCovCMCreate_AgentActorMissingID(t *testing.T) {
	t.Parallel()
	h, db := newCredHandler(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)

	rr := covCMCreate(t, h, userID, wsID, "OWNER",
		`{"name":"a","value":"v","created_by_actor_type":"agent"}`)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body: %s", rr.Code, rr.Body.String())
	}
	if !strings.Contains(rr.Body.String(), "created_by_actor_id is required") {
		t.Errorf("body missing reason: %s", rr.Body.String())
	}
}

func TestCovCMCreate_AgentActorSuccess(t *testing.T) {
	t.Parallel()
	h, db := newCredHandler(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)

	rr := covCMCreate(t, h, userID, wsID, "OWNER",
		`{"name":"a","value":"v","created_by_actor_type":"agent","created_by_actor_id":"ag1"}`)
	if rr.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201; body: %s", rr.Code, rr.Body.String())
	}
	var actorType string
	var actorID sql.NullString
	db.QueryRow(`SELECT created_by_actor_type, created_by_actor_id FROM credentials WHERE name='a'`).
		Scan(&actorType, &actorID)
	if actorType != "agent" || !actorID.Valid || actorID.String != "ag1" {
		t.Errorf("attribution = (%q,%v), want (agent,ag1)", actorType, actorID)
	}
}

func TestCovCMCreate_SystemActorRejectsID(t *testing.T) {
	t.Parallel()
	h, db := newCredHandler(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)

	rr := covCMCreate(t, h, userID, wsID, "OWNER",
		`{"name":"a","value":"v","created_by_actor_type":"system","created_by_actor_id":"x"}`)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body: %s", rr.Code, rr.Body.String())
	}
	if !strings.Contains(rr.Body.String(), "must be empty when") {
		t.Errorf("body missing reason: %s", rr.Body.String())
	}
}

func TestCovCMCreate_UserActorForeignIDForbidden(t *testing.T) {
	t.Parallel()
	h, db := newCredHandler(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)

	// MANAGER (not OWNER/ADMIN) supplying a foreign user actor_id is a
	// spoof attempt → 403.
	rr := covCMCreate(t, h, userID, wsID, "MANAGER",
		`{"name":"a","value":"v","created_by_actor_type":"user","created_by_actor_id":"someone-else"}`)
	if rr.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403; body: %s", rr.Code, rr.Body.String())
	}
}

// ---- Create: provisioned_for_service branches ----

func TestCovCMCreate_ProvisionedForServiceWrongProvenance(t *testing.T) {
	t.Parallel()
	h, db := newCredHandler(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)

	// Right shape but wrong provider/actor → reserved-field rejection.
	rr := covCMCreate(t, h, userID, wsID, "OWNER",
		`{"name":"a","value":"v","provisioned_for_service":"crew/svc"}`)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body: %s", rr.Code, rr.Body.String())
	}
	if !strings.Contains(rr.Body.String(), "reserved for AUTO_MANAGED") {
		t.Errorf("body missing reason: %s", rr.Body.String())
	}
}

func TestCovCMCreate_ProvisionedForServiceBadShape(t *testing.T) {
	t.Parallel()
	h, db := newCredHandler(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)

	// Correct provenance (AUTO_MANAGED + system) but non-canonical value.
	rr := covCMCreate(t, h, userID, wsID, "OWNER",
		`{"name":"a","value":"v","provider":"AUTO_MANAGED","created_by_actor_type":"system","provisioned_for_service":"no-slash"}`)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body: %s", rr.Code, rr.Body.String())
	}
	if !strings.Contains(rr.Body.String(), "must be canonical") {
		t.Errorf("body missing reason: %s", rr.Body.String())
	}
}

func TestCovCMCreate_ProvisionedForServiceSuccess(t *testing.T) {
	t.Parallel()
	h, db := newCredHandler(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)

	rr := covCMCreate(t, h, userID, wsID, "OWNER",
		`{"name":"a","value":"v","provider":"AUTO_MANAGED","created_by_actor_type":"system","provisioned_for_service":"crew/svc"}`)
	if rr.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201; body: %s", rr.Code, rr.Body.String())
	}
	var pfs sql.NullString
	db.QueryRow(`SELECT provisioned_for_service FROM credentials WHERE name='a'`).Scan(&pfs)
	if !pfs.Valid || pfs.String != "crew/svc" {
		t.Errorf("provisioned_for_service = %v, want crew/svc", pfs)
	}
}

func TestCovCMCreate_ProvisionedForServiceWhitespaceIgnored(t *testing.T) {
	t.Parallel()
	h, db := newCredHandler(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)

	// Whitespace-only → treated as "not stamped", falls through to nil.
	rr := covCMCreate(t, h, userID, wsID, "OWNER",
		`{"name":"a","value":"v","provisioned_for_service":"   "}`)
	if rr.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201; body: %s", rr.Code, rr.Body.String())
	}
	var pfs sql.NullString
	db.QueryRow(`SELECT provisioned_for_service FROM credentials WHERE name='a'`).Scan(&pfs)
	if pfs.Valid {
		t.Errorf("provisioned_for_service = %q, want NULL", pfs.String)
	}
}

// ---- Create: legacy crew_id merge path ----

func TestCovCMCreate_LegacyCrewIDMerged(t *testing.T) {
	t.Parallel()
	h, db := newCredHandler(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	covCMSeedCrew(t, db, wsID, "crew-legacy")

	rr := covCMCreate(t, h, userID, wsID, "OWNER",
		`{"name":"lc","value":"v","crew_id":"crew-legacy"}`)
	if rr.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201; body: %s", rr.Code, rr.Body.String())
	}
	var resp credentialResponse
	json.Unmarshal(rr.Body.Bytes(), &resp)
	if resp.Scope != "CREW" {
		t.Errorf("scope = %q, want CREW", resp.Scope)
	}
	var count int
	db.QueryRow(`SELECT COUNT(*) FROM credential_crews WHERE credential_id=?`, resp.ID).Scan(&count)
	if count != 1 {
		t.Errorf("credential_crews = %d, want 1", count)
	}
}

// ---- Update: security_level validation ----

func TestCovCMUpdate_SecurityLevelBadType(t *testing.T) {
	t.Parallel()
	h, db := newCredHandler(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	seedCredentialEnc(t, db, wsID, userID, "c1", "n", "v")

	rr := covCMUpdate(t, h, userID, wsID, "OWNER", "c1", `{"security_level":"high"}`)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body: %s", rr.Code, rr.Body.String())
	}
	if !strings.Contains(rr.Body.String(), "security_level must be 1, 2, or 3") {
		t.Errorf("body missing reason: %s", rr.Body.String())
	}
}

func TestCovCMUpdate_SecurityLevelOutOfRange(t *testing.T) {
	t.Parallel()
	h, db := newCredHandler(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	seedCredentialEnc(t, db, wsID, userID, "c1", "n", "v")

	rr := covCMUpdate(t, h, userID, wsID, "OWNER", "c1", `{"security_level":7}`)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body: %s", rr.Code, rr.Body.String())
	}
}

func TestCovCMUpdate_SecurityLevelSuccess(t *testing.T) {
	t.Parallel()
	h, db := newCredHandler(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	seedCredentialEnc(t, db, wsID, userID, "c1", "n", "v")

	rr := covCMUpdate(t, h, userID, wsID, "OWNER", "c1", `{"security_level":2}`)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", rr.Code, rr.Body.String())
	}
	var lvl int
	db.QueryRow(`SELECT security_level FROM credentials WHERE id='c1'`).Scan(&lvl)
	if lvl != 2 {
		t.Errorf("security_level = %d, want 2", lvl)
	}
}

// ---- Update: tags set + clear ----

func TestCovCMUpdate_TagsSet(t *testing.T) {
	t.Parallel()
	h, db := newCredHandler(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	seedCredentialEnc(t, db, wsID, userID, "c1", "n", "v")

	rr := covCMUpdate(t, h, userID, wsID, "OWNER", "c1", `{"tags":["a","b"]}`)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", rr.Code, rr.Body.String())
	}
	var tagsCol sql.NullString
	db.QueryRow(`SELECT tags FROM credentials WHERE id='c1'`).Scan(&tagsCol)
	if !tagsCol.Valid || !strings.Contains(tagsCol.String, "a") {
		t.Errorf("tags = %v, want JSON containing a", tagsCol)
	}
}

func TestCovCMUpdate_TagsCleared(t *testing.T) {
	t.Parallel()
	h, db := newCredHandler(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	seedCredentialEnc(t, db, wsID, userID, "c1", "n", "v")

	// Empty array clears the column back to NULL.
	rr := covCMUpdate(t, h, userID, wsID, "OWNER", "c1", `{"tags":[]}`)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", rr.Code, rr.Body.String())
	}
	var tagsCol sql.NullString
	db.QueryRow(`SELECT tags FROM credentials WHERE id='c1'`).Scan(&tagsCol)
	if tagsCol.Valid {
		t.Errorf("tags = %q, want NULL after clear", tagsCol.String)
	}
}

// ---- Update: value-must-be-string at top-level value handler ----

func TestCovCMUpdate_ValueNotStringRejected(t *testing.T) {
	t.Parallel()
	h, db := newCredHandler(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	seedCredentialEnc(t, db, wsID, userID, "c1", "n", "v")

	rr := covCMUpdate(t, h, userID, wsID, "OWNER", "c1", `{"value":123}`)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body: %s", rr.Code, rr.Body.String())
	}
	if !strings.Contains(rr.Body.String(), "value must be a string") {
		t.Errorf("body missing reason: %s", rr.Body.String())
	}
}

// ---- Update: provider metadata + rotation reset semantics ----

func TestCovCMUpdate_RotateResetsStatusAndClearsError(t *testing.T) {
	t.Parallel()
	h, db := newCredHandler(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	seedCredentialEnc(t, db, wsID, userID, "c1", "n", "old")
	// Pretend the monitor flagged it.
	db.Exec(`UPDATE credentials SET status='EXPIRED', last_error='boom' WHERE id='c1'`)

	rr := covCMUpdate(t, h, userID, wsID, "OWNER", "c1", `{"value":"fresh"}`)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", rr.Code, rr.Body.String())
	}
	var status string
	var lastErr sql.NullString
	db.QueryRow(`SELECT status, last_error FROM credentials WHERE id='c1'`).Scan(&status, &lastErr)
	if status != "ACTIVE" {
		t.Errorf("status = %q, want ACTIVE after rotate", status)
	}
	if lastErr.Valid {
		t.Errorf("last_error = %q, want NULL after rotate", lastErr.String)
	}
}
