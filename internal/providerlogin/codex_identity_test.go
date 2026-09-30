package providerlogin

import (
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"io"
	"math/big"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type identityTransport func(*http.Request) (*http.Response, error)

func (f identityTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
func identityClaims(now time.Time, audience any) map[string]any {
	return map[string]any{"iss": openAIIssuer, "aud": audience, "sub": "subject-own", "iat": now.Unix() - 60, "nbf": now.Unix() - 60, "exp": now.Unix() + 3600, "https://api.openai.com/auth": map[string]any{"chatgpt_account_id": "account-own", "chatgpt_user_id": "user-own", "chatgpt_plan_type": "plus"}}
}
func signIdentity(t *testing.T, key *rsa.PrivateKey, kid string, claims map[string]any) string {
	t.Helper()
	payload, e := json.Marshal(claims)
	if e != nil {
		t.Fatal(e)
	}
	return signIdentityRaw(t, key, kid, payload)
}
func signIdentityRaw(t *testing.T, key *rsa.PrivateKey, kid string, payload []byte) string {
	t.Helper()
	header, _ := json.Marshal(map[string]string{"alg": "RS256", "typ": "JWT", "kid": kid})
	message := base64.RawURLEncoding.EncodeToString(header) + "." + base64.RawURLEncoding.EncodeToString(payload)
	hash := sha256.Sum256([]byte(message))
	sig, e := rsa.SignPKCS1v15(rand.Reader, key, crypto.SHA256, hash[:])
	if e != nil {
		t.Fatal(e)
	}
	return message + "." + base64.RawURLEncoding.EncodeToString(sig)
}
func identityKey(t *testing.T) *rsa.PrivateKey {
	t.Helper()
	key, e := rsa.GenerateKey(rand.Reader, 2048)
	if e != nil {
		t.Fatal(e)
	}
	return key
}
func identityJWK(key *rsa.PrivateKey, kid string) map[string]string {
	return map[string]string{"kty": "RSA", "kid": kid, "alg": "RS256", "use": "sig", "n": base64.RawURLEncoding.EncodeToString(key.N.Bytes()), "e": base64.RawURLEncoding.EncodeToString(big.NewInt(int64(key.E)).Bytes())}
}
func identityResponse(raw []byte) *http.Response {
	return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": []string{"application/json"}}, Body: io.NopCloser(strings.NewReader(string(raw)))}
}
func identityVerifier(t *testing.T, key *rsa.PrivateKey, now time.Time) (*CodexIdentityVerifier, *atomic.Int32) {
	t.Helper()
	calls := new(atomic.Int32)
	raw, _ := json.Marshal(map[string]any{"keys": []any{identityJWK(key, "own-key")}})
	v := NewCodexIdentityVerifier()
	v.now = func() time.Time { return now }
	v.client.Transport = identityTransport(func(r *http.Request) (*http.Response, error) {
		calls.Add(1)
		if r.URL.String() != openAIJWKS || r.Method != "GET" || r.Header.Get("Authorization") != "" {
			t.Errorf("unexpected JWKS request")
		}
		return identityResponse(raw), nil
	})
	return v, calls
}

func TestCodexIdentityRejectsUnsignedForeignOrAmbiguousClaims(t *testing.T) {
	now := time.Unix(1800000000, 0)
	key := identityKey(t)
	v, calls := identityVerifier(t, key, now)
	id := signIdentity(t, key, "own-key", identityClaims(now, OpenAIClientID))
	access := signIdentity(t, key, "own-key", identityClaims(now, "https://api.openai.com/v1"))
	identity, e := v.Verify(t.Context(), id, access, "account-own")
	if e != nil || identity.Subject != "subject-own" || identity.AccountID != "account-own" || identity.UserID != "user-own" || identity.Plan != "plus" || calls.Load() != 1 {
		t.Fatalf("identity=%+v error=%v fetches=%d", identity, e, calls.Load())
	}
	for name, change := range map[string]func(map[string]any){
		"issuer":             func(m map[string]any) { m["iss"] = "https://foreign.invalid" },
		"audience":           func(m map[string]any) { m["aud"] = "foreign-client" },
		"duplicate audience": func(m map[string]any) { m["aud"] = []string{OpenAIClientID, OpenAIClientID} },
		"expired":            func(m map[string]any) { m["exp"] = now.Unix() },
		"missing expiry":     func(m map[string]any) { delete(m, "exp") },
		"fractional expiry":  func(m map[string]any) { m["exp"] = float64(now.Unix()) + .5 },
		"unbounded expiry":   func(m map[string]any) { m["exp"] = now.Add(9 * 24 * time.Hour).Unix() },
		"future issue":       func(m map[string]any) { m["iat"] = now.Add(time.Minute).Unix() },
		"not yet valid":      func(m map[string]any) { m["nbf"] = now.Add(time.Minute).Unix() },
		"subject":            func(m map[string]any) { m["sub"] = "foreign-subject" },
		"account": func(m map[string]any) {
			m["https://api.openai.com/auth"].(map[string]any)["chatgpt_account_id"] = "foreign-account"
		},
		"missing account": func(m map[string]any) {
			delete(m["https://api.openai.com/auth"].(map[string]any), "chatgpt_account_id")
		},
		"user": func(m map[string]any) {
			m["https://api.openai.com/auth"].(map[string]any)["chatgpt_user_id"] = "foreign-user"
		},
		"conflicting user": func(m map[string]any) { m["https://api.openai.com/auth"].(map[string]any)["user_id"] = "foreign-user" },
		"plan": func(m map[string]any) {
			m["https://api.openai.com/auth"].(map[string]any)["chatgpt_plan_type"] = "foreign-plan"
		},
	} {
		t.Run(name, func(t *testing.T) {
			m := identityClaims(now, OpenAIClientID)
			change(m)
			token := signIdentity(t, key, "own-key", m)
			if _, e := v.Verify(t.Context(), token, access, "account-own"); e == nil {
				t.Fatal("unbound identity admitted")
			}
		})
	}
	if _, e := v.Verify(t.Context(), id, access, "foreign-account"); e == nil {
		t.Fatal("caller account overrode signed account")
	}
	other := identityKey(t)
	forged := signIdentity(t, other, "own-key", identityClaims(now, OpenAIClientID))
	if _, e := v.Verify(t.Context(), forged, access, "account-own"); e == nil {
		t.Fatal("forged signature admitted")
	}
	duplicate := []byte(`{"iss":"https://foreign.invalid","iss":"https://auth.openai.com","aud":"` + OpenAIClientID + `","sub":"subject-own","iat":1799999940,"exp":1800003600}`)
	if _, e := v.Verify(t.Context(), signIdentityRaw(t, key, "own-key", duplicate), access, "account-own"); e == nil {
		t.Fatal("duplicate signed claim admitted")
	}
	if calls.Load() != 1 {
		t.Fatal("ordinary claim failures amplified key fetches")
	}
}

func TestCodexIdentityKeyRotationCacheAndUnknownKidBound(t *testing.T) {
	now := time.Unix(1800000000, 0)
	old := identityKey(t)
	next := identityKey(t)
	v, calls := identityVerifier(t, old, now)
	var clockMu sync.Mutex
	v.now = func() time.Time { clockMu.Lock(); defer clockMu.Unlock(); return now }
	id := signIdentity(t, old, "own-key", identityClaims(now, OpenAIClientID))
	access := signIdentity(t, old, "own-key", identityClaims(now, "https://api.openai.com/v1"))
	if _, e := v.Verify(t.Context(), id, access, "account-own"); e != nil {
		t.Fatal(e)
	}
	clockMu.Lock()
	now = now.Add(31 * time.Second)
	clockMu.Unlock()
	var wg sync.WaitGroup
	for i := 0; i < 32; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, e := v.Verify(t.Context(), signIdentity(t, next, "foreign-key", identityClaims(now, OpenAIClientID)), access, "account-own"); e == nil {
				t.Error("unknown key admitted")
			}
		}()
	}
	wg.Wait()
	if calls.Load() != 2 {
		t.Fatalf("unknown-kid fetch amplification: %d", calls.Load())
	}
	clockMu.Lock()
	now = now.Add(31 * time.Second)
	clockMu.Unlock()
	raw, _ := json.Marshal(map[string]any{"keys": []any{identityJWK(next, "next-key")}})
	v.client.Transport = identityTransport(func(*http.Request) (*http.Response, error) { calls.Add(1); return identityResponse(raw), nil })
	newID := signIdentity(t, next, "next-key", identityClaims(now, OpenAIClientID))
	newAccess := signIdentity(t, next, "next-key", identityClaims(now, "https://api.openai.com/v1"))
	if _, e := v.Verify(t.Context(), newID, newAccess, "account-own"); e != nil {
		t.Fatal("legitimate provider key rotation denied", e)
	}
	if calls.Load() != 3 {
		t.Fatal("rotation not shared")
	}
	clockMu.Lock()
	now = now.Add(6 * time.Minute)
	clockMu.Unlock()
	v.client.Transport = identityTransport(func(*http.Request) (*http.Response, error) {
		calls.Add(1)
		return &http.Response{StatusCode: 503, Header: http.Header{}, Body: io.NopCloser(strings.NewReader("unavailable"))}, nil
	})
	if _, e := v.Verify(t.Context(), newID, newAccess, "account-own"); e == nil {
		t.Fatal("expired key cache used through provider failure")
	}
}

func TestCodexIdentityProductionTransportAndMalformedJWKS(t *testing.T) {
	v := NewCodexIdentityVerifier()
	transport, ok := v.client.Transport.(*http.Transport)
	if !ok || transport.Proxy != nil || transport.DialContext == nil || v.client.Timeout != 3*time.Second || v.client.CheckRedirect(&http.Request{}, nil) != http.ErrUseLastResponse {
		t.Fatal("production JWKS transport relaxed")
	}
	now := time.Unix(1800000000, 0)
	key := identityKey(t)
	for _, raw := range []string{`{"keys":[]}`, `{"keys":[],"keys":[]}`, `{"keys":[{"kty":"RSA","kid":"own-key","n":"AQ","e":"AQAB"}]}`, strings.Repeat("x", (256<<10)+1)} {
		v, _ := identityVerifier(t, key, now)
		v.client.Transport = identityTransport(func(*http.Request) (*http.Response, error) { return identityResponse([]byte(raw)), nil })
		token := signIdentity(t, key, "own-key", identityClaims(now, OpenAIClientID))
		if _, e := v.Verify(context.Background(), token, token, "account-own"); e == nil {
			t.Fatal("malformed JWKS admitted")
		}
	}
}
