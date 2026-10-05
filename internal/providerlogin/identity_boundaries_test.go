package providerlogin

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"
)

func TestIdentityDocumentRejectsAmbiguousOrTruncatedInput(t *testing.T) {
	for _, raw := range []string{"", "[]", "null", "{} {}", "{", `{"x":`, `{"x":[`, `{"x":[1,`, `{"x":{`, `{"x":1,"x":2}`, `{"x":` + strings.Repeat("[", 18) + "0" + strings.Repeat("]", 18) + "}", "{\"x\":\"\xff\"}", strings.Repeat(" ", (256<<10)+1)} {
		if _, err := identityObject([]byte(raw)); !errors.Is(err, ErrUnverifiedCodexIdentity) {
			t.Fatalf("invalid document accepted: length=%d error=%v", len(raw), err)
		}
	}
}

func TestIdentityHeadersFailBeforeFetchingKeys(t *testing.T) {
	now := time.Unix(1800000000, 0)
	key := identityKey(t)
	v, calls := identityVerifier(t, key, now)
	for _, raw := range []string{`{}`, `{"alg":"none"}`, `{"alg":"RS256","kid":"own-key","jku":"https://foreign.invalid"}`, `{"alg":"RS256","kid":"own-key","typ":"opaque"}`, `{"alg":"RS256","kid":17}`, `{"alg":"RS256","kid":"bad key"}`} {
		token := base64.RawURLEncoding.EncodeToString([]byte(raw)) + ".e30.signature"
		if _, err := v.claims(t.Context(), token); !errors.Is(err, ErrUnverifiedCodexIdentity) {
			t.Fatalf("untrusted header accepted: %s %v", raw, err)
		}
	}
	for _, token := range []string{"", strings.Repeat("x", (64<<10)+1), "one.two", "!.e30.signature"} {
		if _, err := v.claims(t.Context(), token); !errors.Is(err, ErrUnverifiedCodexIdentity) {
			t.Fatalf("malformed token accepted: %v", err)
		}
	}
	if calls.Load() != 0 {
		t.Fatal("malformed header triggered key fetch")
	}
	valid := signIdentity(t, key, "own-key", identityClaims(now, OpenAIClientID))
	parts := strings.Split(valid, ".")
	parts[2] = "!"
	if _, err := v.claims(t.Context(), strings.Join(parts, ".")); !errors.Is(err, ErrUnverifiedCodexIdentity) {
		t.Fatal("invalid signature encoding accepted")
	}
}

func TestIdentityKeySetRejectsInvalidKeyMetadata(t *testing.T) {
	now := time.Unix(1800000000, 0)
	key := identityKey(t)
	for _, tc := range []struct {
		name, field string
		value       any
	}{
		{"wrong kind", "kty", "EC"}, {"missing id", "kid", nil}, {"wrong algorithm", "alg", "HS256"}, {"encryption key", "use", "enc"},
		{"wrong operations", "key_ops", []any{"sign"}}, {"wrong operation shape", "key_ops", "verify"},
		{"missing modulus", "n", nil}, {"large modulus encoding", "n", strings.Repeat("a", 1025)}, {"bad modulus encoding", "n", "!"}, {"weak modulus", "n", "AQ"},
		{"missing exponent", "e", nil}, {"large exponent encoding", "e", strings.Repeat("a", 17)}, {"bad exponent encoding", "e", "!"}, {"even exponent", "e", "Ag"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			jwk := map[string]any{}
			for k, v := range identityJWK(key, "own-key") {
				jwk[k] = v
			}
			jwk[tc.field] = tc.value
			raw, _ := json.Marshal(map[string]any{"keys": []any{jwk}})
			v, _ := identityVerifier(t, key, now)
			v.client.Transport = identityTransport(func(*http.Request) (*http.Response, error) { return identityResponse(raw), nil })
			if _, err := v.key(t.Context(), "own-key"); !errors.Is(err, ErrUnverifiedCodexIdentity) {
				t.Fatalf("invalid signing key accepted: %v", err)
			}
			if len(v.keys) != 0 {
				t.Fatal("failed key set polluted cache")
			}
		})
	}
	for _, raw := range []string{`{`, `{}`, `{"keys":[]}`, `{"keys":[null]}`, `{"keys":[1]}`} {
		v, _ := identityVerifier(t, key, now)
		v.client.Transport = identityTransport(func(*http.Request) (*http.Response, error) { return identityResponse([]byte(raw)), nil })
		if _, err := v.key(t.Context(), "own-key"); !errors.Is(err, ErrUnverifiedCodexIdentity) {
			t.Fatalf("malformed key set accepted: %v", err)
		}
	}
}

func TestIdentityRequiresCompleteVerifierAndSignedMetadata(t *testing.T) {
	now := time.Unix(1800000000, 0)
	key := identityKey(t)
	v, _ := identityVerifier(t, key, now)
	id := signIdentity(t, key, "own-key", identityClaims(now, OpenAIClientID))
	access := signIdentity(t, key, "own-key", identityClaims(now, "https://api.openai.com/v1"))
	for _, broken := range []*CodexIdentityVerifier{nil, {}, {client: &http.Client{}}} {
		if _, err := broken.Verify(t.Context(), id, access, "account-own"); !errors.Is(err, ErrUnverifiedCodexIdentity) {
			t.Fatal("incomplete verifier admitted identity")
		}
	}
	for _, bad := range []any{nil, "invalid", []any{}} {
		claims := identityClaims(now, OpenAIClientID)
		claims["https://api.openai.com/auth"] = bad
		if _, err := v.Verify(t.Context(), signIdentity(t, key, "own-key", claims), access, "account-own"); !errors.Is(err, ErrUnverifiedCodexIdentity) {
			t.Fatal("missing signed metadata admitted")
		}
	}
	for _, aud := range []any{nil, []any{}, []any{OpenAIClientID, 17}, []any{OpenAIClientID, "bad audience"}} {
		if audienceContains(aud, OpenAIClientID) {
			t.Fatal("malformed audience admitted")
		}
	}
	if signedUser(map[string]any{"user_id": "legacy-user"}) != "legacy-user" {
		t.Fatal("unambiguous signed legacy user lost")
	}
}
