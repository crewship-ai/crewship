// Package codexauth is the one place that knows the shape of Codex CLI's
// ChatGPT-subscription login — the `auth.json` that `codex login` writes under
// `$CODEX_HOME` — and the one rule Crewship applies to it before it reaches a
// container: the refresh token never leaves the server.
//
// Why a package of its own. The API tier validates a pasted login at submit
// time and the orchestrator renders it at run time; both need the same parser,
// and internal/api already imports internal/orchestrator, so the parser cannot
// live in either without the two disagreeing about what "a valid login" is —
// which is exactly how a credential that stores cleanly and fails at run time
// comes to exist.
//
// What was measured (codex-cli 0.153.2, 2026-09-06 — docs/prd/provider-logins.md
// §3.2, and the memory note codex-auth-is-a-file-not-a-token):
//
//   - Codex has no environment variable for a subscription login. The only
//     carrier is `$CODEX_HOME/auth.json`, and every one of its token fields is
//     required: a file missing `id_token` or `refresh_token` is rejected
//     outright ("missing field").
//   - The refresh token is only touched when the access token nears expiry, so
//     it may be a PLACEHOLDER. With PlaceholderRefreshToken in the file Codex
//     reports "Logged in using ChatGPT" and authenticates normally.
//   - ChatGPT access tokens live 240 h; the id_token expires after 1 h and
//     Codex does not care. The plan is in the access token's
//     `https://api.openai.com/auth` claim (`chatgpt_plan_type`).
//   - `CODEX_API_KEY` OVERRIDES auth.json; `OPENAI_API_KEY` does not. So the
//     dummy OPENAI_API_KEY every sidecar run carries is harmless here, and a
//     dummy CODEX_API_KEY would silently break subscription mode.
//
// Why the placeholder is the whole design and not a shortcut. OAuth refresh
// tokens ROTATE: each refresh returns a new one and invalidates the old. Ten
// containers holding a copy of one login are ten clients refreshing
// independently, and whichever refreshes last breaks the others. A container
// that holds no real refresh token cannot rotate anything, so one login can
// fan out to any number of agents. Each agent receives a derivative cache
// without the server's rotating OAuth state.
// Refreshing centrally, from the sealed copy the server keeps, is a separate
// increment (PRD §5.3).
package codexauth

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
	// as a Codex/ChatGPT login rather than a Claude Code one. Uppercase, the
	// way the API tier stores it and llmroute names the spec.
	ProviderID = "OPENAI"

	// HomeRel is Codex's config directory relative to the agent's HOME —
	// what CODEX_HOME is set to. Codex defaults to ~/.codex, and Crewship
	// already writes the MCP config there (mcp_writers.go); naming it
	// explicitly keeps a future HOME change from silently moving the login.
	HomeRel = ".codex"

	// FileRel is the login file relative to the agent's HOME.
	FileRel = HomeRel + "/auth.json"

	// PlaceholderRefreshToken stands in for the real refresh token inside
	// every delivered file. It is recognisably not a token (real ones are
	// `rt.` followed by ~200 opaque characters), so a log line or a file
	// listing that shows it says "Crewship-managed" rather than "leaked".
	PlaceholderRefreshToken = "rt.crewship-managed-no-refresh"

	// authModeChatGPT is the auth_mode Codex writes for a subscription login.
	authModeChatGPT = "chatgpt"

	// authClaim is the JWT claim that carries the ChatGPT account and plan.
	authClaim = "https://api.openai.com/auth"
)

// Tokens is the `tokens` object of auth.json. All four fields are required by
// Codex; Parse enforces the three it cannot invent and Render supplies the
// fourth.
type Tokens struct {
	IDToken      string `json:"id_token"`
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	AccountID    string `json:"account_id"`
}

// File is auth.json as Codex reads it. OpenAIAPIKey is kept as a pointer so a
// stored login round-trips its explicit `null` — Codex writes the key and a
// file without it is not the file `codex login` produced.
type File struct {
	AuthMode     string  `json:"auth_mode"`
	OpenAIAPIKey *string `json:"OPENAI_API_KEY"`
	Tokens       Tokens  `json:"tokens"`
	LastRefresh  string  `json:"last_refresh"`
}

// IsLogin reports whether a credential of this type and provider is a Codex
// login — the single predicate every caller (env builder, file writer, sidecar
// CredStore gate, revoke, API validation) uses, so they cannot drift.
func IsLogin(credType, provider string) bool {
	return credType == "AI_CLI_TOKEN" && strings.EqualFold(strings.TrimSpace(provider), ProviderID)
}

// Parse reads a stored login. It accepts the auth.json that `codex login`
// wrote — the whole file, pasted — and nothing else: a bare access token
// cannot become a working file because Codex insists on an id_token too, and
// storing something that can never be rendered is the failure this package
// exists to prevent.
//
// A missing account_id is filled from the access token's claim when the claim
// carries one; Codex writes it, but a hand-edited file may not.
func Parse(value string) (File, error) {
	v := strings.TrimSpace(value)
	if v == "" {
		return File{}, errors.New("empty value")
	}
	if !strings.HasPrefix(v, "{") {
		return File{}, errors.New("not a JSON object: paste the whole ~/.codex/auth.json that `codex login` wrote, not a bare token")
	}
	var f File
	if err := json.Unmarshal([]byte(v), &f); err != nil {
		return File{}, fmt.Errorf("auth.json is not valid JSON: %w", err)
	}
	f.Tokens.AccessToken = strings.TrimSpace(f.Tokens.AccessToken)
	f.Tokens.IDToken = strings.TrimSpace(f.Tokens.IDToken)
	f.Tokens.RefreshToken = strings.TrimSpace(f.Tokens.RefreshToken)
	f.Tokens.AccountID = strings.TrimSpace(f.Tokens.AccountID)
	if f.Tokens.AccessToken == "" {
		return File{}, errors.New("auth.json has no tokens.access_token — run `codex login` and paste the file it writes")
	}
	if f.Tokens.IDToken == "" {
		return File{}, errors.New("auth.json has no tokens.id_token — Codex refuses a login without it")
	}
	if f.Tokens.AccountID == "" {
		f.Tokens.AccountID = claimString(f.Tokens.AccessToken, "chatgpt_account_id")
	}
	if f.Tokens.AccountID == "" {
		return File{}, errors.New("auth.json has no tokens.account_id and the access token carries none")
	}
	if f.AuthMode == "" {
		f.AuthMode = authModeChatGPT
	}
	return f, nil
}

// ShapeError is Parse for the API tier: the operator-facing reason a pasted
// value cannot be stored as a Codex login, or "" when it can.
func ShapeError(value string) string {
	if _, err := Parse(value); err != nil {
		return "OpenAI login: " + err.Error()
	}
	return ""
}

// Render produces the file a container receives: the real id token, access
// token and account id, the PLACEHOLDER refresh token, and last_refresh
// stamped now. The stamp matters — Codex decides whether to attempt a refresh
// from last_refresh, and a stale one would make it try, fail on the
// placeholder, and refuse the run.
//
// OPENAI_API_KEY is written as an explicit null: with a key there Codex would
// prefer it over the tokens (the CODEX_API_KEY precedence, in file form).
func Render(f File, now time.Time) ([]byte, error) {
	if f.Tokens.AccessToken == "" || f.Tokens.IDToken == "" || f.Tokens.AccountID == "" {
		return nil, errors.New("login is missing a required token field")
	}
	out := File{
		AuthMode:     authModeChatGPT,
		OpenAIAPIKey: nil,
		Tokens: Tokens{
			IDToken:      f.Tokens.IDToken,
			AccessToken:  f.Tokens.AccessToken,
			RefreshToken: PlaceholderRefreshToken,
			AccountID:    f.Tokens.AccountID,
		},
		LastRefresh: now.UTC().Format("2006-01-02T15:04:05.000000000Z07:00"),
	}
	b, err := json.MarshalIndent(out, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(b, '\n'), nil
}

// PlanType returns the ChatGPT plan named in the access token's auth claim
// ("plus", "pro", "team", "business", "enterprise", "free"), or "" when the
// token carries none. value may be the stored auth.json or a bare JWT.
func PlanType(value string) string {
	return claimString(accessTokenOf(value), "chatgpt_plan_type")
}

// PlanLabel is PlanType as the Paymaster shows it: "ChatGPT Plus". A token
// with no plan claim is labelled plainly "ChatGPT" rather than guessed at.
func PlanLabel(value string) string {
	switch strings.ToLower(PlanType(value)) {
	case "plus":
		return "ChatGPT Plus"
	case "pro":
		return "ChatGPT Pro"
	case "team":
		return "ChatGPT Team"
	case "business":
		return "ChatGPT Business"
	case "enterprise":
		return "ChatGPT Enterprise"
	case "free":
		return "ChatGPT Free"
	case "":
		return "ChatGPT"
	default:
		// A plan this build has not seen: show it rather than hide it.
		return "ChatGPT " + strings.ToUpper(PlanType(value)[:1]) + PlanType(value)[1:]
	}
}

// AccessTokenExpiry returns the access token's `exp` claim. ok is false when
// the value is not a JWT or carries no expiry.
func AccessTokenExpiry(value string) (time.Time, bool) {
	claims, ok := jwtClaims(accessTokenOf(value))
	if !ok {
		return time.Time{}, false
	}
	exp, ok := claims["exp"].(float64)
	if !ok || exp <= 0 {
		return time.Time{}, false
	}
	return time.Unix(int64(exp), 0).UTC(), true
}

// accessTokenOf accepts either a stored auth.json or a bare token and returns
// the access token, or "" when there is none to find.
func accessTokenOf(value string) string {
	v := strings.TrimSpace(value)
	if strings.HasPrefix(v, "{") {
		f, err := Parse(v)
		if err != nil {
			return ""
		}
		return f.Tokens.AccessToken
	}
	return v
}

// claimString reads a string field of the OpenAI auth claim object.
func claimString(token, key string) string {
	claims, ok := jwtClaims(token)
	if !ok {
		return ""
	}
	auth, ok := claims[authClaim].(map[string]any)
	if !ok {
		return ""
	}
	s, _ := auth[key].(string)
	return strings.TrimSpace(s)
}

// jwtClaims decodes a JWT payload WITHOUT verifying the signature. The claims
// are used for labelling and expiry only — never for authorisation — and the
// token is one the operator pasted from their own login, so the trust question
// this would normally raise does not arise here.
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
