package encryption

import "testing"

// #1072/#1029: at-rest helpers used by the fail-open webhook-secret encryption.

func TestEncryptAtRest_WithKey_RoundTrip(t *testing.T) {
	t.Setenv("ENCRYPTION_KEY", extraTestKey)

	stored, encrypted, err := EncryptAtRest("whsec_plain")
	if err != nil {
		t.Fatalf("EncryptAtRest: %v", err)
	}
	if !encrypted {
		t.Fatal("expected encrypted=true with a key configured")
	}
	if stored == "whsec_plain" {
		t.Fatal("value stored as plaintext despite a configured key")
	}
	if !IsEncrypted(stored) {
		t.Errorf("EncryptAtRest output is not an envelope: %q", stored)
	}
	got, err := DecryptIfEncrypted(stored)
	if err != nil {
		t.Fatalf("DecryptIfEncrypted: %v", err)
	}
	if got != "whsec_plain" {
		t.Errorf("round-trip = %q, want whsec_plain", got)
	}
}

func TestEncryptAtRest_NoKey_FailsOpen(t *testing.T) {
	t.Setenv("ENCRYPTION_KEY", "")
	t.Setenv("CREWSHIP_ENCRYPTION_KEY_VERSION", "")

	if KeyConfigured() {
		t.Fatal("KeyConfigured() should be false with no key set")
	}
	stored, encrypted, err := EncryptAtRest("whsec_plain")
	if err != nil {
		t.Fatalf("EncryptAtRest fail-open should not error: %v", err)
	}
	if encrypted {
		t.Error("expected encrypted=false with no key")
	}
	if stored != "whsec_plain" {
		t.Errorf("no-key store = %q, want the plaintext unchanged", stored)
	}
	// An empty value is always a no-op.
	if s, enc, _ := EncryptAtRest(""); s != "" || enc {
		t.Errorf("empty value: got (%q, %v), want (\"\", false)", s, enc)
	}
}

func TestDecryptIfEncrypted_PassthroughAndFallback(t *testing.T) {
	t.Setenv("ENCRYPTION_KEY", extraTestKey)

	// Bare plaintext (legacy / key-less write) passes through untouched.
	if got, err := DecryptIfEncrypted("64hexplaintextvalue"); err != nil || got != "64hexplaintextvalue" {
		t.Errorf("plaintext passthrough = (%q, %v), want (unchanged, nil)", got, err)
	}
	// A malformed envelope returns the raw value AND an error (caller warns;
	// HMAC then fails safely).
	got, err := DecryptIfEncrypted("v1:not-valid-base64-!!!")
	if err == nil {
		t.Error("expected an error decrypting a malformed envelope")
	}
	if got != "v1:not-valid-base64-!!!" {
		t.Errorf("fallback = %q, want the raw value", got)
	}
}
