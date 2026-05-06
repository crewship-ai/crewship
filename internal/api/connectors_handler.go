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
	"database/sql"
	"log/slog"
	"net/http"

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

// List handles GET /api/v1/connectors.
//
// Response: 200 with []ConnectorListItem (stable order). Empty array
// if catalog is empty. No filtering parameters in v1 — frontend filters
// client-side.
func (h *ConnectorHandler) List(w http.ResponseWriter, r *http.Request) {
	panic("TDD STUB — implement me")
}

// Get handles GET /api/v1/connectors/{connectorId}.
//
// Response: 200 with the full Manifest. 404 if the catalog has no
// matching id. Returns the manifest verbatim so frontend can render
// the schema-driven form without a second round-trip.
func (h *ConnectorHandler) Get(w http.ResponseWriter, r *http.Request) {
	panic("TDD STUB — implement me")
}

// Verify handles POST /api/v1/connectors/{connectorId}/verify.
//
// For PAT manifests this typically resolves Verify.HTTP and makes one
// HTTP call. For ConnString it can attempt a TCP dial to host:port.
// For mcp_oauth and byo_oauth, Verify is a no-op (returns ok=true)
// since auth happens via redirect, not paste.
//
// Auth: requires ?workspace_id=X and MANAGER+ role.
func (h *ConnectorHandler) Verify(w http.ResponseWriter, r *http.Request) {
	panic("TDD STUB — implement me")
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
func (h *ConnectorHandler) Install(w http.ResponseWriter, r *http.Request) {
	panic("TDD STUB — implement me")
}

// InstanceURLFromRequest is a small helper extracted onto the package
// surface so the handler, tests, and any future routes share one
// definition of "what URL is this Crewship instance?". Production
// behavior:
//
//   - Scheme: X-Forwarded-Proto header → "https" → "http"
//   - Host:   X-Forwarded-Host header  → r.Host
//
// Returned without trailing slash so callers can append paths
// directly: `instance + "/oauth/callback"`.
//
// TDD STUB — body panics until implemented.
func InstanceURLFromRequest(r *http.Request) string {
	panic("TDD STUB — implement me")
}
