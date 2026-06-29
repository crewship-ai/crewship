package auth

import (
	"crypto/rand"
	"crypto/rsa"
	"encoding/base64"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/go-jose/go-jose/v4"
)

// These tests cover finding T0.5 from the 2026-06 security audit
// (.claude/context/SECURITY-AUDIT-2026-06.md): JWE alg/kind confusion and
// token forgery resistance against internal/auth/jwt.go.
//
// Unlike the scrubber/docker tripwires, this finding is ALREADY SECURE: the
// validator pins KeyAlgorithm DIRECT + ContentEncryption A256CBC_HS512 at parse
// time (jwt.go:170), rejects cross-kind tokens (ErrWrongKind), and enforces
// expiry + jti/sid presence at the validation boundary. These are therefore
// written as PASSING REGRESSION GUARDS — they assert the secure behavior so a
// future loosening of the parse allowlist or claim checks flips them to FAIL.
//
// Run: go test ./internal/auth/ -run Confusion -v

// forgeRSAJWE builds a JWE encrypted with an asymmetric algorithm (RSA-OAEP +
// A256GCM) under an attacker-controlled key. A validator that didn't pin the
// permitted KeyAlgorithm to DIRECT could be coaxed into an algorithm-confusion
// decrypt; ours rejects it at ParseEncrypted before any key material is used.
func forgeRSAJWE(t *testing.T, claims map[string]interface{}) string {
	t.Helper()

	priv, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("rsa keygen: %v", err)
	}
	enc, err := jose.NewEncrypter(
		jose.A256GCM,
		jose.Recipient{Algorithm: jose.RSA_OAEP, Key: &priv.PublicKey},
		(&jose.EncrypterOptions{}).WithType("JWT"),
	)
	if err != nil {
		t.Fatalf("new rsa encrypter: %v", err)
	}
	payload, err := json.Marshal(claims)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	jwe, err := enc.Encrypt(payload)
	if err != nil {
		t.Fatalf("encrypt: %v", err)
	}
	compact, err := jwe.CompactSerialize()
	if err != nil {
		t.Fatalf("serialize: %v", err)
	}
	return compact
}

// forgeDirectGCMJWE encrypts with the correct DIRECT key algorithm but the
// WRONG content encryption (A256GCM instead of the pinned A256CBC_HS512). This
// isolates the ContentEncryption pin from the KeyAlgorithm pin.
func forgeDirectGCMJWE(t *testing.T, claims map[string]interface{}, secret, salt string) string {
	t.Helper()

	// A256GCM needs a 32-byte symmetric key.
	key, err := deriveEncryptionKey(secret, salt, 32)
	if err != nil {
		t.Fatalf("derive key: %v", err)
	}
	enc, err := jose.NewEncrypter(
		jose.A256GCM,
		jose.Recipient{Algorithm: jose.DIRECT, Key: key},
		(&jose.EncrypterOptions{}).WithType("JWT"),
	)
	if err != nil {
		t.Fatalf("new gcm encrypter: %v", err)
	}
	payload, err := json.Marshal(claims)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	jwe, err := enc.Encrypt(payload)
	if err != nil {
		t.Fatalf("encrypt: %v", err)
	}
	compact, err := jwe.CompactSerialize()
	if err != nil {
		t.Fatalf("serialize: %v", err)
	}
	return compact
}

// forgeAlgNoneToken hand-crafts a 5-segment compact token whose protected
// header advertises {"alg":"none"}. There is no legitimate "none" key algorithm
// for JWE; the validator must refuse to even parse it.
func forgeAlgNoneToken(t *testing.T, claims map[string]interface{}) string {
	t.Helper()

	header := map[string]string{"alg": "none", "enc": "A256CBC-HS512", "typ": "JWT"}
	hb, err := json.Marshal(header)
	if err != nil {
		t.Fatalf("marshal header: %v", err)
	}
	pb, err := json.Marshal(claims)
	if err != nil {
		t.Fatalf("marshal claims: %v", err)
	}
	b64 := func(b []byte) string { return base64.RawURLEncoding.EncodeToString(b) }
	// header . encrypted_key(empty) . iv . ciphertext . tag
	return strings.Join([]string{
		b64(hb), "", b64([]byte("iv-placeholder")), b64(pb), b64([]byte("tag-placeholder")),
	}, ".")
}

// TestForgeryConfusion_AllRejected is the single invariant sweep for T0.5: every
// forgery / confusion class below must be rejected by ValidateAccess. Each case
// names the attack and the validate function it is presented to.
func TestForgeryConfusion_AllRejected(t *testing.T) {
	v, err := NewJWTValidator(testSecret)
	if err != nil {
		t.Fatalf("new validator: %v", err)
	}

	// Pre-mint legitimately-typed tokens so the cross-kind cases below test the
	// kind/salt separation rather than a coincidental empty-token rejection.
	refresh, err := v.IssueRefreshToken("u1", "s1")
	if err != nil {
		t.Fatalf("issue refresh: %v", err)
	}
	ws, err := v.IssueWSTicket("u1", "s1", "", "")
	if err != nil {
		t.Fatalf("issue ws: %v", err)
	}
	access, err := v.IssueAccessToken("u1", "s1", "", "")
	if err != nil {
		t.Fatalf("issue access: %v", err)
	}

	validClaims := func() map[string]interface{} {
		return map[string]interface{}{
			"id":   "attacker",
			"sid":  "s1",
			"kind": KindAccess,
			"jti":  "forged-jti",
			"exp":  time.Now().Add(time.Hour).Unix(),
			"iat":  time.Now().Unix(),
		}
	}

	cases := []struct {
		name    string
		fn      func(string) (*Claims, error)
		token   string
		wantErr error
	}{
		{
			// (a) algorithm confusion: RSA-encrypted JWE presented where only
			// DIRECT is permitted — must be refused at parse.
			name:    "rsa_oaep_forgery_to_access",
			fn:      v.ValidateAccess,
			token:   forgeRSAJWE(t, validClaims()),
			wantErr: ErrInvalidToken,
		},
		{
			// (a') alg:none — no key algorithm at all.
			name:    "alg_none_to_access",
			fn:      v.ValidateAccess,
			token:   forgeAlgNoneToken(t, validClaims()),
			wantErr: ErrInvalidToken,
		},
		{
			// content-encryption confusion: DIRECT but A256GCM, not the pinned
			// A256CBC-HS512.
			name:    "direct_gcm_enc_confusion_to_access",
			fn:      v.ValidateAccess,
			token:   forgeDirectGCMJWE(t, validClaims(), testSecret, saltAccess),
			wantErr: ErrInvalidToken,
		},
		{
			// (b) refresh token presented to ValidateAccess. Different salt ⇒
			// decrypt fails before the kind check ⇒ ErrInvalidToken.
			name:    "refresh_as_access",
			fn:      v.ValidateAccess,
			token:   refresh,
			wantErr: ErrInvalidToken,
		},
		{
			// (c) WS ticket presented as an access/cookie token.
			name:    "ws_ticket_as_access",
			fn:      v.ValidateAccess,
			token:   ws,
			wantErr: ErrInvalidToken,
		},
		{
			// (c') symmetric: access token smuggled into the WS validator.
			name:    "access_as_ws",
			fn:      v.ValidateWS,
			token:   access,
			wantErr: ErrInvalidToken,
		},
		{
			// kind-claim forgery: encrypted under the ACCESS key (so it
			// decrypts) but carries kind=refresh ⇒ ErrWrongKind.
			name: "right_key_wrong_kind_claim",
			fn:   v.ValidateAccess,
			token: encryptTestTokenWithSalt(t, map[string]interface{}{
				"id":   "u1",
				"sid":  "s1",
				"kind": KindRefresh,
				"jti":  "j1",
				"exp":  time.Now().Add(time.Hour).Unix(),
				"iat":  time.Now().Unix(),
			}, testSecret, saltAccess),
			wantErr: ErrWrongKind,
		},
		{
			// (d) expired token (decrypts under the access key) ⇒ ErrTokenExpired.
			name: "expired_access",
			fn:   v.ValidateAccess,
			token: encryptTestTokenWithSalt(t, map[string]interface{}{
				"id":   "u1",
				"sid":  "s1",
				"kind": KindAccess,
				"jti":  "j1",
				"exp":  time.Now().Add(-time.Hour).Unix(),
				"iat":  time.Now().Add(-2 * time.Hour).Unix(),
			}, testSecret, saltAccess),
			wantErr: ErrTokenExpired,
		},
		{
			// (e) empty jti — the revocation/rotation invariant ⇒ ErrInvalidToken.
			name: "empty_jti",
			fn:   v.ValidateAccess,
			token: encryptTestTokenWithSalt(t, map[string]interface{}{
				"id":   "u1",
				"sid":  "s1",
				"kind": KindAccess,
				"exp":  time.Now().Add(time.Hour).Unix(),
				"iat":  time.Now().Unix(),
			}, testSecret, saltAccess),
			wantErr: ErrInvalidToken,
		},
		{
			// (e') empty sid on an access token ⇒ ErrInvalidToken (sid is what
			// makes the token revocable).
			name: "empty_sid_access",
			fn:   v.ValidateAccess,
			token: encryptTestTokenWithSalt(t, map[string]interface{}{
				"id":   "u1",
				"kind": KindAccess,
				"jti":  "j1",
				"exp":  time.Now().Add(time.Hour).Unix(),
				"iat":  time.Now().Unix(),
			}, testSecret, saltAccess),
			wantErr: ErrInvalidToken,
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			claims, err := c.fn(c.token)
			if claims != nil {
				t.Fatalf("T0.5 REGRESSION: forgery %q ACCEPTED — returned claims %+v", c.name, claims)
			}
			if !errors.Is(err, c.wantErr) {
				t.Fatalf("T0.5: forgery %q got err %v, want %v", c.name, err, c.wantErr)
			}
			t.Logf("T0.5 ok: %q rejected with %v", c.name, err)
		})
	}
}

// TestForgeryConfusion_RefreshValidatorSymmetric guards the converse direction:
// an access token (and a WS ticket) must not satisfy ValidateRefresh either.
func TestForgeryConfusion_RefreshValidatorSymmetric(t *testing.T) {
	v, err := NewJWTValidator(testSecret)
	if err != nil {
		t.Fatalf("new validator: %v", err)
	}
	access, err := v.IssueAccessToken("u1", "s1", "", "")
	if err != nil {
		t.Fatalf("issue access: %v", err)
	}
	ws, err := v.IssueWSTicket("u1", "s1", "", "")
	if err != nil {
		t.Fatalf("issue ws: %v", err)
	}
	for name, tok := range map[string]string{"access_as_refresh": access, "ws_as_refresh": ws} {
		t.Run(name, func(t *testing.T) {
			if _, err := v.ValidateRefresh(tok); !errors.Is(err, ErrInvalidToken) {
				t.Fatalf("T0.5: %q got %v, want ErrInvalidToken", name, err)
			}
		})
	}
}
