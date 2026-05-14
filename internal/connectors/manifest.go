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
	"fmt"
	"regexp"
	"strings"

	"gopkg.in/yaml.v3"
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

	// ErrManifestNotImplemented is the safe scaffold return value for
	// manifest entry points that haven't been implemented yet.
	// Returning a sentinel rather than panicking means an accidental
	// production call surfaces as a typed error in the caller's
	// errors.Is chain, not a process crash.
	ErrManifestNotImplemented = errors.New("connectors: manifest logic not implemented")
)

// IDPattern is the regex Validate() applies to Manifest.ID. Exposed so
// tests + future tooling (linters, registry sync) can reuse it.
//
// Rule: lowercase ASCII letter, then any number of lowercase letters,
// digits, underscores, or hyphens. No spaces, no uppercase, no leading
// digit. Mirrors npm-style package names without the scope.
const IDPattern = `^[a-z][a-z0-9_-]*$`

// idRegex compiles the package's IDPattern once. Used by Validate.
var idRegex = regexp.MustCompile(IDPattern)

// hexColorRegex matches a 6-digit hex like #5E6AD2. Used by Validate.
var hexColorRegex = regexp.MustCompile(`^#[0-9a-fA-F]{6}$`)

// placeholderRegex parses ${prefix.key} or ${instance_url}. Resolve
// walks raw indices instead of using FindAll because it needs to
// detect malformed (no-closing-brace) placeholders, which a Go regex
// can't model — but the regex is reused inside Resolve for the
// successful-match path.
var placeholderRegex = regexp.MustCompile(`^\$\{([a-zA-Z_][a-zA-Z0-9_]*)(?:\.([a-zA-Z_][a-zA-Z0-9_]*))?\}`)

// ParseManifest parses YAML bytes into a Manifest. Whitespace-only
// input is treated the same as truly empty input so a missing fixture
// (`fs.ReadFile` returning a blank stub on certain CI failures)
// doesn't silently masquerade as a valid empty manifest.
//
// Uses a strict YAML decoder (KnownFields(true)) so a misspelled key
// or a malformed value surfaces here rather than as a Validate
// failure further along the chain — the test
// TestParseManifest_InvalidYAML pins this contract.
//
// After unmarshal, empty slices / maps are normalized to nil. yaml.v3
// preserves an explicit `fields: []` in source as an empty (non-nil)
// slice, but json.Marshal + omitempty omits the key entirely, and a
// subsequent json.Unmarshal back into a Manifest decodes the missing
// key as nil — which breaks the JSON roundtrip equality test. Coalescing
// to nil at parse time keeps both shapes identical.
//
// Does NOT call Validate; callers chain Parse → Validate explicitly so
// they can decide whether to surface the parse error or the schema
// error to the user.
func ParseManifest(data []byte) (*Manifest, error) {
	if len(strings.TrimSpace(string(data))) == 0 {
		return nil, ErrManifestEmpty
	}
	dec := yaml.NewDecoder(strings.NewReader(string(data)))
	dec.KnownFields(true)
	var m Manifest
	if err := dec.Decode(&m); err != nil {
		return nil, fmt.Errorf("connectors: parse yaml: %w", err)
	}
	if len(m.Fields) == 0 {
		m.Fields = nil
	}
	if len(m.MCP.Args) == 0 {
		m.MCP.Args = nil
	}
	if len(m.MCP.Env) == 0 {
		m.MCP.Env = nil
	}
	if len(m.Derived) == 0 {
		m.Derived = nil
	}
	return &m, nil
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
//
func (m *Manifest) Validate() error {
	if m == nil {
		return ErrManifestEmpty
	}

	// --- Identity ---
	if m.ID == "" {
		return ErrManifestMissingID
	}
	if !idRegex.MatchString(m.ID) {
		return fmt.Errorf("%w: %q", ErrManifestInvalidID, m.ID)
	}
	if m.Name == "" {
		return ErrManifestMissingName
	}

	// --- Auth mode + brand ---
	switch m.AuthMode {
	case AuthModeMCPOAuth, AuthModePAT, AuthModeConnString, AuthModeBYOOAuth, AuthModeNone:
	default:
		return fmt.Errorf("%w: %q", ErrManifestUnknownAuthMode, m.AuthMode)
	}
	if m.Brand.Color != "" && !hexColorRegex.MatchString(m.Brand.Color) {
		return fmt.Errorf("%w: %q", ErrManifestInvalidColor, m.Brand.Color)
	}

	// --- MCP transport / field-shape consistency ---
	if !IsValidTransport(m.MCP.Transport) {
		return fmt.Errorf("%w: %q", ErrManifestInvalidTransport, m.MCP.Transport)
	}
	switch m.MCP.Transport {
	case "stdio":
		if m.MCP.Command == "" {
			return fmt.Errorf("%w: stdio transport requires mcp.command", ErrManifestMissingField)
		}
		if m.MCP.Endpoint != "" {
			return fmt.Errorf("%w: stdio transport must not set mcp.endpoint", ErrManifestTransportFieldMismatch)
		}
	case "streamable-http":
		if m.MCP.Endpoint == "" {
			return fmt.Errorf("%w: streamable-http transport requires mcp.endpoint", ErrManifestMissingField)
		}
		if m.MCP.Command != "" {
			return fmt.Errorf("%w: streamable-http transport must not set mcp.command", ErrManifestTransportFieldMismatch)
		}
	}

	// --- Fields: uniqueness, valid Type, Select-implies-choices ---
	seenKeys := map[string]struct{}{}
	for i := range m.Fields {
		f := &m.Fields[i]
		if _, dup := seenKeys[f.Key]; dup {
			return fmt.Errorf("%w: %q", ErrManifestDuplicateField, f.Key)
		}
		seenKeys[f.Key] = struct{}{}
		if f.Type == "" {
			return fmt.Errorf("%w: field %q", ErrManifestMissingType, f.Key)
		}
		switch f.Type {
		case FieldTypeText, FieldTypePassword, FieldTypeNumber, FieldTypeSelect, FieldTypeBool:
		default:
			return fmt.Errorf("%w: field %q type %q", ErrManifestUnknownFieldType, f.Key, f.Type)
		}
		if f.Type == FieldTypeSelect && len(f.Choices) == 0 {
			return fmt.Errorf("%w: field %q (type=select)", ErrManifestEmptyChoices, f.Key)
		}
	}

	// --- Verify: exactly one of HTTP / MCPMethod ---
	if m.Verify != nil && m.Verify.HTTP != nil && m.Verify.MCPMethod != "" {
		return ErrManifestVerifyAmbiguous
	}

	// --- Auth-mode-specific shape requirements ---
	switch m.AuthMode {
	case AuthModeMCPOAuth:
		if m.MCP.Transport != "streamable-http" {
			return fmt.Errorf("%w: mcp_oauth requires streamable-http", ErrManifestInvalidTransport)
		}
		if m.MCP.Endpoint == "" {
			return fmt.Errorf("%w: mcp_oauth requires mcp.endpoint", ErrManifestMissingField)
		}
	case AuthModePAT:
		hasPassword := false
		for i := range m.Fields {
			if m.Fields[i].Type == FieldTypePassword {
				hasPassword = true
				break
			}
		}
		if !hasPassword {
			return fmt.Errorf("%w: pat requires at least one password field", ErrManifestMissingField)
		}
	case AuthModeConnString:
		if len(m.Fields) == 0 {
			return fmt.Errorf("%w: conn_string requires at least one field", ErrManifestMissingField)
		}
	case AuthModeBYOOAuth:
		if m.OAuth == nil || m.OAuth.AuthorizationURL == "" || m.OAuth.TokenURL == "" {
			return ErrManifestMissingOAuth
		}
		if m.fieldByKey("client_id") == nil || m.fieldByKey("client_secret") == nil {
			return fmt.Errorf("%w: byo_oauth requires client_id and client_secret fields", ErrManifestMissingField)
		}
	}

	// --- Derived map cycle detection ---
	if err := checkDerivedCycles(m.Derived); err != nil {
		return err
	}

	return nil
}

// checkDerivedCycles runs DFS over the derived map, treating each
// `${derived.X}` reference as an edge. Self-references and longer
// cycles both surface as ErrManifestCyclicDerived. Fields and
// instance_url placeholders are leaves and ignored — the cycle
// check is strictly inside the derived namespace.
func checkDerivedCycles(derived map[string]string) error {
	if len(derived) == 0 {
		return nil
	}
	// Build adjacency list: derived key → derived keys it references.
	deps := make(map[string][]string, len(derived))
	for k, tmpl := range derived {
		for _, ref := range extractDerivedRefs(tmpl) {
			deps[k] = append(deps[k], ref)
		}
	}

	// 3-color DFS (white=0, gray=1, black=2) for cycle detection.
	color := make(map[string]int, len(derived))
	var visit func(k string) error
	visit = func(k string) error {
		if color[k] == 2 {
			return nil
		}
		if color[k] == 1 {
			return fmt.Errorf("%w: at %q", ErrManifestCyclicDerived, k)
		}
		color[k] = 1
		for _, dep := range deps[k] {
			if _, ok := derived[dep]; !ok {
				// References an undeclared derived key — that's a
				// different problem (Resolve will catch it at
				// materialize time); for cycle detection we just skip.
				continue
			}
			if err := visit(dep); err != nil {
				return err
			}
		}
		color[k] = 2
		return nil
	}
	for k := range derived {
		if err := visit(k); err != nil {
			return err
		}
	}
	return nil
}

// extractDerivedRefs returns every `${derived.X}` key referenced in s,
// in source order. Malformed placeholders are skipped silently —
// Resolve rejects them at materialize time so the validate path
// doesn't double-error on shape problems.
func extractDerivedRefs(s string) []string {
	var out []string
	for i := 0; i < len(s); i++ {
		if s[i] != '$' || i+1 >= len(s) || s[i+1] != '{' {
			continue
		}
		end := strings.IndexByte(s[i:], '}')
		if end < 0 {
			return out
		}
		inner := s[i+2 : i+end]
		if strings.HasPrefix(inner, "derived.") {
			out = append(out, strings.TrimPrefix(inner, "derived."))
		}
		i += end
	}
	return out
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
//
func (m *Manifest) Resolve(s string, ctx ResolveContext) (string, error) {
	return m.resolveWith(s, ctx, nil)
}

// resolveWith is the recursive engine behind Resolve. `visiting`
// tracks the derived keys currently on the resolution stack so a
// cyclic `${derived.a}` → `${derived.b}` → `${derived.a}` template
// surfaces ErrManifestPlaceholder instead of stack-overflowing. The
// public Resolve passes nil; recursive calls own the map.
func (m *Manifest) resolveWith(s string, ctx ResolveContext, visiting map[string]struct{}) (string, error) {
	if s == "" {
		return "", nil
	}
	var b strings.Builder
	for i := 0; i < len(s); {
		// Fast path: not a placeholder start, copy literal.
		if s[i] != '$' || i+1 >= len(s) || s[i+1] != '{' {
			b.WriteByte(s[i])
			i++
			continue
		}
		// Confirmed `${...` — the closing brace must exist or it's an
		// authoring bug we surface rather than passing literal
		// "${field.X" through to the MCP server.
		end := strings.IndexByte(s[i:], '}')
		if end < 0 {
			return "", fmt.Errorf("%w: unclosed placeholder at offset %d", ErrManifestPlaceholder, i)
		}
		full := s[i : i+end+1]
		match := placeholderRegex.FindStringSubmatch(full)
		if match == nil {
			return "", fmt.Errorf("%w: malformed %q", ErrManifestPlaceholder, full)
		}
		ns, key := match[1], match[2]
		switch {
		case ns == "instance_url" && key == "":
			b.WriteString(ctx.InstanceURL)
		case ns == "field" && key != "":
			if m.fieldByKey(key) == nil {
				return "", fmt.Errorf("%w: unknown field %q", ErrManifestPlaceholder, key)
			}
			b.WriteString(ctx.Fields[key])
		case ns == "derived" && key != "":
			val, err := m.resolveDerived(key, ctx, visiting)
			if err != nil {
				return "", err
			}
			b.WriteString(val)
		default:
			return "", fmt.Errorf("%w: unsupported namespace in %q", ErrManifestPlaceholder, full)
		}
		i += end + 1
	}
	return b.String(), nil
}

// resolveDerived looks up a derived key, preferring a pre-computed
// value in ctx.Derived (set by MaterializeMCP), and otherwise
// expanding the template from m.Derived recursively. The fallback
// path is what lets tests call Resolve("…${derived.X}…") directly
// without pre-populating ctx.Derived themselves.
func (m *Manifest) resolveDerived(key string, ctx ResolveContext, visiting map[string]struct{}) (string, error) {
	if v, ok := ctx.Derived[key]; ok {
		return v, nil
	}
	tmpl, ok := m.Derived[key]
	if !ok {
		return "", fmt.Errorf("%w: unknown derived %q", ErrManifestPlaceholder, key)
	}
	if _, cycle := visiting[key]; cycle {
		return "", fmt.Errorf("%w: cyclic derived reference at %q", ErrManifestPlaceholder, key)
	}
	if visiting == nil {
		visiting = map[string]struct{}{}
	}
	visiting[key] = struct{}{}
	defer delete(visiting, key)
	return m.resolveWith(tmpl, ctx, visiting)
}

// MaterializeMCP returns a copy of MCP with all placeholders resolved
// against fields. It also computes Derived values from the manifest's
// `derived` map (transitively, so derived can reference field.X).
//
// Returns ErrManifestMissingFieldVal if a required field has no value
// in the input map.
//
func (m *Manifest) MaterializeMCP(fields map[string]string, instanceURL string) (*MCPConfig, error) {
	// 1. Resolve effective field values: caller input + manifest
	//    defaults. Required fields with no value (and no default) are
	//    rejected here so the MCP server never starts with a missing
	//    secret silently substituted with the empty string.
	effective := make(map[string]string, len(m.Fields))
	for i := range m.Fields {
		f := &m.Fields[i]
		if v, ok := fields[f.Key]; ok && v != "" {
			effective[f.Key] = v
		} else if f.Default != "" {
			effective[f.Key] = f.Default
		} else if f.Required {
			return nil, fmt.Errorf("%w: %q", ErrManifestMissingFieldVal, f.Key)
		} else {
			effective[f.Key] = ""
		}
	}

	// 2. Resolve derived values in dependency order. Validate
	//    already rejected cycles, so a bounded pass count (= number of
	//    derived keys) is enough to converge on any acyclic chain.
	derived := make(map[string]string, len(m.Derived))
	ctxBase := ResolveContext{Fields: effective, InstanceURL: instanceURL, Derived: derived}
	remaining := make(map[string]string, len(m.Derived))
	for k, v := range m.Derived {
		remaining[k] = v
	}
	for pass := 0; pass < len(m.Derived)+1 && len(remaining) > 0; pass++ {
		progress := false
		for k, tmpl := range remaining {
			out, err := m.Resolve(tmpl, ctxBase)
			if err != nil {
				// Couldn't resolve yet — likely depends on a later
				// derived key. Skip this pass; cycle would have been
				// caught in Validate.
				if errors.Is(err, ErrManifestPlaceholder) {
					continue
				}
				return nil, err
			}
			derived[k] = out
			delete(remaining, k)
			progress = true
		}
		if !progress {
			break
		}
	}
	if len(remaining) > 0 {
		// Something in derived references an unknown key. Surface the
		// first failure with its real error message.
		for k, tmpl := range remaining {
			if _, err := m.Resolve(tmpl, ctxBase); err != nil {
				return nil, fmt.Errorf("derived %q: %w", k, err)
			}
		}
	}

	// 3. Resolve MCP block strings against the full context.
	out := &MCPConfig{Transport: m.MCP.Transport}
	rctx := ResolveContext{Fields: effective, Derived: derived, InstanceURL: instanceURL}

	resolved, err := m.Resolve(m.MCP.Command, rctx)
	if err != nil {
		return nil, err
	}
	out.Command = resolved

	resolved, err = m.Resolve(m.MCP.Endpoint, rctx)
	if err != nil {
		return nil, err
	}
	out.Endpoint = resolved

	if len(m.MCP.Args) > 0 {
		out.Args = make([]string, len(m.MCP.Args))
		for i, a := range m.MCP.Args {
			resolved, err = m.Resolve(a, rctx)
			if err != nil {
				return nil, err
			}
			out.Args[i] = resolved
		}
	}

	if len(m.MCP.Env) > 0 {
		out.Env = make(map[string]string, len(m.MCP.Env))
		for k, v := range m.MCP.Env {
			resolved, err = m.Resolve(v, rctx)
			if err != nil {
				return nil, err
			}
			out.Env[k] = resolved
		}
	}

	return out, nil
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
