package auth

import (
	"crypto/rand"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/go-jose/go-jose/v4"
)

const testSecret = "supersecretkeythatisatleast32chars!!"
const testSalt = "authjs.session-token"

func encryptTestToken(t *testing.T, claims map[string]interface{}, secret, salt string) string {
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

func TestValidateValidToken(t *testing.T) {
	v, err := NewJWTValidator(testSecret, testSalt)
	if err != nil {
		t.Fatalf("new validator: %v", err)
	}

	token := encryptTestToken(t, map[string]interface{}{
		"id":    "user_123",
		"name":  "Test User",
		"email": "test@example.com",
		"exp":   time.Now().Add(time.Hour).Unix(),
		"iat":   time.Now().Unix(),
		"jti":   "test-jti",
	}, testSecret, testSalt)

	claims, err := v.Validate(token)
	if err != nil {
		t.Fatalf("validate: %v", err)
	}

	if claims.ID != "user_123" {
		t.Errorf("expected id user_123, got %s", claims.ID)
	}
	if claims.Name != "Test User" {
		t.Errorf("expected name Test User, got %s", claims.Name)
	}
	if claims.Email != "test@example.com" {
		t.Errorf("expected email test@example.com, got %s", claims.Email)
	}
}

func TestValidateExpiredToken(t *testing.T) {
	v, err := NewJWTValidator(testSecret, testSalt)
	if err != nil {
		t.Fatalf("new validator: %v", err)
	}

	token := encryptTestToken(t, map[string]interface{}{
		"id":  "user_123",
		"exp": time.Now().Add(-time.Hour).Unix(),
		"iat": time.Now().Add(-2 * time.Hour).Unix(),
	}, testSecret, testSalt)

	_, err = v.Validate(token)
	if !errors.Is(err, ErrTokenExpired) {
		t.Errorf("expected ErrTokenExpired, got %v", err)
	}
}

func TestValidateWrongSecret(t *testing.T) {
	v, err := NewJWTValidator("wrong-secret-that-is-long-enough!!", testSalt)
	if err != nil {
		t.Fatalf("new validator: %v", err)
	}

	token := encryptTestToken(t, map[string]interface{}{
		"id":  "user_123",
		"exp": time.Now().Add(time.Hour).Unix(),
	}, testSecret, testSalt)

	_, err = v.Validate(token)
	if !errors.Is(err, ErrInvalidToken) {
		t.Errorf("expected ErrInvalidToken, got %v", err)
	}
}

func TestValidateMissingUserID(t *testing.T) {
	v, err := NewJWTValidator(testSecret, testSalt)
	if err != nil {
		t.Fatalf("new validator: %v", err)
	}

	token := encryptTestToken(t, map[string]interface{}{
		"email": "test@example.com",
		"exp":   time.Now().Add(time.Hour).Unix(),
	}, testSecret, testSalt)

	_, err = v.Validate(token)
	if !errors.Is(err, ErrInvalidToken) {
		t.Errorf("expected ErrInvalidToken for missing id, got %v", err)
	}
}

func TestValidateGarbageToken(t *testing.T) {
	v, err := NewJWTValidator(testSecret, testSalt)
	if err != nil {
		t.Fatalf("new validator: %v", err)
	}

	_, err = v.Validate("not.a.valid.jwe.token")
	if !errors.Is(err, ErrInvalidToken) {
		t.Errorf("expected ErrInvalidToken for garbage, got %v", err)
	}
}

func TestNewValidatorEmptySecret(t *testing.T) {
	_, err := NewJWTValidator("", testSalt)
	if err == nil {
		t.Error("expected error for empty secret")
	}
}

func TestNewValidatorDefaultSalt(t *testing.T) {
	v, err := NewJWTValidator(testSecret, "")
	if err != nil {
		t.Fatalf("new validator: %v", err)
	}

	token := encryptTestToken(t, map[string]interface{}{
		"id":  "user_456",
		"exp": time.Now().Add(time.Hour).Unix(),
	}, testSecret, "authjs.session-token")

	claims, err := v.Validate(token)
	if err != nil {
		t.Fatalf("validate: %v", err)
	}
	if claims.ID != "user_456" {
		t.Errorf("expected user_456, got %s", claims.ID)
	}
}

func TestDeriveEncryptionKeyLength(t *testing.T) {
	key, err := deriveEncryptionKey(testSecret, testSalt, 64)
	if err != nil {
		t.Fatalf("derive: %v", err)
	}
	if len(key) != 64 {
		t.Errorf("expected 64 bytes, got %d", len(key))
	}
}

func TestDeriveEncryptionKeyDeterministic(t *testing.T) {
	k1, _ := deriveEncryptionKey(testSecret, testSalt, 64)
	k2, _ := deriveEncryptionKey(testSecret, testSalt, 64)

	if len(k1) != len(k2) {
		t.Fatal("key lengths differ")
	}
	for i := range k1 {
		if k1[i] != k2[i] {
			t.Fatal("keys differ -- HKDF should be deterministic")
		}
	}
}

// Suppress unused import warning
var _ = rand.Reader
