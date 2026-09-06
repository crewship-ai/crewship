package codexauth

// Codex's device-code sign-in, as Crewship drives it without the codex binary
// (docs/prd/provider-logins.md §5.6 v2, §10.3).
//
// This is NOT RFC 8628. What `codex login --device-auth` actually speaks was
// read from the open-source client (github.com/openai/codex,
// codex-rs/login/src/device_code_auth.rs and server.rs, main on 2026-09-06)
// and every endpoint path, field name and the client id were then found
// verbatim in the strings of the installed codex-cli 0.153.2 binary
// (/api/accounts, /deviceauth/usercode, /deviceauth/token,
// /deviceauth/callback, /codex/device, /oauth/token, device_auth_id,
// user_code, authorization_code, code_verifier, app_EMoamEEZ73f0CkXaXp7hrann,
// "device auth timed out after 15 minutes"). The wire shapes below are the
// client's serde structs; the server's exact responses were not observed on
// this machine (no interactive browser step was run), which is why every
// response field the client tolerates as optional is tolerated here too.
//
// The flow:
//
//  1. POST {issuer}/api/accounts/deviceauth/usercode  {"client_id": …}
//     → {"device_auth_id": …, "user_code": … (alias "usercode"),
//     "interval": "5"}   — interval is a STRING of seconds.
//     A 404 means device-code login is not enabled for this issuer.
//  2. The person opens {issuer}/codex/device and types user_code.
//  3. POST {issuer}/api/accounts/deviceauth/token
//     {"device_auth_id": …, "user_code": …}, every `interval` seconds:
//     403 or 404 → not yet authorised, keep polling;
//     200 → {"authorization_code": …, "code_challenge": …,
//     "code_verifier": …} — the server minted the PKCE pair itself;
//     anything else → the flow failed. The client gives up after 15 min.
//  4. POST {issuer}/oauth/token, form-encoded:
//     grant_type=authorization_code&code=…&redirect_uri={issuer}/deviceauth/callback
//     &client_id=…&code_verifier=…
//     → {"id_token": …, "access_token": …, "refresh_token": …}
//  5. auth.json = auth_mode "chatgpt", OPENAI_API_KEY null, the three tokens,
//     account_id from the id_token's chatgpt_account_id claim, last_refresh
//     now. The browser login additionally exchanges the id_token for an API
//     key; the device flow does not (api_key: None), and neither does this.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

const (
	// DefaultIssuer is auth.openai.com — DEFAULT_ISSUER in the Codex source.
	DefaultIssuer = "https://auth.openai.com"

	// ClientID is Codex's public OAuth client id, present in the binary and
	// in the `client_id` claim of every ChatGPT token Codex holds.
	ClientID = "app_EMoamEEZ73f0CkXaXp7hrann"

	// DeviceFlowTimeout is how long Codex itself waits for the browser step
	// before giving up ("device auth timed out after 15 minutes").
	DeviceFlowTimeout = 15 * time.Minute

	// DefaultPollInterval is used when the usercode response names none.
	DefaultPollInterval = 5 * time.Second

	deviceUserCodePath = "/api/accounts/deviceauth/usercode"
	deviceTokenPath    = "/api/accounts/deviceauth/token"
	deviceCallbackPath = "/deviceauth/callback"
	deviceVerifyPath   = "/codex/device"
	oauthTokenPath     = "/oauth/token"
)

// DeviceStart is what the usercode endpoint hands back, plus the page the
// person has to open.
type DeviceStart struct {
	DeviceAuthID    string
	UserCode        string
	VerificationURL string
	Interval        time.Duration
}

// DevicePoll is one answer of the token endpoint.
type DevicePoll struct {
	// Authorized is true when the person finished the browser step; Code
	// and Verifier are then set and the flow moves to Exchange.
	Authorized bool
	Code       string
	Verifier   string
	// RetryAfter is a server-requested back-off (a 429 or a Retry-After
	// header), zero when the caller should keep its interval.
	RetryAfter time.Duration
}

// ErrDeviceDenied is returned by Poll when the issuer answered with a
// status that means the flow is over without a token — anything other than
// "not yet" (403/404) or "slow down" (429/5xx).
var ErrDeviceDenied = errors.New("device sign-in was refused by the issuer")

// ErrDeviceNotEnabled is the usercode endpoint's 404.
var ErrDeviceNotEnabled = errors.New("device-code sign-in is not enabled for this issuer")

// DeviceClient speaks the flow above against one issuer. The zero value is
// not usable; use NewDeviceClient.
type DeviceClient struct {
	issuer   string
	clientID string
	http     *http.Client
}

// NewDeviceClient returns a client for issuer (DefaultIssuer when empty).
// httpClient may be nil for a 30 s default.
func NewDeviceClient(issuer string, httpClient *http.Client) *DeviceClient {
	if issuer == "" {
		issuer = DefaultIssuer
	}
	if httpClient == nil {
		httpClient = &http.Client{Timeout: 30 * time.Second}
	}
	return &DeviceClient{issuer: strings.TrimRight(issuer, "/"), clientID: ClientID, http: httpClient}
}

// Start requests a user code.
func (c *DeviceClient) Start(ctx context.Context) (DeviceStart, error) {
	body, _ := json.Marshal(map[string]string{"client_id": c.clientID})
	resp, err := c.postJSON(ctx, c.issuer+deviceUserCodePath, body)
	if err != nil {
		return DeviceStart{}, err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<16))
	if resp.StatusCode == http.StatusNotFound {
		return DeviceStart{}, ErrDeviceNotEnabled
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return DeviceStart{}, fmt.Errorf("device code request failed with status %d", resp.StatusCode)
	}
	var uc struct {
		DeviceAuthID string          `json:"device_auth_id"`
		UserCode     string          `json:"user_code"`
		UserCodeAlt  string          `json:"usercode"`
		Interval     json.RawMessage `json:"interval"`
	}
	if err := json.Unmarshal(raw, &uc); err != nil {
		return DeviceStart{}, fmt.Errorf("device code response is not JSON: %w", err)
	}
	code := uc.UserCode
	if code == "" {
		code = uc.UserCodeAlt
	}
	if uc.DeviceAuthID == "" || code == "" {
		return DeviceStart{}, errors.New("device code response is missing device_auth_id or user_code")
	}
	return DeviceStart{
		DeviceAuthID:    uc.DeviceAuthID,
		UserCode:        code,
		VerificationURL: c.issuer + deviceVerifyPath,
		Interval:        parseInterval(uc.Interval),
	}, nil
}

// parseInterval reads the usercode response's interval, which Codex
// deserialises from a STRING ("5"); a bare number is accepted too, and
// anything unreadable or non-positive falls back to the default.
func parseInterval(raw json.RawMessage) time.Duration {
	s := strings.Trim(strings.TrimSpace(string(raw)), `"`)
	n, err := strconv.Atoi(strings.TrimSpace(s))
	if err != nil || n <= 0 {
		return DefaultPollInterval
	}
	return time.Duration(n) * time.Second
}

// Poll asks once whether the person has finished the browser step.
func (c *DeviceClient) Poll(ctx context.Context, deviceAuthID, userCode string) (DevicePoll, error) {
	body, _ := json.Marshal(map[string]string{"device_auth_id": deviceAuthID, "user_code": userCode})
	resp, err := c.postJSON(ctx, c.issuer+deviceTokenPath, body)
	if err != nil {
		return DevicePoll{}, err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<16))
	switch {
	case resp.StatusCode >= 200 && resp.StatusCode <= 299:
		var ok struct {
			AuthorizationCode string `json:"authorization_code"`
			CodeVerifier      string `json:"code_verifier"`
		}
		if err := json.Unmarshal(raw, &ok); err != nil {
			return DevicePoll{}, fmt.Errorf("device token response is not JSON: %w", err)
		}
		if ok.AuthorizationCode == "" || ok.CodeVerifier == "" {
			return DevicePoll{}, errors.New("device token response is missing authorization_code or code_verifier")
		}
		return DevicePoll{Authorized: true, Code: ok.AuthorizationCode, Verifier: ok.CodeVerifier}, nil
	case resp.StatusCode == http.StatusForbidden, resp.StatusCode == http.StatusNotFound:
		// Codex: "not yet", keep polling at the interval.
		return DevicePoll{}, nil
	case resp.StatusCode == http.StatusTooManyRequests, resp.StatusCode >= 500:
		// Not part of Codex's client, which only knows 403/404; a rate limit
		// or an outage is still "not yet", with a back-off the caller
		// honours (Retry-After when the server names one, otherwise a
		// doubled interval is the caller's choice).
		return DevicePoll{RetryAfter: retryAfter(resp.Header.Get("Retry-After"))}, nil
	default:
		return DevicePoll{}, fmt.Errorf("%w (status %d)", ErrDeviceDenied, resp.StatusCode)
	}
}

// retryAfter reads a Retry-After header given in seconds; 0 when absent or
// unreadable (the HTTP-date form is not worth the parser here).
func retryAfter(h string) time.Duration {
	n, err := strconv.Atoi(strings.TrimSpace(h))
	if err != nil || n <= 0 {
		return 0
	}
	return time.Duration(n) * time.Second
}

// Exchange turns the authorization code into tokens and assembles the
// auth.json File — account_id from the id_token claim the way Codex's
// persist_tokens does, with the access token's claim as the fallback.
func (c *DeviceClient) Exchange(ctx context.Context, code, verifier string) (File, error) {
	form := url.Values{}
	form.Set("grant_type", "authorization_code")
	form.Set("code", code)
	form.Set("redirect_uri", c.issuer+deviceCallbackPath)
	form.Set("client_id", c.clientID)
	form.Set("code_verifier", verifier)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.issuer+oauthTokenPath, strings.NewReader(form.Encode()))
	if err != nil {
		return File{}, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := c.http.Do(req)
	if err != nil {
		return File{}, fmt.Errorf("token exchange: %w", err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<18))
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return File{}, fmt.Errorf("token exchange failed with status %d", resp.StatusCode)
	}
	var tok struct {
		IDToken      string `json:"id_token"`
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
	}
	if err := json.Unmarshal(raw, &tok); err != nil {
		return File{}, fmt.Errorf("token response is not JSON: %w", err)
	}
	return AssembleFile(tok.IDToken, tok.AccessToken, tok.RefreshToken)
}

// AssembleFile builds the auth.json a fresh sign-in produces. It is what
// Parse would accept back, so the device flow stores exactly the shape a
// pasted `codex login` file has.
func AssembleFile(idToken, accessToken, refreshToken string) (File, error) {
	idToken, accessToken, refreshToken = strings.TrimSpace(idToken), strings.TrimSpace(accessToken), strings.TrimSpace(refreshToken)
	if idToken == "" || accessToken == "" || refreshToken == "" {
		return File{}, errors.New("token response is missing id_token, access_token or refresh_token")
	}
	accountID := claimString(idToken, "chatgpt_account_id")
	if accountID == "" {
		accountID = claimString(accessToken, "chatgpt_account_id")
	}
	if accountID == "" {
		return File{}, errors.New("neither token names a chatgpt_account_id")
	}
	return File{
		AuthMode:     authModeChatGPT,
		OpenAIAPIKey: nil,
		Tokens:       Tokens{IDToken: idToken, AccessToken: accessToken, RefreshToken: refreshToken, AccountID: accountID},
		LastRefresh:  time.Now().UTC().Format("2006-01-02T15:04:05.000000000Z07:00"),
	}, nil
}

// Email returns the `email` claim of the stored login's id_token, or "".
// Labelling only; the signature is not verified.
func Email(value string) string {
	v := strings.TrimSpace(value)
	idToken := v
	if strings.HasPrefix(v, "{") {
		f, err := Parse(v)
		if err != nil {
			return ""
		}
		idToken = f.Tokens.IDToken
	}
	claims, ok := jwtClaims(idToken)
	if !ok {
		return ""
	}
	s, _ := claims["email"].(string)
	return strings.TrimSpace(s)
}

func (c *DeviceClient) postJSON(ctx context.Context, u string, body []byte) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", u, err)
	}
	return resp, nil
}
