package providerlogin

import (
	"net/http"
	"time"
)

// Public installed-app identity from google-gemini/gemini-cli
// packages/core/src/code_assist/oauth2.ts. This is not an operator credential.
func NewGoogleRefresher(client *http.Client) *OpenAIRefresher {
	r := NewOpenAIRefresher(client)
	r.TokenURL = "https://oauth2.googleapis.com/token"
	r.ClientID = "" // Removed bundled identity; configure the matching client server-side.
	r.ClientSecret = "" // Removed bundled identity; configure the matching client server-side.
	return r
}

// Google access tokens last about an hour; Codex's day-scale lead would
// continuously refresh Google logins. Refresh before the SDK's eager window.
func RefreshLeadFor(provider string) time.Duration {
	if Canonical(provider) == "GOOGLE" {
		return 10 * time.Minute
	}
	return RefreshLead
}

func RunStartLeadFor(provider string) time.Duration {
	if Canonical(provider) == "GOOGLE" {
		return 15 * time.Minute
	}
	return RunStartLead
}
