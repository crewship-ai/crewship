package seeddata

import (
	"encoding/json"
	"fmt"
	"os"

	"gopkg.in/yaml.v3"
)

// ResolveIntegrationEnvJSON returns envJSON with empty secret placeholders
// filled from the corresponding SEED_* env var. Today the only such secret is
// the GitHub MCP server's GITHUB_PERSONAL_ACCESS_TOKEN, fed from
// SEED_GITHUB_TOKEN. Without this the github integration seeds with the empty
// token from integrations.yaml and the MCP server runs UNAUTHENTICATED even
// when SEED_GITHUB_TOKEN is set (the token is otherwise only wired to agents
// as GH_TOKEN). Returns envJSON unchanged when the token isn't set or the JSON
// is malformed, so seeding still proceeds.
func ResolveIntegrationEnvJSON(envJSON string) string {
	if envJSON == "" {
		return ""
	}
	tok := os.Getenv("SEED_GITHUB_TOKEN")
	if tok == "" {
		return envJSON
	}
	var env map[string]string
	if err := json.Unmarshal([]byte(envJSON), &env); err != nil {
		return envJSON
	}
	if v, ok := env["GITHUB_PERSONAL_ACCESS_TOKEN"]; !ok || v != "" {
		return envJSON
	}
	env["GITHUB_PERSONAL_ACCESS_TOKEN"] = tok
	b, err := json.Marshal(env)
	if err != nil {
		return envJSON
	}
	return string(b)
}

// IntegrationDef defines a crew-level MCP server integration.
type IntegrationDef struct {
	Name        string `yaml:"name"`
	DisplayName string `yaml:"display_name"`
	Transport   string `yaml:"transport"`
	Endpoint    string `yaml:"endpoint,omitempty"`  // for streamable-http
	Command     string `yaml:"command,omitempty"`   // for stdio
	ArgsJSON    string `yaml:"args_json,omitempty"` // for stdio
	EnvJSON     string `yaml:"env_json,omitempty"`  // for stdio
	CrewSlug    string `yaml:"crew_slug"`           // which crew to attach to
}

// OAuthCredentialDef defines an OAuth2 credential for an MCP integration.
// Built at runtime from env vars (SEED_LINEAR_OAUTH_*, SEED_GOOGLE_OAUTH_*)
// so it intentionally does NOT live in YAML — secrets must not be on disk.
type OAuthCredentialDef struct {
	IntegrationName   string
	CredName          string
	OAuthClientID     string
	OAuthClientSecret string
	OAuthAuthURL      string
	OAuthTokenURL     string
	OAuthScopes       string
	AccessToken       string // from env var, may be empty
}

// Integrations — the MCP integrations attached to demo crews.
//
// Loaded from builtin/integrations.yaml at init time. Migrated from a
// Go-literal list in F2 step 6.
var Integrations = mustLoadIntegrations()

func mustLoadIntegrations() []IntegrationDef {
	data, err := builtinFS.ReadFile("builtin/integrations.yaml")
	if err != nil {
		panic(fmt.Sprintf("seeddata: read builtin/integrations.yaml: %v", err))
	}
	var doc struct {
		Integrations []IntegrationDef `yaml:"integrations"`
	}
	if err := yaml.Unmarshal(data, &doc); err != nil {
		panic(fmt.Sprintf("seeddata: parse builtin/integrations.yaml: %v", err))
	}
	if len(doc.Integrations) == 0 {
		panic("seeddata: builtin/integrations.yaml decoded to zero integrations — schema drift?")
	}
	return doc.Integrations
}

// AgentBindingSlugs lists which agent slugs should be bound to engineering
// crew integrations. Stays as a Go slice (not YAML) because it's a
// relationship list, not a data catalogue.
var AgentBindingSlugs = []string{"alex", "sam", "robin"}

// ResolveOAuthCredentials returns OAuth credential definitions for MCP
// integrations, based on available environment variables. Stays in Go
// because reading env vars at YAML-load time would either freeze the
// values at startup (wrong for tests that mutate env) or require a
// template layer. The few dozen lines of env-lookup are clearer than
// either alternative.
func ResolveOAuthCredentials() []OAuthCredentialDef {
	var creds []OAuthCredentialDef

	linearToken := os.Getenv("SEED_LINEAR_OAUTH_ACCESS_TOKEN")
	linearClientID := os.Getenv("SEED_LINEAR_OAUTH_CLIENT_ID")
	if linearToken != "" || (linearClientID != "" && os.Getenv("SEED_LINEAR_OAUTH_CLIENT_SECRET") != "") {
		creds = append(creds, OAuthCredentialDef{
			IntegrationName:   "linear",
			CredName:          "linear-oauth",
			OAuthClientID:     linearClientID,
			OAuthClientSecret: os.Getenv("SEED_LINEAR_OAUTH_CLIENT_SECRET"),
			OAuthAuthURL:      "https://linear.app/oauth/authorize",
			OAuthTokenURL:     "https://api.linear.app/oauth/token",
			OAuthScopes:       "read write",
			AccessToken:       linearToken,
		})
	}

	googleToken := os.Getenv("SEED_GOOGLE_OAUTH_ACCESS_TOKEN")
	googleClientID := os.Getenv("SEED_GOOGLE_OAUTH_CLIENT_ID")
	if googleToken != "" || (googleClientID != "" && os.Getenv("SEED_GOOGLE_OAUTH_CLIENT_SECRET") != "") {
		creds = append(creds, OAuthCredentialDef{
			IntegrationName:   "google-workspace",
			CredName:          "google-workspace-oauth",
			OAuthClientID:     googleClientID,
			OAuthClientSecret: os.Getenv("SEED_GOOGLE_OAUTH_CLIENT_SECRET"),
			OAuthAuthURL:      "https://accounts.google.com/o/oauth2/v2/auth",
			OAuthTokenURL:     "https://oauth2.googleapis.com/token",
			OAuthScopes:       "https://mail.google.com/ https://www.googleapis.com/auth/calendar https://www.googleapis.com/auth/drive",
			AccessToken:       googleToken,
		})
	}

	return creds
}
