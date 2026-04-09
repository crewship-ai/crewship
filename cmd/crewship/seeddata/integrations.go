package seeddata

import "os"

// IntegrationDef defines a crew-level MCP server integration.
type IntegrationDef struct {
	Name        string
	DisplayName string
	Transport   string
	Endpoint    string // for streamable-http
	Command     string // for stdio
	ArgsJSON    string // for stdio
	EnvJSON     string // for stdio
	CrewSlug    string // which crew to attach to
}

// OAuthCredentialDef defines an OAuth2 credential for an MCP integration.
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

var Integrations = []IntegrationDef{
	{
		Name: "linear", DisplayName: "Linear", Transport: "streamable-http",
		Endpoint: "https://mcp.linear.app/mcp", CrewSlug: "engineering",
	},
	{
		Name: "google-workspace", DisplayName: "Google Workspace", Transport: "stdio",
		Command:  "npx",
		ArgsJSON: `["-y", "@anthropic-ai/google-workspace-mcp"]`,
		EnvJSON:  `{"GOOGLE_ACCESS_TOKEN": ""}`,
		CrewSlug: "engineering",
	},
}

// AgentBindingSlugs lists which agent slugs should be bound to engineering
// crew integrations.
var AgentBindingSlugs = []string{"tomas", "viktor", "nela", "martin"}

// ResolveOAuthCredentials returns OAuth credential definitions for MCP
// integrations, based on available environment variables.
func ResolveOAuthCredentials() []OAuthCredentialDef {
	var creds []OAuthCredentialDef

	linearToken := os.Getenv("SEED_LINEAR_OAUTH_ACCESS_TOKEN")
	linearClientID := os.Getenv("SEED_LINEAR_OAUTH_CLIENT_ID")
	if linearToken != "" || linearClientID != "" {
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
	if googleToken != "" || googleClientID != "" {
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
