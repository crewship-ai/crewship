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
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/crewship-ai/crewship/internal/connectors"
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

// notImplemented is the safe TDD-scaffold response: 501 with a small
// hint rather than a panic. Returned by all four handler entry points
// until the implementer fills in the bodies. Tests assert specific
// behavior (200/201/404/etc.) so they fail loudly while production
// requests get a controlled, non-crash response.
func notImplemented(w http.ResponseWriter, name string) {
	http.Error(w, "connectors: "+name+" not implemented", http.StatusNotImplemented)
}

// verifyHTTPClient handles the outbound HTTP request the Verify
// endpoint issues against a provider. Bounded timeout keeps a slow
// provider from holding a server worker indefinitely. Package-level
// so tests using httptest.NewServer don't need to override anything.
var verifyHTTPClient = &http.Client{Timeout: 10 * time.Second}

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
	url, err := m.Resolve(v.URL, rctx)
	if err != nil {
		return false, "verify URL resolution failed: " + err.Error()
	}

	method := v.Method
	if method == "" {
		method = http.MethodGet
	}
	req, err := http.NewRequestWithContext(ctx, method, url, nil)
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
// Persists a workspace_mcp_servers (or crew_mcp_servers) row plus a
// credentials row (where applicable), wires them via mcp_credentials,
// and returns the new integration_id. For OAuth flows, sets NextStep
// + OAuthURL so the frontend can drive the dance. The new row's
// connector_id column (migration v76) is set to manifest.ID so the
// installed instance is traceable back to its catalog source.
//
// instance_url placeholder source: derived from the request as
// `<scheme>://<host>` where scheme honours the X-Forwarded-Proto
// header when set (proxy deployments) and falls back to "https".
// Used for byo_oauth setup_md and OAuth redirect_uri construction.
//
// Auth: requires ?workspace_id=X and MANAGER+ role.
//
// TDD STUB — returns 501 until wired up.
func (h *ConnectorHandler) Install(w http.ResponseWriter, r *http.Request) {
	notImplemented(w, "Install")
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
