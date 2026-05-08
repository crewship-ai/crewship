package api

import (
	"strings"
	"testing"
	"time"
)

func TestSaveToken_RoundTripVerifies(t *testing.T) {
	secret := []byte("test-secret-32-bytes-min-please")
	now := time.Now()
	tok := signSaveToken(secret, "ws_a", "hash_x", "user_42", now)
	if tok == "" {
		t.Fatal("expected non-empty token")
	}
	if err := verifySaveToken(secret, tok, "ws_a", "hash_x", "user_42"); err != nil {
		t.Errorf("round-trip verify: %v", err)
	}
}

func TestSaveToken_RejectsWrongWorkspace(t *testing.T) {
	secret := []byte("secret")
	tok := signSaveToken(secret, "ws_a", "hash_x", "u1", time.Now())
	if err := verifySaveToken(secret, tok, "ws_b", "hash_x", "u1"); err == nil {
		t.Error("token signed for ws_a must NOT verify for ws_b")
	}
}

func TestSaveToken_RejectsWrongDefinitionHash(t *testing.T) {
	secret := []byte("secret")
	tok := signSaveToken(secret, "ws_a", "hash_x", "u1", time.Now())
	if err := verifySaveToken(secret, tok, "ws_a", "hash_y", "u1"); err == nil {
		t.Error("token signed for hash_x must NOT verify for hash_y")
	}
}

func TestSaveToken_RejectsWrongUser(t *testing.T) {
	secret := []byte("secret")
	tok := signSaveToken(secret, "ws_a", "hash_x", "u1", time.Now())
	if err := verifySaveToken(secret, tok, "ws_a", "hash_x", "u2"); err == nil {
		t.Error("token signed for u1 must NOT verify for u2 (this is the no-impersonation guarantee)")
	}
}

func TestSaveToken_RejectsExpired(t *testing.T) {
	secret := []byte("secret")
	old := time.Now().Add(-10 * time.Minute)
	tok := signSaveToken(secret, "ws_a", "hash_x", "u1", old)
	err := verifySaveToken(secret, tok, "ws_a", "hash_x", "u1")
	if err == nil || !strings.Contains(err.Error(), "expired") {
		t.Errorf("expired token must be rejected, got %v", err)
	}
}

func TestSaveToken_RejectsFutureTimestamp(t *testing.T) {
	secret := []byte("secret")
	future := time.Now().Add(2 * time.Minute)
	tok := signSaveToken(secret, "ws_a", "hash_x", "u1", future)
	err := verifySaveToken(secret, tok, "ws_a", "hash_x", "u1")
	if err == nil || !strings.Contains(err.Error(), "expired or future") {
		t.Errorf("future token must be rejected, got %v", err)
	}
}

func TestSaveToken_RejectsTamperedHMAC(t *testing.T) {
	secret := []byte("secret")
	tok := signSaveToken(secret, "ws_a", "hash_x", "u1", time.Now())
	parts := strings.SplitN(tok, ".", 2)
	if len(parts) != 2 {
		t.Fatal("token format")
	}
	// Flip a hex char in the HMAC
	tampered := parts[0] + "." + flipFirstHexNibble(parts[1])
	err := verifySaveToken(secret, tampered, "ws_a", "hash_x", "u1")
	if err == nil || !strings.Contains(err.Error(), "HMAC mismatch") {
		t.Errorf("tampered HMAC must be rejected, got %v", err)
	}
}

func TestSaveToken_RejectsDifferentSecret(t *testing.T) {
	tok := signSaveToken([]byte("secret_a"), "ws_a", "hash_x", "u1", time.Now())
	if err := verifySaveToken([]byte("secret_b"), tok, "ws_a", "hash_x", "u1"); err == nil {
		t.Error("token signed under secret_a must NOT verify under secret_b")
	}
}

func TestSaveToken_RejectsMalformed(t *testing.T) {
	secret := []byte("secret")
	cases := []string{
		"",
		"not-a-token",
		"123",
		"abc.def.ghi",  // > 2 parts after split is fine but bad ts
		"abc.deadbeef", // unparseable ts
		"1234567890",   // missing separator
	}
	for _, c := range cases {
		if err := verifySaveToken(secret, c, "ws", "h", "u"); err == nil {
			t.Errorf("malformed token %q must be rejected", c)
		}
	}
}

func TestSaveToken_EmptySecretReturnsEmptyToken(t *testing.T) {
	tok := signSaveToken(nil, "ws", "h", "u", time.Now())
	if tok != "" {
		t.Error("nil secret must produce empty token (signing disabled)")
	}
	if err := verifySaveToken(nil, "anything", "ws", "h", "u"); err == nil {
		t.Error("verify with nil secret must fail (server config error)")
	}
}

// flipFirstHexNibble flips the first hex nibble of s by toggling its
// low bit. Used to produce a token that's structurally valid but has
// a bad HMAC.
func flipFirstHexNibble(s string) string {
	if s == "" {
		return s
	}
	b := []byte(s)
	switch b[0] {
	case '0':
		b[0] = '1'
	case '1':
		b[0] = '0'
	default:
		b[0] = '0'
	}
	return string(b)
}
