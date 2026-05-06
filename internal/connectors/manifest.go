// Package connectors models the curated catalog of integrations that
// Crewship can install for a workspace. A "connector" is a manifest —
// not a running thing — that describes (a) how to authenticate against
// a third-party service, and (b) how to spawn the matching MCP server
// once the credentials are in hand.
//
// Manifests are authored as YAML, embedded at build time, and parsed
// into the typed values below. Frontend forms are rendered directly
// from a manifest; backend handlers dispatch on AuthMode.
//
// The four auth modes (mcp_oauth / pat / conn_string / byo_oauth) and
// the placeholder grammar (${field.X}, ${derived.Y}, ${instance_url})
// are the spec the rest of the code is built against. Tests in
// manifest_test.go are the authoritative reference for behavior.
//
// TDD STUB — types are stable, function bodies are not implemented.
// Do not call Parse/Validate/Resolve/MaterializeMCP from production
// code paths until they are wired up.
package connectors

import (
	"errors"
)

// AuthMode is the authentication strategy a connector advertises.
//
// The four modes are mutually exclusive and each maps to a distinct
// handler at runtime. AuthModeNone is reserved for connectors that
// require no credential (e.g. a public read-only MCP server) — it is
// rare in the curated catalog but kept legal so the long tail can
// describe itself.
type AuthMode string

const (
	// AuthModeMCPOAuth — the upstream MCP server implements OAuth 2.1
	// with Dynamic Client Registration (RFC 7591). No customer setup
	// is required: the handler discovers the server's metadata, runs
	// DCR to mint a client, and walks the user through the consent
	// flow. This is the best UX (Linear, Anthropic Connectors).
	AuthModeMCPOAuth AuthMode = "mcp_oauth"

	// AuthModePAT — the user pastes a token (API key, PAT,
	// setup-token). The token is stored encrypted via credstore and
	// injected into the MCP server via env vars at spawn time.
	AuthModePAT AuthMode = "pat"

	// AuthModeConnString — the user fills a structured form (host,
	// port, db, user, password, ssl). The handler combines them into
	// a connection string via the manifest's `derived` map and
	// passes it to the MCP server.
	AuthModeConnString AuthMode = "conn_string"

	// AuthModeBYOOAuth — provider does not support DCR and Crewship
	// does not host an OAuth broker. The customer registers an OAuth
	// app at the provider's developer console and pastes its
	// client_id + client_secret. The instance handles the OAuth
	// dance locally; redirect_uri is `${instance_url}/oauth/callback`.
	AuthModeBYOOAuth AuthMode = "byo_oauth"

	// AuthModeNone — connector requires no credential.
	AuthModeNone AuthMode = "none"
)

// FieldType is the form-input type a Field renders as on the frontend.
type FieldType string

const (
	FieldTypeText     FieldType = "text"
	FieldTypePassword FieldType = "password"
	FieldTypeNumber   FieldType = "number"
	FieldTypeSelect   FieldType = "select"
	FieldTypeBool     FieldType = "bool"
)

// Field is one input rendered in the connect form.
//
// Required + Default + Choices are honored both client-side (rendered
// in the form) and server-side (validated on submit). Help is markdown
// — short, one-line preferred — shown beneath the input.
//
// Both yaml AND json tags are present so the same struct can be (a)
// parsed from a fixture file and (b) serialized to the frontend
// without an intermediate DTO.
type Field struct {
	Key         string    `yaml:"key" json:"key"`
	Label       string    `yaml:"label" json:"label"`
	Type        FieldType `yaml:"type" json:"type"`
	Required    bool      `yaml:"required" json:"required"`
	Default     string    `yaml:"default,omitempty" json:"default,omitempty"`
	Placeholder string    `yaml:"placeholder,omitempty" json:"placeholder,omitempty"`
	Help        string    `yaml:"help,omitempty" json:"help,omitempty"`
	Choices     []string  `yaml:"choices,omitempty" json:"choices,omitempty"`
}

// Brand carries the visual identity used in tile + sheet headers.
type Brand struct {
	Logo  string `yaml:"logo" json:"logo"`   // key into components/icons/mcp-logos
	Color string `yaml:"color" json:"color"` // hex like "#5E6AD2"
}

// OAuthConfig is filled only when AuthMode == byo_oauth.
type OAuthConfig struct {
	AuthorizationURL string   `yaml:"authorization_url" json:"authorization_url"`
	TokenURL         string   `yaml:"token_url" json:"token_url"`
	Scopes           []string `yaml:"scopes" json:"scopes"`
	PKCE             bool     `yaml:"pkce" json:"pkce"`
}

// MCPConfig describes how to launch the MCP server once credentials
// are known. Strings may contain ${field.X} / ${derived.Y} /
// ${instance_url} placeholders; they are resolved at materialize-time,
// not parse-time.
type MCPConfig struct {
	Transport string            `yaml:"transport" json:"transport"`                   // stdio | streamable-http
	Command   string            `yaml:"command,omitempty" json:"command,omitempty"`   // stdio
	Args      []string          `yaml:"args,omitempty" json:"args,omitempty"`         // stdio
	Endpoint  string            `yaml:"endpoint,omitempty" json:"endpoint,omitempty"` // streamable-http
	Env       map[string]string `yaml:"env,omitempty" json:"env,omitempty"`
}

// VerifyHTTP is an optional post-connect check: hit this URL with the
// resolved headers, expect ExpectStatus. Used to surface "your token
// is invalid" before the agent runs.
type VerifyHTTP struct {
	Method       string            `yaml:"method" json:"method"`
	URL          string            `yaml:"url" json:"url"`
	Headers      map[string]string `yaml:"headers,omitempty" json:"headers,omitempty"`
	ExpectStatus int               `yaml:"expect_status" json:"expect_status"`
}

// Verify is exactly one of HTTP or MCPMethod (mutually exclusive).
// Manifests that omit Verify skip the post-connect check.
type Verify struct {
	HTTP      *VerifyHTTP `yaml:"http,omitempty" json:"http,omitempty"`
	MCPMethod string      `yaml:"mcp_method,omitempty" json:"mcp_method,omitempty"` // e.g. "tools/list"
}

// Docs is markdown-only documentation rendered in the connect sheet.
type Docs struct {
	SetupMD string `yaml:"setup_md" json:"setup_md"`
}

// Manifest is the top-level connector definition.
//
// One manifest = one tile in the catalog. The combination of
// AuthMode + Fields + MCP + (optional OAuth/Verify/Docs) is enough
// for the frontend to render a complete connect flow without any
// per-connector React code.
type Manifest struct {
	ID          string            `yaml:"id" json:"id"`
	Name        string            `yaml:"name" json:"name"`
	Description string            `yaml:"description" json:"description"`
	Brand       Brand             `yaml:"brand" json:"brand"`
	Category    string            `yaml:"category" json:"category"`
	AuthMode    AuthMode          `yaml:"auth_mode" json:"auth_mode"`
	Fields      []Field           `yaml:"fields,omitempty" json:"fields,omitempty"`
	OAuth       *OAuthConfig      `yaml:"oauth,omitempty" json:"oauth,omitempty"`
	MCP         MCPConfig         `yaml:"mcp" json:"mcp"`
	Derived     map[string]string `yaml:"derived,omitempty" json:"derived,omitempty"`
	Verify      *Verify           `yaml:"verify,omitempty" json:"verify,omitempty"`
	Docs        *Docs             `yaml:"docs,omitempty" json:"docs,omitempty"`
}

// Common errors returned from manifest operations. Tests assert
// against these sentinel values (errors.Is) rather than message text.
var (
	ErrManifestEmpty                  = errors.New("connectors: manifest is empty")
	ErrManifestMissingID              = errors.New("connectors: manifest missing id")
	ErrManifestInvalidID              = errors.New("connectors: manifest id must match [a-z][a-z0-9_-]*")
	ErrManifestMissingName            = errors.New("connectors: manifest missing name")
	ErrManifestUnknownAuthMode        = errors.New("connectors: unknown auth_mode")
	ErrManifestMissingField           = errors.New("connectors: manifest missing required field")
	ErrManifestInvalidTransport       = errors.New("connectors: invalid transport")
	ErrManifestMissingOAuth           = errors.New("connectors: byo_oauth manifest must include oauth block")
	ErrManifestPlaceholder            = errors.New("connectors: unresolvable placeholder")
	ErrManifestMissingFieldVal        = errors.New("connectors: required field has no value")
	ErrManifestDuplicateField         = errors.New("connectors: field key declared more than once")
	ErrManifestInvalidColor           = errors.New("connectors: brand.color must be a 6-digit hex like #5E6AD2")
	ErrManifestEmptyChoices           = errors.New("connectors: select field requires at least one choice")
	ErrManifestMissingType            = errors.New("connectors: field missing type")
	ErrManifestUnknownFieldType       = errors.New("connectors: unknown field type")
	ErrManifestVerifyAmbiguous        = errors.New("connectors: verify block must declare exactly one of http or mcp_method, not both")
	ErrManifestTransportFieldMismatch = errors.New("connectors: mcp block has fields incompatible with the declared transport")
	ErrManifestCyclicDerived          = errors.New("connectors: derived map has a cycle")
)

// IDPattern is the regex Validate() applies to Manifest.ID. Exposed so
// tests + future tooling (linters, registry sync) can reuse it.
//
// Rule: lowercase ASCII letter, then any number of lowercase letters,
// digits, underscores, or hyphens. No spaces, no uppercase, no leading
// digit. Mirrors npm-style package names without the scope.
const IDPattern = `^[a-z][a-z0-9_-]*$`

// ParseManifest parses YAML bytes into a Manifest.
//
// Returns ErrManifestEmpty if data is empty. Does NOT call Validate;
// callers should chain Parse → Validate explicitly so they can decide
// whether to surface the parse error or the schema error to the user.
func ParseManifest(data []byte) (*Manifest, error) {
	panic("TDD STUB — implement me")
}

// Validate enforces invariants that depend on AuthMode:
//
//   - AuthModeMCPOAuth: MCP.Transport == streamable-http, MCP.Endpoint set
//   - AuthModePAT: at least one Field with Type=password
//   - AuthModeConnString: at least one Field of any type, MCP set
//   - AuthModeBYOOAuth: OAuth block set with AuthorizationURL+TokenURL,
//     plus client_id and client_secret Fields
//   - AuthModeNone: no Fields required (typically a public/demo MCP server)
//
// All modes also require:
//
//   - ID matching IDPattern (slug-like, lowercase, no spaces)
//   - Non-empty Name
//   - Recognized AuthMode (one of the const block above)
//   - MCP.Transport ∈ {stdio, streamable-http}
//   - Brand.Color a valid 6-digit hex like #5E6AD2 (or empty)
//   - Each Field.Key declared exactly once
//   - Each Field.Type recognized; Type=select implies non-empty Choices
//   - Verify (if present) declares exactly one of HTTP / MCPMethod
//   - MCP transport matches the populated MCP fields:
//   - stdio           → MCP.Command set, MCP.Endpoint empty
//   - streamable-http → MCP.Endpoint set, MCP.Command empty
//   - Derived map is acyclic (no key references itself transitively)
func (m *Manifest) Validate() error {
	panic("TDD STUB — implement me")
}

// ResolveContext carries the values that placeholders are resolved
// against. Fields holds user-submitted form values; Derived holds
// values computed from the manifest's `derived` map (resolved
// transitively); InstanceURL is the customer's Crewship base URL,
// used only for ${instance_url} (e.g. OAuth redirect_uri).
type ResolveContext struct {
	Fields      map[string]string
	Derived     map[string]string
	InstanceURL string
}

// Resolve substitutes ${field.X}, ${derived.Y}, and ${instance_url}
// placeholders in the given string. Unknown keys produce
// ErrManifestPlaceholder so the caller can surface a clear error
// before the MCP server tries to start with a literal "${field.host}"
// in its argv.
func (m *Manifest) Resolve(s string, ctx ResolveContext) (string, error) {
	panic("TDD STUB — implement me")
}

// MaterializeMCP returns a copy of MCP with all placeholders resolved
// against fields. It also computes Derived values from the manifest's
// `derived` map (transitively, so derived can reference field.X).
//
// Returns ErrManifestMissingFieldVal if a required field has no value
// in the input map.
func (m *Manifest) MaterializeMCP(fields map[string]string, instanceURL string) (*MCPConfig, error) {
	panic("TDD STUB — implement me")
}

// fieldByKey is a small helper used by validators and resolvers.
//
// Defined here (rather than in tests) so future implementers have a
// natural place to extend lookup (e.g. case-insensitive keys).
//
//nolint:unused // called by Validate/MaterializeMCP once impl lands
func (m *Manifest) fieldByKey(key string) *Field {
	for i := range m.Fields {
		if m.Fields[i].Key == key {
			return &m.Fields[i]
		}
	}
	return nil
}

// IsValidTransport reports whether t is one of the two transports
// the manifest schema allows. Exported because Validate() and the API
// layer both need it; package-internal helpers shouldn't duplicate.
func IsValidTransport(t string) bool {
	return t == "stdio" || t == "streamable-http"
}
