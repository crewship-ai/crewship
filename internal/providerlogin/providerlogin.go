// Package providerlogin is the vocabulary of a PROVIDER_LOGIN credential —
// the seat a model is paid with — as opposed to a secret an agent uses
// (docs/prd/provider-logins.md §0, §5.1, §10).
//
// It holds the three things the API tier and the orchestrator must agree on
// without importing each other: how a pasted value splits into the parts the
// vault stores (Split), which providers count as model providers and how each
// one's login reaches a container (IsProvider, DeliveryFor), and the refresh
// policy for the providers whose access tokens the server renews itself
// (TokenRefresher, Due, the backoff constants). The database, encryption and
// the HTTP handlers stay in internal/api; nothing here touches storage.
//
// Codex-specific parsing is delegated to internal/codexauth so there is one
// reading of auth.json in the tree. The Anthropic branch is a prefix check:
// a Claude Code setup-token is opaque and has no refresh flow, which is why
// RefreshSupported is a property of the split and not of the provider alone.
package providerlogin

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/crewship-ai/crewship/internal/codexauth"
	"github.com/crewship-ai/crewship/internal/geminiauth"
)

const (
	// Type is credentials.type for a provider login.
	Type = "PROVIDER_LOGIN"

	// ModeSubscription: a seat (Claude Max setup-token, ChatGPT auth.json).
	// Traffic goes through the CONNECT tunnel, billed flat-rate.
	ModeSubscription = "subscription"
	// ModeAPIKey: a metered key. Delivered exactly like an API_KEY — through
	// the sidecar CredStore where the adapter has a route, env otherwise.
	ModeAPIKey = "api_key"

	// Part keys in credential_fields. The value column (encrypted_value)
	// holds the access token / setup-token / key; these are the rest.
	PartRefreshToken = "refresh_token"
	PartIDToken      = "id_token"
	PartAccountID    = "account_id"
	PartPlan         = "plan"
	PartExpiresAt    = "expires_at"
	PartMode         = "mode"
	PartScope        = "scope"

	// RefreshLead is how far before expiry the monitor refreshes (§5.3:
	// ≥ 24 h for a 10-day token).
	RefreshLead = 24 * time.Hour
	// RunStartLead is the wider window checked before a run starts (§5.3,
	// §10.4: < 48 h remaining).
	RunStartLead = 48 * time.Hour
	// FailureBackoff is the wait after a failed refresh (CLIProxyAPI's 5 min).
	FailureBackoff = 5 * time.Minute
	// PendingBackoff is the single-flight hold: a refresh claimed and not
	// yet released blocks a second attempt for this long (1 min).
	PendingBackoff = time.Minute
	// MaxFailures is the consecutive-failure count after which the login is
	// marked needs_relogin and the owner is told.
	MaxFailures = 3

	// OpenAITokenURL and OpenAIClientID are the public token endpoint and
	// client id Codex itself uses (PRD §3.2, measured from the token's own
	// claim). Nothing secret: the client is a public OAuth client.
	OpenAITokenURL = "https://auth.openai.com/oauth/token"
	OpenAIClientID = "app_EMoamEEZ73f0CkXaXp7hrann"
)

// Refresh statuses as login.refresh.status carries them (§10.1).
const (
	StatusOK           = "ok"
	StatusPending      = "pending"
	StatusFailed       = "failed"
	StatusNeedsRelogin = "needs_relogin"
	StatusNone         = "none"
)

// providers is the closed set of model providers a login can belong to —
// the brands the console registry marks cli: true.
var providers = map[string]struct{}{
	"ANTHROPIC": {}, "OPENAI": {}, "GOOGLE": {}, "CURSOR": {}, "FACTORY": {},
	"XAI": {}, "GROQ": {}, "OPENROUTER": {}, "DEEPSEEK": {},
	"MOONSHOT": {}, "ZAI": {}, "MINIMAX": {},
}

// Canonical folds a provider onto its stored spelling.
func Canonical(provider string) string {
	return strings.ToUpper(strings.TrimSpace(provider))
}

// IsProvider reports whether provider is one a login can pay for.
func IsProvider(provider string) bool {
	_, ok := providers[Canonical(provider)]
	return ok
}

// Providers lists the accepted providers, for error messages and help text.
func Providers() []string {
	return []string{"ANTHROPIC", "OPENAI", "GOOGLE", "CURSOR", "FACTORY", "XAI", "GROQ", "OPENROUTER", "DEEPSEEK", "MOONSHOT", "ZAI", "MINIMAX"}
}

// ValidMode reports whether mode is one of the two.
func ValidMode(mode string) bool {
	return mode == ModeSubscription || mode == ModeAPIKey
}

// Login is a pasted value split into the parts the vault stores.
type Login struct {
	Provider     string
	Mode         string
	AccessToken  string // encrypted_value: access token, setup-token, or key
	RefreshToken string // SEALED secret part; empty when the provider has none
	IDToken      string // secret part; Codex only
	AccountID    string
	Plan         string
	Scope        string
	ExpiresAt    time.Time // zero when unknown / never
}

// RefreshSupported reports whether the server can renew this login itself:
// only a subscription login that carries a refresh token.
func (l Login) RefreshSupported() bool {
	return l.Mode == ModeSubscription && l.RefreshToken != ""
}

// Split turns what the user pasted into a Login, or explains why it cannot
// be stored. mode may be empty, in which case it is inferred from the value's
// shape — an auth.json or a setup-token is a subscription, anything else a
// key — so a client that only knows "here is what I pasted" still stores the
// right thing.
func Split(provider, mode, value string) (Login, error) {
	p := Canonical(provider)
	if !IsProvider(p) {
		return Login{}, fmt.Errorf("provider %q is not a model provider; a provider login pays for a model and must be one of %s",
			strings.TrimSpace(provider), strings.Join(Providers(), ", "))
	}
	v := strings.TrimSpace(value)
	if v == "" {
		return Login{}, errors.New("value is required: the auth.json, setup-token or key the login consists of")
	}
	mode = strings.ToLower(strings.TrimSpace(mode))
	if mode == "" {
		mode = inferMode(p, v)
	}
	if !ValidMode(mode) {
		return Login{}, fmt.Errorf("mode must be %q or %q", ModeSubscription, ModeAPIKey)
	}
	l := Login{Provider: p, Mode: mode}
	if mode == ModeAPIKey {
		l.AccessToken = v
		return l, nil
	}
	switch p {
	case "GOOGLE":
		f, err := geminiauth.Parse(v)
		if err != nil {
			return Login{}, fmt.Errorf("Google login: %w", err)
		}
		l.AccessToken, l.RefreshToken, l.IDToken = f.AccessToken, f.RefreshToken, f.IDToken
		l.Scope, l.Plan, l.ExpiresAt = f.Scope, f.Plan, time.UnixMilli(f.ExpiryDate).UTC()
	case "OPENAI":
		f, err := codexauth.Parse(v)
		if err != nil {
			return Login{}, errors.New("OpenAI login: " + err.Error())
		}
		l.AccessToken = f.Tokens.AccessToken
		l.RefreshToken = f.Tokens.RefreshToken
		l.IDToken = f.Tokens.IDToken
		l.AccountID = f.Tokens.AccountID
		l.Plan = codexauth.PlanType(f.Tokens.AccessToken)
		if exp, ok := codexauth.AccessTokenExpiry(f.Tokens.AccessToken); ok {
			l.ExpiresAt = exp
		}
	case "ANTHROPIC":
		if !strings.HasPrefix(v, "sk-ant-oat") {
			return Login{}, errors.New("Anthropic login: a subscription login is the output of `claude setup-token` (sk-ant-oat…); store an API key with mode api_key instead")
		}
		l.AccessToken = v
	default:
		return Login{}, fmt.Errorf("%s: a subscription login is not supported yet; store an API key with mode api_key", p)
	}
	return l, nil
}

// inferMode guesses the mode from the value's shape.
func inferMode(provider, value string) string {
	switch provider {
	case "OPENAI", "GOOGLE":
		if strings.HasPrefix(value, "{") {
			return ModeSubscription
		}
	case "ANTHROPIC":
		if strings.HasPrefix(value, "sk-ant-oat") {
			return ModeSubscription
		}
	}
	return ModeAPIKey
}

// Delivery says how a login reaches a container (§10.1 login.delivery).
type Delivery struct {
	Kind   string `json:"kind"`   // "env" | "file"
	Target string `json:"target"` // variable name or HOME-relative path
}

// apiKeyEnvVar is the conventional variable each provider's CLI reads a key
// from — the sidecar-injected dummy for the routed adapters, the real value
// for those with no endpoint override (Cursor, Factory).
var apiKeyEnvVar = map[string]string{
	"ANTHROPIC":  "ANTHROPIC_API_KEY",
	"OPENAI":     "OPENAI_API_KEY",
	"GOOGLE":     "GEMINI_API_KEY",
	"CURSOR":     "CURSOR_API_KEY",
	"FACTORY":    "FACTORY_API_KEY",
	"XAI":        "XAI_API_KEY",
	"GROQ":       "GROQ_API_KEY",
	"OPENROUTER": "OPENROUTER_API_KEY",
	"DEEPSEEK":   "DEEPSEEK_API_KEY",
	"MOONSHOT":   "MOONSHOT_API_KEY",
	"ZAI":        "ZAI_API_KEY",
	"MINIMAX":    "MINIMAX_API_KEY",
}

// DeliveryFor returns the delivery shape for a (provider, mode) pair.
func DeliveryFor(provider, mode string) Delivery {
	p := Canonical(provider)
	if mode == ModeSubscription {
		switch p {
		case "OPENAI":
			return Delivery{Kind: "file", Target: codexauth.FileRel}
		case "GOOGLE":
			return Delivery{Kind: "file", Target: geminiauth.FileRel}
		case "ANTHROPIC":
			return Delivery{Kind: "env", Target: "CLAUDE_CODE_OAUTH_TOKEN"}
		}
	}
	return Delivery{Kind: "env", Target: apiKeyEnvVar[p]}
}

// AdapterProvider is the provider a CLI adapter pays through. OpenCode is
// bring-your-own: its provider is whatever the agent's llm_provider names.
func AdapterProvider(adapter, llmProvider string) string {
	switch adapter {
	case "CLAUDE_CODE":
		return "ANTHROPIC"
	case "CODEX_CLI":
		return "OPENAI"
	case "GEMINI_CLI":
		return "GOOGLE"
	case "CURSOR_CLI":
		return "CURSOR"
	case "FACTORY_DROID":
		return "FACTORY"
	case "OPENCODE":
		return Canonical(llmProvider)
	}
	return ""
}

// PlanLabel is the plan as the Paymaster and the Providers tab show it.
func PlanLabel(provider, plan string) string {
	p := Canonical(provider)
	plan = strings.TrimSpace(plan)
	if p == "OPENAI" {
		if plan == "" {
			return "ChatGPT"
		}
		// codexauth already knows the ChatGPT plan names; feed it a bare
		// plan by way of the label function's own switch.
		return chatgptLabel(plan)
	}
	brand := map[string]string{"ANTHROPIC": "Claude", "GOOGLE": "Gemini", "CURSOR": "Cursor", "FACTORY": "Factory"}[p]
	if brand == "" {
		brand = p
	}
	if plan == "" {
		return brand
	}
	return brand + " " + strings.ToUpper(plan[:1]) + plan[1:]
}

func chatgptLabel(plan string) string {
	switch strings.ToLower(plan) {
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
	}
	return "ChatGPT " + strings.ToUpper(plan[:1]) + plan[1:]
}

// Due reports whether a token expiring at expires should be refreshed now,
// given how much lead the caller wants. An unknown expiry is due: the
// refresh is the only way to learn it.
func Due(expires, now time.Time, lead time.Duration) bool {
	if expires.IsZero() {
		return true
	}
	return !now.Add(lead).Before(expires)
}

// RefreshResult is what a provider's token endpoint returned.
type RefreshResult struct {
	AccessToken  string
	RefreshToken string // the ROTATED token; the old one is dead
	IDToken      string
	ExpiresAt    time.Time
}

// TokenRefresher renews an access token from a refresh token. Behind an
// interface so the API tier's state machine is tested against a fake and
// the real one is tested against an httptest server — no test dials a
// provider.
type TokenRefresher interface {
	Refresh(ctx context.Context, refreshToken string) (RefreshResult, error)
}

// permanentError marks a failure a retry cannot fix: the refresh token is
// dead (invalid_grant), the client is refused. The caller escalates to
// needs_relogin on the first one rather than after MaxFailures.
type permanentError struct{ msg string }

func (e *permanentError) Error() string { return e.msg }

// IsPermanent reports whether a refresh failure is one only a re-login fixes.
func IsPermanent(err error) bool {
	var pe *permanentError
	return errors.As(err, &pe)
}

// OpenAIRefresher renews a ChatGPT login at OpenAI's public token endpoint.
type OpenAIRefresher struct {
	Client       *http.Client
	TokenURL     string
	ClientID     string
	ClientSecret string
}

// NewOpenAIRefresher returns a refresher for the production endpoint. A nil
// client gets one with a timeout — a hung token endpoint must not hold the
// single-flight lock past PendingBackoff.
func NewOpenAIRefresher(client *http.Client) *OpenAIRefresher {
	if client == nil {
		client = &http.Client{Timeout: 30 * time.Second}
	}
	return &OpenAIRefresher{Client: client, TokenURL: OpenAITokenURL, ClientID: OpenAIClientID}
}

// Refresh posts grant_type=refresh_token. The refresh token appears in the
// request body only — never in an error, which lands in last_error and logs.
func (r *OpenAIRefresher) Refresh(ctx context.Context, refreshToken string) (RefreshResult, error) {
	if strings.TrimSpace(refreshToken) == "" {
		return RefreshResult{}, &permanentError{"no refresh token stored for this login"}
	}
	form := url.Values{}
	form.Set("grant_type", "refresh_token")
	form.Set("refresh_token", refreshToken)
	form.Set("client_id", r.ClientID)
	if r.ClientSecret != "" {
		form.Set("client_secret", r.ClientSecret)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, r.TokenURL, strings.NewReader(form.Encode()))
	if err != nil {
		return RefreshResult{}, fmt.Errorf("build token request: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	resp, err := r.Client.Do(req)
	if err != nil {
		return RefreshResult{}, fmt.Errorf("token endpoint: %w", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 64*1024))

	if resp.StatusCode != http.StatusOK {
		var oe struct {
			Error       string `json:"error"`
			Description string `json:"error_description"`
		}
		_ = json.Unmarshal(body, &oe)
		msg := fmt.Sprintf("token endpoint returned %d", resp.StatusCode)
		// Never persist upstream free text: an OAuth server or proxy may
		// reflect the submitted token in either error field. Only known
		// protocol codes are safe for the audit log and owner notification.
		switch oe.Error {
		case "invalid_request", "invalid_client", "invalid_grant", "unauthorized_client", "unsupported_grant_type", "invalid_scope", "temporarily_unavailable", "server_error":
			msg += ": " + oe.Error
		}
		// 400 and 401 are the provider's verdict on the grant itself; 429
		// and 5xx are the provider's day.
		if resp.StatusCode == http.StatusBadRequest || resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
			return RefreshResult{}, &permanentError{msg}
		}
		return RefreshResult{}, errors.New(msg)
	}
	var tr struct {
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
		IDToken      string `json:"id_token"`
		ExpiresIn    int64  `json:"expires_in"`
	}
	if err := json.Unmarshal(body, &tr); err != nil {
		return RefreshResult{}, errors.New("token endpoint answered 200 with a body that is not JSON")
	}
	if tr.AccessToken == "" {
		return RefreshResult{}, errors.New("token endpoint answered with no access_token")
	}
	res := RefreshResult{AccessToken: tr.AccessToken, RefreshToken: tr.RefreshToken, IDToken: tr.IDToken}
	if exp, ok := codexauth.AccessTokenExpiry(tr.AccessToken); ok {
		res.ExpiresAt = exp
	} else if tr.ExpiresIn > 0 {
		res.ExpiresAt = time.Now().UTC().Add(time.Duration(tr.ExpiresIn) * time.Second)
	}
	return res, nil
}
