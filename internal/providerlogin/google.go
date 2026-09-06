package providerlogin

import (
	"net/http"
	"os"
	"strings"
	"time"
)

const GoogleOAuthConfigurationRequired = "Google token refresh is not configured on this server; an administrator must configure the matching OAuth client and restart the server"

// These credentials identify the OAuth client that issued the imported grant,
// not Crewship's separate Google web sign-in client. Never ship upstream client
// credentials, fetch them automatically, or send them to the browser/agent.
func GoogleRefreshConfigured() bool {
	return strings.TrimSpace(os.Getenv("CREWSHIP_GEMINI_OAUTH_CLIENT_ID")) != "" &&
		strings.TrimSpace(os.Getenv("CREWSHIP_GEMINI_OAUTH_CLIENT_SECRET")) != ""
}

// NewGoogleRefresher returns nil when operator configuration is incomplete.
// A missing deployment setting must not consume retry attempts or invalidate
// an otherwise valid stored user grant.
func NewGoogleRefresher(client *http.Client) *OpenAIRefresher {
	if !GoogleRefreshConfigured() {
		return nil
	}
	r := NewOpenAIRefresher(client)
	r.TokenURL = "https://oauth2.googleapis.com/token"
	r.ClientID = strings.TrimSpace(os.Getenv("CREWSHIP_GEMINI_OAUTH_CLIENT_ID"))
	r.ClientSecret = strings.TrimSpace(os.Getenv("CREWSHIP_GEMINI_OAUTH_CLIENT_SECRET"))
	return r
}

// Google access tokens last about an hour; refresh before the SDK's eager window.
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
