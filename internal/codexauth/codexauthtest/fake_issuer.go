// Package codexauthtest is a fake auth.openai.com for tests of the Codex
// device-code sign-in. It answers the four endpoints codexauth.DeviceClient
// speaks with the shapes documented in internal/codexauth/device.go, and lets
// a test decide when the person "finishes the browser step". Nothing here
// ever reaches OpenAI.
package codexauthtest

import (
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
)

// FakeIssuer is one httptest server standing in for auth.openai.com.
type FakeIssuer struct {
	Server *httptest.Server

	mu sync.Mutex
	// Authorized flips the token endpoint from 403 to 200.
	authorized bool
	// PollStatus overrides the not-yet answer (403 by default) — set 429 to
	// exercise the slow-down path, 400 to exercise a denial.
	pollStatus int
	retryAfter string
	// StartStatus overrides the usercode endpoint's status (200 by default).
	startStatus int
	// Interval is what the usercode endpoint reports, as the STRING Codex
	// deserialises.
	Interval string
	// Counters a test can assert on.
	StartCalls, PollCalls, ExchangeCalls int
	// The last poll body, so a test can check the ids round-tripped.
	LastPoll map[string]string
	// The last exchange form, so a test can check the PKCE verifier and
	// redirect_uri round-tripped.
	LastExchange map[string]string
	// Tokens returned by the exchange; JWTs built by NewFakeIssuer.
	IDToken, AccessToken, RefreshToken string
}

// FakeJWT builds an unsigned JWT with the given claims — the shape
// codexauth reads plan, account and email from.
func FakeJWT(t testing.TB, claims map[string]any) string {
	t.Helper()
	payload, err := json.Marshal(claims)
	if err != nil {
		t.Fatal(err)
	}
	header := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"RS256","typ":"JWT"}`))
	return header + "." + base64.RawURLEncoding.EncodeToString(payload) + ".sig"
}

// NewFakeIssuer starts the fake. plan and email land in the tokens the
// exchange returns: plan in the access token's auth claim (where Codex reads
// it), email and account id in the id_token (where Codex's persist reads the
// account id).
func NewFakeIssuer(t testing.TB, plan, email string) *FakeIssuer {
	t.Helper()
	f := &FakeIssuer{
		pollStatus:  http.StatusForbidden,
		startStatus: http.StatusOK,
		Interval:    "1",
		IDToken: FakeJWT(t, map[string]any{
			"email": email,
			"https://api.openai.com/auth": map[string]any{
				"chatgpt_account_id": "acct-device-1",
			},
		}),
		AccessToken: FakeJWT(t, map[string]any{
			"exp": float64(1789000000),
			"https://api.openai.com/auth": map[string]any{
				"chatgpt_plan_type":  plan,
				"chatgpt_account_id": "acct-device-1",
			},
		}),
		RefreshToken: "rt.FAKE-REFRESH-SECRET",
	}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/accounts/deviceauth/usercode", f.usercode)
	mux.HandleFunc("POST /api/accounts/deviceauth/token", f.token)
	mux.HandleFunc("POST /oauth/token", f.exchange)
	f.Server = httptest.NewServer(mux)
	t.Cleanup(f.Server.Close)
	return f
}

// URL is the issuer base URL to hand codexauth.NewDeviceClient.
func (f *FakeIssuer) URL() string { return f.Server.URL }

// Authorize simulates the person finishing the browser step.
func (f *FakeIssuer) Authorize() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.authorized = true
}

// SetPollStatus makes the not-yet answer carry this status (and Retry-After
// when given), until Authorize.
func (f *FakeIssuer) SetPollStatus(status int, retryAfter string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.pollStatus = status
	f.retryAfter = retryAfter
}

// SetStartStatus makes the usercode endpoint answer with this status.
func (f *FakeIssuer) SetStartStatus(status int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.startStatus = status
}

// Calls returns the three counters under the lock.
func (f *FakeIssuer) Calls() (start, poll, exchange int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.StartCalls, f.PollCalls, f.ExchangeCalls
}

func (f *FakeIssuer) usercode(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.StartCalls++
	var body map[string]string
	_ = json.NewDecoder(r.Body).Decode(&body)
	if body["client_id"] == "" {
		http.Error(w, `{"error":"client_id required"}`, http.StatusBadRequest)
		return
	}
	if f.startStatus != http.StatusOK {
		http.Error(w, `{"error":"nope"}`, f.startStatus)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"device_auth_id": "dev-auth-123",
		"user_code":      "ABCD-EFGHJ",
		"interval":       f.Interval,
	})
}

func (f *FakeIssuer) token(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.PollCalls++
	var body map[string]string
	_ = json.NewDecoder(r.Body).Decode(&body)
	f.LastPoll = body
	if body["device_auth_id"] != "dev-auth-123" || body["user_code"] != "ABCD-EFGHJ" {
		http.Error(w, `{"error":"unknown device"}`, http.StatusBadRequest)
		return
	}
	if !f.authorized {
		if f.retryAfter != "" {
			w.Header().Set("Retry-After", f.retryAfter)
		}
		http.Error(w, `{"error":"authorization_pending"}`, f.pollStatus)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"authorization_code": "authz-code-xyz",
		"code_challenge":     "chal",
		"code_verifier":      "verifier-abc",
	})
}

func (f *FakeIssuer) exchange(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.ExchangeCalls++
	_ = r.ParseForm()
	f.LastExchange = map[string]string{}
	for k := range r.PostForm {
		f.LastExchange[k] = r.PostForm.Get(k)
	}
	if r.PostForm.Get("grant_type") != "authorization_code" || r.PostForm.Get("code") != "authz-code-xyz" ||
		r.PostForm.Get("code_verifier") != "verifier-abc" {
		http.Error(w, `{"error":"invalid_grant"}`, http.StatusBadRequest)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"id_token":      f.IDToken,
		"access_token":  f.AccessToken,
		"refresh_token": f.RefreshToken,
		"token_type":    "Bearer",
		"expires_in":    864000,
	})
}
