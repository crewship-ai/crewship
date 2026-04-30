package auth

import (
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/go-jose/go-jose/v4"
)

const testSecret = "supersecretkeythatisatleast32chars!!"

func encryptTestTokenWithSalt(t *testing.T, claims map[string]interface{}, secret, salt string) string {
	t.Helper()

	key, err := deriveEncryptionKey(secret, salt, 64)
	if err != nil {
		t.Fatalf("derive key: %v", err)
	}

	encrypter, err := jose.NewEncrypter(jose.A256CBC_HS512, jose.Recipient{
		Algorithm: jose.DIRECT,
		Key:       key,
	}, (&jose.EncrypterOptions{}).WithType("JWT"))
	if err != nil {
		t.Fatalf("new encrypter: %v", err)
	}

	payload, err := json.Marshal(claims)
	if err != nil {
		t.Fatalf("marshal claims: %v", err)
	}

	jwe, err := encrypter.Encrypt(payload)
	if err != nil {
		t.Fatalf("encrypt: %v", err)
	}

	compact, err := jwe.CompactSerialize()
	if err != nil {
		t.Fatalf("serialize: %v", err)
	}
	return compact
}

func TestIssueAndValidateAccessToken(t *testing.T) {
	v, err := NewJWTValidator(testSecret)
	if err != nil {
		t.Fatalf("new validator: %v", err)
	}

	token, err := v.IssueAccessToken("user_123", "sess_abc", "Test User", "test@example.com")
	if err != nil {
		t.Fatalf("issue: %v", err)
	}

	claims, err := v.ValidateAccess(token)
	if err != nil {
		t.Fatalf("validate: %v", err)
	}

	if claims.ID != "user_123" {
		t.Errorf("id: got %q want user_123", claims.ID)
	}
	if claims.Sid != "sess_abc" {
		t.Errorf("sid: got %q want sess_abc", claims.Sid)
	}
	if claims.Kind != KindAccess {
		t.Errorf("kind: got %q want %q", claims.Kind, KindAccess)
	}
	if claims.Name != "Test User" {
		t.Errorf("name: got %q want Test User", claims.Name)
	}
	if claims.Email != "test@example.com" {
		t.Errorf("email: got %q want test@example.com", claims.Email)
	}
	if claims.Jti == "" {
		t.Error("jti should be non-empty")
	}
}

func TestIssueAndValidateRefreshToken(t *testing.T) {
	v, err := NewJWTValidator(testSecret)
	if err != nil {
		t.Fatalf("new validator: %v", err)
	}

	token, err := v.IssueRefreshToken("user_123", "sess_abc")
	if err != nil {
		t.Fatalf("issue: %v", err)
	}

	claims, err := v.ValidateRefresh(token)
	if err != nil {
		t.Fatalf("validate: %v", err)
	}

	if claims.Kind != KindRefresh {
		t.Errorf("kind: got %q want %q", claims.Kind, KindRefresh)
	}
	if claims.Sid != "sess_abc" {
		t.Errorf("sid: got %q want sess_abc", claims.Sid)
	}
}

func TestIssueAndValidateWSTicket(t *testing.T) {
	v, err := NewJWTValidator(testSecret)
	if err != nil {
		t.Fatalf("new validator: %v", err)
	}

	token, err := v.IssueWSTicket("user_123", "sess_abc", "Test", "t@e.com")
	if err != nil {
		t.Fatalf("issue: %v", err)
	}

	claims, err := v.ValidateWS(token)
	if err != nil {
		t.Fatalf("validate: %v", err)
	}

	if claims.Kind != KindWS {
		t.Errorf("kind: got %q want %q", claims.Kind, KindWS)
	}
}

func TestKindCrossUseRejected(t *testing.T) {
	v, err := NewJWTValidator(testSecret)
	if err != nil {
		t.Fatalf("new validator: %v", err)
	}

	access, _ := v.IssueAccessToken("u1", "s1", "", "")
	refresh, _ := v.IssueRefreshToken("u1", "s1")
	ws, _ := v.IssueWSTicket("u1", "s1", "", "")

	// Each validator must refuse the other kinds. The salts are
	// different so decryption itself fails before we even reach the
	// kind check — both produce ErrInvalidToken in practice.
	cases := []struct {
		name    string
		fn      func(string) (*Claims, error)
		token   string
		wantErr error
	}{
		{"access vs refresh", v.ValidateAccess, refresh, ErrInvalidToken},
		{"access vs ws", v.ValidateAccess, ws, ErrInvalidToken},
		{"refresh vs access", v.ValidateRefresh, access, ErrInvalidToken},
		{"refresh vs ws", v.ValidateRefresh, ws, ErrInvalidToken},
		{"ws vs access", v.ValidateWS, access, ErrInvalidToken},
		{"ws vs refresh", v.ValidateWS, refresh, ErrInvalidToken},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := c.fn(c.token)
			if !errors.Is(err, c.wantErr) {
				t.Errorf("got %v, want %v", err, c.wantErr)
			}
		})
	}
}

func TestKindClaimMismatchRejected(t *testing.T) {
	// Forge a token at the access salt but with kind="refresh" — the
	// validator should reject it via ErrWrongKind even though it
	// decrypts cleanly with the access key.
	v, err := NewJWTValidator(testSecret)
	if err != nil {
		t.Fatalf("new validator: %v", err)
	}

	tok := encryptTestTokenWithSalt(t, map[string]interface{}{
		"id":   "u1",
		"sid":  "s1",
		"kind": KindRefresh,
		"exp":  time.Now().Add(time.Hour).Unix(),
		"iat":  time.Now().Unix(),
	}, testSecret, saltAccess)

	_, err = v.ValidateAccess(tok)
	if !errors.Is(err, ErrWrongKind) {
		t.Errorf("got %v, want ErrWrongKind", err)
	}
}

func TestValidateExpiredToken(t *testing.T) {
	v, err := NewJWTValidator(testSecret)
	if err != nil {
		t.Fatalf("new validator: %v", err)
	}

	// Forge an expired access token at the access salt.
	tok := encryptTestTokenWithSalt(t, map[string]interface{}{
		"id":   "u1",
		"sid":  "s1",
		"kind": KindAccess,
		"exp":  time.Now().Add(-time.Hour).Unix(),
		"iat":  time.Now().Add(-2 * time.Hour).Unix(),
	}, testSecret, saltAccess)

	_, err = v.ValidateAccess(tok)
	if !errors.Is(err, ErrTokenExpired) {
		t.Errorf("expected ErrTokenExpired, got %v", err)
	}
}

func TestValidateWrongSecret(t *testing.T) {
	v, err := NewJWTValidator("wrong-secret-that-is-long-enough!!")
	if err != nil {
		t.Fatalf("new validator: %v", err)
	}

	other, _ := NewJWTValidator(testSecret)
	tok, _ := other.IssueAccessToken("u1", "s1", "", "")

	_, err = v.ValidateAccess(tok)
	if !errors.Is(err, ErrInvalidToken) {
		t.Errorf("expected ErrInvalidToken, got %v", err)
	}
}

func TestValidateMissingUserID(t *testing.T) {
	v, err := NewJWTValidator(testSecret)
	if err != nil {
		t.Fatalf("new validator: %v", err)
	}

	tok := encryptTestTokenWithSalt(t, map[string]interface{}{
		"sid":   "s1",
		"kind":  KindAccess,
		"email": "test@example.com",
		"exp":   time.Now().Add(time.Hour).Unix(),
	}, testSecret, saltAccess)

	_, err = v.ValidateAccess(tok)
	if !errors.Is(err, ErrInvalidToken) {
		t.Errorf("expected ErrInvalidToken for missing id, got %v", err)
	}
}

func TestValidateGarbageToken(t *testing.T) {
	v, err := NewJWTValidator(testSecret)
	if err != nil {
		t.Fatalf("new validator: %v", err)
	}

	_, err = v.ValidateAccess("not.a.valid.jwe.token")
	if !errors.Is(err, ErrInvalidToken) {
		t.Errorf("expected ErrInvalidToken for garbage, got %v", err)
	}
}

func TestNewValidatorEmptySecret(t *testing.T) {
	_, err := NewJWTValidator("")
	if err == nil {
		t.Error("expected error for empty secret")
	}
}

func TestIssueRequiresUserAndSession(t *testing.T) {
	v, err := NewJWTValidator(testSecret)
	if err != nil {
		t.Fatalf("new validator: %v", err)
	}
	if _, err := v.IssueAccessToken("", "s1", "", ""); err == nil {
		t.Error("expected error for empty user id")
	}
	if _, err := v.IssueAccessToken("u1", "", "", ""); err == nil {
		t.Error("expected error for empty session id")
	}
}

func TestJtiUniqueness(t *testing.T) {
	v, _ := NewJWTValidator(testSecret)
	seen := map[string]bool{}
	for i := 0; i < 100; i++ {
		tok, err := v.IssueAccessToken("u1", "s1", "", "")
		if err != nil {
			t.Fatalf("issue: %v", err)
		}
		c, err := v.ValidateAccess(tok)
		if err != nil {
			t.Fatalf("validate: %v", err)
		}
		if c.Jti == "" {
			t.Fatal("empty jti")
		}
		if seen[c.Jti] {
			t.Fatalf("duplicate jti %q at i=%d", c.Jti, i)
		}
		seen[c.Jti] = true
	}
}

func TestDeriveEncryptionKeyLength(t *testing.T) {
	key, err := deriveEncryptionKey(testSecret, saltAccess, 64)
	if err != nil {
		t.Fatalf("derive: %v", err)
	}
	if len(key) != 64 {
		t.Errorf("expected 64 bytes, got %d", len(key))
	}
}

func TestDeriveEncryptionKeyDeterministic(t *testing.T) {
	k1, _ := deriveEncryptionKey(testSecret, saltAccess, 64)
	k2, _ := deriveEncryptionKey(testSecret, saltAccess, 64)

	if len(k1) != len(k2) {
		t.Fatal("key lengths differ")
	}
	for i := range k1 {
		if k1[i] != k2[i] {
			t.Fatal("keys differ -- HKDF should be deterministic")
		}
	}
}

func TestDifferentSaltsProduceDifferentKeys(t *testing.T) {
	a, _ := deriveEncryptionKey(testSecret, saltAccess, 64)
	r, _ := deriveEncryptionKey(testSecret, saltRefresh, 64)
	w, _ := deriveEncryptionKey(testSecret, saltWS, 64)

	if string(a) == string(r) || string(a) == string(w) || string(r) == string(w) {
		t.Fatal("salts produced equal keys — HKDF info string isn't separating")
	}
}
