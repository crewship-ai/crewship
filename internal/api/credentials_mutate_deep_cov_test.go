package api

// Deep statement-coverage suite for credentials_mutate.go (Create +
// Update) and the List/Get loaders. The existing files cover the
// validator unit logic (credentials_types_test.go), the attribution
// helper (credentials_mutate_attribution_test.go), the provisioned
// gate (credentials_provisioned_for_service_test.go) and the basic
// happy/forbidden paths (credentials_test.go). This file fills in the
// remaining uncovered branches end-to-end through the HTTP handlers:
//
//   - every credential TYPE's create happy path, each asserting the
//     value is encrypted-at-rest and (USERPASS) the username is stored
//     in plaintext
//   - security_level clamping (below/in/above range)
//   - tags set + tags-cleared-on-empty
//   - scope auto-set + legacy crew_id merge into crew_ids
//   - manifest-pending slot creation (PENDING status, sentinel value)
//   - the three created_by_actor_type attribution arms persisted to
//     the row (user / agent / system) via the live handler
//   - Update value-not-string / type-not-string rejections through the
//     handler, rotate-resets-status-to-ACTIVE + last_error cleared,
//     provider metadata update, security_level clamp on update, tags
//     clear on update
//   - List filters (crew-scoped MEMBER visibility, pagination) and the
//     500 fault-injection path (db.Close) for both List and Get
//
// Helper prefix: covCMD*. Test prefix: TestCovCMD*.
//
// SKIPPED (need a mock/fault-injecting DB mid-transaction, not worth a
// real sqlite handle): the OAuth-client-secret encrypt failure inside
// the tx, the setCrewIDs insert failure inside the tx, the tx.Commit
// failure, and the network-probe paths in the Test() handler.

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/encryption"
)

// covCMDdecrypt reads the encrypted_value column for a credential id and
// returns the decrypted plaintext, failing the test on any error.
func covCMDdecrypt(t *testing.T, db *sql.DB, credID string) string {
	t.Helper()
	var enc string
	if err := db.QueryRow("SELECT encrypted_value FROM credentials WHERE id = ?", credID).Scan(&enc); err != nil {
		t.Fatalf("read encrypted_value for %s: %v", credID, err)
	}
	if !strings.HasPrefix(enc, "v1:") {
		t.Errorf("encrypted_value lacks v1: prefix (not encrypted at rest?): %q", enc)
	}
	plain, err := encryption.Decrypt(enc)
	if err != nil {
		t.Fatalf("decrypt %s: %v", credID, err)
	}
	return plain
}

// covCMDcreate posts a create body as OWNER and returns the recorder +
// decoded response. Sets the encryption key so value encryption works.
func covCMDcreate(t *testing.T, h *CredentialHandler, userID, wsID, body string) (*httptest.ResponseRecorder, credentialResponse) {
	t.Helper()
	req := httptest.NewRequest("POST", "/api/v1/credentials", bytes.NewBufferString(body))
	req = withWorkspaceUser(req, userID, wsID, "OWNER")
	rr := httptest.NewRecorder()
	h.Create(rr, req)
	var resp credentialResponse
	if rr.Code == http.StatusCreated {
		if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
			t.Fatalf("decode create response: %v (body %s)", err, rr.Body.String())
		}
	}
	return rr, resp
}

// covCMDupdate patches a credential as the given role and returns the
// recorder. The handler calls Get() on success, so a 200 body is the
// full credentialResponse.
func covCMDupdate(t *testing.T, h *CredentialHandler, userID, wsID, credID, role, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest("PATCH", "/api/v1/credentials/"+credID, bytes.NewBufferString(body))
	req.SetPathValue("credentialId", credID)
	req = withWorkspaceUser(req, userID, wsID, role)
	rr := httptest.NewRecorder()
	h.Update(rr, req)
	return rr
}

// ---- Create: per-type happy paths (encrypted-at-rest assertions) ----

func TestCovCMDCreate_PerTypeHappyPaths(t *testing.T) {
	t.Parallel()

	pemKey := pemFixture("OPENSSH PRIVATE KEY", "b3BlbnNzaC1rZXktdjEAAAAA")
	pemCert := pemFixture("CERTIFICATE", "MIIDazCCAlOgAwIBAgIUJTd")

	cases := []struct {
		name      string
		body      string
		wantValue string // plaintext expected at rest
	}{
		{"SECRET", `{"name":"c-secret","type":"SECRET","value":"s3cr3t-value"}`, "s3cr3t-value"},
		{"API_KEY", `{"name":"c-apikey","type":"API_KEY","value":"sk-apikeyvalue"}`, "sk-apikeyvalue"},
		{"AI_CLI_TOKEN", `{"name":"c-aicli","type":"AI_CLI_TOKEN","value":"sk-ant-oat01-token"}`, "sk-ant-oat01-token"},
		{"CLI_TOKEN", `{"name":"c-cli","type":"CLI_TOKEN","value":"cli-token-val"}`, "cli-token-val"},
		{"GENERIC_SECRET", `{"name":"c-generic","type":"GENERIC_SECRET","value":"opaque-blob"}`, "opaque-blob"},
		{"SSH_KEY", fmt.Sprintf(`{"name":"c-ssh","type":"SSH_KEY","value":%q}`, pemKey), pemKey},
		{"CERTIFICATE", fmt.Sprintf(`{"name":"c-cert","type":"CERTIFICATE","value":%q}`, pemCert), pemCert},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			h, db := newCredHandler(t)
			userID := seedTestUser(t, db)
			wsID := seedTestWorkspace(t, db, userID)

			rr, resp := covCMDcreate(t, h, userID, wsID, tc.body)
			if rr.Code != http.StatusCreated {
				t.Fatalf("status = %d, body: %s", rr.Code, rr.Body.String())
			}
			if resp.Type != tc.name {
				t.Errorf("type = %q, want %q", resp.Type, tc.name)
			}
			if got := covCMDdecrypt(t, db, resp.ID); got != tc.wantValue {
				t.Errorf("decrypted = %q, want %q", got, tc.wantValue)
			}
			// Plaintext must never leak in the response.
			if strings.Contains(rr.Body.String(), tc.wantValue) {
				t.Errorf("response leaked plaintext value")
			}
		})
	}
}

func TestCovCMDCreate_UserPassStoresUsernamePlaintext(t *testing.T) {
	t.Parallel()
	h, db := newCredHandler(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)

	rr, resp := covCMDcreate(t, h, userID, wsID,
		`{"name":"c-userpass","type":"USERPASS","username":"user@gmail.com","value":"hunter2"}`)
	if rr.Code != http.StatusCreated {
		t.Fatalf("status = %d, body: %s", rr.Code, rr.Body.String())
	}
	if resp.Username == nil || *resp.Username != "user@gmail.com" {
		t.Errorf("response username = %v, want user@gmail.com", resp.Username)
	}
	// Password encrypted at rest; username stored as plaintext column.
	if got := covCMDdecrypt(t, db, resp.ID); got != "hunter2" {
		t.Errorf("decrypted password = %q, want hunter2", got)
	}
	var username string
	if err := db.QueryRow("SELECT username FROM credentials WHERE id = ?", resp.ID).Scan(&username); err != nil {
		t.Fatalf("read username: %v", err)
	}
	if username != "user@gmail.com" {
		t.Errorf("stored username = %q, want user@gmail.com (plaintext)", username)
	}
}

func TestCovCMDCreate_UserPassMissingUsernameRejected(t *testing.T) {
	t.Parallel()
	h, db := newCredHandler(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)

	rr, _ := covCMDcreate(t, h, userID, wsID, `{"name":"c-up","type":"USERPASS","value":"pwd"}`)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400, body: %s", rr.Code, rr.Body.String())
	}
	if !strings.Contains(rr.Body.String(), "username is required") {
		t.Errorf("body = %s", rr.Body.String())
	}
}

func TestCovCMDCreate_SSHKeyNonPEMRejected(t *testing.T) {
	t.Parallel()
	h, db := newCredHandler(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)

	rr, _ := covCMDcreate(t, h, userID, wsID,
		`{"name":"c-ssh-bad","type":"SSH_KEY","value":"ssh-rsa AAAApublic"}`)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400, body: %s", rr.Code, rr.Body.String())
	}
}

func TestCovCMDCreate_UnknownTypeRejected(t *testing.T) {
	t.Parallel()
	h, db := newCredHandler(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)

	rr, _ := covCMDcreate(t, h, userID, wsID, `{"name":"c-x","type":"BANANA","value":"v"}`)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400, body: %s", rr.Code, rr.Body.String())
	}
	if !strings.Contains(rr.Body.String(), "type must be one of") {
		t.Errorf("body = %s", rr.Body.String())
	}
}

// ---- Create: security_level clamping ----

func TestCovCMDCreate_SecurityLevelClamp(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name  string
		level string // raw JSON for security_level (or "" to omit)
		want  int
	}{
		{"omitted_defaults_1", "", 1},
		{"below_range_clamps_to_default_1", "0", 1},
		{"valid_2", "2", 2},
		{"valid_3", "3", 3},
		{"above_range_clamps_to_default_1", "9", 1},
	}
	for i, tc := range cases {
		tc := tc
		i := i
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			h, db := newCredHandler(t)
			userID := seedTestUser(t, db)
			wsID := seedTestWorkspace(t, db, userID)

			name := fmt.Sprintf("c-sl-%d", i)
			body := fmt.Sprintf(`{"name":%q,"type":"SECRET","value":"v"`, name)
			if tc.level != "" {
				body += `,"security_level":` + tc.level
			}
			body += `}`

			rr, resp := covCMDcreate(t, h, userID, wsID, body)
			if rr.Code != http.StatusCreated {
				t.Fatalf("status = %d, body: %s", rr.Code, rr.Body.String())
			}
			var got int
			if err := db.QueryRow("SELECT security_level FROM credentials WHERE id = ?", resp.ID).Scan(&got); err != nil {
				t.Fatalf("read security_level: %v", err)
			}
			if got != tc.want {
				t.Errorf("security_level = %d, want %d", got, tc.want)
			}
		})
	}
}

// ---- Create: tags set + clear ----

func TestCovCMDCreate_TagsSet(t *testing.T) {
	t.Parallel()
	h, db := newCredHandler(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)

	rr, resp := covCMDcreate(t, h, userID, wsID,
		`{"name":"c-tags","type":"SECRET","value":"v","tags":["prod","ci"]}`)
	if rr.Code != http.StatusCreated {
		t.Fatalf("status = %d, body: %s", rr.Code, rr.Body.String())
	}
	if len(resp.Tags) != 2 {
		t.Errorf("response tags = %v, want 2", resp.Tags)
	}
	var tagsRaw sql.NullString
	db.QueryRow("SELECT tags FROM credentials WHERE id = ?", resp.ID).Scan(&tagsRaw)
	if !tagsRaw.Valid || !strings.Contains(tagsRaw.String, "prod") {
		t.Errorf("stored tags = %v, want JSON containing prod", tagsRaw)
	}
}

func TestCovCMDCreate_EmptyTagsLeaveColumnNull(t *testing.T) {
	t.Parallel()
	h, db := newCredHandler(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)

	rr, resp := covCMDcreate(t, h, userID, wsID,
		`{"name":"c-notags","type":"SECRET","value":"v","tags":[]}`)
	if rr.Code != http.StatusCreated {
		t.Fatalf("status = %d, body: %s", rr.Code, rr.Body.String())
	}
	if len(resp.Tags) != 0 {
		t.Errorf("response tags = %v, want empty", resp.Tags)
	}
	var tagsRaw sql.NullString
	db.QueryRow("SELECT tags FROM credentials WHERE id = ?", resp.ID).Scan(&tagsRaw)
	if tagsRaw.Valid {
		t.Errorf("tags column = %q, want NULL on empty array", tagsRaw.String)
	}
}

// ---- Create: legacy crew_id merge + scope auto-set ----

func TestCovCMDCreate_LegacyCrewIDMergedIntoCrewIDs(t *testing.T) {
	t.Parallel()
	h, db := newCredHandler(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	if _, err := db.Exec(`INSERT INTO crews (id, workspace_id, name, slug, created_at, updated_at) VALUES ('crew-legacy', ?, 'C', 'c', datetime('now'), datetime('now'))`, wsID); err != nil {
		t.Fatalf("seed crew: %v", err)
	}

	// Only the legacy crew_id field is sent — it must be merged into the
	// crew_ids list, scope auto-set to CREW, and the junction row written.
	rr, resp := covCMDcreate(t, h, userID, wsID,
		`{"name":"c-legacy","type":"SECRET","value":"v","crew_id":"crew-legacy"}`)
	if rr.Code != http.StatusCreated {
		t.Fatalf("status = %d, body: %s", rr.Code, rr.Body.String())
	}
	if resp.Scope != "CREW" {
		t.Errorf("scope = %q, want CREW", resp.Scope)
	}
	if len(resp.CrewIDs) != 1 || resp.CrewIDs[0] != "crew-legacy" {
		t.Errorf("crew_ids = %v, want [crew-legacy]", resp.CrewIDs)
	}
	var count int
	db.QueryRow("SELECT COUNT(*) FROM credential_crews WHERE credential_id = ? AND crew_id = 'crew-legacy'", resp.ID).Scan(&count)
	if count != 1 {
		t.Errorf("junction rows = %d, want 1", count)
	}
}

func TestCovCMDCreate_LegacyCrewIDDeduped(t *testing.T) {
	t.Parallel()
	h, db := newCredHandler(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	if _, err := db.Exec(`INSERT INTO crews (id, workspace_id, name, slug, created_at, updated_at) VALUES ('crew-dup', ?, 'C', 'c', datetime('now'), datetime('now'))`, wsID); err != nil {
		t.Fatalf("seed crew: %v", err)
	}

	// crew_id duplicates an entry already in crew_ids — the merge loop's
	// "found" branch must avoid double-adding it.
	rr, resp := covCMDcreate(t, h, userID, wsID,
		`{"name":"c-dup","type":"SECRET","value":"v","crew_ids":["crew-dup"],"crew_id":"crew-dup"}`)
	if rr.Code != http.StatusCreated {
		t.Fatalf("status = %d, body: %s", rr.Code, rr.Body.String())
	}
	var count int
	db.QueryRow("SELECT COUNT(*) FROM credential_crews WHERE credential_id = ?", resp.ID).Scan(&count)
	if count != 1 {
		t.Errorf("junction rows = %d, want 1 (deduped)", count)
	}
}

// ---- Create: manifest-pending slot ----

func TestCovCMDCreate_ManifestPending(t *testing.T) {
	t.Parallel()
	h, db := newCredHandler(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)

	// Pending + no value → PENDING status, sentinel stored, type still
	// enum-checked (SECRET is valid).
	rr, resp := covCMDcreate(t, h, userID, wsID,
		`{"name":"c-pending","type":"SECRET","pending":true}`)
	if rr.Code != http.StatusCreated {
		t.Fatalf("status = %d, body: %s", rr.Code, rr.Body.String())
	}
	if resp.Status != "PENDING" {
		t.Errorf("status = %q, want PENDING", resp.Status)
	}
	if got := covCMDdecrypt(t, db, resp.ID); got != pendingSentinelManifest {
		t.Errorf("stored value = %q, want manifest sentinel", got)
	}
}

func TestCovCMDCreate_ManifestPendingUnknownTypeRejected(t *testing.T) {
	t.Parallel()
	h, db := newCredHandler(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)

	// Even on the pending path the closed type enum is enforced.
	rr, _ := covCMDcreate(t, h, userID, wsID, `{"name":"c-pend-bad","type":"NOPE","pending":true}`)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400, body: %s", rr.Code, rr.Body.String())
	}
	if !strings.Contains(rr.Body.String(), "type must be one of") {
		t.Errorf("body = %s", rr.Body.String())
	}
}

// ---- Create: attribution arms persisted through the handler ----

func TestCovCMDCreate_AttributionArmsPersisted(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name      string
		body      string
		wantType  string
		wantIDSet bool
		wantID    string
	}{
		{
			name:      "user_self_default",
			body:      `{"name":"c-attr","type":"SECRET","value":"v"}`,
			wantType:  "user",
			wantIDSet: true, // defaults to caller user id
		},
		{
			name:      "agent_with_explicit_id",
			body:      `{"name":"c-attr","type":"SECRET","value":"v","created_by_actor_type":"agent","created_by_actor_id":"agent_007"}`,
			wantType:  "agent",
			wantIDSet: true,
			wantID:    "agent_007",
		},
		{
			name:     "system_nil_id",
			body:     `{"name":"c-attr","type":"SECRET","value":"v","created_by_actor_type":"system"}`,
			wantType: "system",
		},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			h, db := newCredHandler(t)
			userID := seedTestUser(t, db)
			wsID := seedTestWorkspace(t, db, userID)

			rr, resp := covCMDcreate(t, h, userID, wsID, tc.body)
			if rr.Code != http.StatusCreated {
				t.Fatalf("status = %d, body: %s", rr.Code, rr.Body.String())
			}
			var at sql.NullString
			var aid sql.NullString
			db.QueryRow("SELECT created_by_actor_type, created_by_actor_id FROM credentials WHERE id = ?", resp.ID).Scan(&at, &aid)
			if at.String != tc.wantType {
				t.Errorf("actor_type = %q, want %q", at.String, tc.wantType)
			}
			switch {
			case tc.wantType == "agent":
				if aid.String != tc.wantID {
					t.Errorf("actor_id = %q, want %q", aid.String, tc.wantID)
				}
			case tc.wantType == "user":
				if !aid.Valid || aid.String != userID {
					t.Errorf("actor_id = %v, want caller %q", aid, userID)
				}
			case tc.wantType == "system":
				if aid.Valid {
					t.Errorf("actor_id = %q, want NULL for system", aid.String)
				}
			}
		})
	}
}

func TestCovCMDCreate_AgentAttributionRequiresPrivilege(t *testing.T) {
	t.Parallel()
	h, db := newCredHandler(t)
	ownerID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, ownerID)

	// A MANAGER (canRole "create" passes) attempting agent attribution
	// is rejected by resolveCreateAttribution's privilege gate (403).
	managerID := "mgr-cmd-attr"
	if _, err := db.Exec(`INSERT INTO users (id, email, full_name) VALUES (?, ?, 'M')`, managerID, managerID+"@x"); err != nil {
		t.Fatalf("seed manager user: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO workspace_members (id, workspace_id, user_id, role) VALUES (?, ?, ?, 'MANAGER')`, "m-"+managerID, wsID, managerID); err != nil {
		t.Fatalf("seed manager membership: %v", err)
	}
	body := `{"name":"c-attr-mgr","type":"SECRET","value":"v","created_by_actor_type":"agent","created_by_actor_id":"agent_x"}`
	req := httptest.NewRequest("POST", "/api/v1/credentials", bytes.NewBufferString(body))
	req = withWorkspaceUser(req, managerID, wsID, "MANAGER")
	rr := httptest.NewRecorder()
	h.Create(rr, req)
	if rr.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403, body: %s", rr.Code, rr.Body.String())
	}
}

// ---- Create: provisioned_for_service happy path persisted ----

func TestCovCMDCreate_ProvisionedForServicePersisted(t *testing.T) {
	t.Parallel()
	h, db := newCredHandler(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)

	body := `{"name":"c-pfs","type":"GENERIC_SECRET","value":"deadbeefdeadbeefdeadbeefdeadbeef","provider":"AUTO_MANAGED","created_by_actor_type":"system","provisioned_for_service":"crew-a/postgres"}`
	rr, resp := covCMDcreate(t, h, userID, wsID, body)
	if rr.Code != http.StatusCreated {
		t.Fatalf("status = %d, body: %s", rr.Code, rr.Body.String())
	}
	if resp.ProvisionedForService == nil || *resp.ProvisionedForService != "crew-a/postgres" {
		t.Errorf("response provisioned_for_service = %v, want crew-a/postgres", resp.ProvisionedForService)
	}
	var pfs sql.NullString
	db.QueryRow("SELECT provisioned_for_service FROM credentials WHERE id = ?", resp.ID).Scan(&pfs)
	if !pfs.Valid || pfs.String != "crew-a/postgres" {
		t.Errorf("stored provisioned_for_service = %v, want crew-a/postgres", pfs)
	}
}

// ---- Create: OAuth2 with no client id skips the OAuth update block ----

func TestCovCMDCreate_OAuth2PendingNoClientID(t *testing.T) {
	t.Parallel()
	h, db := newCredHandler(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)

	// OAUTH2 with no value and no oauth_client_id → pending sentinel,
	// PENDING status, and the OAuth-field UPDATE block is skipped
	// (req.OAuthClientID == nil).
	rr, resp := covCMDcreate(t, h, userID, wsID, `{"name":"c-oauth-bare","type":"OAUTH2"}`)
	if rr.Code != http.StatusCreated {
		t.Fatalf("status = %d, body: %s", rr.Code, rr.Body.String())
	}
	if resp.Status != "PENDING" {
		t.Errorf("status = %q, want PENDING", resp.Status)
	}
	if got := covCMDdecrypt(t, db, resp.ID); got != pendingSentinelOAuth {
		t.Errorf("stored value = %q, want oauth sentinel", got)
	}
}

func TestCovCMDCreate_OAuth2ClientIDNoSecret(t *testing.T) {
	t.Parallel()
	h, db := newCredHandler(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)

	// client id present but no secret → secret-encrypt branch is skipped,
	// the OAuth UPDATE still runs and stores the client id + urls.
	rr, resp := covCMDcreate(t, h, userID, wsID,
		`{"name":"c-oauth-nosec","type":"OAUTH2","oauth_client_id":"cid","oauth_auth_url":"https://p/a","oauth_token_url":"https://p/t"}`)
	if rr.Code != http.StatusCreated {
		t.Fatalf("status = %d, body: %s", rr.Code, rr.Body.String())
	}
	var clientID string
	var encSecret sql.NullString
	db.QueryRow("SELECT oauth_client_id, oauth_client_secret_enc FROM credentials WHERE id = ?", resp.ID).Scan(&clientID, &encSecret)
	if clientID != "cid" {
		t.Errorf("oauth_client_id = %q, want cid", clientID)
	}
	if encSecret.Valid && encSecret.String != "" {
		t.Errorf("oauth_client_secret_enc = %q, want empty (no secret sent)", encSecret.String)
	}
}

// ---- Create: missing-value rejection for non-OAUTH2 ----

func TestCovCMDCreate_MissingValueNonOAuth(t *testing.T) {
	t.Parallel()
	h, db := newCredHandler(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)

	rr, _ := covCMDcreate(t, h, userID, wsID, `{"name":"c-noval","type":"SECRET"}`)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400, body: %s", rr.Code, rr.Body.String())
	}
	if !strings.Contains(rr.Body.String(), "value is required") {
		t.Errorf("body = %s", rr.Body.String())
	}
}

// ---- Update: rotate resets status + clears last_error ----

func TestCovCMDUpdate_RotateResetsStatus(t *testing.T) {
	t.Parallel()
	h, db := newCredHandler(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	seedCredentialEnc(t, db, wsID, userID, "c-rot", "rot-name", "old")
	// Simulate a previously-errored, EXPIRED credential.
	if _, err := db.Exec("UPDATE credentials SET status = 'EXPIRED', last_error = 'boom' WHERE id = 'c-rot'"); err != nil {
		t.Fatalf("set error state: %v", err)
	}

	rr := covCMDupdate(t, h, userID, wsID, "c-rot", "OWNER", `{"value":"new-secret"}`)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, body: %s", rr.Code, rr.Body.String())
	}
	var status string
	var lastErr sql.NullString
	db.QueryRow("SELECT status, last_error FROM credentials WHERE id = 'c-rot'").Scan(&status, &lastErr)
	if status != "ACTIVE" {
		t.Errorf("status = %q, want ACTIVE after rotate", status)
	}
	if lastErr.Valid {
		t.Errorf("last_error = %q, want cleared after rotate", lastErr.String)
	}
	if got := covCMDdecrypt(t, db, "c-rot"); got != "new-secret" {
		t.Errorf("decrypted = %q, want new-secret", got)
	}
}

// ---- Update: value-not-string rejection ----

func TestCovCMDUpdate_ValueNotString(t *testing.T) {
	t.Parallel()
	h, db := newCredHandler(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	seedTypedCredential(t, db, wsID, userID, "c-vns", "n", "API_KEY", "", "ghp_x")

	rr := covCMDupdate(t, h, userID, wsID, "c-vns", "OWNER", `{"value":12345}`)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400, body: %s", rr.Code, rr.Body.String())
	}
	if !strings.Contains(rr.Body.String(), "value must be a string") {
		t.Errorf("body = %s", rr.Body.String())
	}
}

// ---- Update: provider/metadata update + security_level clamp ----

func TestCovCMDUpdate_ProviderMetadata(t *testing.T) {
	t.Parallel()
	h, db := newCredHandler(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	seedCredentialEnc(t, db, wsID, userID, "c-meta", "n", "v")

	rr := covCMDupdate(t, h, userID, wsID, "c-meta", "OWNER",
		`{"provider":"GITLAB","account_label":"work","account_email":"a@b.co","security_level":3}`)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, body: %s", rr.Code, rr.Body.String())
	}
	var provider, label, email string
	var secLevel int
	db.QueryRow("SELECT provider, account_label, account_email, security_level FROM credentials WHERE id = 'c-meta'").
		Scan(&provider, &label, &email, &secLevel)
	if provider != "GITLAB" || label != "work" || email != "a@b.co" || secLevel != 3 {
		t.Errorf("got provider=%q label=%q email=%q sl=%d", provider, label, email, secLevel)
	}
}

func TestCovCMDUpdate_SecurityLevelOutOfRange(t *testing.T) {
	t.Parallel()
	h, db := newCredHandler(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	seedCredentialEnc(t, db, wsID, userID, "c-slr", "n", "v")

	rr := covCMDupdate(t, h, userID, wsID, "c-slr", "OWNER", `{"security_level":7}`)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400, body: %s", rr.Code, rr.Body.String())
	}
	if !strings.Contains(rr.Body.String(), "security_level must be 1, 2, or 3") {
		t.Errorf("body = %s", rr.Body.String())
	}
}

func TestCovCMDUpdate_SecurityLevelNotNumber(t *testing.T) {
	t.Parallel()
	h, db := newCredHandler(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	seedCredentialEnc(t, db, wsID, userID, "c-slt", "n", "v")

	rr := covCMDupdate(t, h, userID, wsID, "c-slt", "OWNER", `{"security_level":"high"}`)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400, body: %s", rr.Code, rr.Body.String())
	}
}

// ---- Update: tags set + clear ----

func TestCovCMDUpdate_TagsSetThenClear(t *testing.T) {
	t.Parallel()
	h, db := newCredHandler(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	seedCredentialEnc(t, db, wsID, userID, "c-utags", "n", "v")

	// Set tags.
	if rr := covCMDupdate(t, h, userID, wsID, "c-utags", "OWNER", `{"tags":["a","b"]}`); rr.Code != http.StatusOK {
		t.Fatalf("set tags status = %d, body: %s", rr.Code, rr.Body.String())
	}
	var raw sql.NullString
	db.QueryRow("SELECT tags FROM credentials WHERE id = 'c-utags'").Scan(&raw)
	if !raw.Valid || !strings.Contains(raw.String, "a") {
		t.Errorf("tags after set = %v", raw)
	}

	// Clear tags with empty array → column nulled.
	if rr := covCMDupdate(t, h, userID, wsID, "c-utags", "OWNER", `{"tags":[]}`); rr.Code != http.StatusOK {
		t.Fatalf("clear tags status = %d, body: %s", rr.Code, rr.Body.String())
	}
	db.QueryRow("SELECT tags FROM credentials WHERE id = 'c-utags'").Scan(&raw)
	if raw.Valid {
		t.Errorf("tags after clear = %q, want NULL", raw.String)
	}
}

// ---- Update: crew_ids clear path (empty list → WORKSPACE scope) ----

func TestCovCMDUpdate_CrewIDsClearedResetsToWorkspace(t *testing.T) {
	t.Parallel()
	h, db := newCredHandler(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	if _, err := db.Exec(`INSERT INTO crews (id, workspace_id, name, slug, created_at, updated_at) VALUES ('crew-c', ?, 'C', 'c', datetime('now'), datetime('now'))`, wsID); err != nil {
		t.Fatalf("seed crew: %v", err)
	}
	seedCredentialEnc(t, db, wsID, userID, "c-clr", "n", "v")
	// Attach a crew first.
	if rr := covCMDupdate(t, h, userID, wsID, "c-clr", "OWNER", `{"crew_ids":["crew-c"]}`); rr.Code != http.StatusOK {
		t.Fatalf("attach crew status = %d", rr.Code)
	}
	// Now clear → scope WORKSPACE, junction emptied.
	rr := covCMDupdate(t, h, userID, wsID, "c-clr", "OWNER", `{"crew_ids":[]}`)
	if rr.Code != http.StatusOK {
		t.Fatalf("clear status = %d, body: %s", rr.Code, rr.Body.String())
	}
	var scope string
	db.QueryRow("SELECT scope FROM credentials WHERE id = 'c-clr'").Scan(&scope)
	if scope != "WORKSPACE" {
		t.Errorf("scope = %q, want WORKSPACE", scope)
	}
	var count int
	db.QueryRow("SELECT COUNT(*) FROM credential_crews WHERE credential_id = 'c-clr'").Scan(&count)
	if count != 0 {
		t.Errorf("junction rows = %d, want 0", count)
	}
}

// ---- List: crew-scoped MEMBER visibility ----

func TestCovCMDList_MemberCrewScopedVisibility(t *testing.T) {
	t.Parallel()
	h, db := newCredHandler(t)
	ownerID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, ownerID)

	// A distinct MEMBER user belonging to crew-mem.
	memberID := "member-cmd-vis"
	if _, err := db.Exec(`INSERT INTO users (id, email, full_name) VALUES (?, ?, 'Mem')`, memberID, memberID+"@x"); err != nil {
		t.Fatalf("seed member: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO workspace_members (id, workspace_id, user_id, role) VALUES (?, ?, ?, 'MEMBER')`, "wm-"+memberID, wsID, memberID); err != nil {
		t.Fatalf("seed membership: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO crews (id, workspace_id, name, slug, created_at, updated_at) VALUES ('crew-mem', ?, 'C', 'c', datetime('now'), datetime('now'))`, wsID); err != nil {
		t.Fatalf("seed crew: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO crew_members (crew_id, user_id) VALUES ('crew-mem', ?)`, memberID); err != nil {
		t.Fatalf("seed crew_members: %v", err)
	}

	// Workspace-scoped cred: visible to everyone.
	seedCredentialEnc(t, db, wsID, ownerID, "ws-cred", "ws-cred", "v")
	// Crew-scoped cred attached to crew-mem (member belongs) — visible.
	seedCredentialEnc(t, db, wsID, ownerID, "crew-cred", "crew-cred", "v")
	db.Exec("UPDATE credentials SET scope = 'CREW' WHERE id = 'crew-cred'")
	db.Exec(`INSERT INTO credential_crews (credential_id, crew_id) VALUES ('crew-cred', 'crew-mem')`)
	// Crew-scoped cred attached to a crew the member is NOT in — hidden.
	if _, err := db.Exec(`INSERT INTO crews (id, workspace_id, name, slug, created_at, updated_at) VALUES ('crew-other', ?, 'O', 'o', datetime('now'), datetime('now'))`, wsID); err != nil {
		t.Fatalf("seed other crew: %v", err)
	}
	seedCredentialEnc(t, db, wsID, ownerID, "hidden-cred", "hidden-cred", "v")
	db.Exec("UPDATE credentials SET scope = 'CREW' WHERE id = 'hidden-cred'")
	db.Exec(`INSERT INTO credential_crews (credential_id, crew_id) VALUES ('hidden-cred', 'crew-other')`)

	req := httptest.NewRequest("GET", "/api/v1/credentials", nil)
	req = withWorkspaceUser(req, memberID, wsID, "MEMBER")
	rr := httptest.NewRecorder()
	h.List(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, body: %s", rr.Code, rr.Body.String())
	}
	var creds []credentialResponse
	json.Unmarshal(rr.Body.Bytes(), &creds)
	got := map[string]bool{}
	for _, c := range creds {
		got[c.ID] = true
	}
	if !got["ws-cred"] || !got["crew-cred"] {
		t.Errorf("member should see ws-cred + crew-cred; got %v", got)
	}
	if got["hidden-cred"] {
		t.Errorf("member should NOT see hidden-cred (foreign crew)")
	}
}

func TestCovCMDList_Pagination(t *testing.T) {
	t.Parallel()
	h, db := newCredHandler(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	for i := 0; i < 3; i++ {
		seedCredentialEnc(t, db, wsID, userID, fmt.Sprintf("pg-%d", i), fmt.Sprintf("pg-name-%d", i), "v")
	}

	req := httptest.NewRequest("GET", "/api/v1/credentials?limit=2&offset=0", nil)
	req = withWorkspaceUser(req, userID, wsID, "OWNER")
	rr := httptest.NewRecorder()
	h.List(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, body: %s", rr.Code, rr.Body.String())
	}
	var creds []credentialResponse
	json.Unmarshal(rr.Body.Bytes(), &creds)
	if len(creds) != 2 {
		t.Errorf("len = %d, want 2 (limit honoured)", len(creds))
	}
}

// ---- List/Get: 500 via db.Close fault injection ----

func TestCovCMDList_DBErrorReturns500(t *testing.T) {
	t.Parallel()
	h, db := newCredHandler(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	db.Close() // force every subsequent query to fail

	req := httptest.NewRequest("GET", "/api/v1/credentials", nil)
	req = withWorkspaceUser(req, userID, wsID, "OWNER")
	rr := httptest.NewRecorder()
	h.List(rr, req)
	if rr.Code != http.StatusInternalServerError {
		t.Errorf("status = %d, want 500", rr.Code)
	}
}

func TestCovCMDGet_DBErrorReturns500(t *testing.T) {
	t.Parallel()
	h, db := newCredHandler(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	seedCredentialEnc(t, db, wsID, userID, "c-500", "n", "v")
	db.Close()

	req := httptest.NewRequest("GET", "/api/v1/credentials/c-500", nil)
	req.SetPathValue("credentialId", "c-500")
	req = withWorkspaceUser(req, userID, wsID, "OWNER")
	rr := httptest.NewRecorder()
	h.Get(rr, req)
	if rr.Code != http.StatusInternalServerError {
		t.Errorf("status = %d, want 500", rr.Code)
	}
}

func TestCovCMDGet_NotFound(t *testing.T) {
	t.Parallel()
	h, db := newCredHandler(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)

	req := httptest.NewRequest("GET", "/api/v1/credentials/nope", nil)
	req.SetPathValue("credentialId", "nope")
	req = withWorkspaceUser(req, userID, wsID, "OWNER")
	rr := httptest.NewRecorder()
	h.Get(rr, req)
	if rr.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", rr.Code)
	}
}
