// Tests for the connector manifest schema (manifest.go).
//
// These tests are the executable spec for ParseManifest, Validate,
// Resolve, and MaterializeMCP. They are intentionally thorough — the
// manifest layer is the contract every other piece (handlers, API,
// frontend, e2e) depends on, so a bug here cascades.
//
// Tests run against the six canonical fixtures in fixtures/*.yaml
// (linear=mcp_oauth, github=pat, slack=pat with bot token,
// postgres=conn_string, everything=none demo, filesystem=none) plus
// inline YAML for edge cases that don't belong in shipped fixtures
// (byo_oauth shape, ID format violations, etc.).
package connectors_test

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/connectors"
)

// -------------------------------------------------------------------
// Helpers
// -------------------------------------------------------------------

func loadFixture(t *testing.T, name string) []byte {
	t.Helper()
	path := filepath.Join("fixtures", name)
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read fixture %s: %v", name, err)
	}
	return data
}

func mustParse(t *testing.T, data []byte) *connectors.Manifest {
	t.Helper()
	m, err := connectors.ParseManifest(data)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	return m
}

// shippedFixtures is the source of truth for what ships in fixtures/.
// Tests that walk all fixtures iterate this slice; if you add or
// rename a fixture, update here too.
var shippedFixtures = []string{
	"linear.yaml",
	"github.yaml",
	"slack.yaml",
	"postgres.yaml",
	"everything.yaml",
	"filesystem.yaml",
}

// -------------------------------------------------------------------
// ParseManifest — happy paths (one per fixture)
// -------------------------------------------------------------------

func TestParseManifest_LinearFixture_MCPOAuth(t *testing.T) {
	m := mustParse(t, loadFixture(t, "linear.yaml"))

	if m.ID != "linear" {
		t.Errorf("id = %q, want linear", m.ID)
	}
	if m.AuthMode != connectors.AuthModeMCPOAuth {
		t.Errorf("auth_mode = %q, want mcp_oauth", m.AuthMode)
	}
	if m.MCP.Transport != "streamable-http" {
		t.Errorf("transport = %q, want streamable-http", m.MCP.Transport)
	}
	if m.MCP.Endpoint != "https://mcp.linear.app/mcp" {
		t.Errorf("endpoint = %q", m.MCP.Endpoint)
	}
	if len(m.Fields) != 0 {
		t.Errorf("mcp_oauth manifest must have no fields, got %d", len(m.Fields))
	}
	if m.Verify == nil || m.Verify.MCPMethod != "tools/list" {
		t.Errorf("expected verify.mcp_method=tools/list, got %+v", m.Verify)
	}
	if m.Brand.Color != "#5E6AD2" {
		t.Errorf("brand.color = %q", m.Brand.Color)
	}
}

func TestParseManifest_GitHubFixture_PAT(t *testing.T) {
	m := mustParse(t, loadFixture(t, "github.yaml"))

	if m.AuthMode != connectors.AuthModePAT {
		t.Errorf("auth_mode = %q, want pat", m.AuthMode)
	}
	if len(m.Fields) != 1 {
		t.Fatalf("expected 1 field, got %d", len(m.Fields))
	}
	f := m.Fields[0]
	if f.Key != "pat" || f.Type != connectors.FieldTypePassword || !f.Required {
		t.Errorf("field = %+v, want pat/password/required", f)
	}
	if got := m.MCP.Env["GITHUB_PERSONAL_ACCESS_TOKEN"]; got != "${field.pat}" {
		t.Errorf("env placeholder = %q", got)
	}
	if m.Verify == nil || m.Verify.HTTP == nil {
		t.Fatal("expected verify.http block")
	}
	if m.Verify.HTTP.ExpectStatus != 200 {
		t.Errorf("expect_status = %d, want 200", m.Verify.HTTP.ExpectStatus)
	}
	// Real MCP package; if this string changes upstream we want to
	// notice fast.
	if !sliceContains(m.MCP.Args, "@modelcontextprotocol/server-github") {
		t.Errorf("args missing real MCP package: %v", m.MCP.Args)
	}
}

func TestParseManifest_SlackFixture_PAT(t *testing.T) {
	m := mustParse(t, loadFixture(t, "slack.yaml"))

	if m.AuthMode != connectors.AuthModePAT {
		t.Errorf("auth_mode = %q, want pat", m.AuthMode)
	}
	// Bot token (required) + team id (optional).
	if len(m.Fields) != 2 {
		t.Fatalf("expected 2 fields, got %d", len(m.Fields))
	}
	keys := map[string]bool{}
	for _, f := range m.Fields {
		keys[f.Key] = true
	}
	if !keys["bot_token"] || !keys["team_id"] {
		t.Errorf("expected bot_token+team_id fields, got %+v", keys)
	}
	if !sliceContains(m.MCP.Args, "@modelcontextprotocol/server-slack") {
		t.Errorf("args missing real MCP package: %v", m.MCP.Args)
	}
	if m.MCP.Env["SLACK_BOT_TOKEN"] != "${field.bot_token}" {
		t.Errorf("SLACK_BOT_TOKEN env = %q", m.MCP.Env["SLACK_BOT_TOKEN"])
	}
}

func TestParseManifest_PostgresFixture_ConnString(t *testing.T) {
	m := mustParse(t, loadFixture(t, "postgres.yaml"))

	if m.AuthMode != connectors.AuthModeConnString {
		t.Errorf("auth_mode = %q, want conn_string", m.AuthMode)
	}
	if len(m.Fields) != 6 {
		t.Errorf("expected 6 fields, got %d", len(m.Fields))
	}

	// SSL field should be select with 3 choices and a default.
	var ssl *connectors.Field
	for i := range m.Fields {
		if m.Fields[i].Key == "ssl" {
			ssl = &m.Fields[i]
			break
		}
	}
	if ssl == nil {
		t.Fatal("ssl field not parsed")
	}
	if ssl.Type != connectors.FieldTypeSelect {
		t.Errorf("ssl.type = %q, want select", ssl.Type)
	}
	if ssl.Default != "require" {
		t.Errorf("ssl.default = %q, want require", ssl.Default)
	}
	if !reflect.DeepEqual(ssl.Choices, []string{"require", "prefer", "disable"}) {
		t.Errorf("ssl.choices = %v", ssl.Choices)
	}

	// Derived map carries the DSN template.
	if m.Derived["dsn"] == "" {
		t.Error("derived.dsn empty")
	}
	if !strings.Contains(m.Derived["dsn"], "${field.host}") {
		t.Errorf("derived.dsn missing host placeholder: %q", m.Derived["dsn"])
	}
}

func TestParseManifest_EverythingFixture_None(t *testing.T) {
	// "everything" is the official MCP demo server, no auth required.
	// Doubles as the smoke-test target for the catalog → install →
	// connect → list-tools pipeline.
	m := mustParse(t, loadFixture(t, "everything.yaml"))

	if m.AuthMode != connectors.AuthModeNone {
		t.Errorf("auth_mode = %q, want none", m.AuthMode)
	}
	if len(m.Fields) != 0 {
		t.Errorf("none-mode demo must have zero fields, got %d", len(m.Fields))
	}
	if !sliceContains(m.MCP.Args, "@modelcontextprotocol/server-everything") {
		t.Errorf("args missing real MCP demo package: %v", m.MCP.Args)
	}
}

func TestParseManifest_FilesystemFixture_None(t *testing.T) {
	m := mustParse(t, loadFixture(t, "filesystem.yaml"))

	if m.AuthMode != connectors.AuthModeNone {
		t.Errorf("auth_mode = %q, want none", m.AuthMode)
	}
	// Filesystem takes a non-secret config field (root_path) under
	// AuthModeNone — confirms "none" doesn't mean "no fields", just
	// "no credential".
	if len(m.Fields) != 1 || m.Fields[0].Key != "root_path" {
		t.Errorf("expected root_path field, got %+v", m.Fields)
	}
	if !sliceContains(m.MCP.Args, "${field.root_path}") {
		t.Errorf("root_path placeholder missing from args: %v", m.MCP.Args)
	}
}

// -------------------------------------------------------------------
// ParseManifest — error paths
// -------------------------------------------------------------------

func TestParseManifest_Empty(t *testing.T) {
	_, err := connectors.ParseManifest(nil)
	if !errors.Is(err, connectors.ErrManifestEmpty) {
		t.Errorf("err = %v, want ErrManifestEmpty", err)
	}
	_, err = connectors.ParseManifest([]byte(""))
	if !errors.Is(err, connectors.ErrManifestEmpty) {
		t.Errorf("err = %v, want ErrManifestEmpty (whitespace)", err)
	}
}

func TestParseManifest_InvalidYAML(t *testing.T) {
	_, err := connectors.ParseManifest([]byte("id: linear\nname:: ::badly"))
	if err == nil {
		t.Fatal("expected parse error on broken yaml")
	}
}

// -------------------------------------------------------------------
// Validate — happy paths (all shipped fixtures must validate)
// -------------------------------------------------------------------

func TestValidate_AllFixturesPass(t *testing.T) {
	for _, name := range shippedFixtures {
		t.Run(name, func(t *testing.T) {
			m := mustParse(t, loadFixture(t, name))
			if err := m.Validate(); err != nil {
				t.Errorf("%s: validate: %v", name, err)
			}
		})
	}
}

func TestValidate_AuthModeNone_ValidWithFields(t *testing.T) {
	// AuthModeNone allows fields (e.g. filesystem.root_path) — fields
	// being non-secret config don't graduate the manifest to a
	// credential mode.
	yml := `
id: nonefields
name: None With Fields
auth_mode: none
brand: {logo: x, color: "#000000"}
fields:
  - {key: path, label: Path, type: text, required: true}
mcp:
  transport: stdio
  command: foo
  args: ["${field.path}"]
`
	m := mustParse(t, []byte(yml))
	if err := m.Validate(); err != nil {
		t.Errorf("none mode with fields should validate, got: %v", err)
	}
}

func TestValidate_BYOOAuthSyntheticShape(t *testing.T) {
	// We don't ship a byo_oauth fixture (Slack moved to PAT). This
	// inline test pins the byo_oauth shape so a future implementer
	// adding e.g. Notion-byo can rely on the contract.
	yml := `
id: synth-byo
name: Synthetic BYO OAuth
auth_mode: byo_oauth
brand: {logo: x, color: "#000000"}
oauth:
  authorization_url: https://provider.example/authorize
  token_url: https://provider.example/token
  scopes: [read, write]
  pkce: true
fields:
  - {key: client_id, label: Client ID, type: text, required: true}
  - {key: client_secret, label: Client Secret, type: password, required: true}
mcp:
  transport: streamable-http
  endpoint: https://provider.example/mcp
docs:
  setup_md: "Use ${instance_url}/oauth/callback as redirect URL."
`
	m := mustParse(t, []byte(yml))
	if err := m.Validate(); err != nil {
		t.Errorf("synthetic byo_oauth should validate: %v", err)
	}
	if m.OAuth == nil || !m.OAuth.PKCE {
		t.Errorf("oauth.pkce did not round-trip: %+v", m.OAuth)
	}
}

// -------------------------------------------------------------------
// Validate — error paths (one per invariant)
// -------------------------------------------------------------------

func TestValidate_MissingID(t *testing.T) {
	yml := `
name: Linear
auth_mode: mcp_oauth
mcp:
  transport: streamable-http
  endpoint: https://x
`
	m := mustParse(t, []byte(yml))
	if err := m.Validate(); !errors.Is(err, connectors.ErrManifestMissingID) {
		t.Errorf("err = %v, want ErrManifestMissingID", err)
	}
}

func TestValidate_InvalidIDFormat(t *testing.T) {
	cases := []struct {
		name string
		id   string
	}{
		{"uppercase", "Linear"},
		{"with space", "my linear"},
		{"leading digit", "1linear"},
		{"dot", "linear.app"},
		{"slash", "vendor/linear"},
		{"empty after parse", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			yml := `
id: "` + tc.id + `"
name: X
auth_mode: none
brand: {logo: x, color: "#000000"}
mcp:
  transport: streamable-http
  endpoint: https://x
`
			m := mustParse(t, []byte(yml))
			err := m.Validate()
			// Empty id surfaces as MissingID; non-empty malformed surfaces as InvalidID.
			if tc.id == "" {
				if !errors.Is(err, connectors.ErrManifestMissingID) {
					t.Errorf("empty id: err = %v, want ErrManifestMissingID", err)
				}
				return
			}
			if !errors.Is(err, connectors.ErrManifestInvalidID) {
				t.Errorf("id=%q: err = %v, want ErrManifestInvalidID", tc.id, err)
			}
		})
	}
}

func TestValidate_MissingName(t *testing.T) {
	yml := `
id: linear
auth_mode: mcp_oauth
brand: {logo: x, color: "#000000"}
mcp:
  transport: streamable-http
  endpoint: https://x
`
	m := mustParse(t, []byte(yml))
	if err := m.Validate(); !errors.Is(err, connectors.ErrManifestMissingName) {
		t.Errorf("err = %v, want ErrManifestMissingName", err)
	}
}

func TestValidate_UnknownAuthMode(t *testing.T) {
	yml := `
id: x
name: X
auth_mode: magic_oauth
brand: {logo: x, color: "#000000"}
mcp:
  transport: stdio
  command: foo
`
	m := mustParse(t, []byte(yml))
	if err := m.Validate(); !errors.Is(err, connectors.ErrManifestUnknownAuthMode) {
		t.Errorf("err = %v, want ErrManifestUnknownAuthMode", err)
	}
}

func TestValidate_InvalidTransport(t *testing.T) {
	yml := `
id: x
name: X
auth_mode: pat
brand: {logo: x, color: "#000000"}
fields:
  - {key: token, label: Token, type: password, required: true}
mcp:
  transport: ssh
  command: foo
`
	m := mustParse(t, []byte(yml))
	if err := m.Validate(); !errors.Is(err, connectors.ErrManifestInvalidTransport) {
		t.Errorf("err = %v, want ErrManifestInvalidTransport", err)
	}
}

func TestValidate_PATRequiresPasswordField(t *testing.T) {
	// PAT manifest with no password field is malformed — we must store
	// the secret somewhere and Type=password is the user-visible
	// signal it's a secret.
	yml := `
id: x
name: X
auth_mode: pat
brand: {logo: x, color: "#000000"}
fields:
  - {key: name, label: Name, type: text, required: true}
mcp:
  transport: stdio
  command: foo
`
	m := mustParse(t, []byte(yml))
	if err := m.Validate(); !errors.Is(err, connectors.ErrManifestMissingField) {
		t.Errorf("err = %v, want ErrManifestMissingField", err)
	}
}

func TestValidate_BYOOAuthRequiresOAuthBlock(t *testing.T) {
	yml := `
id: x
name: X
auth_mode: byo_oauth
brand: {logo: x, color: "#000000"}
fields:
  - {key: client_id, label: Client ID, type: text, required: true}
  - {key: client_secret, label: Client Secret, type: password, required: true}
mcp:
  transport: streamable-http
  endpoint: https://x
`
	m := mustParse(t, []byte(yml))
	if err := m.Validate(); !errors.Is(err, connectors.ErrManifestMissingOAuth) {
		t.Errorf("err = %v, want ErrManifestMissingOAuth", err)
	}
}

func TestValidate_BYOOAuthRequiresClientIDAndSecret(t *testing.T) {
	yml := `
id: x
name: X
auth_mode: byo_oauth
brand: {logo: x, color: "#000000"}
oauth:
  authorization_url: https://x
  token_url: https://x/token
  scopes: [a]
fields:
  - {key: only_id, label: ID, type: text, required: true}
mcp:
  transport: streamable-http
  endpoint: https://x
`
	m := mustParse(t, []byte(yml))
	if err := m.Validate(); !errors.Is(err, connectors.ErrManifestMissingField) {
		t.Errorf("err = %v, want ErrManifestMissingField", err)
	}
}

func TestValidate_MCPOAuthRejectsStdio(t *testing.T) {
	// mcp_oauth presupposes a remote MCP server (DCR is over HTTP).
	// stdio has no metadata endpoint to discover, so it can't work.
	yml := `
id: x
name: X
auth_mode: mcp_oauth
brand: {logo: x, color: "#000000"}
mcp:
  transport: stdio
  command: foo
`
	m := mustParse(t, []byte(yml))
	if err := m.Validate(); !errors.Is(err, connectors.ErrManifestInvalidTransport) {
		t.Errorf("err = %v, want ErrManifestInvalidTransport", err)
	}
}

func TestValidate_MCPOAuthRequiresEndpoint(t *testing.T) {
	yml := `
id: x
name: X
auth_mode: mcp_oauth
brand: {logo: x, color: "#000000"}
mcp:
  transport: streamable-http
`
	m := mustParse(t, []byte(yml))
	if err := m.Validate(); !errors.Is(err, connectors.ErrManifestMissingField) {
		t.Errorf("err = %v, want ErrManifestMissingField", err)
	}
}

func TestValidate_DuplicateFieldKey(t *testing.T) {
	yml := `
id: x
name: X
auth_mode: pat
brand: {logo: x, color: "#000000"}
fields:
  - {key: dup, label: A, type: text, required: true}
  - {key: dup, label: B, type: password, required: true}
mcp:
  transport: stdio
  command: foo
`
	m := mustParse(t, []byte(yml))
	if err := m.Validate(); !errors.Is(err, connectors.ErrManifestDuplicateField) {
		t.Errorf("err = %v, want ErrManifestDuplicateField", err)
	}
}

func TestValidate_InvalidBrandColor(t *testing.T) {
	cases := []string{"red", "#ABC", "rgb(0,0,0)", "#GGGGGG", "#1234567"}
	for _, col := range cases {
		t.Run(col, func(t *testing.T) {
			yml := `
id: x
name: X
auth_mode: none
brand: {logo: x, color: "` + col + `"}
mcp:
  transport: streamable-http
  endpoint: https://x
`
			m := mustParse(t, []byte(yml))
			if err := m.Validate(); !errors.Is(err, connectors.ErrManifestInvalidColor) {
				t.Errorf("color=%q: err = %v, want ErrManifestInvalidColor", col, err)
			}
		})
	}
}

func TestValidate_BrandColorEmpty_OK(t *testing.T) {
	// Empty brand.color is allowed (default to neutral chrome on the
	// frontend). Only malformed non-empty values must fail.
	yml := `
id: x
name: X
auth_mode: none
brand: {logo: x, color: ""}
mcp:
  transport: streamable-http
  endpoint: https://x
`
	m := mustParse(t, []byte(yml))
	if err := m.Validate(); err != nil {
		t.Errorf("empty brand.color must validate: %v", err)
	}
}

func TestValidate_SelectFieldRequiresChoices(t *testing.T) {
	yml := `
id: x
name: X
auth_mode: conn_string
brand: {logo: x, color: "#000000"}
fields:
  - {key: mode, label: Mode, type: select, required: true}
mcp:
  transport: stdio
  command: foo
`
	m := mustParse(t, []byte(yml))
	if err := m.Validate(); !errors.Is(err, connectors.ErrManifestEmptyChoices) {
		t.Errorf("err = %v, want ErrManifestEmptyChoices", err)
	}
}

func TestValidate_FieldMissingType(t *testing.T) {
	yml := `
id: x
name: X
auth_mode: pat
brand: {logo: x, color: "#000000"}
fields:
  - {key: token, label: Token, required: true}
mcp:
  transport: stdio
  command: foo
`
	m := mustParse(t, []byte(yml))
	err := m.Validate()
	// Empty Type is "missing"; if the implementer chooses to default
	// to "text", make sure the contract surfaces that explicitly.
	if !errors.Is(err, connectors.ErrManifestMissingType) {
		t.Errorf("err = %v, want ErrManifestMissingType", err)
	}
}

func TestValidate_FieldUnknownType(t *testing.T) {
	yml := `
id: x
name: X
auth_mode: pat
brand: {logo: x, color: "#000000"}
fields:
  - {key: token, label: Token, type: hyperhash, required: true}
mcp:
  transport: stdio
  command: foo
`
	m := mustParse(t, []byte(yml))
	if err := m.Validate(); !errors.Is(err, connectors.ErrManifestUnknownFieldType) {
		t.Errorf("err = %v, want ErrManifestUnknownFieldType", err)
	}
}

// -------------------------------------------------------------------
// Resolve — placeholder substitution
// -------------------------------------------------------------------

func TestResolve_FieldPlaceholder(t *testing.T) {
	m := mustParse(t, loadFixture(t, "github.yaml"))
	got, err := m.Resolve("Bearer ${field.pat}", connectors.ResolveContext{
		Fields: map[string]string{"pat": "ghp_test"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if got != "Bearer ghp_test" {
		t.Errorf("got %q", got)
	}
}

func TestResolve_DerivedPlaceholder(t *testing.T) {
	m := mustParse(t, loadFixture(t, "postgres.yaml"))
	got, err := m.Resolve("connect ${derived.dsn}", connectors.ResolveContext{
		Fields: map[string]string{
			"host":     "db.example.com",
			"port":     "5432",
			"database": "app",
			"user":     "alice",
			"password": "secret",
			"ssl":      "require",
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	wantDSN := "connect postgres://alice:secret@db.example.com:5432/app?sslmode=require"
	if got != wantDSN {
		t.Errorf("got %q, want %q", got, wantDSN)
	}
}

func TestResolve_InstanceURLPlaceholder(t *testing.T) {
	// The shipped Slack fixture is now PAT (no ${instance_url}); this
	// test uses an inline byo_oauth manifest where the placeholder
	// genuinely matters (redirect URI in setup_md).
	yml := `
id: synth-byo
name: Synth
auth_mode: byo_oauth
brand: {logo: x, color: "#000000"}
oauth:
  authorization_url: https://provider.example/authorize
  token_url: https://provider.example/token
  scopes: [a]
fields:
  - {key: client_id, label: ID, type: text, required: true}
  - {key: client_secret, label: Secret, type: password, required: true}
mcp:
  transport: streamable-http
  endpoint: https://provider.example/mcp
docs:
  setup_md: "${instance_url}/oauth/callback"
`
	m := mustParse(t, []byte(yml))
	got, err := m.Resolve("redirect_uri=${instance_url}/oauth/callback", connectors.ResolveContext{
		InstanceURL: "https://acme.example.com:8080",
	})
	if err != nil {
		t.Fatal(err)
	}
	want := "redirect_uri=https://acme.example.com:8080/oauth/callback"
	if got != want {
		t.Errorf("got %q", got)
	}
}

func TestResolve_MultiplePlaceholdersInOneString(t *testing.T) {
	// The Postgres derived.dsn template is the canonical test of
	// stacking many placeholders in one string. Resolve directly,
	// not via MaterializeMCP, so we know who's failing if it breaks.
	m := mustParse(t, loadFixture(t, "postgres.yaml"))
	tmpl := "${field.user}@${field.host}:${field.port}/${field.database}"
	got, err := m.Resolve(tmpl, connectors.ResolveContext{
		Fields: map[string]string{
			"user":     "alice",
			"host":     "db.example.com",
			"port":     "5432",
			"database": "app",
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	want := "alice@db.example.com:5432/app"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestResolve_EmptyString(t *testing.T) {
	m := mustParse(t, loadFixture(t, "linear.yaml"))
	got, err := m.Resolve("", connectors.ResolveContext{})
	if err != nil {
		t.Fatalf("empty string must not error: %v", err)
	}
	if got != "" {
		t.Errorf("got %q, want empty", got)
	}
}

func TestResolve_UnknownFieldKey(t *testing.T) {
	m := mustParse(t, loadFixture(t, "github.yaml"))
	_, err := m.Resolve("${field.does_not_exist}", connectors.ResolveContext{
		Fields: map[string]string{"pat": "x"},
	})
	if !errors.Is(err, connectors.ErrManifestPlaceholder) {
		t.Errorf("err = %v, want ErrManifestPlaceholder", err)
	}
}

func TestResolve_LiteralWithoutPlaceholder(t *testing.T) {
	m := mustParse(t, loadFixture(t, "linear.yaml"))
	got, err := m.Resolve("plain string with $dollar but no braces", connectors.ResolveContext{})
	if err != nil {
		t.Fatal(err)
	}
	if got != "plain string with $dollar but no braces" {
		t.Errorf("literal mangled: %q", got)
	}
}

func TestResolve_RejectsLiteralDollarBrace(t *testing.T) {
	// If a manifest accidentally has a malformed placeholder like
	// "${field.api_key" (no closing brace), Resolve must fail rather
	// than silently passing the literal through to the MCP server.
	m := mustParse(t, loadFixture(t, "github.yaml"))
	_, err := m.Resolve("${field.pat", connectors.ResolveContext{
		Fields: map[string]string{"pat": "x"},
	})
	if err == nil {
		t.Error("expected error for malformed placeholder")
	}
}

// -------------------------------------------------------------------
// MaterializeMCP — the integration of Resolve over MCP block
// -------------------------------------------------------------------

func TestMaterializeMCP_PostgresFullDSN(t *testing.T) {
	m := mustParse(t, loadFixture(t, "postgres.yaml"))
	out, err := m.MaterializeMCP(map[string]string{
		"host":     "db.example.com",
		"port":     "5432",
		"database": "app",
		"user":     "alice",
		"password": "s3cret",
		"ssl":      "prefer",
	}, "https://acme.example.com:8080")
	if err != nil {
		t.Fatal(err)
	}

	if out.Transport != "stdio" {
		t.Errorf("transport = %q", out.Transport)
	}
	if out.Command != "npx" {
		t.Errorf("command = %q", out.Command)
	}
	// Last arg should be the resolved DSN, not the placeholder.
	last := out.Args[len(out.Args)-1]
	wantDSN := "postgres://alice:s3cret@db.example.com:5432/app?sslmode=prefer"
	if last != wantDSN {
		t.Errorf("dsn = %q, want %q", last, wantDSN)
	}
}

func TestMaterializeMCP_GitHubInjectsEnvVar(t *testing.T) {
	m := mustParse(t, loadFixture(t, "github.yaml"))
	out, err := m.MaterializeMCP(map[string]string{"pat": "ghp_real"}, "")
	if err != nil {
		t.Fatal(err)
	}
	if out.Env["GITHUB_PERSONAL_ACCESS_TOKEN"] != "ghp_real" {
		t.Errorf("env[GITHUB_PERSONAL_ACCESS_TOKEN] = %q", out.Env["GITHUB_PERSONAL_ACCESS_TOKEN"])
	}
}

func TestMaterializeMCP_MissingRequiredField(t *testing.T) {
	m := mustParse(t, loadFixture(t, "github.yaml"))
	_, err := m.MaterializeMCP(map[string]string{}, "")
	if !errors.Is(err, connectors.ErrManifestMissingFieldVal) {
		t.Errorf("err = %v, want ErrManifestMissingFieldVal", err)
	}
}

func TestMaterializeMCP_NoFieldsManifest(t *testing.T) {
	// Linear (mcp_oauth) has no fields — materialize must still
	// return a valid MCPConfig with the literal endpoint.
	m := mustParse(t, loadFixture(t, "linear.yaml"))
	out, err := m.MaterializeMCP(map[string]string{}, "")
	if err != nil {
		t.Fatal(err)
	}
	if out.Endpoint != "https://mcp.linear.app/mcp" {
		t.Errorf("endpoint = %q", out.Endpoint)
	}
}

func TestMaterializeMCP_AppliesFieldDefault(t *testing.T) {
	// Postgres `port` has default=5432. If user omits port, the
	// materialized DSN should still be valid using the default.
	m := mustParse(t, loadFixture(t, "postgres.yaml"))
	out, err := m.MaterializeMCP(map[string]string{
		"host":     "db.example.com",
		"database": "app",
		"user":     "alice",
		"password": "x",
		"ssl":      "require",
		// port omitted — default=5432 should fill in
	}, "")
	if err != nil {
		t.Fatal(err)
	}
	last := out.Args[len(out.Args)-1]
	if !strings.Contains(last, ":5432/") {
		t.Errorf("default port not applied: %q", last)
	}
}

func TestMaterializeMCP_OmitsOptionalFieldEnv(t *testing.T) {
	// Slack's team_id is optional. If user leaves it blank, the
	// SLACK_TEAM_ID env value should be empty, not "${field.team_id}".
	m := mustParse(t, loadFixture(t, "slack.yaml"))
	out, err := m.MaterializeMCP(map[string]string{
		"bot_token": "xoxb-real",
		// team_id omitted
	}, "")
	if err != nil {
		t.Fatal(err)
	}
	if got := out.Env["SLACK_TEAM_ID"]; strings.Contains(got, "${") {
		t.Errorf("optional placeholder leaked: %q", got)
	}
}

// sliceContains is a tiny test helper kept in this file so it doesn't
// pollute the public package surface.
func sliceContains(haystack []string, needle string) bool {
	for _, h := range haystack {
		if h == needle {
			return true
		}
	}
	return false
}

// -------------------------------------------------------------------
// Round-3 invariants — verify ambiguity, transport mismatch, cycles
// -------------------------------------------------------------------

func TestValidate_VerifyBlockAmbiguous(t *testing.T) {
	// A manifest declaring both verify.http AND verify.mcp_method is
	// ambiguous — we wouldn't know which path to run pre-install.
	yml := `
id: ambig
name: Ambig
description: x
auth_mode: pat
brand: {logo: x, color: "#000000"}
fields:
  - {key: token, label: Token, type: password, required: true}
mcp:
  transport: stdio
  command: foo
verify:
  http:
    method: GET
    url: https://x/me
    expect_status: 200
  mcp_method: tools/list
`
	m := mustParse(t, []byte(yml))
	if err := m.Validate(); !errors.Is(err, connectors.ErrManifestVerifyAmbiguous) {
		t.Errorf("err = %v, want ErrManifestVerifyAmbiguous", err)
	}
}

func TestValidate_StdioWithEndpoint_Mismatch(t *testing.T) {
	yml := `
id: mismatch
name: Mismatch
description: x
auth_mode: none
brand: {logo: x, color: "#000000"}
mcp:
  transport: stdio
  command: foo
  endpoint: https://x/should-not-be-here
`
	m := mustParse(t, []byte(yml))
	if err := m.Validate(); !errors.Is(err, connectors.ErrManifestTransportFieldMismatch) {
		t.Errorf("err = %v, want ErrManifestTransportFieldMismatch", err)
	}
}

func TestValidate_HTTPWithCommand_Mismatch(t *testing.T) {
	yml := `
id: mismatch2
name: Mismatch2
description: x
auth_mode: none
brand: {logo: x, color: "#000000"}
mcp:
  transport: streamable-http
  endpoint: https://x
  command: should-not-be-here
`
	m := mustParse(t, []byte(yml))
	if err := m.Validate(); !errors.Is(err, connectors.ErrManifestTransportFieldMismatch) {
		t.Errorf("err = %v, want ErrManifestTransportFieldMismatch", err)
	}
}

func TestValidate_StdioWithoutCommand_Missing(t *testing.T) {
	yml := `
id: nocmd
name: NoCmd
description: x
auth_mode: none
brand: {logo: x, color: "#000000"}
mcp:
  transport: stdio
`
	m := mustParse(t, []byte(yml))
	if err := m.Validate(); !errors.Is(err, connectors.ErrManifestMissingField) {
		t.Errorf("err = %v, want ErrManifestMissingField (stdio needs command)", err)
	}
}

func TestValidate_DerivedSelfReference_Cycle(t *testing.T) {
	// `${derived.a}` referencing `${derived.a}` is an infinite loop —
	// MaterializeMCP would either stack-overflow or emit garbage.
	yml := `
id: cyclic
name: Cyclic
description: x
auth_mode: none
brand: {logo: x, color: "#000000"}
derived:
  a: "${derived.a}-suffix"
mcp:
  transport: stdio
  command: foo
  args: ["${derived.a}"]
`
	m := mustParse(t, []byte(yml))
	if err := m.Validate(); !errors.Is(err, connectors.ErrManifestCyclicDerived) {
		t.Errorf("err = %v, want ErrManifestCyclicDerived", err)
	}
}

func TestValidate_DerivedTwoStepCycle(t *testing.T) {
	yml := `
id: cyclic2
name: Cyclic2
description: x
auth_mode: none
brand: {logo: x, color: "#000000"}
derived:
  a: "${derived.b}"
  b: "${derived.a}"
mcp:
  transport: stdio
  command: foo
  args: ["${derived.a}"]
`
	m := mustParse(t, []byte(yml))
	if err := m.Validate(); !errors.Is(err, connectors.ErrManifestCyclicDerived) {
		t.Errorf("err = %v, want ErrManifestCyclicDerived (a → b → a)", err)
	}
}

func TestValidate_DerivedAcyclicChain_OK(t *testing.T) {
	// a → b → field.x is acyclic — must validate.
	yml := `
id: chained
name: Chained
description: x
auth_mode: pat
brand: {logo: x, color: "#000000"}
fields:
  - {key: token, label: Token, type: password, required: true}
derived:
  raw: "${field.token}"
  prefixed: "Bearer ${derived.raw}"
mcp:
  transport: stdio
  command: foo
  env:
    AUTH: "${derived.prefixed}"
`
	m := mustParse(t, []byte(yml))
	if err := m.Validate(); err != nil {
		t.Errorf("acyclic chain must validate: %v", err)
	}
}

// -------------------------------------------------------------------
// JSON roundtrip — guards against a future PR forgetting `json:` tags
// or drifting a tag name. If either side breaks, the unmarshaled
// struct loses fields and DeepEqual flags the regression.
// -------------------------------------------------------------------

func TestManifest_JSONRoundtrip_AllFixtures(t *testing.T) {
	for _, name := range shippedFixtures {
		t.Run(name, func(t *testing.T) {
			original := mustParse(t, loadFixture(t, name))

			encoded, err := json.Marshal(original)
			if err != nil {
				t.Fatalf("marshal: %v", err)
			}

			var decoded connectors.Manifest
			if err := json.Unmarshal(encoded, &decoded); err != nil {
				t.Fatalf("unmarshal: %v body=%s", err, encoded)
			}

			// DeepEqual catches dropped fields, renamed JSON keys,
			// nil-vs-empty-slice drift, and pointer mismatches.
			if !reflect.DeepEqual(original, &decoded) {
				t.Errorf("roundtrip mismatch for %s\noriginal: %+v\ndecoded:  %+v", name, original, &decoded)
			}
		})
	}
}

func TestManifest_JSONKeysAreLowercase(t *testing.T) {
	// Regression guard for the round-1 BLOCKER (Brand had no json
	// tags so keys serialized as "Logo"/"Color"). If any future
	// struct field forgets json tags, the lowercase-key check fails.
	m := mustParse(t, loadFixture(t, "github.yaml"))
	encoded, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	body := string(encoded)
	for _, want := range []string{
		`"id":`, `"name":`, `"auth_mode":`, `"brand":`, `"logo":`, `"color":`,
		`"mcp":`, `"transport":`, `"command":`, `"args":`, `"env":`,
		`"fields":`, `"key":`, `"type":`, `"required":`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("encoded body missing key %s; got:\n%s", want, body)
		}
	}
	for _, forbidden := range []string{`"ID":`, `"Name":`, `"AuthMode":`, `"Logo":`, `"Color":`, `"Transport":`} {
		if strings.Contains(body, forbidden) {
			t.Errorf("encoded body has uppercased key %s; tags missing somewhere", forbidden)
		}
	}
}
