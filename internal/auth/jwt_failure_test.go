package auth

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/go-jose/go-jose/v4"
)

func TestValidatorRejectsEncryptedMalformedClaims(t *testing.T) {
	v, err := NewJWTValidator(testSecret)
	if err != nil {
		t.Fatal(err)
	}
	encrypter, err := jose.NewEncrypter(jose.A256CBC_HS512, jose.Recipient{Algorithm: jose.DIRECT, Key: v.accessKey}, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, payload := range []string{`not JSON`, `{"id":7,"kind":"access"}`, `[]`} {
		t.Run(payload, func(t *testing.T) {
			encrypted, err := encrypter.Encrypt([]byte(payload))
			if err != nil {
				t.Fatal(err)
			}
			token, err := encrypted.CompactSerialize()
			if err != nil {
				t.Fatal(err)
			}
			claims, err := v.ValidateAccess(token)
			if claims != nil || !errors.Is(err, ErrInvalidToken) {
				t.Fatalf("malformed plaintext accepted: %#v, %v", claims, err)
			}
		})
	}
}

func TestIssueWithInvalidEncryptionKeyReturnsNoToken(t *testing.T) {
	v, err := NewJWTValidator(testSecret)
	if err != nil {
		t.Fatal(err)
	}
	for _, length := range []int{0, 1, 32, 63, 65} {
		token, err := v.issue(make([]byte, length), KindAccess, time.Minute, "user", "session", "", "")
		if err == nil || token != "" {
			t.Fatalf("key length %d produced token %q: %v", length, token, err)
		}
	}
}

func TestDeriveKeyRejectsHKDFExpansionBeyondLimit(t *testing.T) {
	// HKDF-SHA256 has at most 255 blocks of 32 bytes. Never silently return
	// a partially expanded encryption key after the reader reaches that limit.
	key, err := deriveEncryptionKey(testSecret, saltAccess, 255*32+1)
	if err == nil || key != nil {
		t.Fatalf("oversized derivation succeeded: %d bytes, %v", len(key), err)
	}
	if !strings.Contains(err.Error(), "entropy limit") {
		t.Fatalf("unexpected derivation failure: %v", err)
	}
}
