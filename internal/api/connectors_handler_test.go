// Tests for the Connectors HTTP API. The contract is the wire shape
// returned by List / Get / Verify / Install plus the RBAC envelope.
//
// The handler bodies are TDD stubs — these tests fail with
// "not implemented" until the implementer fills them in. The
// authoritative reference for what each test asserts is the doc
// comments in connectors_handler.go.
package api

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"testing/fstest"
	"time"

	"github.com/crewship-ai/crewship/internal/connectors"
	"github.com/crewship-ai/crewship/internal/httpsafe"
)

// -------------------------------------------------------------------
// Test fixtures: a tiny synthetic catalog that doesn't depend on the
// shipped manifests. Using a synthetic catalog keeps these tests
// deterministic if the fixtures evolve.
// -------------------------------------------------------------------

const synthPATManifest = `id: synth-pat
name: Synthetic PAT Connector
description: Test PAT connector
category: testing
brand: {logo: synth, color: "#000000"}
auth_mode: pat
fields:
  - {key: api_key, label: API Key, type: password, required: true, placeholder: sk-...}
mcp:
  transport: stdio
  command: echo
  args: ["${field.api_key}"]
  env:
    SYNTH_KEY: "${field.api_key}"
verify:
  http:
    method: GET
    url: https://example.com/me
    headers:
      Authorization: "Bearer ${field.api_key}"
    expect_status: 200
`

const synthMCPOAuthManifest = `id: synth-mcp-oauth
name: Synthetic MCP OAuth
description: Test mcp_oauth connector
category: testing
brand: {logo: synth, color: "#111111"}
auth_mode: mcp_oauth
mcp:
  transport: streamable-http
  endpoint: https://mcp.synth.example/mcp
verify:
  mcp_method: tools/list
`

const synthBYOOAuthManifest = `id: synth-byo
name: Synthetic BYO OAuth
description: Test byo_oauth connector
category: testing
brand: {logo: synth, color: "#222222"}
auth_mode: byo_oauth
oauth:
  authorization_url: https://provider.example/oauth/authorize
  token_url: https://provider.example/oauth/token
  scopes: [read, write]
  pkce: true
fields:
  - {key: client_id, label: Client ID, type: text, required: true}
  - {key: client_secret, label: Client Secret, type: password, required: true}
mcp:
  transport: streamable-http
  endpoint: https://provider.example/mcp
docs:
  setup_md: "Register at https://provider.example then paste creds. Use ${instance_url}/oauth/callback as redirect."
`

func newSynthCatalog(t *testing.T) *connectors.Catalog {
	t.Helper()
	memFS := fstest.MapFS{
		"fixtures/synth-pat.yaml":       &fstest.MapFile{Data: []byte(synthPATManifest)},
		"fixtures/synth-mcp-oauth.yaml": &fstest.MapFile{Data: []byte(synthMCPOAuthManifest)},
		"fixtures/synth-byo.yaml":       &fstest.MapFile{Data: []byte(synthBYOOAuthManifest)},
	}
	cat, errs := connectors.LoadAll(memFS)
	if len(errs) != 0 {
		t.Fatalf("synth catalog load errors: %v", errs)
	}
	if cat.Len() != 3 {
		t.Fatalf("expected 3 synth manifests, got %d", cat.Len())
	}
	return cat
}

func newSynthHandler(t *testing.T) *ConnectorHandler {
	t.Helper()
	// Install persists user-submitted secrets via encryption.Encrypt,
	// which fails on a missing ENCRYPTION_KEY. The shared parallel-
	// safe helper sets it once per test binary so List/Get/Verify
	// (which don't touch encryption) still work, and Install tests
	// (which do) succeed without per-test plumbing.
	setTestEncryptionKeyParallelSafe(t)
	db := setupTestDB(t)
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))
	return NewConnectorHandlerWithCatalog(db, logger, newSynthCatalog(t))
}

// -------------------------------------------------------------------
// GET /api/v1/connectors — catalog list
// -------------------------------------------------------------------

func TestConnectors_List_OK(t *testing.T) {
	h := newSynthHandler(t)

	req := httptest.NewRequest("GET", "/api/v1/connectors", nil)
	req = req.WithContext(withUser(req.Context(), &AuthUser{ID: "u1"}))
	rr := httptest.NewRecorder()

	h.List(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, body: %s", rr.Code, rr.Body.String())
	}

	var items []ConnectorListItem
	if err := json.Unmarshal(rr.Body.Bytes(), &items); err != nil {
		t.Fatalf("decode: %v body=%s", err, rr.Body.String())
	}
	if len(items) != 3 {
		t.Errorf("len = %d, want 3", len(items))
	}

	// Stable-order contract: ConnectorHandler.List documents that
	// items come back in catalog insertion order. Verify the exact
	// sequence so a non-deterministic map-walk implementation can't
	// silently regress past this test. fstest.MapFS yields keys
	// in lexical filename order, which is also what embed.FS does
	// for the shipped FixturesFS — so the expected order is fixed.
	expectedOrder := []string{"synth-byo", "synth-mcp-oauth", "synth-pat"}
	for i, want := range expectedOrder {
		if i >= len(items) {
			break
		}
		if items[i].ID != want {
			t.Errorf("items[%d].ID = %q, want %q (stable order broken)", i, items[i].ID, want)
		}
	}

	// Each item must surface the fields the catalog UI needs.
	seen := map[string]ConnectorListItem{}
	for _, it := range items {
		seen[it.ID] = it
	}
	if _, ok := seen["synth-pat"]; !ok {
		t.Error("synth-pat missing from list")
	}
	if got := seen["synth-pat"].AuthMode; got != "pat" {
		t.Errorf("synth-pat auth_mode = %q", got)
	}
	if got := seen["synth-pat"].BrandColor; got != "#000000" {
		t.Errorf("synth-pat brand_color = %q", got)
	}
}

func TestConnectors_List_UnauthenticatedRejected(t *testing.T) {
	h := newSynthHandler(t)
	req := httptest.NewRequest("GET", "/api/v1/connectors", nil)
	rr := httptest.NewRecorder()
	h.List(rr, req)
	// Without an authed user in ctx the handler must refuse.
	if rr.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401", rr.Code)
	}
}

func TestConnectors_List_EmptyCatalog(t *testing.T) {
	db := setupTestDB(t)
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))
	emptyCat, _ := connectors.LoadAll(fstest.MapFS{})
	h := NewConnectorHandlerWithCatalog(db, logger, emptyCat)

	req := httptest.NewRequest("GET", "/api/v1/connectors", nil)
	req = req.WithContext(withUser(req.Context(), &AuthUser{ID: "u1"}))
	rr := httptest.NewRecorder()

	h.List(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rr.Code)
	}
	// Must be an empty JSON array, not null.
	if got := bytes.TrimSpace(rr.Body.Bytes()); !bytes.Equal(got, []byte("[]")) {
		t.Errorf("empty catalog body = %q, want []", string(got))
	}
}

// -------------------------------------------------------------------
// GET /api/v1/connectors/{id} — manifest detail
// -------------------------------------------------------------------

func TestConnectors_Get_PAT_FullManifest(t *testing.T) {
	h := newSynthHandler(t)

	req := httptest.NewRequest("GET", "/api/v1/connectors/synth-pat", nil)
	req.SetPathValue("connectorId", "synth-pat")
	req = req.WithContext(withUser(req.Context(), &AuthUser{ID: "u1"}))
	rr := httptest.NewRecorder()

	h.Get(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rr.Code, rr.Body.String())
	}

	var m connectors.Manifest
	if err := json.Unmarshal(rr.Body.Bytes(), &m); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if m.ID != "synth-pat" {
		t.Errorf("id = %q", m.ID)
	}
	if m.AuthMode != connectors.AuthModePAT {
		t.Errorf("auth_mode = %q", m.AuthMode)
	}
	// Detail must include the fields[] array the form needs.
	if len(m.Fields) != 1 {
		t.Errorf("fields len = %d, want 1", len(m.Fields))
	}
	if m.MCP.Command != "echo" {
		t.Errorf("mcp.command = %q", m.MCP.Command)
	}
}

func TestConnectors_Get_NotFound(t *testing.T) {
	h := newSynthHandler(t)
	req := httptest.NewRequest("GET", "/api/v1/connectors/nope", nil)
	req.SetPathValue("connectorId", "nope")
	req = req.WithContext(withUser(req.Context(), &AuthUser{ID: "u1"}))
	rr := httptest.NewRecorder()
	h.Get(rr, req)
	if rr.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", rr.Code)
	}
}

// -------------------------------------------------------------------
// POST /api/v1/connectors/{id}/verify — pre-install probe
// -------------------------------------------------------------------

func TestConnectors_Verify_PATCallsHTTPEndpoint(t *testing.T) {
	// Stand up a fake provider that asserts the bearer header was
	// substituted from the user-submitted api_key.
	//
	// The httptest handler runs in its own goroutine; the assertion
	// below reads `called` from the test goroutine. atomic.Bool keeps
	// the write/read pair race-free under `go test -race`.
	var called atomic.Bool
	fake := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called.Store(true)
		if r.Header.Get("Authorization") != "Bearer sk-test-123" {
			t.Errorf("Authorization header = %q", r.Header.Get("Authorization"))
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer fake.Close()

	// Tests use a verify URL of "https://verify.test/me" that passes
	// httpsafe.ValidateURL; SetVerifyHTTPClientForTesting installs a
	// rewriteRoundTripper that routes the actual bytes to the
	// loopback fake without weakening the SSRF guard in production.
	target, err := url.Parse(fake.URL)
	if err != nil {
		t.Fatalf("parse fake URL: %v", err)
	}
	restoreVerify := SetVerifyHTTPClientForTesting(&http.Client{
		Timeout:   5 * time.Second,
		Transport: &httpsafe.RewriteRoundTripper{Target: target},
	})
	defer restoreVerify()

	yaml := `id: ad-hoc
name: Ad hoc
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
	cat, errs := connectors.LoadAll(fstest.MapFS{
		"fixtures/ad-hoc.yaml": &fstest.MapFile{Data: []byte(yaml)},
	})
	if len(errs) != 0 {
		t.Fatalf("load: %v", errs)
	}

	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))
	h := NewConnectorHandlerWithCatalog(db, logger, cat)

	body := bytes.NewBufferString(`{"fields":{"api_key":"sk-test-123"}}`)
	req := httptest.NewRequest("POST", "/api/v1/connectors/ad-hoc/verify?workspace_id="+wsID, body)
	req.SetPathValue("connectorId", "ad-hoc")
	ctx := withUser(req.Context(), &AuthUser{ID: userID})
	ctx = withWorkspace(ctx, wsID, "MANAGER")
	req = req.WithContext(ctx)
	rr := httptest.NewRecorder()

	h.Verify(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rr.Code, rr.Body.String())
	}
	if !called.Load() {
		t.Error("expected verify URL to be called")
	}

	var resp VerifyResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !resp.OK {
		t.Errorf("ok = false, message: %s", resp.Message)
	}
}

func TestConnectors_Verify_PATInvalidToken(t *testing.T) {
	fake := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer fake.Close()

	// rewriteRoundTripper retargets verify.test → loopback so the
	// production code path's httpsafe.ValidateURL stays unconditional
	// (see PATCallsHTTPEndpoint for the full rationale).
	target, err := url.Parse(fake.URL)
	if err != nil {
		t.Fatalf("parse fake URL: %v", err)
	}
	restoreVerify := SetVerifyHTTPClientForTesting(&http.Client{
		Timeout:   5 * time.Second,
		Transport: &httpsafe.RewriteRoundTripper{Target: target},
	})
	defer restoreVerify()

	yaml := `id: bad-token
name: Bad
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
	cat, _ := connectors.LoadAll(fstest.MapFS{
		"fixtures/bad-token.yaml": &fstest.MapFile{Data: []byte(yaml)},
	})

	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))
	h := NewConnectorHandlerWithCatalog(db, logger, cat)

	body := bytes.NewBufferString(`{"fields":{"api_key":"junk"}}`)
	req := httptest.NewRequest("POST", "/api/v1/connectors/bad-token/verify?workspace_id="+wsID, body)
	req.SetPathValue("connectorId", "bad-token")
	ctx := withUser(req.Context(), &AuthUser{ID: userID})
	ctx = withWorkspace(ctx, wsID, "MANAGER")
	req = req.WithContext(ctx)
	rr := httptest.NewRecorder()

	h.Verify(rr, req)
	if rr.Code != http.StatusOK {
		// The HTTP request itself succeeded; only ok=false signals
		// invalid creds. 4xx would mean a system-level error.
		t.Fatalf("status = %d", rr.Code)
	}

	var resp VerifyResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v body=%s", err, rr.Body.String())
	}
	if resp.OK {
		t.Errorf("expected ok=false on 401 from provider")
	}
	if resp.Message == "" {
		t.Error("expected human-readable message on failure")
	}
}

func TestConnectors_Verify_MissingRequiredField(t *testing.T) {
	h := newSynthHandler(t)
	db := h.db
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)

	body := bytes.NewBufferString(`{"fields":{}}`)
	req := httptest.NewRequest("POST", "/api/v1/connectors/synth-pat/verify?workspace_id="+wsID, body)
	req.SetPathValue("connectorId", "synth-pat")
	ctx := withUser(req.Context(), &AuthUser{ID: userID})
	ctx = withWorkspace(ctx, wsID, "MANAGER")
	req = req.WithContext(ctx)
	rr := httptest.NewRecorder()

	h.Verify(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400 (missing required field)", rr.Code)
	}
}

func TestConnectors_Verify_RBAC_ViewerForbidden(t *testing.T) {
	h := newSynthHandler(t)
	db := h.db
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)

	body := bytes.NewBufferString(`{"fields":{"api_key":"x"}}`)
	req := httptest.NewRequest("POST", "/api/v1/connectors/synth-pat/verify?workspace_id="+wsID, body)
	req.SetPathValue("connectorId", "synth-pat")
	ctx := withUser(req.Context(), &AuthUser{ID: userID})
	ctx = withWorkspace(ctx, wsID, "VIEWER")
	req = req.WithContext(ctx)
	rr := httptest.NewRecorder()

	h.Verify(rr, req)
	if rr.Code != http.StatusForbidden {
		t.Errorf("status = %d, want 403 for VIEWER", rr.Code)
	}
}

func TestConnectors_Verify_MCPOAuthIsNoOp(t *testing.T) {
	// mcp_oauth connectors don't require user-submitted credentials.
	// Verify must return ok=true without making any HTTP call.
	h := newSynthHandler(t)
	db := h.db
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)

	body := bytes.NewBufferString(`{"fields":{}}`)
	req := httptest.NewRequest("POST", "/api/v1/connectors/synth-mcp-oauth/verify?workspace_id="+wsID, body)
	req.SetPathValue("connectorId", "synth-mcp-oauth")
	ctx := withUser(req.Context(), &AuthUser{ID: userID})
	ctx = withWorkspace(ctx, wsID, "MANAGER")
	req = req.WithContext(ctx)
	rr := httptest.NewRecorder()

	h.Verify(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d", rr.Code)
	}
	var resp VerifyResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v body=%s", err, rr.Body.String())
	}
	if !resp.OK {
		t.Errorf("mcp_oauth verify must be ok, got message: %s", resp.Message)
	}
}

// -------------------------------------------------------------------
// POST /api/v1/connectors/{id}/install — materialize + persist
// -------------------------------------------------------------------

func TestConnectors_Install_PAT_CreatesIntegrationAndCredential(t *testing.T) {
	h := newSynthHandler(t)
	db := h.db
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)

	body := bytes.NewBufferString(`{"name":"My Synth","fields":{"api_key":"sk-real-456"}}`)
	req := httptest.NewRequest("POST", "/api/v1/connectors/synth-pat/install?workspace_id="+wsID, body)
	req.SetPathValue("connectorId", "synth-pat")
	ctx := withUser(req.Context(), &AuthUser{ID: userID})
	ctx = withWorkspace(ctx, wsID, "MANAGER")
	req = req.WithContext(ctx)
	rr := httptest.NewRecorder()

	h.Install(rr, req)
	if rr.Code != http.StatusCreated {
		t.Fatalf("status = %d body=%s", rr.Code, rr.Body.String())
	}

	var resp InstallResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.IntegrationID == "" {
		t.Error("missing integration_id")
	}
	if resp.NextStep != "" {
		t.Errorf("PAT install must complete inline, got next_step=%q", resp.NextStep)
	}

	// A workspace_mcp_servers row should now exist.
	var count int
	if err := db.QueryRow(`SELECT COUNT(*) FROM workspace_mcp_servers WHERE workspace_id = ?`, wsID).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Errorf("workspace_mcp_servers count = %d, want 1", count)
	}

	// connector_id must be populated so the row is traceable back to
	// its manifest. Migration v76 added the column; the Install
	// handler must set it.
	var connectorID *string
	if err := db.QueryRow(
		`SELECT connector_id FROM workspace_mcp_servers WHERE workspace_id = ?`,
		wsID,
	).Scan(&connectorID); err != nil {
		t.Fatal(err)
	}
	if connectorID == nil || *connectorID != "synth-pat" {
		got := "<nil>"
		if connectorID != nil {
			got = *connectorID
		}
		t.Errorf("connector_id = %q, want synth-pat", got)
	}

	// And a credentials row should exist for the api_key.
	var credCount int
	if err := db.QueryRow(`SELECT COUNT(*) FROM credentials WHERE workspace_id = ?`, wsID).Scan(&credCount); err != nil {
		t.Fatal(err)
	}
	if credCount < 1 {
		t.Errorf("credentials count = %d, want >= 1", credCount)
	}
}

func TestConnectors_Install_BYOOAuth_ReturnsOAuthURL(t *testing.T) {
	h := newSynthHandler(t)
	db := h.db
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)

	body := bytes.NewBufferString(`{"fields":{"client_id":"abc","client_secret":"def"}}`)
	req := httptest.NewRequest("POST", "/api/v1/connectors/synth-byo/install?workspace_id="+wsID, body)
	req.SetPathValue("connectorId", "synth-byo")
	ctx := withUser(req.Context(), &AuthUser{ID: userID})
	ctx = withWorkspace(ctx, wsID, "MANAGER")
	req = req.WithContext(ctx)
	rr := httptest.NewRecorder()

	h.Install(rr, req)
	if rr.Code != http.StatusCreated {
		t.Fatalf("status = %d body=%s", rr.Code, rr.Body.String())
	}
	var resp InstallResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v body=%s", err, rr.Body.String())
	}
	if resp.NextStep != "oauth" {
		t.Errorf("next_step = %q, want oauth", resp.NextStep)
	}
	if resp.OAuthURL == "" {
		t.Error("oauth_url empty")
	}
	// URL must reference the configured authorization_url.
	if !bytes.Contains([]byte(resp.OAuthURL), []byte("provider.example/oauth/authorize")) {
		t.Errorf("oauth_url = %q does not reference authorization_url", resp.OAuthURL)
	}
	// Must include client_id from the user-submitted fields.
	if !bytes.Contains([]byte(resp.OAuthURL), []byte("client_id=abc")) {
		t.Errorf("oauth_url missing client_id: %q", resp.OAuthURL)
	}
}

func TestConnectors_Install_MCPOAuth_ReturnsMCPOAuthStep(t *testing.T) {
	h := newSynthHandler(t)
	db := h.db
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)

	body := bytes.NewBufferString(`{"fields":{}}`)
	req := httptest.NewRequest("POST", "/api/v1/connectors/synth-mcp-oauth/install?workspace_id="+wsID, body)
	req.SetPathValue("connectorId", "synth-mcp-oauth")
	ctx := withUser(req.Context(), &AuthUser{ID: userID})
	ctx = withWorkspace(ctx, wsID, "MANAGER")
	req = req.WithContext(ctx)
	rr := httptest.NewRecorder()

	h.Install(rr, req)
	if rr.Code != http.StatusCreated {
		t.Fatalf("status = %d body=%s", rr.Code, rr.Body.String())
	}
	var resp InstallResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v body=%s", err, rr.Body.String())
	}
	if resp.NextStep != "mcp_oauth" {
		t.Errorf("next_step = %q, want mcp_oauth", resp.NextStep)
	}
}

func TestConnectors_Install_RBAC_ViewerForbidden(t *testing.T) {
	h := newSynthHandler(t)
	db := h.db
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)

	body := bytes.NewBufferString(`{"fields":{"api_key":"x"}}`)
	req := httptest.NewRequest("POST", "/api/v1/connectors/synth-pat/install?workspace_id="+wsID, body)
	req.SetPathValue("connectorId", "synth-pat")
	ctx := withUser(req.Context(), &AuthUser{ID: userID})
	ctx = withWorkspace(ctx, wsID, "VIEWER")
	req = req.WithContext(ctx)
	rr := httptest.NewRecorder()

	h.Install(rr, req)
	if rr.Code != http.StatusForbidden {
		t.Errorf("status = %d, want 403 for VIEWER", rr.Code)
	}
}

func TestConnectors_Install_MissingWorkspaceID(t *testing.T) {
	h := newSynthHandler(t)
	body := bytes.NewBufferString(`{"fields":{"api_key":"x"}}`)
	req := httptest.NewRequest("POST", "/api/v1/connectors/synth-pat/install", body)
	req.SetPathValue("connectorId", "synth-pat")
	req = req.WithContext(withUser(req.Context(), &AuthUser{ID: "u1"}))
	rr := httptest.NewRecorder()
	h.Install(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400 (missing workspace_id)", rr.Code)
	}
}

func TestConnectors_Install_UnknownConnector_404(t *testing.T) {
	h := newSynthHandler(t)
	db := h.db
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)

	body := bytes.NewBufferString(`{"fields":{}}`)
	req := httptest.NewRequest("POST", "/api/v1/connectors/nope/install?workspace_id="+wsID, body)
	req.SetPathValue("connectorId", "nope")
	ctx := withUser(req.Context(), &AuthUser{ID: userID})
	ctx = withWorkspace(ctx, wsID, "MANAGER")
	req = req.WithContext(ctx)
	rr := httptest.NewRecorder()

	h.Install(rr, req)
	if rr.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", rr.Code)
	}
}

// -------------------------------------------------------------------
// InstanceURLFromRequest — pins the "where does ${instance_url} come
// from" contract so handler implementations can't drift away from
// what the docs/setup_md authoring guide promises.
// -------------------------------------------------------------------

func TestInstanceURLFromRequest_FallsBackToRequestHost(t *testing.T) {
	t.Parallel()
	req := httptest.NewRequest("GET", "/", nil)
	req.Host = "acme.example.com:8080"
	got := InstanceURLFromRequest(req, "")
	if got != "https://acme.example.com:8080" {
		t.Errorf("got %q, want https://acme.example.com:8080", got)
	}
}

func TestInstanceURLFromRequest_PublicBaseURLOverrides(t *testing.T) {
	t.Parallel()
	// Even with a totally different r.Host, the configured public
	// URL wins — this is the path operators wire up in production.
	req := httptest.NewRequest("GET", "/", nil)
	req.Host = "internal.svc.local:8080"
	got := InstanceURLFromRequest(req, "https://acme.example.com")
	if got != "https://acme.example.com" {
		t.Errorf("got %q, want https://acme.example.com (config wins)", got)
	}
}

func TestInstanceURLFromRequest_PublicBaseURLStripsTrailingSlash(t *testing.T) {
	t.Parallel()
	got := InstanceURLFromRequest(nil, "https://acme.example.com/")
	if got != "https://acme.example.com" {
		t.Errorf("got %q, want trailing slash stripped", got)
	}
}

func TestInstanceURLFromRequest_IgnoresForwardedHost(t *testing.T) {
	t.Parallel()
	// Security: an attacker setting X-Forwarded-Host on a directly-
	// exposed instance must NOT poison ${instance_url}. The helper
	// is bounded by r.Host (the listener's hostname) when no public
	// URL is configured.
	req := httptest.NewRequest("GET", "/", nil)
	req.Host = "real.example.com"
	req.Header.Set("X-Forwarded-Host", "evil.attacker.com")
	req.Header.Set("X-Forwarded-Proto", "http")
	got := InstanceURLFromRequest(req, "")
	if got != "https://real.example.com" {
		t.Errorf("got %q, forwarded headers must not override r.Host", got)
	}
}

func TestInstanceURLFromRequest_NoTrailingSlash(t *testing.T) {
	t.Parallel()
	req := httptest.NewRequest("GET", "/", nil)
	req.Host = "acme.example.com"
	got := InstanceURLFromRequest(req, "")
	if strings.HasSuffix(got, "/") {
		t.Errorf("instance_url must not end with slash: %q", got)
	}
}

func TestInstanceURLFromRequest_NilRequestNoConfig(t *testing.T) {
	t.Parallel()
	if got := InstanceURLFromRequest(nil, ""); got != "" {
		t.Errorf("got %q, want empty for nil request + no config", got)
	}
}

func TestInstanceURLFromRequest_EmptyHost(t *testing.T) {
	t.Parallel()
	req := httptest.NewRequest("GET", "/", nil)
	req.Host = ""
	if got := InstanceURLFromRequest(req, ""); got != "" {
		t.Errorf("got %q, want empty for empty host", got)
	}
}

func TestConnectors_Install_MalformedBody(t *testing.T) {
	h := newSynthHandler(t)
	db := h.db
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)

	body := bytes.NewBufferString(`{not valid json`)
	req := httptest.NewRequest("POST", "/api/v1/connectors/synth-pat/install?workspace_id="+wsID, body)
	req.SetPathValue("connectorId", "synth-pat")
	ctx := withUser(req.Context(), &AuthUser{ID: userID})
	ctx = withWorkspace(ctx, wsID, "MANAGER")
	req = req.WithContext(ctx)
	rr := httptest.NewRecorder()

	h.Install(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", rr.Code)
	}
}
