package api

// Tests for the multi-part credential surface (PRD-CREDENTIALS-V2-2026 §2.2).
//
// The load-bearing one is TestCredentialFields_SecretValueNeverLeavesTheServer.
// Every other test here protects a shape; that one protects the reason the
// vault exists. It asserts on the bytes a HANDLER wrote, not on a helper's
// return value, because "the helper redacts" is a property of the helper and
// the thing that ships is the handler.

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/encryption"
)

// ---------------------------------------------------------------------------
// Rig
// ---------------------------------------------------------------------------

type fieldRig struct {
	h      *CredentialFieldHandler
	creds  *CredentialHandler
	db     *sql.DB
	wsID   string
	userID string
	credID string
}

// newFieldRig builds a workspace with one ACTIVE workspace-scoped credential
// that has a value and a username — i.e. the pre-fields shape every existing
// row has, so the compatibility assertions start from something real.
func newFieldRig(t *testing.T) *fieldRig {
	t.Helper()
	setTestEncryptionKeyParallelSafe(t)
	db := setupTestDB(t)
	r := &fieldRig{
		h:      NewCredentialFieldHandler(db, newTestLogger()),
		creds:  NewCredentialHandler(db, newTestLogger()),
		db:     db,
		wsID:   "ws-fields",
		userID: "u-fields",
		credID: "cred-fields",
	}
	r.seedWorkspace(t, r.wsID, r.userID)
	r.seedCredential(t, r.credID, r.wsID, "aws-prod", "WORKSPACE")
	return r
}

func (r *fieldRig) seedWorkspace(t *testing.T, wsID, userID string) {
	t.Helper()
	if _, err := r.db.Exec(
		`INSERT OR IGNORE INTO users (id, email, full_name) VALUES (?, ?, ?)`,
		userID, userID+"@example.com", userID); err != nil {
		t.Fatalf("seed user: %v", err)
	}
	if _, err := r.db.Exec(
		`INSERT INTO workspaces (id, name, slug) VALUES (?, ?, ?)`, wsID, wsID, wsID); err != nil {
		t.Fatalf("seed workspace: %v", err)
	}
	if _, err := r.db.Exec(
		`INSERT INTO workspace_members (id, workspace_id, user_id, role) VALUES (?, ?, ?, 'OWNER')`,
		"mem-"+wsID+"-"+userID, wsID, userID); err != nil {
		t.Fatalf("seed membership: %v", err)
	}
}

// seedCredential writes the legacy single-value shape: encrypted_value plus a
// cleartext username. Fields are additive on top of this, never a replacement.
func (r *fieldRig) seedCredential(t *testing.T, credID, wsID, name, scope string) {
	t.Helper()
	enc, err := encryption.Encrypt(legacyCredValue)
	if err != nil {
		t.Fatalf("encrypt: %v", err)
	}
	if _, err := r.db.Exec(`
		INSERT INTO credentials (id, workspace_id, name, encrypted_value, username, type, provider,
			scope, status, created_by, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, 'USERPASS', 'NONE', ?, 'ACTIVE', ?,
			strftime('%Y-%m-%dT%H:%M:%fZ','now'), strftime('%Y-%m-%dT%H:%M:%fZ','now'))`,
		credID, wsID, name, enc, legacyCredUsername, scope, r.userID); err != nil {
		t.Fatalf("seed credential: %v", err)
	}
}

const (
	legacyCredValue    = "legacy-password-do-not-touch"
	legacyCredUsername = "ops@example.com"
)

// req builds an authenticated request against the field surface.
func (r *fieldRig) req(method, path string, body any, credID, key, wsID, userID, role string) *http.Request {
	var rdr *strings.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rdr = strings.NewReader(string(b))
	} else {
		rdr = strings.NewReader("")
	}
	req := httptest.NewRequest(method, path, rdr)
	req = withWorkspaceUser(req, userID, wsID, role)
	req.SetPathValue("credentialId", credID)
	if key != "" {
		req.SetPathValue("fieldKey", key)
	}
	return req
}

// create POSTs a new field as the rig's OWNER.
func (r *fieldRig) create(t *testing.T, body map[string]any) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	r.h.Create(rec, r.req("POST", "/api/v1/credentials/"+r.credID+"/fields", body,
		r.credID, "", r.wsID, r.userID, "OWNER"))
	return rec
}

func (r *fieldRig) list(t *testing.T) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	r.h.List(rec, r.req("GET", "/api/v1/credentials/"+r.credID+"/fields", nil,
		r.credID, "", r.wsID, r.userID, "OWNER"))
	return rec
}

// decodeFields parses a field list response.
func decodeFields(t *testing.T, rec *httptest.ResponseRecorder) []credentialFieldResponse {
	t.Helper()
	var out []credentialFieldResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode field list (%d): %v — body %s", rec.Code, err, rec.Body.String())
	}
	return out
}

// ---------------------------------------------------------------------------
// Round-trip
// ---------------------------------------------------------------------------

// The headline case from the PRD: AWS static credentials are three parts, two
// of which are not secret.
func TestCredentialFields_ThreeFieldsRoundTrip(t *testing.T) {
	r := newFieldRig(t)

	for _, f := range []map[string]any{
		{"key": "access_key_id", "value": "AKIAIOSFODNN7EXAMPLE", "is_secret": false},
		{"key": "secret_access_key", "value": "wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY", "is_secret": true},
		{"key": "region", "value": "eu-central-1", "is_secret": false},
	} {
		if rec := r.create(t, f); rec.Code != http.StatusCreated {
			t.Fatalf("create %v: code = %d, body %s", f["key"], rec.Code, rec.Body.String())
		}
	}

	got := decodeFields(t, r.list(t))
	if len(got) != 3 {
		t.Fatalf("list returned %d fields, want 3: %s", len(got), r.list(t).Body.String())
	}
	// Ordinals are assigned in insertion order and the list is sorted by them,
	// so the UI renders the parts in the order the operator entered them.
	wantKeys := []string{"access_key_id", "secret_access_key", "region"}
	for i, want := range wantKeys {
		if got[i].Key != want {
			t.Errorf("field[%d].Key = %q, want %q", i, got[i].Key, want)
		}
	}
	// Both non-secret halves come back with their value; the secret half does
	// not (asserted exhaustively in the disclosure test below).
	if got[0].Value == nil || *got[0].Value != "AKIAIOSFODNN7EXAMPLE" {
		t.Errorf("access_key_id value = %v, want the stored identifier", got[0].Value)
	}
	if got[2].Value == nil || *got[2].Value != "eu-central-1" {
		t.Errorf("region value = %v, want eu-central-1", got[2].Value)
	}
	if got[1].Value != nil {
		t.Errorf("secret_access_key value = %v, want nil", *got[1].Value)
	}

	// And the secret half really did round-trip: it decrypts back to what was
	// stored. Read straight from the column — no handler is allowed to do this,
	// which is the point of proving it here instead.
	var enc string
	if err := r.db.QueryRow(
		`SELECT encrypted_value FROM credential_fields WHERE credential_id = ? AND key = 'secret_access_key'`,
		r.credID).Scan(&enc); err != nil {
		t.Fatalf("read encrypted_value: %v", err)
	}
	plain, err := decryptCredential(enc)
	if err != nil {
		t.Fatalf("decrypt: %v", err)
	}
	if plain != "wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY" {
		t.Errorf("decrypted secret = %q, want the value that was written", plain)
	}
}

// ---------------------------------------------------------------------------
// Disclosure — the one that matters
// ---------------------------------------------------------------------------

// A secret field's value must not appear in ANY read path's response, in any
// field, at any status code. Getting this wrong turns the vault into a
// disclosure API, so this asserts on the raw response bytes of every handler
// that can be reached with a GET-shaped intent — including the write handlers,
// whose 201/200 echo is a read path too.
func TestCredentialFields_SecretValueNeverLeavesTheServer(t *testing.T) {
	r := newFieldRig(t)
	const secret = "wJalrXUtnFEMI-UNIQUE-SENTINEL-7f3a"

	createRec := r.create(t, map[string]any{"key": "secret_access_key", "value": secret, "is_secret": true})
	if createRec.Code != http.StatusCreated {
		t.Fatalf("create: %d %s", createRec.Code, createRec.Body.String())
	}

	updRec := httptest.NewRecorder()
	r.h.Update(updRec, r.req("PUT", "/api/v1/credentials/"+r.credID+"/fields/secret_access_key",
		map[string]any{"value": secret + "-rotated", "is_secret": true},
		r.credID, "secret_access_key", r.wsID, r.userID, "OWNER"))
	if updRec.Code != http.StatusOK {
		t.Fatalf("update: %d %s", updRec.Code, updRec.Body.String())
	}

	listRec := r.list(t)

	// The stored ciphertext is just as disqualifying as the plaintext: an
	// attacker who can read it offline has the value the moment the master key
	// leaks, and it has no legitimate reason to cross the wire.
	var ciphertext string
	if err := r.db.QueryRow(
		`SELECT encrypted_value FROM credential_fields WHERE credential_id = ? AND key = 'secret_access_key'`,
		r.credID).Scan(&ciphertext); err != nil {
		t.Fatalf("read ciphertext: %v", err)
	}

	for _, tc := range []struct {
		path string
		rec  *httptest.ResponseRecorder
	}{
		{"POST /fields", createRec},
		{"PUT /fields/{key}", updRec},
		{"GET /fields", listRec},
	} {
		body := tc.rec.Body.String()
		if strings.Contains(body, secret) {
			t.Errorf("%s echoed the secret field's PLAINTEXT: %s", tc.path, body)
		}
		if strings.Contains(body, secret+"-rotated") {
			t.Errorf("%s echoed the rotated plaintext: %s", tc.path, body)
		}
		if ciphertext != "" && strings.Contains(body, ciphertext) {
			t.Errorf("%s echoed the stored ciphertext: %s", tc.path, body)
		}
	}

	// And positively: the key and its classification ARE returned, because a
	// UI that cannot see which parts exist cannot manage them.
	got := decodeFields(t, listRec)
	if len(got) != 1 || got[0].Key != "secret_access_key" || !got[0].IsSecret {
		t.Fatalf("list = %+v, want one secret field named secret_access_key", got)
	}
	if got[0].Value != nil {
		t.Errorf("secret field carried a value on read: %q", *got[0].Value)
	}
}

// ---------------------------------------------------------------------------
// Non-secret fields
// ---------------------------------------------------------------------------

// The deliberate cleartext half. `region` is an identifier, not a secret —
// same reasoning as credentials.username — so it is stored unencrypted and
// returned on read. Asserted against the COLUMN, because "we meant to store it
// cleartext" is only true if the bytes in the database say so.
func TestCredentialFields_NonSecretIsReturnedAndStoredCleartext(t *testing.T) {
	r := newFieldRig(t)
	if rec := r.create(t, map[string]any{"key": "region", "value": "eu-central-1", "is_secret": false}); rec.Code != http.StatusCreated {
		t.Fatalf("create: %d %s", rec.Code, rec.Body.String())
	}

	var value, encValue sql.NullString
	var isSecret int
	if err := r.db.QueryRow(
		`SELECT value, encrypted_value, is_secret FROM credential_fields WHERE credential_id = ? AND key = 'region'`,
		r.credID).Scan(&value, &encValue, &isSecret); err != nil {
		t.Fatalf("read row: %v", err)
	}
	if !value.Valid || value.String != "eu-central-1" {
		t.Errorf("value column = %+v, want the literal cleartext 'eu-central-1'", value)
	}
	if encValue.Valid {
		t.Errorf("encrypted_value = %q for a non-secret field; it must be NULL", encValue.String)
	}
	if isSecret != 0 {
		t.Errorf("is_secret = %d, want 0", isSecret)
	}

	got := decodeFields(t, r.list(t))
	if len(got) != 1 || got[0].Value == nil || *got[0].Value != "eu-central-1" {
		t.Fatalf("list = %+v, want the cleartext value echoed back", got)
	}
}

// is_secret defaults to TRUE when the caller omits it. A field whose secrecy
// the client forgot to state must be encrypted, not published — the default
// has to be the safe one because that is the case nobody tests by hand.
func TestCredentialFields_DefaultsToSecretWhenUnspecified(t *testing.T) {
	r := newFieldRig(t)
	if rec := r.create(t, map[string]any{"key": "totp_seed", "value": "JBSWY3DPEHPK3PXP"}); rec.Code != http.StatusCreated {
		t.Fatalf("create: %d %s", rec.Code, rec.Body.String())
	}
	var value sql.NullString
	var isSecret int
	if err := r.db.QueryRow(
		`SELECT value, is_secret FROM credential_fields WHERE credential_id = ? AND key = 'totp_seed'`,
		r.credID).Scan(&value, &isSecret); err != nil {
		t.Fatalf("read row: %v", err)
	}
	if isSecret != 1 || value.Valid {
		t.Errorf("is_secret=%d value=%+v — an unspecified field must default to encrypted", isSecret, value)
	}
}

// Flipping is_secret has to MOVE the value between columns, not leave a copy
// behind. A stale cleartext row for a now-secret field would be a plaintext
// secret sitting in a column the read path happily returns.
func TestCredentialFields_UpdateMovesValueBetweenColumns(t *testing.T) {
	r := newFieldRig(t)
	if rec := r.create(t, map[string]any{"key": "account_id", "value": "123456789012", "is_secret": false}); rec.Code != http.StatusCreated {
		t.Fatalf("create: %d %s", rec.Code, rec.Body.String())
	}

	rec := httptest.NewRecorder()
	r.h.Update(rec, r.req("PUT", "/api/v1/credentials/"+r.credID+"/fields/account_id",
		map[string]any{"value": "123456789012", "is_secret": true},
		r.credID, "account_id", r.wsID, r.userID, "OWNER"))
	if rec.Code != http.StatusOK {
		t.Fatalf("update: %d %s", rec.Code, rec.Body.String())
	}

	var value, encValue sql.NullString
	if err := r.db.QueryRow(
		`SELECT value, encrypted_value FROM credential_fields WHERE credential_id = ? AND key = 'account_id'`,
		r.credID).Scan(&value, &encValue); err != nil {
		t.Fatalf("read row: %v", err)
	}
	if value.Valid {
		t.Errorf("cleartext value survived the flip to secret: %q", value.String)
	}
	if !encValue.Valid {
		t.Error("encrypted_value is NULL after flipping to secret")
	}
	if strings.Contains(r.list(t).Body.String(), "123456789012") {
		t.Error("the now-secret value is still being returned on read")
	}
}

func TestCredentialFields_DeleteRemovesOnlyThatField(t *testing.T) {
	r := newFieldRig(t)
	for _, k := range []string{"region", "account_id"} {
		if rec := r.create(t, map[string]any{"key": k, "value": "v-" + k, "is_secret": false}); rec.Code != http.StatusCreated {
			t.Fatalf("create %s: %d", k, rec.Code)
		}
	}
	rec := httptest.NewRecorder()
	r.h.Delete(rec, r.req("DELETE", "/api/v1/credentials/"+r.credID+"/fields/region", nil,
		r.credID, "region", r.wsID, r.userID, "OWNER"))
	if rec.Code != http.StatusOK {
		t.Fatalf("delete: %d %s", rec.Code, rec.Body.String())
	}
	got := decodeFields(t, r.list(t))
	if len(got) != 1 || got[0].Key != "account_id" {
		t.Fatalf("after delete list = %+v, want only account_id", got)
	}

	// Deleting a key that is not there is a 404, not a silent success — an
	// operator retrying a revoke needs to know whether it ever existed.
	rec2 := httptest.NewRecorder()
	r.h.Delete(rec2, r.req("DELETE", "/api/v1/credentials/"+r.credID+"/fields/region", nil,
		r.credID, "region", r.wsID, r.userID, "OWNER"))
	if rec2.Code != http.StatusNotFound {
		t.Errorf("second delete: code = %d, want 404", rec2.Code)
	}
}

// ---------------------------------------------------------------------------
// Validation
// ---------------------------------------------------------------------------

func TestCredentialFields_DuplicateKeyRejected(t *testing.T) {
	r := newFieldRig(t)
	if rec := r.create(t, map[string]any{"key": "region", "value": "eu-central-1", "is_secret": false}); rec.Code != http.StatusCreated {
		t.Fatalf("first create: %d %s", rec.Code, rec.Body.String())
	}
	rec := r.create(t, map[string]any{"key": "region", "value": "us-east-1", "is_secret": false})
	if rec.Code != http.StatusConflict {
		t.Fatalf("duplicate key: code = %d, want 409 — body %s", rec.Code, rec.Body.String())
	}
	// The original must be untouched: a rejected write that silently overwrote
	// would be worse than either accepting or refusing cleanly.
	got := decodeFields(t, r.list(t))
	if len(got) != 1 || got[0].Value == nil || *got[0].Value != "eu-central-1" {
		t.Fatalf("after rejected duplicate list = %+v, want the original value intact", got)
	}
}

func TestCredentialFields_OversizedValueRejected(t *testing.T) {
	r := newFieldRig(t)
	huge := strings.Repeat("A", maxCredentialFieldValueLen+1)

	rec := r.create(t, map[string]any{"key": "blob", "value": huge, "is_secret": true})
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("oversized create: code = %d, want 400", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "too long") {
		t.Errorf("400 body should name the limit, got %s", rec.Body.String())
	}

	// The cap applies on update too, or an oversized value just takes two
	// requests instead of one.
	if c := r.create(t, map[string]any{"key": "blob", "value": "ok", "is_secret": true}); c.Code != http.StatusCreated {
		t.Fatalf("seed field: %d", c.Code)
	}
	upd := httptest.NewRecorder()
	r.h.Update(upd, r.req("PUT", "/api/v1/credentials/"+r.credID+"/fields/blob",
		map[string]any{"value": huge, "is_secret": true}, r.credID, "blob", r.wsID, r.userID, "OWNER"))
	if upd.Code != http.StatusBadRequest {
		t.Errorf("oversized update: code = %d, want 400", upd.Code)
	}
}

func TestCredentialFields_FieldCountCapped(t *testing.T) {
	r := newFieldRig(t)
	for i := 0; i < maxCredentialFields; i++ {
		rec := r.create(t, map[string]any{"key": fmt.Sprintf("f_%02d", i), "value": "v", "is_secret": false})
		if rec.Code != http.StatusCreated {
			t.Fatalf("create %d: code = %d, body %s", i, rec.Code, rec.Body.String())
		}
	}
	rec := r.create(t, map[string]any{"key": "one_too_many", "value": "v", "is_secret": false})
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("field %d: code = %d, want 400", maxCredentialFields+1, rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "at most") {
		t.Errorf("400 body should name the cap, got %s", rec.Body.String())
	}
}

func TestCredentialFields_BadKeyShapeRejected(t *testing.T) {
	r := newFieldRig(t)
	cases := []struct{ name, key string }{
		{"empty", ""},
		{"uppercase", "Region"},               // would collide with `region` once upcased for an env var
		{"leading digit", "1region"},          // not a legal env-var component
		{"dash", "access-key-id"},             // ditto
		{"dot", "aws.region"},                 // ditto
		{"space", "access key"},               //
		{"leading underscore", "_region"},     // reads as a private/reserved name
		{"newline", "region\nSECOND=x"},       // injection shape for the future delivery path
		{"nul", "region\x00"},                 //
		{"too long", strings.Repeat("a", 65)}, //
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := r.create(t, map[string]any{"key": tc.key, "value": "v", "is_secret": false})
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("key %q: code = %d, want 400 — body %s", tc.key, rec.Code, rec.Body.String())
			}
		})
	}
}

// The three names that already have a home on the credentials row. Allowing
// them here would create two writable copies of one datum with no owner: the
// delivery path reads the column, a field write writes the row, and they drift
// apart silently. Refusing is the only outcome that cannot lie.
func TestCredentialFields_ReservedKeysRejected(t *testing.T) {
	r := newFieldRig(t)
	for _, key := range []string{"username", "value", "password"} {
		t.Run(key, func(t *testing.T) {
			rec := r.create(t, map[string]any{"key": key, "value": "v", "is_secret": false})
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("reserved key %q: code = %d, want 400", key, rec.Code)
			}
			if !strings.Contains(rec.Body.String(), "reserved") {
				t.Errorf("400 body should say the key is reserved, got %s", rec.Body.String())
			}
		})
	}
}

// ---------------------------------------------------------------------------
// Compatibility
// ---------------------------------------------------------------------------

// The guarantee this whole change lives or dies by: every credential that
// exists today has no fields, and nothing about it may change.
func TestCredentialFields_ExistingSingleValueCredentialIsUntouched(t *testing.T) {
	r := newFieldRig(t)

	// A credential with no fields lists as [] — not null, which a JS client
	// would blow up on, and not 404, which would read as "no such credential".
	rec := r.list(t)
	if rec.Code != http.StatusOK {
		t.Fatalf("list on a field-less credential: code = %d", rec.Code)
	}
	if strings.TrimSpace(rec.Body.String()) != "[]" {
		t.Errorf("body = %q, want []", rec.Body.String())
	}

	// The pre-existing read path still answers exactly as before.
	getRec := httptest.NewRecorder()
	getReq := withWorkspaceUser(httptest.NewRequest("GET", "/api/v1/credentials/"+r.credID, nil),
		r.userID, r.wsID, "OWNER")
	getReq.SetPathValue("credentialId", r.credID)
	r.creds.Get(getRec, getReq)
	if getRec.Code != http.StatusOK {
		t.Fatalf("credential Get: code = %d, body %s", getRec.Code, getRec.Body.String())
	}
	var cred credentialResponse
	if err := json.Unmarshal(getRec.Body.Bytes(), &cred); err != nil {
		t.Fatalf("decode credential: %v", err)
	}
	if cred.Username == nil || *cred.Username != legacyCredUsername {
		t.Errorf("username = %v, want %q — the legacy cleartext identifier must be untouched", cred.Username, legacyCredUsername)
	}
	if strings.Contains(getRec.Body.String(), legacyCredValue) {
		t.Error("the credential read path leaked the legacy secret value")
	}

	// Adding fields must not disturb the row's own value/username either.
	if c := r.create(t, map[string]any{"key": "region", "value": "eu-central-1", "is_secret": false}); c.Code != http.StatusCreated {
		t.Fatalf("create field: %d", c.Code)
	}
	var enc, username string
	if err := r.db.QueryRow(
		`SELECT encrypted_value, username FROM credentials WHERE id = ?`, r.credID).Scan(&enc, &username); err != nil {
		t.Fatalf("read credential row: %v", err)
	}
	plain, err := decryptCredential(enc)
	if err != nil {
		t.Fatalf("decrypt legacy value: %v", err)
	}
	if plain != legacyCredValue || username != legacyCredUsername {
		t.Errorf("credential row changed: value=%q username=%q — fields are additive, never a rewrite", plain, username)
	}
}

// ---------------------------------------------------------------------------
// Tenancy and RBAC
// ---------------------------------------------------------------------------

// Every field route resolves the credential through the caller's workspace.
// A leaked credential id from another tenant must answer 404 on all four —
// 403 would confirm the id is real somewhere in the fleet.
func TestCredentialFields_CrossTenantUnreachable(t *testing.T) {
	r := newFieldRig(t)
	if c := r.create(t, map[string]any{"key": "region", "value": "eu-central-1", "is_secret": false}); c.Code != http.StatusCreated {
		t.Fatalf("seed field: %d", c.Code)
	}
	// A second tenant, whose OWNER knows the first tenant's credential id.
	r.seedWorkspace(t, "ws-other", "u-other")

	calls := []struct {
		name string
		run  func() *httptest.ResponseRecorder
	}{
		{"GET /fields", func() *httptest.ResponseRecorder {
			rec := httptest.NewRecorder()
			r.h.List(rec, r.req("GET", "/x", nil, r.credID, "", "ws-other", "u-other", "OWNER"))
			return rec
		}},
		{"POST /fields", func() *httptest.ResponseRecorder {
			rec := httptest.NewRecorder()
			r.h.Create(rec, r.req("POST", "/x", map[string]any{"key": "host", "value": "h", "is_secret": false},
				r.credID, "", "ws-other", "u-other", "OWNER"))
			return rec
		}},
		{"PUT /fields/{key}", func() *httptest.ResponseRecorder {
			rec := httptest.NewRecorder()
			r.h.Update(rec, r.req("PUT", "/x", map[string]any{"value": "pwned", "is_secret": false},
				r.credID, "region", "ws-other", "u-other", "OWNER"))
			return rec
		}},
		{"DELETE /fields/{key}", func() *httptest.ResponseRecorder {
			rec := httptest.NewRecorder()
			r.h.Delete(rec, r.req("DELETE", "/x", nil, r.credID, "region", "ws-other", "u-other", "OWNER"))
			return rec
		}},
	}
	for _, c := range calls {
		rec := c.run()
		if rec.Code != http.StatusNotFound {
			t.Errorf("%s across tenants: code = %d, want 404 — body %s", c.name, rec.Code, rec.Body.String())
		}
		if strings.Contains(rec.Body.String(), "eu-central-1") {
			t.Errorf("%s leaked another tenant's field value", c.name)
		}
	}
	// And nothing was written into the victim's credential.
	got := decodeFields(t, r.list(t))
	if len(got) != 1 || got[0].Key != "region" || got[0].Value == nil || *got[0].Value != "eu-central-1" {
		t.Fatalf("victim's fields were modified across the tenant boundary: %+v", got)
	}
}

// Reading fields must be no looser than reading the credential itself. The
// credential read path hides a CREW-scoped credential from a MEMBER outside
// that crew (credentialVisibilityFilter); the field read path has to use the
// same filter, or the parts become a way to read a credential the caller
// cannot see.
func TestCredentialFields_ReadIsCrewScopedLikeTheCredential(t *testing.T) {
	r := newFieldRig(t)
	if c := r.create(t, map[string]any{"key": "region", "value": "eu-central-1", "is_secret": false}); c.Code != http.StatusCreated {
		t.Fatalf("seed field: %d", c.Code)
	}
	// Scope the credential to a crew the MEMBER does not belong to.
	if _, err := r.db.Exec(`INSERT INTO crews (id, workspace_id, name, slug) VALUES ('crew-a', ?, 'A', 'a')`, r.wsID); err != nil {
		t.Fatalf("seed crew: %v", err)
	}
	if _, err := r.db.Exec(`UPDATE credentials SET scope = 'CREW' WHERE id = ?`, r.credID); err != nil {
		t.Fatalf("scope credential: %v", err)
	}
	if _, err := r.db.Exec(`INSERT INTO credential_crews (credential_id, crew_id) VALUES (?, 'crew-a')`, r.credID); err != nil {
		t.Fatalf("credential_crews: %v", err)
	}
	if _, err := r.db.Exec(
		`INSERT OR IGNORE INTO users (id, email, full_name) VALUES ('u-outsider', 'o@example.com', 'O')`); err != nil {
		t.Fatalf("seed outsider: %v", err)
	}

	// Baseline: the credential itself is invisible to this MEMBER.
	credRec := httptest.NewRecorder()
	credReq := withWorkspaceUser(httptest.NewRequest("GET", "/x", nil), "u-outsider", r.wsID, "MEMBER")
	credReq.SetPathValue("credentialId", r.credID)
	r.creds.Get(credRec, credReq)
	if credRec.Code != http.StatusNotFound {
		t.Fatalf("precondition: credential Get for an outsider MEMBER = %d, want 404", credRec.Code)
	}

	rec := httptest.NewRecorder()
	r.h.List(rec, r.req("GET", "/x", nil, r.credID, "", r.wsID, "u-outsider", "MEMBER"))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("field list for an outsider MEMBER = %d, want 404 — body %s", rec.Code, rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), "eu-central-1") {
		t.Error("field list leaked a value from a credential the caller cannot see")
	}

	// A member of the crew sees it, exactly as they see the credential.
	if _, err := r.db.Exec(`INSERT INTO crew_members (crew_id, user_id) VALUES ('crew-a', 'u-outsider')`); err != nil {
		t.Fatalf("crew_members: %v", err)
	}
	rec2 := httptest.NewRecorder()
	r.h.List(rec2, r.req("GET", "/x", nil, r.credID, "", r.wsID, "u-outsider", "MEMBER"))
	if rec2.Code != http.StatusOK {
		t.Fatalf("field list for a crew member = %d, want 200", rec2.Code)
	}
}

// Writing a field is writing the credential, so the field mutations must
// declare the same role class as PATCH /credentials/{id} (roleCreate,
// MANAGER+). Asserted against the route table rather than by calling handlers,
// because the route table is where the gate actually lives (#809/#811) — a
// handler-level test would pass even if the route were registered ungated.
func TestCredentialFields_MutationRoutesDeclareTheCredentialWriteRole(t *testing.T) {
	r, err := NewRouter(setupTestDB(t), "this-is-a-32-char-test-secret-pad", newTestLogger())
	if err != nil {
		t.Fatalf("NewRouter: %v", err)
	}

	declared := map[string]mutRoute{}
	for _, mr := range r.mutationRoutes {
		declared[mr.Method+" "+mr.Pattern] = mr
	}

	// The reference: how writing the credential itself is declared.
	credWrite, ok := declared["PATCH /api/v1/credentials/{credentialId}"]
	if !ok {
		t.Fatal("PATCH /api/v1/credentials/{credentialId} is not in the route table")
	}

	for _, key := range []string{
		"POST /api/v1/credentials/{credentialId}/fields",
		"PUT /api/v1/credentials/{credentialId}/fields/{fieldKey}",
		"DELETE /api/v1/credentials/{credentialId}/fields/{fieldKey}",
	} {
		mr, ok := declared[key]
		if !ok {
			t.Errorf("%s is not registered through authedMut — an unrecorded mutation route is an ungated one", key)
			continue
		}
		if mr.Role != credWrite.Role {
			t.Errorf("%s declares role %q; writing a field must be no looser than writing the credential (%q)",
				key, mr.Role, credWrite.Role)
		}
		// The CLI-token scope has to match too, or a token scoped away from
		// credentials could still rewrite their parts.
		if mr.Scope != credWrite.Scope {
			t.Errorf("%s declares scope %q, want %q", key, mr.Scope, credWrite.Scope)
		}
	}
}
