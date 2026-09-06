// Package geminiauth is the one place that knows the shape of Gemini CLI's
// Google-account login — the `oauth_creds.json` that `gemini` writes under
// `~/.gemini` after "Login with Google" — and applies the same rule to it that
// internal/codexauth applies to Codex's auth.json: the refresh token never
// leaves the server.
//
// It mirrors codexauth on purpose. The API tier validates a pasted login at
// submit time and the orchestrator renders it at run time; one parser shared
// by both is what keeps "stored fine, failed at run time" from happening.
//
// What the file is (google-gemini/gemini-cli, packages/core/src/code_assist/
// oauth2.ts `cacheCredentials`, and packages/core/src/config/storage.ts
// `OAUTH_FILE`; read from the upstream source on 2026-09-06 — the CLI is not
// installed on the dev box, so the shape is sourced, not measured):
//
//   - The path is `<HOME>/.gemini/oauth_creds.json`, mode 0600.
//   - The content is `JSON.stringify(credentials, null, 2)` of the
//     google-auth-library `Credentials` object the OAuth flow returned:
//     access_token, refresh_token, scope, token_type, id_token, expiry_date.
//     expiry_date is a Unix timestamp in MILLISECONDS — the access token's
//     expiry, roughly an hour after it was minted.
//   - Headless runs pick the Google-login path when GOOGLE_GENAI_USE_GCA is
//     set (packages/cli/src/validateNonInterActiveAuth.ts names it, next to
//     GEMINI_API_KEY and GOOGLE_GENAI_USE_VERTEXAI); with an API key in the
//     environment the key wins, so a subscription run must carry no dummy
//     key at all.
//
// Why the placeholder refresh token. google-auth-library reads the file, uses
// access_token while expiry_date is in the future, and only then reaches for
// refresh_token. A container that holds a placeholder therefore works for the
// access token's lifetime and cannot rotate the login — which is what lets one
// login fan out to any number of agents without the containers invalidating
// each other's refresh tokens. The lifetime is ~1 h here rather than Codex's
// 240 h, so the central refresh (docs/prd/provider-logins.md §5.3) is what
// makes a Gemini login usable for a long run; until it lands, the file is good
// for the hour the pasted access token has left.
package geminiauth

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

const (
	// ProviderID is the credentials.provider value that marks an AI_CLI_TOKEN
	// as a Gemini/Google login. Uppercase, the way the API tier stores it.
	ProviderID = "GOOGLE"

	// HomeRel is Gemini CLI's config directory relative to the agent's HOME.
	HomeRel = ".gemini"

	// FileRel is the login file relative to the agent's HOME.
	FileRel = HomeRel + "/oauth_creds.json"

	// PlaceholderRefreshToken stands in for the real refresh token inside
	// every delivered file. Google refresh tokens begin `1//`; the rest is
	// recognisably not one, so a listing that shows it reads
	// "Crewship-managed", not "leaked".
	PlaceholderRefreshToken = "1//crewship-managed-no-refresh"

	// UseGCAEnv is the variable that makes a headless Gemini CLI run take the
	// Google-login path instead of asking for an API key.
	UseGCAEnv = "GOOGLE_GENAI_USE_GCA"

	// DefaultScope is what the Gemini CLI login requests; a pasted file that
	// omits it (hand-edited) gets it back so the rendered file is complete.
	DefaultScope = "https://www.googleapis.com/auth/cloud-platform https://www.googleapis.com/auth/userinfo.email https://www.googleapis.com/auth/userinfo.profile"

	defaultTokenType = "Bearer"
)

// File is oauth_creds.json as Gemini CLI writes and reads it. Plan is a
// Crewship extension — Google's tokens carry no plan claim, so the tier
// ("pro", "ultra") is known only when the operator says so; a file without it
// is labelled plainly "Google AI".
type File struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	Scope        string `json:"scope,omitempty"`
	TokenType    string `json:"token_type,omitempty"`
	IDToken      string `json:"id_token,omitempty"`
	ExpiryDate   int64  `json:"expiry_date"`
	Plan         string `json:"plan,omitempty"`
}

// IsLogin reports whether a credential of this type and provider is a Gemini
// login — the single predicate every caller uses, so they cannot drift.
func IsLogin(credType, provider string) bool {
	return credType == "AI_CLI_TOKEN" && strings.EqualFold(strings.TrimSpace(provider), ProviderID)
}

// Parse reads a stored login: the oauth_creds.json that Gemini CLI wrote,
// pasted whole. A bare access token is refused — without expiry_date the
// library cannot tell whether the token is live, and without refresh_token the
// server has nothing to refresh centrally, so the credential would be an
// hour-long token pretending to be a login.
func Parse(value string) (File, error) {
	v := strings.TrimSpace(value)
	if v == "" {
		return File{}, errors.New("empty value")
	}
	if !strings.HasPrefix(v, "{") {
		return File{}, errors.New("not a JSON object: paste the whole ~/.gemini/oauth_creds.json that `gemini` wrote after Login with Google, not a bare token")
	}
	var f File
	if err := json.Unmarshal([]byte(v), &f); err != nil {
		return File{}, fmt.Errorf("oauth_creds.json is not valid JSON: %w", err)
	}
	f.AccessToken = strings.TrimSpace(f.AccessToken)
	f.RefreshToken = strings.TrimSpace(f.RefreshToken)
	f.IDToken = strings.TrimSpace(f.IDToken)
	f.Scope = strings.TrimSpace(f.Scope)
	f.TokenType = strings.TrimSpace(f.TokenType)
	f.Plan = strings.ToLower(strings.TrimSpace(f.Plan))
	if f.AccessToken == "" {
		return File{}, errors.New("oauth_creds.json has no access_token — sign in with `gemini` and paste the file it writes")
	}
	if f.RefreshToken == "" {
		return File{}, errors.New("oauth_creds.json has no refresh_token — the server keeps it to renew the login; sign in again with `gemini` and paste the fresh file")
	}
	if f.ExpiryDate <= 0 {
		return File{}, errors.New("oauth_creds.json has no expiry_date — Gemini CLI cannot tell whether the access token is live without it")
	}
	if f.Scope == "" {
		f.Scope = DefaultScope
	}
	if f.TokenType == "" {
		f.TokenType = defaultTokenType
	}
	return f, nil
}

// ShapeError is Parse for the API tier: the operator-facing reason a pasted
// value cannot be stored as a Gemini login, or "" when it can.
func ShapeError(value string) string {
	if _, err := Parse(value); err != nil {
		return "Google login: " + err.Error()
	}
	return ""
}

// Render produces the file a container receives: the real access token, id
// token, scope, token type and expiry, and the PLACEHOLDER refresh token. The
// Crewship-only plan field is not written — Gemini CLI has no use for it and
// an unknown key in the file is a needless difference from what it wrote.
func Render(f File) ([]byte, error) {
	if f.AccessToken == "" || f.ExpiryDate <= 0 {
		return nil, errors.New("login is missing a required token field")
	}
	scope := f.Scope
	if scope == "" {
		scope = DefaultScope
	}
	tokenType := f.TokenType
	if tokenType == "" {
		tokenType = defaultTokenType
	}
	out := File{
		AccessToken:  f.AccessToken,
		RefreshToken: PlaceholderRefreshToken,
		Scope:        scope,
		TokenType:    tokenType,
		IDToken:      f.IDToken,
		ExpiryDate:   f.ExpiryDate,
	}
	b, err := json.MarshalIndent(out, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(b, '\n'), nil
}

// PlanLabel is the subscription label the Paymaster shows: "Google AI Pro" or
// "Google AI Ultra" when the stored login names its tier, "Google AI" when it
// does not. value may be the stored oauth_creds.json or anything else, in
// which case the plain label is returned.
func PlanLabel(value string) string {
	f, err := Parse(value)
	if err != nil {
		return "Google AI"
	}
	switch f.Plan {
	case "pro":
		return "Google AI Pro"
	case "ultra":
		return "Google AI Ultra"
	case "":
		return "Google AI"
	default:
		return "Google AI " + strings.ToUpper(f.Plan[:1]) + f.Plan[1:]
	}
}

// AccessTokenExpiry returns expiry_date as a time. ok is false when the value
// is not a login or carries no expiry.
func AccessTokenExpiry(value string) (time.Time, bool) {
	f, err := Parse(value)
	if err != nil || f.ExpiryDate <= 0 {
		return time.Time{}, false
	}
	return time.UnixMilli(f.ExpiryDate).UTC(), true
}

// Email returns the `email` claim of the stored id_token, or "" when there is
// none. Used for labelling only — the signature is not verified, and the token
// is one the operator pasted from their own login.
func Email(value string) string {
	f, err := Parse(value)
	if err != nil {
		return ""
	}
	claims, ok := jwtClaims(f.IDToken)
	if !ok {
		return ""
	}
	s, _ := claims["email"].(string)
	return strings.TrimSpace(s)
}

// jwtClaims decodes a JWT payload WITHOUT verifying the signature.
func jwtClaims(token string) (map[string]any, bool) {
	parts := strings.Split(strings.TrimSpace(token), ".")
	if len(parts) != 3 {
		return nil, false
	}
	payload, err := base64.RawURLEncoding.DecodeString(strings.TrimRight(parts[1], "="))
	if err != nil {
		return nil, false
	}
	var claims map[string]any
	if err := json.Unmarshal(payload, &claims); err != nil {
		return nil, false
	}
	return claims, true
}
