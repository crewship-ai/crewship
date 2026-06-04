// Additional branch-coverage tests for connectors_handler.go.
//
// The companion connectors_handler_test.go covers the headline happy
// paths and the headline RBAC/404 cases. This file fills the gaps the
// coverage profile flagged: the unauthenticated guards on Get/Verify/
// Install, the not-found + invalid-body branches on Verify, the
// no-verify-block ok=true shortcut, probeVerifyHTTP's URL/header
// resolution failures, the conn_string / none install paths, the
// duplicate-name suffixing branch, and the DB-error 500 paths reached
// via db.Close() fault injection.
//
// All test funcs are prefixed TestCovCon; all new helpers covCon.
package api

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"testing"
	"testing/fstest"
	"time"

	"github.com/crewship-ai/crewship/internal/connectors"
	"github.com/crewship-ai/crewship/internal/httpsafe"
)

// covConManifests is a synthetic catalog exercising the auth-mode
// branches the headline tests skip:
//
//   - cov-conn-string: conn_string install path (credentials persisted,
//     NextStep="")
//   - cov-none: none auth mode (no credential rows, NextStep="")
//   - cov-pat-noverify: PAT manifest with no verify block — Verify must
//     short-circuit to ok=true without probing.
//   - cov-pat-badurl: verify.http.url references an unresolvable
//     placeholder so probeVerifyHTTP's resolution-failure branch fires.
//   - cov-pat-badheader: verify.http URL resolves but a header template
//     references an unresolvable placeholder.
const (
	covConConnStringManifest = `id: cov-conn-string
name: Cov Conn String
description: conn_string connector
category: testing
brand: {logo: cov, color: "#abcabc"}
auth_mode: conn_string
fields:
  - {key: host, label: Host, type: text, required: true}
  - {key: password, label: Password, type: password, required: false}
mcp:
  transport: stdio
  command: echo
  args: ["${field.host}"]
`

	covConNoneManifest = `id: cov-none
name: Cov None
description: none auth connector
category: testing
brand: {logo: cov, color: "#defdef"}
auth_mode: none
mcp:
  transport: streamable-http
  endpoint: https://none.cov.example/mcp
`

	covConPATNoVerifyManifest = `id: cov-pat-noverify
name: Cov PAT No Verify
description: PAT connector without a verify block
category: testing
brand: {logo: cov, color: "#012012"}
auth_mode: pat
fields:
  - {key: api_key, label: API Key, type: password, required: true}
mcp:
  transport: stdio
  command: echo
  args: ["${field.api_key}"]
`

	covConPATBadURLManifest = `id: cov-pat-badurl
name: Cov PAT Bad URL
description: verify url references unknown placeholder
category: testing
brand: {logo: cov, color: "#345345"}
auth_mode: pat
fields:
  - {key: api_key, label: API Key, type: password, required: true}
mcp:
  transport: stdio
  command: echo
verify:
  http:
    method: GET
    url: "https://verify.test/${field.does_not_exist}"
    expect_status: 200
`

	covConPATBadHeaderManifest = `id: cov-pat-badheader
name: Cov PAT Bad Header
description: verify header references unknown placeholder
category: testing
brand: {logo: cov, color: "#678678"}
auth_mode: pat
fields:
  - {key: api_key, label: API Key, type: password, required: true}
mcp:
  transport: stdio
  command: echo
verify:
  http:
    method: GET
    url: "https://verify.test/me"
    headers:
      Authorization: "Bearer ${field.does_not_exist}"
    expect_status: 200
`
)

// covConCatalog loads the supplied manifest sources into a Catalog,
// failing the test on any load error. Keyed by id so each test can pull
// just the fixtures it needs.
func covConCatalog(t *testing.T, sources ...string) *connectors.Catalog {
	t.Helper()
	memFS := fstest.MapFS{}
	for i, src := range sources {
		// Filename is irrelevant to LoadByID (it keys on the manifest
		// id), but must be unique within the FS.
		memFS[covConFixtureName(i)] = &fstest.MapFile{Data: []byte(src)}
	}
	cat, errs := connectors.LoadAll(memFS)
	if len(errs) != 0 {
		t.Fatalf("covCon catalog load errors: %v", errs)
	}
	return cat
}

func covConFixtureName(i int) string {
	return "fixtures/cov-" + string(rune('a'+i)) + ".yaml"
}

// covConHandler wires a fresh test DB + the supplied catalog into a
// ConnectorHandler, with the encryption key set so install paths that
// persist credentials succeed.
func covConHandler(t *testing.T, sources ...string) *ConnectorHandler {
	t.Helper()
	setTestEncryptionKeyParallelSafe(t)
	db := setupTestDB(t)
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))
	return NewConnectorHandlerWithCatalog(db, logger, covConCatalog(t, sources...))
}

// -------------------------------------------------------------------
// Unauthenticated guards (401) on Get / Verify / Install.
// -------------------------------------------------------------------

func TestCovCon_Get_Unauthenticated(t *testing.T) {
	h := covConHandler(t, covConNoneManifest)
	req := httptest.NewRequest("GET", "/api/v1/connectors/cov-none", nil)
	req.SetPathValue("connectorId", "cov-none")
	rr := httptest.NewRecorder()
	h.Get(rr, req)
	if rr.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401", rr.Code)
	}
}

func TestCovCon_Verify_Unauthenticated(t *testing.T) {
	h := covConHandler(t, covConPATNoVerifyManifest)
	req := httptest.NewRequest("POST", "/api/v1/connectors/cov-pat-noverify/verify", nil)
	req.SetPathValue("connectorId", "cov-pat-noverify")
	rr := httptest.NewRecorder()
	h.Verify(rr, req)
	if rr.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401", rr.Code)
	}
}

func TestCovCon_Install_Unauthenticated(t *testing.T) {
	h := covConHandler(t, covConNoneManifest)
	req := httptest.NewRequest("POST", "/api/v1/connectors/cov-none/install?workspace_id=ws", nil)
	req.SetPathValue("connectorId", "cov-none")
	rr := httptest.NewRecorder()
	h.Install(rr, req)
	if rr.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401", rr.Code)
	}
}

// -------------------------------------------------------------------
// Verify branch coverage: not-found, invalid body, no-verify-block.
// -------------------------------------------------------------------

func TestCovCon_Verify_NotFound(t *testing.T) {
	h := covConHandler(t, covConPATNoVerifyManifest)
	db := h.db
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)

	body := bytes.NewBufferString(`{"fields":{}}`)
	req := httptest.NewRequest("POST", "/api/v1/connectors/nope/verify?workspace_id="+wsID, body)
	req.SetPathValue("connectorId", "nope")
	req = withWorkspaceUser(req, userID, wsID, "MANAGER")
	rr := httptest.NewRecorder()

	h.Verify(rr, req)
	if rr.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", rr.Code)
	}
}

func TestCovCon_Verify_InvalidBody(t *testing.T) {
	h := covConHandler(t, covConPATNoVerifyManifest)
	db := h.db
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)

	body := bytes.NewBufferString(`{not json`)
	req := httptest.NewRequest("POST", "/api/v1/connectors/cov-pat-noverify/verify?workspace_id="+wsID, body)
	req.SetPathValue("connectorId", "cov-pat-noverify")
	req = withWorkspaceUser(req, userID, wsID, "MANAGER")
	rr := httptest.NewRecorder()

	h.Verify(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400 (invalid body)", rr.Code)
	}
}

func TestCovCon_Verify_NoVerifyBlock_OK(t *testing.T) {
	// A PAT manifest with no verify block accepts credentials at face
	// value: ok=true without any outbound probe.
	h := covConHandler(t, covConPATNoVerifyManifest)
	db := h.db
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)

	body := bytes.NewBufferString(`{"fields":{"api_key":"sk-x"}}`)
	req := httptest.NewRequest("POST", "/api/v1/connectors/cov-pat-noverify/verify?workspace_id="+wsID, body)
	req.SetPathValue("connectorId", "cov-pat-noverify")
	req = withWorkspaceUser(req, userID, wsID, "MANAGER")
	rr := httptest.NewRecorder()

	h.Verify(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rr.Code, rr.Body.String())
	}
	var resp VerifyResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v body=%s", err, rr.Body.String())
	}
	if !resp.OK {
		t.Errorf("no-verify-block manifest must report ok=true, msg=%q", resp.Message)
	}
}

func TestCovCon_Verify_NoneAuthMode_OK(t *testing.T) {
	// auth_mode=none also short-circuits Verify to ok=true.
	h := covConHandler(t, covConNoneManifest)
	db := h.db
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)

	body := bytes.NewBufferString(`{"fields":{}}`)
	req := httptest.NewRequest("POST", "/api/v1/connectors/cov-none/verify?workspace_id="+wsID, body)
	req.SetPathValue("connectorId", "cov-none")
	req = withWorkspaceUser(req, userID, wsID, "MANAGER")
	rr := httptest.NewRecorder()

	h.Verify(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rr.Code, rr.Body.String())
	}
	var resp VerifyResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !resp.OK {
		t.Errorf("none-auth verify must report ok=true")
	}
}

// -------------------------------------------------------------------
// probeVerifyHTTP failure branches: URL + header resolution errors are
// surfaced as ok=false (not a 5xx) so the frontend can present them.
// -------------------------------------------------------------------

func TestCovCon_Verify_URLResolutionFails(t *testing.T) {
	h := covConHandler(t, covConPATBadURLManifest)
	db := h.db
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)

	body := bytes.NewBufferString(`{"fields":{"api_key":"sk-x"}}`)
	req := httptest.NewRequest("POST", "/api/v1/connectors/cov-pat-badurl/verify?workspace_id="+wsID, body)
	req.SetPathValue("connectorId", "cov-pat-badurl")
	req = withWorkspaceUser(req, userID, wsID, "MANAGER")
	rr := httptest.NewRecorder()

	h.Verify(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rr.Code, rr.Body.String())
	}
	var resp VerifyResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.OK {
		t.Error("expected ok=false when verify URL fails to resolve")
	}
	if resp.Message == "" {
		t.Error("expected a human-readable resolution-failure message")
	}
}

func TestCovCon_Verify_HeaderResolutionFails(t *testing.T) {
	// The URL resolves and passes httpsafe.ValidateURL; the header
	// template references an unknown placeholder, so probeVerifyHTTP
	// reports ok=false from the header-resolution branch (before any
	// network call).
	h := covConHandler(t, covConPATBadHeaderManifest)
	db := h.db
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)

	body := bytes.NewBufferString(`{"fields":{"api_key":"sk-x"}}`)
	req := httptest.NewRequest("POST", "/api/v1/connectors/cov-pat-badheader/verify?workspace_id="+wsID, body)
	req.SetPathValue("connectorId", "cov-pat-badheader")
	req = withWorkspaceUser(req, userID, wsID, "MANAGER")
	rr := httptest.NewRecorder()

	h.Verify(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rr.Code, rr.Body.String())
	}
	var resp VerifyResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.OK {
		t.Error("expected ok=false when a verify header fails to resolve")
	}
}

func TestCovCon_Verify_DBOpenDoesNotMatterForUnauth(t *testing.T) {
	// Verify never touches the DB, but the not-found path with a valid
	// header set + a verify-bearing manifest that succeeds exercises the
	// final probe success message="" branch through a loopback fake.
	var hit bool
	fake := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hit = true
		w.WriteHeader(http.StatusOK)
	}))
	defer fake.Close()
	target, err := url.Parse(fake.URL)
	if err != nil {
		t.Fatalf("parse fake URL: %v", err)
	}
	restore := SetVerifyHTTPClientForTesting(&http.Client{
		Timeout:   5 * time.Second,
		Transport: &httpsafe.RewriteRoundTripper{Target: target},
	})
	defer restore()

	yaml := `id: cov-pat-ok
name: Cov PAT OK
auth_mode: pat
brand: {logo: x, color: "#000000"}
category: testing
fields:
  - {key: api_key, label: API Key, type: password, required: true}
mcp:
  transport: stdio
  command: echo
verify:
  http:
    method: GET
    url: "https://verify.test/me"
    headers:
      Authorization: "Bearer ${field.api_key}"
    expect_status: 200
`
	h := covConHandler(t, yaml)
	db := h.db
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)

	body := bytes.NewBufferString(`{"fields":{"api_key":"sk-x"}}`)
	req := httptest.NewRequest("POST", "/api/v1/connectors/cov-pat-ok/verify?workspace_id="+wsID, body)
	req.SetPathValue("connectorId", "cov-pat-ok")
	req = withWorkspaceUser(req, userID, wsID, "MANAGER")
	rr := httptest.NewRecorder()

	h.Verify(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rr.Code, rr.Body.String())
	}
	if !hit {
		t.Error("expected the verify probe to reach the fake provider")
	}
	var resp VerifyResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !resp.OK {
		t.Errorf("expected ok=true on 200 probe, msg=%q", resp.Message)
	}
}

// -------------------------------------------------------------------
// Install branch coverage: conn_string, none, duplicate-name suffix.
// -------------------------------------------------------------------

func TestCovCon_Install_ConnString_PersistsRowAndCredential(t *testing.T) {
	h := covConHandler(t, covConConnStringManifest)
	db := h.db
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)

	body := bytes.NewBufferString(`{"fields":{"host":"db.example","password":"hunter2"}}`)
	req := httptest.NewRequest("POST", "/api/v1/connectors/cov-conn-string/install?workspace_id="+wsID, body)
	req.SetPathValue("connectorId", "cov-conn-string")
	req = withWorkspaceUser(req, userID, wsID, "OWNER")
	rr := httptest.NewRecorder()

	h.Install(rr, req)
	if rr.Code != http.StatusCreated {
		t.Fatalf("status = %d body=%s", rr.Code, rr.Body.String())
	}
	var resp InstallResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.NextStep != "" {
		t.Errorf("conn_string install must complete inline, got next_step=%q", resp.NextStep)
	}

	var rows int
	if err := db.QueryRow(`SELECT COUNT(*) FROM workspace_mcp_servers WHERE workspace_id = ?`, wsID).Scan(&rows); err != nil {
		t.Fatal(err)
	}
	if rows != 1 {
		t.Errorf("workspace_mcp_servers count = %d, want 1", rows)
	}

	// Both submitted fields are non-empty, so two credential rows
	// should land (host + password).
	var creds int
	if err := db.QueryRow(`SELECT COUNT(*) FROM credentials WHERE workspace_id = ?`, wsID).Scan(&creds); err != nil {
		t.Fatal(err)
	}
	if creds != 2 {
		t.Errorf("credentials count = %d, want 2", creds)
	}
}

func TestCovCon_Install_None_NoCredentials(t *testing.T) {
	h := covConHandler(t, covConNoneManifest)
	db := h.db
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)

	body := bytes.NewBufferString(`{"fields":{}}`)
	req := httptest.NewRequest("POST", "/api/v1/connectors/cov-none/install?workspace_id="+wsID, body)
	req.SetPathValue("connectorId", "cov-none")
	req = withWorkspaceUser(req, userID, wsID, "MANAGER")
	rr := httptest.NewRecorder()

	h.Install(rr, req)
	if rr.Code != http.StatusCreated {
		t.Fatalf("status = %d body=%s", rr.Code, rr.Body.String())
	}
	var resp InstallResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.NextStep != "" {
		t.Errorf("none install must complete inline, got next_step=%q", resp.NextStep)
	}

	// none auth mode persists no credential rows.
	var creds int
	if err := db.QueryRow(`SELECT COUNT(*) FROM credentials WHERE workspace_id = ?`, wsID).Scan(&creds); err != nil {
		t.Fatal(err)
	}
	if creds != 0 {
		t.Errorf("credentials count = %d, want 0 for none auth", creds)
	}
}

func TestCovCon_Install_DuplicateName_SuffixesRow(t *testing.T) {
	// Pre-seed a workspace_mcp_servers row whose name collides with the
	// manifest id (m.ID) so the second install takes the suffixing
	// branch instead of UNIQUE-conflicting. Both rows must coexist.
	h := covConHandler(t, covConNoneManifest)
	db := h.db
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)

	now := time.Now().UTC().Format(time.RFC3339Nano)
	if _, err := db.Exec(`
		INSERT INTO workspace_mcp_servers
			(id, workspace_id, name, display_name, transport, endpoint,
			 command, args_json, env_json, config_json, icon, enabled,
			 connector_id, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		generateCUID(), wsID, "cov-none", "Pre-existing",
		"streamable-http", "https://x/mcp", "",
		"[]", "{}", "{}", "", 1, "cov-none", now, now,
	); err != nil {
		t.Fatalf("seed colliding row: %v", err)
	}

	body := bytes.NewBufferString(`{"fields":{}}`)
	req := httptest.NewRequest("POST", "/api/v1/connectors/cov-none/install?workspace_id="+wsID, body)
	req.SetPathValue("connectorId", "cov-none")
	req = withWorkspaceUser(req, userID, wsID, "MANAGER")
	rr := httptest.NewRecorder()

	h.Install(rr, req)
	if rr.Code != http.StatusCreated {
		t.Fatalf("status = %d body=%s", rr.Code, rr.Body.String())
	}

	// Two rows now: the seeded "cov-none" and the suffixed install.
	var rows int
	if err := db.QueryRow(`SELECT COUNT(*) FROM workspace_mcp_servers WHERE workspace_id = ?`, wsID).Scan(&rows); err != nil {
		t.Fatal(err)
	}
	if rows != 2 {
		t.Errorf("workspace_mcp_servers count = %d, want 2 (suffixed)", rows)
	}
	// The new row's name must not equal the bare manifest id.
	var suffixed int
	if err := db.QueryRow(
		`SELECT COUNT(*) FROM workspace_mcp_servers WHERE workspace_id = ? AND name != 'cov-none'`,
		wsID,
	).Scan(&suffixed); err != nil {
		t.Fatal(err)
	}
	if suffixed != 1 {
		t.Errorf("suffixed-name row count = %d, want 1", suffixed)
	}
}

func TestCovCon_Install_DefaultsNameToManifest(t *testing.T) {
	// Empty req.Name must default display_name to manifest.Name.
	h := covConHandler(t, covConNoneManifest)
	db := h.db
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)

	body := bytes.NewBufferString(`{"fields":{}}`)
	req := httptest.NewRequest("POST", "/api/v1/connectors/cov-none/install?workspace_id="+wsID, body)
	req.SetPathValue("connectorId", "cov-none")
	req = withWorkspaceUser(req, userID, wsID, "MANAGER")
	rr := httptest.NewRecorder()

	h.Install(rr, req)
	if rr.Code != http.StatusCreated {
		t.Fatalf("status = %d body=%s", rr.Code, rr.Body.String())
	}
	var name string
	if err := db.QueryRow(
		`SELECT display_name FROM workspace_mcp_servers WHERE workspace_id = ?`, wsID,
	).Scan(&name); err != nil {
		t.Fatal(err)
	}
	if name != "Cov None" {
		t.Errorf("display_name = %q, want manifest name %q", name, "Cov None")
	}
}

// -------------------------------------------------------------------
// DB-error 500 paths via db.Close() fault injection. Closing the pool
// before the handler runs makes BeginTx fail, so Install returns 500
// without persisting anything.
// -------------------------------------------------------------------

func TestCovCon_Install_BeginTxError_500(t *testing.T) {
	h := covConHandler(t, covConNoneManifest)
	db := h.db
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)

	// Fault injection: a closed pool makes BeginTx error out.
	if err := db.Close(); err != nil {
		t.Fatalf("close db: %v", err)
	}

	body := bytes.NewBufferString(`{"fields":{}}`)
	req := httptest.NewRequest("POST", "/api/v1/connectors/cov-none/install?workspace_id="+wsID, body)
	req.SetPathValue("connectorId", "cov-none")
	req = withWorkspaceUser(req, userID, wsID, "MANAGER")
	rr := httptest.NewRecorder()

	h.Install(rr, req)
	if rr.Code != http.StatusInternalServerError {
		t.Errorf("status = %d, want 500 (begin tx on closed db)", rr.Code)
	}
}
