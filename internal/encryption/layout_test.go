package encryption

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"io"
	"strings"
	"testing"
)

const testHexKey = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

func mustKeyBytes(t *testing.T) []byte {
	t.Helper()
	b, err := hex.DecodeString(testHexKey)
	if err != nil {
		t.Fatalf("decode test key: %v", err)
	}
	return b
}

// TestEncryptByteLayoutIVAuthTagCiphertext pins the on-disk byte layout to
// `IV || AuthTag || Ciphertext`. The order is deliberate and shared with the
// TypeScript encoder in lib/encryption.ts — reversing or rearranging it would
// corrupt every existing credential. This test is a belt to go with the
// suspenders: the round-trip tests pass even if Encrypt/Decrypt *both* flip
// layout, so we validate the layout directly.
func TestEncryptByteLayoutIVAuthTagCiphertext(t *testing.T) {
	t.Setenv("ENCRYPTION_KEY", testHexKey)

	plaintext := "layout-probe"
	encrypted, err := Encrypt(plaintext)
	if err != nil {
		t.Fatalf("Encrypt: %v", err)
	}
	if !strings.HasPrefix(encrypted, "v1:") {
		t.Fatalf("expected v1: prefix, got %q", encrypted)
	}

	blob, err := base64.StdEncoding.DecodeString(encrypted[3:])
	if err != nil {
		t.Fatalf("decode base64 payload: %v", err)
	}

	// Layout invariant: exactly 16 IV bytes + 16 AuthTag bytes + len(plaintext) ciphertext.
	const ivLen, tagLen = 16, 16
	want := ivLen + tagLen + len(plaintext)
	if len(blob) != want {
		t.Fatalf("payload length: got %d, want %d (IV=16 + AuthTag=16 + plaintext=%d)",
			len(blob), want, len(plaintext))
	}

	iv := blob[:ivLen]
	authTag := blob[ivLen : ivLen+tagLen]
	ciphertext := blob[ivLen+tagLen:]

	// Re-assemble in Go GCM's native order (ciphertext + tag) and decrypt with
	// Open. If IV/AuthTag/Ciphertext slot assignments are wrong, Open fails.
	block, err := aes.NewCipher(mustKeyBytes(t))
	if err != nil {
		t.Fatalf("cipher: %v", err)
	}
	gcm, err := cipher.NewGCMWithNonceSize(block, 16)
	if err != nil {
		t.Fatalf("gcm: %v", err)
	}

	sealed := append([]byte{}, ciphertext...)
	sealed = append(sealed, authTag...)

	got, err := gcm.Open(nil, iv, sealed, nil)
	if err != nil {
		t.Fatalf("manual Open with IV||AuthTag||Ciphertext split failed — layout has drifted: %v", err)
	}
	if string(got) != plaintext {
		t.Errorf("layout decrypt: got %q, want %q", got, plaintext)
	}
}

// TestDecryptAcceptsTSCompatPayload proves interop with TypeScript's
// lib/encryption.ts: build a payload in the cross-language format
// (v1:base64(IV||AuthTag||Ciphertext)) using standard crypto primitives, hand
// it to Decrypt, and verify the plaintext.
func TestDecryptAcceptsTSCompatPayload(t *testing.T) {
	t.Setenv("ENCRYPTION_KEY", testHexKey)

	plaintext := "sk-ant-test-ts-compat"
	key := mustKeyBytes(t)

	block, err := aes.NewCipher(key)
	if err != nil {
		t.Fatalf("cipher: %v", err)
	}
	gcm, err := cipher.NewGCMWithNonceSize(block, 16)
	if err != nil {
		t.Fatalf("gcm: %v", err)
	}

	iv := make([]byte, 16)
	if _, err := io.ReadFull(rand.Reader, iv); err != nil {
		t.Fatalf("random iv: %v", err)
	}

	sealed := gcm.Seal(nil, iv, []byte(plaintext), nil) // ciphertext || authTag
	ctLen := len(sealed) - 16
	ciphertext := sealed[:ctLen]
	authTag := sealed[ctLen:]

	combined := append([]byte{}, iv...)
	combined = append(combined, authTag...)
	combined = append(combined, ciphertext...)

	// With padding (standard base64) — what the Go encoder produces.
	payload := "v1:" + base64.StdEncoding.EncodeToString(combined)
	got, err := Decrypt(payload)
	if err != nil {
		t.Fatalf("Decrypt padded: %v", err)
	}
	if got != plaintext {
		t.Errorf("padded: got %q, want %q", got, plaintext)
	}

	// Without padding (RawStdEncoding) — TS emits base64 without trailing `=`.
	payloadRaw := "v1:" + base64.RawStdEncoding.EncodeToString(combined)
	got, err = Decrypt(payloadRaw)
	if err != nil {
		t.Fatalf("Decrypt unpadded: %v", err)
	}
	if got != plaintext {
		t.Errorf("unpadded: got %q, want %q", got, plaintext)
	}
}

// TestKeyVersioningPicksRightEnvVar verifies that a "v2:" prefixed payload
// reads ENCRYPTION_KEY_V2, not ENCRYPTION_KEY. Mixing key rings is a common
// rotation footgun; pin the lookup.
func TestKeyVersioningPicksRightEnvVar(t *testing.T) {
	otherKey := "fedcba9876543210fedcba9876543210fedcba9876543210fedcba9876543210"

	t.Setenv("ENCRYPTION_KEY", testHexKey)
	t.Setenv("ENCRYPTION_KEY_V2", otherKey)

	// Encrypt under v1 (the current version).
	ct, err := Encrypt("rotate-me")
	if err != nil {
		t.Fatalf("Encrypt: %v", err)
	}
	if !strings.HasPrefix(ct, "v1:") {
		t.Fatalf("expected v1 prefix, got %q", ct)
	}

	// Rewriting the prefix to v2 MUST make decryption fail — v2 env var holds
	// a different key.
	v2 := "v2:" + ct[3:]
	if _, err := Decrypt(v2); err == nil {
		t.Error("expected Decrypt to fail when payload is relabelled as v2 and v2 key differs")
	}

	// Sanity check: when ENCRYPTION_KEY_V2 matches the v1 key, v2 decrypt succeeds.
	t.Setenv("ENCRYPTION_KEY_V2", testHexKey)
	got, err := Decrypt(v2)
	if err != nil {
		t.Fatalf("Decrypt v2 with matching key: %v", err)
	}
	if got != "rotate-me" {
		t.Errorf("got %q, want %q", got, "rotate-me")
	}
}

// TestMissingVersionEnvVar surfaces the specific env var name so rotation
// failures are self-diagnosing.
func TestMissingVersionEnvVar(t *testing.T) {
	// Do NOT set ENCRYPTION_KEY_V2. Also clear ENCRYPTION_KEY so the code
	// can't silently fall through (getEncryptionKey falls back to
	// ENCRYPTION_KEY when the version-specific var is empty — and that
	// fallback is itself a behaviour we should document).
	t.Setenv("ENCRYPTION_KEY", "")
	t.Setenv("ENCRYPTION_KEY_V2", "")

	_, err := getEncryptionKey("v2")
	if err == nil {
		t.Fatal("expected error when neither v2 nor default key is set")
	}
	if !strings.Contains(err.Error(), "ENCRYPTION_KEY_V2") {
		t.Errorf("error should name ENCRYPTION_KEY_V2 for diagnostics; got %v", err)
	}
}

// TestFallbackToDefaultKeyForUnknownVersion documents that when a versioned
// env var is unset but the default ENCRYPTION_KEY is present, getEncryptionKey
// falls back to the default. This is intentional so a v1-only installation
// keeps working during a rollout.
func TestFallbackToDefaultKeyForUnknownVersion(t *testing.T) {
	t.Setenv("ENCRYPTION_KEY", testHexKey)
	t.Setenv("ENCRYPTION_KEY_V2", "")

	key, err := getEncryptionKey("v2")
	if err != nil {
		t.Fatalf("expected fallback to ENCRYPTION_KEY; got %v", err)
	}
	wantBytes := mustKeyBytes(t)
	if string(key) != string(wantBytes) {
		t.Error("getEncryptionKey should have returned the default key bytes")
	}
}

// TestInvalidHexKeyRejected — a mistyped hex key (odd length or non-hex chars)
// must surface a decode error rather than silently running with a garbage key.
func TestInvalidHexKeyRejected(t *testing.T) {
	t.Setenv("ENCRYPTION_KEY", "zzz-not-hex")

	if _, err := Encrypt("whatever"); err == nil {
		t.Error("expected hex decode error for non-hex key")
	}
}

// TestDecryptShortPayloadError checks the explicit "too short" guard (<32B)
// that protects against out-of-bounds slicing when the blob is truncated.
func TestDecryptShortPayloadError(t *testing.T) {
	t.Setenv("ENCRYPTION_KEY", testHexKey)

	// 30 zero bytes, base64-encoded. Has v1: prefix but is too short to
	// contain both IV(16) and AuthTag(16).
	short := "v1:" + base64.StdEncoding.EncodeToString(make([]byte, 30))
	_, err := Decrypt(short)
	if err == nil {
		t.Fatal("expected 'too short' error")
	}
	if !strings.Contains(err.Error(), "too short") {
		t.Errorf("error should mention 'too short'; got %v", err)
	}
}

// TestUnknownPrefixTreatedAsData guards the version-regex narrow: a
// colon-containing string that doesn't match `^v\d+$` must be treated as raw
// base64 legacy data, not parsed as a key version.
func TestUnknownPrefixTreatedAsData(t *testing.T) {
	t.Setenv("ENCRYPTION_KEY", testHexKey)

	// "x1:" is NOT a valid version token. versionPattern rejects it so the
	// whole string becomes the "encoded" payload — which is invalid base64 and
	// will surface as a decode error, NOT a "missing ENCRYPTION_KEY_X1" error.
	_, err := Decrypt("x1:whatever")
	if err == nil {
		t.Fatal("expected an error for invalid data")
	}
	if strings.Contains(err.Error(), "ENCRYPTION_KEY_X1") {
		t.Errorf("unknown prefix was parsed as version; got %v", err)
	}
}
