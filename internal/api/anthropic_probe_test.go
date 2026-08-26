package api

// The rule these tests exist to hold: a check that could not run must not be
// reported as a check that passed.
//
// probeProviderInner used to return Valid:true with the text "OAuth token
// accepted (cannot validate via API, will be verified at runtime)" for every
// sk-ant-oat token, having contacted nothing. That is the credential type
// Crewship's onboarding accepts, and that function backs /credentials/test,
// /credentials/{id}/test, the "Test now" button and `crewship credential
// test-stored` — so the only tools anyone had for checking a key all answered
// "fine" without asking. The claim in the comment was disproven by
// probeAnthropicOAuthToken sitting in the same package.

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// withAnthropicStub points the probe at a local server for the duration of a
// test. Restores the real endpoint even if the test fails.
func withAnthropicStub(t *testing.T, h http.HandlerFunc) {
	t.Helper()
	srv := httptest.NewServer(h)
	prev := anthropicAPIBase
	anthropicAPIBase = srv.URL
	t.Cleanup(func() {
		anthropicAPIBase = prev
		srv.Close()
	})
}

func TestProbeAnthropicCredential(t *testing.T) {
	cases := []struct {
		name             string
		status           int
		errType          string
		wantOK           bool
		wantRejected     bool
		wantModelMissing bool
		why              string
	}{
		{
			name: "accepted", status: 200, wantOK: true,
			why: "a working token must read as working",
		},
		{
			name: "unauthorised", status: 401, errType: "authentication_error", wantRejected: true,
			why: "the case the old short-circuit reported as valid",
		},
		{
			name: "forbidden", status: 403, errType: "permission_error", wantRejected: true,
			why: "revoked keys answer 403, not 401",
		},
		{
			name: "model not found", status: 404, errType: "not_found_error", wantModelMissing: true,
			why: "a token can authenticate and still have no access to the chosen model — " +
				"different problem, different fix, and the old probe could not see it at all",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			withAnthropicStub(t, func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(tc.status)
				_ = json.NewEncoder(w).Encode(map[string]any{
					"error": map[string]string{"type": tc.errType},
				})
			})
			got := probeAnthropicCredential(context.Background(), "sk-ant-oat01-x", "claude-sonnet-5")
			if !got.Reached {
				t.Fatal("Reached=false against a live stub — the probe never got an answer")
			}
			if got.OK() != tc.wantOK {
				t.Errorf("OK()=%v want %v — %s", got.OK(), tc.wantOK, tc.why)
			}
			if got.Rejected() != tc.wantRejected {
				t.Errorf("Rejected()=%v want %v — %s", got.Rejected(), tc.wantRejected, tc.why)
			}
			if got.ModelUnavailable() != tc.wantModelMissing {
				t.Errorf("ModelUnavailable()=%v want %v — %s", got.ModelUnavailable(), tc.wantModelMissing, tc.why)
			}
		})
	}
}

// The decisive one. An unreachable provider is neither a good nor a bad
// credential, and must not be rendered as either.
func TestProbeAnthropicCredentialUnreachableIsNotAVerdict(t *testing.T) {
	prev := anthropicAPIBase
	// A port nothing listens on: the request fails at the transport layer.
	anthropicAPIBase = "http://127.0.0.1:1"
	t.Cleanup(func() { anthropicAPIBase = prev })

	got := probeAnthropicCredential(context.Background(), "sk-ant-oat01-x", "")
	if got.Reached {
		t.Fatal("Reached=true with nothing listening")
	}
	if got.OK() {
		t.Error("an unreachable provider reported the credential as OK — this is the bug class")
	}
	if got.Rejected() {
		t.Error("an unreachable provider reported the credential as rejected — equally wrong, " +
			"it would send someone to regenerate a token that is fine")
	}
	msg := anthropicProbeMessage(got)
	if msg == "" {
		t.Error("no message for an unchecked credential; silence reads as success")
	}
}

// The auth header differs by credential shape, and getting it wrong makes a
// good token look rejected — which is how "OAuth cannot be validated" became
// folklore in the first place.
func TestProbeAnthropicCredentialUsesTheRightAuthHeader(t *testing.T) {
	cases := []struct {
		name       string
		token      string
		wantBearer bool
	}{
		{name: "oauth cli token", token: "sk-ant-oat01-abc", wantBearer: true},
		{name: "raw api key", token: "sk-ant-api03-abc", wantBearer: false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var sawAuth, sawAPIKey, sawBeta string
			withAnthropicStub(t, func(w http.ResponseWriter, r *http.Request) {
				sawAuth = r.Header.Get("Authorization")
				sawAPIKey = r.Header.Get("x-api-key")
				sawBeta = r.Header.Get("anthropic-beta")
				w.WriteHeader(http.StatusOK)
				_, _ = w.Write([]byte(`{}`))
			})
			probeAnthropicCredential(context.Background(), tc.token, "")

			if tc.wantBearer {
				if sawAuth != "Bearer "+tc.token {
					t.Errorf("Authorization = %q, want a Bearer with the token", sawAuth)
				}
				if sawBeta == "" {
					t.Error("no anthropic-beta header; an OAuth token is refused without the oauth beta")
				}
				if sawAPIKey != "" {
					t.Error("x-api-key sent alongside a Bearer token")
				}
			} else {
				if sawAPIKey != tc.token {
					t.Errorf("x-api-key = %q, want the raw key", sawAPIKey)
				}
				if sawAuth != "" {
					t.Error("Bearer sent for a raw API key")
				}
			}
		})
	}
}

// The model the user picked must be the model that gets probed. Hardcoding a
// cheap one meant onboarding verified a question nobody asked.
func TestProbeAnthropicCredentialProbesTheRequestedModel(t *testing.T) {
	var got string
	withAnthropicStub(t, func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Model string `json:"model"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		got = body.Model
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{}`))
	})

	probeAnthropicCredential(context.Background(), "sk-ant-oat01-x", "claude-opus-5")
	if got != "claude-opus-5" {
		t.Errorf("probed model = %q, want claude-opus-5", got)
	}

	probeAnthropicCredential(context.Background(), "sk-ant-oat01-x", "")
	if got != anthropicProbeModel {
		t.Errorf("probed model = %q with no model given, want the cheap default %q", got, anthropicProbeModel)
	}
}

// probeProviderInner is the function the Test endpoints and the CLI call.
func TestProbeProviderInnerActuallyChecksOAuthTokens(t *testing.T) {
	t.Run("a rejected token is reported as invalid", func(t *testing.T) {
		withAnthropicStub(t, func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`{"error":{"type":"authentication_error"}}`))
		})
		res := probeProviderInner(context.Background(), "ANTHROPIC", "AI_CLI_TOKEN", "sk-ant-oat01-bad", false)
		if res.Valid {
			t.Error("a 401 OAuth token reported Valid — this is exactly what shipped")
		}
		if res.Error == "" {
			t.Error("no reason given for an invalid token")
		}
	})

	t.Run("a good token is reported as valid", func(t *testing.T) {
		withAnthropicStub(t, func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{}`))
		})
		res := probeProviderInner(context.Background(), "ANTHROPIC", "AI_CLI_TOKEN", "sk-ant-oat01-good", false)
		if !res.Valid {
			t.Errorf("a 200 token reported invalid: %q", res.Error)
		}
	})

	t.Run("a model 404 still reports the credential as valid", func(t *testing.T) {
		withAnthropicStub(t, func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"error":{"type":"not_found_error"}}`))
		})
		res := probeProviderInner(context.Background(), "ANTHROPIC", "AI_CLI_TOKEN", "sk-ant-oat01-good", false)
		if !res.Valid {
			t.Errorf("an authenticated token was rejected because the probe model was unavailable: %q", res.Error)
		}
		if res.Status != http.StatusNotFound {
			t.Errorf("status = %d, want 404 retained for diagnostics", res.Status)
		}
	})

	t.Run("rate limiting still reports the credential as valid", func(t *testing.T) {
		withAnthropicStub(t, func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusTooManyRequests)
			_, _ = w.Write([]byte(`{"error":{"type":"rate_limit_error"}}`))
		})
		res := probeProviderInner(context.Background(), "ANTHROPIC", "AI_CLI_TOKEN", "sk-ant-oat01-good", false)
		if !res.Valid {
			t.Errorf("an authenticated, rate-limited token was reported invalid: %q", res.Error)
		}
	})

	t.Run("an unreachable provider is not reported as valid", func(t *testing.T) {
		prev := anthropicAPIBase
		anthropicAPIBase = "http://127.0.0.1:1"
		t.Cleanup(func() { anthropicAPIBase = prev })

		res := probeProviderInner(context.Background(), "ANTHROPIC", "AI_CLI_TOKEN", "sk-ant-oat01-x", false)
		if res.Valid {
			t.Error("an unchecked token reported Valid — the precise defect this replaces")
		}
	})
}
