package seeddata

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"os"
	"strings"
)

// CredentialDef defines a credential to seed.
type CredentialDef struct {
	Name        string
	Description string
	Type        string // API_KEY, AI_CLI_TOKEN, SECRET, OAUTH2
	Provider    string // ANTHROPIC, GOOGLE, etc.
	EnvVarName  string // env var name when assigning to agents
	Value       string // plaintext value (server encrypts)
}

// ResolveAnthropicCredential returns the credential definition for the
// Anthropic API key / CLI token, reading from SEED_ANTHROPIC_API_KEY env var.
func ResolveAnthropicCredential() CredentialDef {
	apiKey := os.Getenv("SEED_ANTHROPIC_API_KEY")
	if apiKey == "" {
		b := make([]byte, 16)
		_, _ = rand.Read(b)
		apiKey = "demo-placeholder-" + hex.EncodeToString(b)
	}

	isOAuth := strings.HasPrefix(apiKey, "sk-ant-oat")
	if isOAuth {
		return CredentialDef{
			Name:        "CLAUDE_CODE_OAUTH_TOKEN",
			Description: "Claude Code OAuth token for all agents",
			Type:        "AI_CLI_TOKEN",
			Provider:    "ANTHROPIC",
			EnvVarName:  "CLAUDE_CODE_OAUTH_TOKEN",
			Value:       apiKey,
		}
	}
	return CredentialDef{
		Name:        "ANTHROPIC_API_KEY",
		Description: "Anthropic API key for all agents",
		Type:        "API_KEY",
		Provider:    "ANTHROPIC",
		EnvVarName:  "ANTHROPIC_API_KEY",
		Value:       apiKey,
	}
}

// ResolveGoogleCredential returns the Google credential if env vars are set.
// Returns nil if SEED_GOOGLE_EMAIL and SEED_GOOGLE_PASSWORD are not both set.
func ResolveGoogleCredential() *CredentialDef {
	email := os.Getenv("SEED_GOOGLE_EMAIL")
	password := os.Getenv("SEED_GOOGLE_PASSWORD")
	if email == "" || password == "" {
		return nil
	}
	return &CredentialDef{
		Name:        "GOOGLE_API_CREDENTIALS",
		Description: "Google API credentials (workspace-scoped, all crews)",
		Type:        "SECRET",
		Provider:    "GOOGLE",
		EnvVarName:  "GOOGLE_API_CREDENTIALS",
		Value:       marshalJSON(map[string]string{"email": email, "password": password}),
	}
}

// marshalJSON marshals v to a JSON string. Panics on error (should never
// happen for simple map types used in seed data).
func marshalJSON(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		panic("seeddata: failed to marshal JSON: " + err.Error())
	}
	return string(b)
}
