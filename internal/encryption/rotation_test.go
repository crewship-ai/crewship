package encryption

import (
	"strings"
	"testing"
)

// Dummy AES-256 test keys (64 hex chars), assembled via Repeat so secret
// scanners don't flag the literals.
var (
	testKeyV1 = strings.Repeat("0123456789abcdef", 4)
	testKeyV2 = strings.Repeat("fedcba9876543210", 4)
)

func TestCurrentKeyVersionDefault(t *testing.T) {
	t.Setenv("CREWSHIP_ENCRYPTION_KEY_VERSION", "")
	v, err := CurrentKeyVersion()
	if err != nil {
		t.Fatalf("CurrentKeyVersion: %v", err)
	}
	if v != "v1" {
		t.Fatalf("expected default version v1, got %q", v)
	}
}

func TestCurrentKeyVersionOverride(t *testing.T) {
	t.Setenv("CREWSHIP_ENCRYPTION_KEY_VERSION", "v2")
	v, err := CurrentKeyVersion()
	if err != nil {
		t.Fatalf("CurrentKeyVersion: %v", err)
	}
	if v != "v2" {
		t.Fatalf("expected v2, got %q", v)
	}
}

func TestCurrentKeyVersionInvalid(t *testing.T) {
	for _, bad := range []string{"2", "V2x", "v", "banana", "v2:extra"} {
		t.Setenv("CREWSHIP_ENCRYPTION_KEY_VERSION", bad)
		if _, err := CurrentKeyVersion(); err == nil {
			t.Fatalf("expected error for CREWSHIP_ENCRYPTION_KEY_VERSION=%q", bad)
		}
	}
}

// TestCurrentKeyVersionCaseInsensitive: operators writing V2 instead of v2
// should get the canonical lowercase version, not a silent mismatch with the
// envelope prefix format.
func TestCurrentKeyVersionCaseInsensitive(t *testing.T) {
	t.Setenv("CREWSHIP_ENCRYPTION_KEY_VERSION", "V2")
	v, err := CurrentKeyVersion()
	if err != nil {
		t.Fatalf("CurrentKeyVersion: %v", err)
	}
	if v != "v2" {
		t.Fatalf("expected v2, got %q", v)
	}
}

// TestEncryptMintsCurrentVersion: with the version switched to v2 and a v2
// key present, Encrypt must mint "v2:" envelopes using ENCRYPTION_KEY_V2 —
// and old "v1:" envelopes must keep decrypting with ENCRYPTION_KEY.
func TestEncryptMintsCurrentVersion(t *testing.T) {
	t.Setenv("ENCRYPTION_KEY", testKeyV1)
	t.Setenv("CREWSHIP_ENCRYPTION_KEY_VERSION", "")
	t.Setenv("ENCRYPTION_KEY_V2", "")

	oldEnvelope, err := Encrypt("rotate-me")
	if err != nil {
		t.Fatalf("Encrypt (v1): %v", err)
	}
	if !strings.HasPrefix(oldEnvelope, "v1:") {
		t.Fatalf("expected v1 envelope, got %q", oldEnvelope)
	}

	// Operator rotates: new key under V2, version flipped to v2, old key kept.
	t.Setenv("ENCRYPTION_KEY_V2", testKeyV2)
	t.Setenv("CREWSHIP_ENCRYPTION_KEY_VERSION", "v2")

	newEnvelope, err := Encrypt("rotate-me")
	if err != nil {
		t.Fatalf("Encrypt (v2): %v", err)
	}
	if !strings.HasPrefix(newEnvelope, "v2:") {
		t.Fatalf("expected v2 envelope, got %q", newEnvelope)
	}

	// Both generations decrypt.
	for _, env := range []string{oldEnvelope, newEnvelope} {
		got, err := Decrypt(env)
		if err != nil {
			t.Fatalf("Decrypt(%q...): %v", env[:8], err)
		}
		if got != "rotate-me" {
			t.Fatalf("round-trip mismatch: %q", got)
		}
	}
}

// TestEncryptFailsClosedWithoutVersionedKey: minting vN envelopes without an
// explicit ENCRYPTION_KEY_VN must fail — silently falling back to
// ENCRYPTION_KEY would stamp new envelopes with a version whose key they were
// NOT encrypted with, corrupting the rotation.
func TestEncryptFailsClosedWithoutVersionedKey(t *testing.T) {
	t.Setenv("ENCRYPTION_KEY", testKeyV1)
	t.Setenv("ENCRYPTION_KEY_V2", "")
	t.Setenv("CREWSHIP_ENCRYPTION_KEY_VERSION", "v2")

	if _, err := Encrypt("nope"); err == nil {
		t.Fatal("expected Encrypt to fail when ENCRYPTION_KEY_V2 is unset")
	}
}

// TestEncryptFailsOnInvalidVersionEnv: a malformed version env var must not
// silently mint v1 envelopes.
func TestEncryptFailsOnInvalidVersionEnv(t *testing.T) {
	t.Setenv("ENCRYPTION_KEY", testKeyV1)
	t.Setenv("CREWSHIP_ENCRYPTION_KEY_VERSION", "banana")

	if _, err := Encrypt("nope"); err == nil {
		t.Fatal("expected Encrypt to fail on malformed CREWSHIP_ENCRYPTION_KEY_VERSION")
	}
}

func TestParseEnvelopeVersion(t *testing.T) {
	cases := []struct {
		in   string
		want string
		ok   bool
	}{
		{"v1:abc", "v1", true},
		{"v2:abc", "v2", true},
		{"v12:abc", "v12", true},
		{"abc", "", false},        // legacy raw base64
		{"v:abc", "", false},      // no digits
		{"vx:abc", "", false},     // not a version
		{"", "", false},           // empty
		{"sk-ant-xxx", "", false}, // plaintext that never got encrypted
		{"v1abc", "", false},      // no colon
	}
	for _, c := range cases {
		got, ok := ParseEnvelopeVersion(c.in)
		if got != c.want || ok != c.ok {
			t.Errorf("ParseEnvelopeVersion(%q) = (%q, %v), want (%q, %v)", c.in, got, ok, c.want, c.ok)
		}
	}
}

// TestVerifyKeys: startup validation — current version key must resolve and
// be a usable AES-256 key.
func TestVerifyKeys(t *testing.T) {
	t.Setenv("ENCRYPTION_KEY", testKeyV1)
	t.Setenv("CREWSHIP_ENCRYPTION_KEY_VERSION", "")
	if err := VerifyCurrentKey(); err != nil {
		t.Fatalf("VerifyCurrentKey (v1): %v", err)
	}

	t.Setenv("CREWSHIP_ENCRYPTION_KEY_VERSION", "v2")
	if err := VerifyCurrentKey(); err == nil {
		t.Fatal("expected VerifyCurrentKey to fail when the v2 key env var is unset")
	}

	t.Setenv("ENCRYPTION_KEY_V2", "not-hex")
	if err := VerifyCurrentKey(); err == nil {
		t.Fatal("expected VerifyCurrentKey to fail when the v2 key is not valid hex")
	}

	// Valid hex but wrong length (16 bytes, not 32).
	t.Setenv("ENCRYPTION_KEY_V2", strings.Repeat("ab", 16))
	if err := VerifyCurrentKey(); err == nil {
		t.Fatal("expected VerifyCurrentKey to fail when the v2 key has the wrong length")
	}

	t.Setenv("ENCRYPTION_KEY_V2", testKeyV2)
	if err := VerifyCurrentKey(); err != nil {
		t.Fatalf("VerifyCurrentKey (v2): %v", err)
	}
}
