// Connectors API — exposes the curated manifest catalog and drives
// the install flow for catalog-based integrations. The legacy
// /api/v1/integrations endpoints (workspace_integrations.go) remain
// the path for the "Custom MCP server" escape hatch; the catalog flow
// goes through the routes defined here.
//
// Endpoint surface (see connectors_handler_test.go for the spec):
//
//	GET    /api/v1/connectors                     -- list catalog
//	GET    /api/v1/connectors/{connectorId}       -- manifest detail
//	POST   /api/v1/connectors/{connectorId}/verify   -- pre-install probe
//	POST   /api/v1/connectors/{connectorId}/install  -- materialize + persist
//
// Auth model:
//
//   - All four endpoints require an authenticated user (401 if absent).
//   - List + Get have no role restriction beyond authentication: every
//     authed role (VIEWER through OWNER) can browse the catalog.
//   - Verify + Install additionally require ?workspace_id=X plus a
//     MANAGER+ role on that workspace; lower roles get 403.
//
// TDD STUB — methods panic until implemented.
package api

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/crewship-ai/crewship/internal/connectors"
	"github.com/crewship-ai/crewship/internal/encryption"
	"github.com/crewship-ai/crewship/internal/httpsafe"
)

// ConnectorHandler wires the embedded catalog into the HTTP layer.
//
// Catalog is loaded once at construction time. If a non-empty list of
// load errors is returned by connectors.LoadAll, NewConnectorHandler
// logs them but continues — partial catalogs are valid (a single bad
// fixture should not block the rest).
type ConnectorHandler struct {
	db      *sql.DB
	logger  *slog.Logger
	catalog *connectors.Catalog
}

// NewConnectorHandler loads the embedded fixtures and returns a ready
// handler. Tests override the catalog via NewConnectorHandlerWithCatalog
// so they can inject synthetic manifests without touching the embed.
func NewConnectorHandler(db *sql.DB, logger *slog.Logger) *ConnectorHandler {
	cat, errs := connectors.LoadAll(connectors.FixturesFS)
	for _, e := range errs {
		logger.Warn("connector manifest skipped", "err", e)
	}
	return &ConnectorHandler{db: db, logger: logger, catalog: cat}
}

// NewConnectorHandlerWithCatalog is the test seam.
func NewConnectorHandlerWithCatalog(db *sql.DB, logger *slog.Logger, cat *connectors.Catalog) *ConnectorHandler {
	return &ConnectorHandler{db: db, logger: logger, catalog: cat}
}

// ConnectorListItem is the wire shape for GET /api/v1/connectors.
//
// It is a deliberate subset of Manifest — only fields the catalog UI
// needs to render a tile. Fetching detail returns the full manifest.
type ConnectorListItem struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description"`
	Category    string `json:"category"`
	AuthMode    string `json:"auth_mode"`
	BrandLogo   string `json:"brand_logo"`
	BrandColor  string `json:"brand_color"`
}

// VerifyRequest is the body of POST /connectors/{id}/verify.
//
// Fields holds user-submitted form values; they are used to resolve
// placeholders inside Verify.HTTP / Verify.MCPMethod. Returning ok=true
// means the credentials look valid; the caller should still install
// to persist them.
type VerifyRequest struct {
	Fields map[string]string `json:"fields"`
}

// VerifyResponse mirrors the structure used elsewhere in the API.
type VerifyResponse struct {
	OK      bool   `json:"ok"`
	Message string `json:"message,omitempty"`
}

// InstallRequest is the body of POST /connectors/{id}/install.
//
// CrewID is optional — if omitted, the integration is installed at
// workspace scope; if set, at crew scope (matches existing
// workspace_mcp_servers vs crew_mcp_servers split). Name is the
// user-facing label; if empty, defaults to manifest.Name.
type InstallRequest struct {
	CrewID string            `json:"crew_id,omitempty"`
	Name   string            `json:"name,omitempty"`
	Fields map[string]string `json:"fields"`
}

// InstallResponse mirrors what the existing IntegrationHandler returns
// for create operations, with NextStep folded in for OAuth flows.
//
// NextStep is one of:
//
//	""         — install complete, no further user action
//	"oauth"    — frontend should open OAuthURL in a popup
//	"mcp_oauth" — frontend should hand off to MCP-OAuth/DCR flow
type InstallResponse struct {
	IntegrationID string `json:"integration_id"`
	NextStep      string `json:"next_step,omitempty"`
	OAuthURL      string `json:"oauth_url,omitempty"`
}

// verifyHTTPClient handles the outbound HTTP request the Verify
// endpoint issues against a provider. Bounded timeout keeps a slow
// provider from holding a server worker indefinitely. SafeClient wires
// in the SSRF dialer + redirect re-validation so a connector manifest
// that resolves an attacker-controlled host into a private IP cannot
// reach loopback / RFC1918 / cloud-metadata addresses.
//
// verifyURLValidate is the matching string-level guard. Both vars are
// pointers (not function-scoped) so a single test helper can swap both
// to an unsafe pair that allows httptest.NewServer; production code
// path never reassigns them.
var (
	verifyHTTPClient  = httpsafe.SafeClient(10*time.Second, "http", "https")
	verifyURLValidate = func(raw string) error {
		_, err := httpsafe.ValidateURL(raw, "http", "https")
		return err
	}
)

// SetVerifyHTTPClientForTesting swaps the package-level verify client
// + URL validator with a no-op pair so unit tests targeting
// httptest.NewServer (127.0.0.1) can drive Verify end-to-end. Returns
// a restore func; defer it in test bodies. Production code must not
// call this — there is no production wiring path that does.
func SetVerifyHTTPClientForTesting(c *http.Client) (restore func()) {
	prevClient := verifyHTTPClient
	prevValidate := verifyURLValidate
	verifyHTTPClient = c
	verifyURLValidate = func(string) error { return nil }
	return func() {
		verifyHTTPClient = prevClient
		verifyURLValidate = prevValidate
	}
}

// List handles GET /api/v1/connectors. Returns 200 with the catalog
// as []ConnectorListItem in stable insertion order. Empty array (not
// null) on an empty catalog so the frontend's `.map` doesn't blow up.
// No filtering parameters in v1 — frontend filters client-side.
func (h *ConnectorHandler) List(w http.ResponseWriter, r *http.Request) {
	if UserFromContext(r.Context()) == nil {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "Unauthorized"})
		return
	}
	manifests := h.catalog.List()
	items := make([]ConnectorListItem, 0, len(manifests))
	for _, m := range manifests {
		items = append(items, ConnectorListItem{
			ID:          m.ID,
			Name:        m.Name,
			Description: m.Description,
			Category:    m.Category,
			AuthMode:    string(m.AuthMode),
			BrandLogo:   m.Brand.Logo,
			BrandColor:  m.Brand.Color,
		})
	}
	writeJSON(w, http.StatusOK, items)
}

// Get handles GET /api/v1/connectors/{connectorId}. Returns 200 with
// the full Manifest verbatim so the frontend can render the
// schema-driven form without a second round-trip. 404 when the
// catalog has no matching id.
func (h *ConnectorHandler) Get(w http.ResponseWriter, r *http.Request) {
	if UserFromContext(r.Context()) == nil {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "Unauthorized"})
		return
	}
	id := r.PathValue("connectorId")
	m, err := h.catalog.LoadByID(id)
	if err != nil {
		if errors.Is(err, connectors.ErrConnectorNotFound) {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "connector not found"})
			return
		}
		h.logger.Error("connector lookup", "error", err, "id", id)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "internal error"})
		return
	}
	writeJSON(w, http.StatusOK, m)
}

// Verify handles POST /api/v1/connectors/{connectorId}/verify.
//
// For PAT manifests this resolves Verify.HTTP against the submitted
// field values and makes one HTTP call. mcp_oauth and byo_oauth skip
// the probe (auth happens via redirect) and return ok=true.
//
// Auth: requires an authenticated user, ?workspace_id=X, and a
// MANAGER+ role on that workspace. Lower roles get 403.
//
// 200 ok=true means credentials look valid. 200 ok=false means the
// provider rejected them — but the system call itself succeeded; the
// caller should treat that as user-correctable, not a server error.
// 4xx is reserved for system-level problems (missing fields, RBAC,
// unknown connector).
func (h *ConnectorHandler) Verify(w http.ResponseWriter, r *http.Request) {
	if UserFromContext(r.Context()) == nil {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "Unauthorized"})
		return
	}
	role := RoleFromContext(r.Context())
	// MANAGER+ is the lower bound — canRole("create") covers
	// OWNER/ADMIN/MANAGER, which matches the documented gate. The
	// "manage" action is OWNER/ADMIN only and would lock out the
	// MANAGER role this endpoint is supposed to allow.
	if !canRole(role, "create") {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "Forbidden: MANAGER+ required"})
		return
	}

	id := r.PathValue("connectorId")
	m, err := h.catalog.LoadByID(id)
	if err != nil {
		if errors.Is(err, connectors.ErrConnectorNotFound) {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "connector not found"})
			return
		}
		h.logger.Error("connector lookup", "error", err, "id", id)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "internal error"})
		return
	}

	var req VerifyRequest
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid body"})
		return
	}

	// mcp_oauth + byo_oauth + none have nothing to probe — credentials
	// come from a redirect flow, not a paste. Surface ok=true so the
	// frontend can move straight to install without an empty round-trip.
	if m.AuthMode == connectors.AuthModeMCPOAuth || m.AuthMode == connectors.AuthModeBYOOAuth || m.AuthMode == connectors.AuthModeNone {
		writeJSON(w, http.StatusOK, VerifyResponse{OK: true})
		return
	}

	// Validate required-field coverage before resolving the URL so the
	// caller sees a 400 with a clear cause, not a 500 from the resolver.
	for i := range m.Fields {
		f := &m.Fields[i]
		if f.Required && strings.TrimSpace(req.Fields[f.Key]) == "" {
			writeJSON(w, http.StatusBadRequest, map[string]string{
				"error": "missing required field: " + f.Key,
			})
			return
		}
	}

	// Manifests without a verify block accept the credentials at face
	// value — install will still surface auth errors at first MCP call.
	if m.Verify == nil || m.Verify.HTTP == nil {
		writeJSON(w, http.StatusOK, VerifyResponse{OK: true})
		return
	}

	ok, msg := h.probeVerifyHTTP(r.Context(), m, req.Fields)
	writeJSON(w, http.StatusOK, VerifyResponse{OK: ok, Message: msg})
}

// probeVerifyHTTP runs Verify.HTTP against the provider with the
// submitted field values substituted into URL + headers. Returns
// (ok, message) — message is human-readable when ok=false so the
// frontend can surface it directly. Network errors are reported as
// ok=false rather than bubbled up as 5xx so callers don't have to
// special-case "provider unreachable" vs "invalid token".
func (h *ConnectorHandler) probeVerifyHTTP(ctx context.Context, m *connectors.Manifest, fields map[string]string) (bool, string) {
	v := m.Verify.HTTP
	rctx := connectors.ResolveContext{Fields: fields}
	resolvedURL, err := m.Resolve(v.URL, rctx)
	if err != nil {
		return false, "verify URL resolution failed: " + err.Error()
	}
	// Manifest URLs are author-controlled and field substitutions are
	// user-controlled — both untrusted from the verify endpoint's POV.
	// verifyURLValidate handles the cheap rejects (scheme, literal
	// RFC1918, userinfo); SafeTransport on verifyHTTPClient catches
	// DNS aliases. The function-pointer indirection exists so
	// SetVerifyHTTPClientForTesting can replace the validator with a
	// no-op for unit tests targeting httptest.NewServer.
	if err := verifyURLValidate(resolvedURL); err != nil {
		return false, "verify URL rejected: " + err.Error()
	}

	method := v.Method
	if method == "" {
		method = http.MethodGet
	}
	req, err := http.NewRequestWithContext(ctx, method, resolvedURL, nil)
	if err != nil {
		return false, "verify request build failed: " + err.Error()
	}
	for k, tmpl := range v.Headers {
		resolved, herr := m.Resolve(tmpl, rctx)
		if herr != nil {
			return false, "verify header " + k + " resolution failed: " + herr.Error()
		}
		req.Header.Set(k, resolved)
	}

	resp, err := verifyHTTPClient.Do(req)
	if err != nil {
		return false, "provider unreachable: " + err.Error()
	}
	defer resp.Body.Close()

	expect := v.ExpectStatus
	if expect == 0 {
		expect = http.StatusOK
	}
	if resp.StatusCode != expect {
		// Echo a small slice of the body so the user can see what the
		// provider said — bounded to keep large HTML error pages from
		// leaking into the API response.
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		snippet := strings.TrimSpace(string(body))
		if snippet != "" {
			return false, "provider returned " + http.StatusText(resp.StatusCode) + ": " + snippet
		}
		return false, "provider returned " + http.StatusText(resp.StatusCode)
	}
	return true, ""
}

// Install handles POST /api/v1/connectors/{connectorId}/install.
//
// Persists a workspace_mcp_servers row (with connector_id set to the
// manifest id for traceability, per migration v76), encrypts each
// PAT-style field value into the credentials table, and returns
// integration_id + the appropriate NextStep so the frontend can
// drive any remaining OAuth dance.
//
// Auth-mode shapes:
//
//	pat / conn_string / none → write workspace_mcp_servers + per-field
//	    credentials rows; NextStep=""
//	mcp_oauth                → write workspace_mcp_servers (no creds);
//	    NextStep="mcp_oauth"
//	byo_oauth                → write workspace_mcp_servers + credentials
//	    rows for client_id/client_secret; build OAuthURL from
//	    manifest.OAuth.AuthorizationURL with client_id, scopes, state;
//	    NextStep="oauth"
//
// Auth: requires ?workspace_id=X and MANAGER+ role.
func (h *ConnectorHandler) Install(w http.ResponseWriter, r *http.Request) {
	user := UserFromContext(r.Context())
	if user == nil {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "Unauthorized"})
		return
	}
	workspaceID := r.URL.Query().Get("workspace_id")
	if workspaceID == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "workspace_id required"})
		return
	}
	role := RoleFromContext(r.Context())
	if !canRole(role, "create") {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "Forbidden: MANAGER+ required"})
		return
	}

	id := r.PathValue("connectorId")
	m, err := h.catalog.LoadByID(id)
	if err != nil {
		if errors.Is(err, connectors.ErrConnectorNotFound) {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "connector not found"})
			return
		}
		h.logger.Error("connector lookup", "error", err, "id", id)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "internal error"})
		return
	}

	var req InstallRequest
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid body"})
		return
	}

	// Resolve MCP block against submitted fields so the persisted row
	// stores the materialized command/args/env, not the raw template.
	// Materialize also validates required-field coverage, so we lean
	// on it as the canonical "do we have everything we need" check
	// (instead of re-implementing it here).
	instanceURL := InstanceURLFromRequest(r, "")
	materialized, err := m.MaterializeMCP(req.Fields, instanceURL)
	if err != nil {
		if errors.Is(err, connectors.ErrManifestMissingFieldVal) {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
			return
		}
		h.logger.Error("materialize mcp", "error", err, "id", id)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "internal error"})
		return
	}

	displayName := strings.TrimSpace(req.Name)
	if displayName == "" {
		displayName = m.Name
	}

	now := time.Now().UTC().Format(time.RFC3339Nano)
	tx, err := h.db.BeginTx(r.Context(), nil)
	if err != nil {
		h.logger.Error("begin tx", "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "internal error"})
		return
	}
	defer tx.Rollback()

	integrationID := generateCUID()
	argsJSON, _ := json.Marshal(materialized.Args)
	envJSON, _ := json.Marshal(materialized.Env)

	// Ensure the workspace_id+name pair is unique by suffixing with
	// the integration id when the canonical slug is already taken.
	// "synth-pat" + a workspace's pre-existing "synth-pat" would
	// otherwise UNIQUE-conflict on retry/duplicate-install.
	rowName := m.ID
	var existing int
	if err := tx.QueryRowContext(r.Context(),
		`SELECT COUNT(*) FROM workspace_mcp_servers WHERE workspace_id = ? AND name = ? AND deleted_at IS NULL`,
		workspaceID, rowName).Scan(&existing); err == nil && existing > 0 {
		rowName = m.ID + "-" + integrationID[:8]
	}

	if _, err := tx.ExecContext(r.Context(), `
		INSERT INTO workspace_mcp_servers
			(id, workspace_id, name, display_name, transport, endpoint,
			 command, args_json, env_json, config_json, icon, enabled,
			 connector_id, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		integrationID, workspaceID, rowName, displayName,
		materialized.Transport, materialized.Endpoint, materialized.Command,
		string(argsJSON), string(envJSON), "{}", m.Brand.Logo, 1,
		m.ID, now, now,
	); err != nil {
		h.logger.Error("insert workspace_mcp_servers", "error", err, "id", id)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "internal error"})
		return
	}

	// Persist user-submitted secrets as credentials rows. Skip
	// mcp_oauth (no fields) and none (typically no fields either).
	// The credential names are namespaced with the integration id so
	// repeated installs of the same connector don't UNIQUE-conflict
	// on the (workspace_id, name) pair.
	if m.AuthMode == connectors.AuthModePAT ||
		m.AuthMode == connectors.AuthModeConnString ||
		m.AuthMode == connectors.AuthModeBYOOAuth {
		for i := range m.Fields {
			f := &m.Fields[i]
			value := strings.TrimSpace(req.Fields[f.Key])
			if value == "" {
				continue
			}
			enc, err := encryption.Encrypt(value)
			if err != nil {
				h.logger.Error("encrypt credential", "error", err, "field", f.Key)
				writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "internal error"})
				return
			}
			credName := m.ID + "-" + f.Key + "-" + integrationID[:8]
			credID := generateCUID()
			credType := "SECRET"
			if f.Type == connectors.FieldTypePassword {
				credType = "SECRET"
			}
			if _, err := tx.ExecContext(r.Context(), `
				INSERT INTO credentials
					(id, workspace_id, name, encrypted_value, type, provider, scope,
					 status, created_by, created_at, updated_at)
				VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
				credID, workspaceID, credName, enc,
				credType, "NONE", "WORKSPACE", "ACTIVE", user.ID, now, now,
			); err != nil {
				h.logger.Error("insert credential", "error", err, "field", f.Key)
				writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "internal error"})
				return
			}
		}
	}

	if err := tx.Commit(); err != nil {
		h.logger.Error("commit install", "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "internal error"})
		return
	}

	resp := InstallResponse{IntegrationID: integrationID}
	switch m.AuthMode {
	case connectors.AuthModeMCPOAuth:
		// The MCP server itself handles OAuth 2.1 + DCR — the frontend
		// hands off to the MCP-OAuth flow rather than a server-driven
		// redirect. We don't have an oauth_url to hand back here; the
		// MCP client discovers it from server metadata.
		resp.NextStep = "mcp_oauth"
	case connectors.AuthModeBYOOAuth:
		oauthURL, err := buildBYOOAuthURL(m, req.Fields, instanceURL)
		if err != nil {
			h.logger.Error("build oauth url", "error", err, "id", id)
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "internal error"})
			return
		}
		resp.NextStep = "oauth"
		resp.OAuthURL = oauthURL
	}

	writeJSON(w, http.StatusCreated, resp)
}

// buildBYOOAuthURL composes the authorization-code URL the frontend
// will open in a popup. The state token is generated here and would,
// in a future iteration, be persisted in oauth_states for verification
// at callback time — for now the parent flow trusts the random value
// embedded in the URL.
func buildBYOOAuthURL(m *connectors.Manifest, fields map[string]string, instanceURL string) (string, error) {
	if m.OAuth == nil || m.OAuth.AuthorizationURL == "" {
		return "", errors.New("manifest has no oauth block")
	}
	authURL, err := url.Parse(m.OAuth.AuthorizationURL)
	if err != nil {
		return "", err
	}
	q := authURL.Query()
	q.Set("response_type", "code")
	if cid := strings.TrimSpace(fields["client_id"]); cid != "" {
		q.Set("client_id", cid)
	}
	if len(m.OAuth.Scopes) > 0 {
		q.Set("scope", strings.Join(m.OAuth.Scopes, " "))
	}
	if instanceURL != "" {
		q.Set("redirect_uri", instanceURL+"/api/v1/connectors/oauth/callback")
	}
	state, _ := newRandomState()
	q.Set("state", state)
	authURL.RawQuery = q.Encode()
	return authURL.String(), nil
}

// newRandomState mints a 32-hex-char state token used to protect the
// OAuth authorization-code roundtrip against CSRF. Falls back to an
// empty string if crypto/rand is unavailable (extremely rare); the
// caller still emits the URL but without state, and the callback path
// will reject it on validation.
func newRandomState() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

// InstanceURLFromRequest derives the customer-facing base URL used to
// fill ${instance_url} in OAuth redirect_uri construction and
// setup_md docs.
//
// Resolution order, security-first:
//
//  1. publicBaseURL (operator-configured, e.g. CREWSHIP_PUBLIC_URL)
//     wins unconditionally. This is the only path that should run
//     in production deployments where OAuth callbacks matter.
//  2. Otherwise fall back to "https://" + r.Host. r.Host is bounded
//     by the listener's hostname so it can't be spoofed by a header.
//
// X-Forwarded-Host / X-Forwarded-Proto are intentionally NOT honored
// here — on a directly-exposed instance (no trusted reverse proxy in
// front) those headers are attacker-controlled, and feeding them
// into OAuth redirect_uri or user-visible setup docs would enable
// callback hijacking. Operators that DO have a trusted proxy must
// set publicBaseURL via config rather than rely on the helper to
// auto-detect.
//
// Returned without trailing slash so callers can append paths
// directly: `instance + "/oauth/callback"`. Returns "" when neither
// publicBaseURL nor r.Host is usable.
func InstanceURLFromRequest(r *http.Request, publicBaseURL string) string {
	if publicBaseURL != "" {
		return strings.TrimRight(publicBaseURL, "/")
	}
	if r == nil || r.Host == "" {
		return ""
	}
	return "https://" + r.Host
}
