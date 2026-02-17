package encryption

import (
	"os"
	"testing"
)

func TestEncryptDecrypt(t *testing.T) {
	// Set test encryption key (32 bytes hex = 64 hex chars)
	os.Setenv("ENCRYPTION_KEY", "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef")
	defer os.Unsetenv("ENCRYPTION_KEY")

	plaintext := "sk-ant-api03-secret-key-test-value"

	encrypted, err := Encrypt(plaintext)
	if err != nil {
		t.Fatalf("Encrypt: %v", err)
	}

	if encrypted[:3] != "v1:" {
		t.Errorf("expected v1: prefix, got %q", encrypted[:3])
	}

	decrypted, err := Decrypt(encrypted)
	if err != nil {
		t.Fatalf("Decrypt: %v", err)
	}

	if decrypted != plaintext {
		t.Errorf("decrypted = %q, want %q", decrypted, plaintext)
	}
}

func TestEncryptDifferentOutputs(t *testing.T) {
	os.Setenv("ENCRYPTION_KEY", "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef")
	defer os.Unsetenv("ENCRYPTION_KEY")

	e1, _ := Encrypt("test")
	e2, _ := Encrypt("test")

	if e1 == e2 {
		t.Error("two encryptions of same plaintext should produce different outputs (random IV)")
	}
}

func TestDecryptInvalidData(t *testing.T) {
	os.Setenv("ENCRYPTION_KEY", "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef")
	defer os.Unsetenv("ENCRYPTION_KEY")

	_, err := Decrypt("v1:invalid-base64")
	if err == nil {
		t.Error("expected error for invalid base64")
	}

	_, err = Decrypt("v1:dGVzdA==") // valid base64 but too short
	if err == nil {
		t.Error("expected error for short ciphertext")
	}
}

func TestMissingKey(t *testing.T) {
	os.Unsetenv("ENCRYPTION_KEY")

	_, err := Encrypt("test")
	if err == nil {
		t.Error("expected error for missing key")
	}
}

func TestEncryptDecryptEmpty(t *testing.T) {
	os.Setenv("ENCRYPTION_KEY", "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef")
	defer os.Unsetenv("ENCRYPTION_KEY")

	encrypted, err := Encrypt("")
	if err != nil {
		t.Fatalf("Encrypt empty: %v", err)
	}
	decrypted, err := Decrypt(encrypted)
	if err != nil {
		t.Fatalf("Decrypt empty: %v", err)
	}
	if decrypted != "" {
		t.Errorf("expected empty string, got %q", decrypted)
	}
}

func TestEncryptDecryptUnicode(t *testing.T) {
	os.Setenv("ENCRYPTION_KEY", "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef")
	defer os.Unsetenv("ENCRYPTION_KEY")

	special := "héllo wörld! 🚀 日本語 中文 <script>alert('xss')</script> \n\t"
	encrypted, err := Encrypt(special)
	if err != nil {
		t.Fatalf("Encrypt unicode: %v", err)
	}
	decrypted, err := Decrypt(encrypted)
	if err != nil {
		t.Fatalf("Decrypt unicode: %v", err)
	}
	if decrypted != special {
		t.Errorf("decrypted = %q, want %q", decrypted, special)
	}
}

func TestEncryptDecryptLong(t *testing.T) {
	os.Setenv("ENCRYPTION_KEY", "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef")
	defer os.Unsetenv("ENCRYPTION_KEY")

	long := ""
	for i := 0; i < 2000; i++ {
		long += "x"
	}
	encrypted, err := Encrypt(long)
	if err != nil {
		t.Fatalf("Encrypt long: %v", err)
	}
	decrypted, err := Decrypt(encrypted)
	if err != nil {
		t.Fatalf("Decrypt long: %v", err)
	}
	if decrypted != long {
		t.Errorf("decrypted length = %d, want 2000", len(decrypted))
	}
}

func TestDecryptWrongKey(t *testing.T) {
	os.Setenv("ENCRYPTION_KEY", "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef")

	encrypted, err := Encrypt("test-data")
	if err != nil {
		t.Fatalf("Encrypt: %v", err)
	}

	// Change key
	os.Setenv("ENCRYPTION_KEY", "fedcba9876543210fedcba9876543210fedcba9876543210fedcba9876543210")
	defer os.Unsetenv("ENCRYPTION_KEY")

	_, err = Decrypt(encrypted)
	if err == nil {
		t.Error("expected error when decrypting with wrong key")
	}
}

func TestLegacyFormat(t *testing.T) {
	os.Setenv("ENCRYPTION_KEY", "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef")
	defer os.Unsetenv("ENCRYPTION_KEY")

	// Encrypt with version, strip prefix for "legacy" format
	encrypted, err := Encrypt("legacy-test")
	if err != nil {
		t.Fatalf("Encrypt: %v", err)
	}

	// Remove "v1:" prefix to test legacy format handling
	legacyFormat := encrypted[3:] // strip "v1:"
	decrypted, err := Decrypt(legacyFormat)
	if err != nil {
		t.Fatalf("Decrypt legacy: %v", err)
	}
	if decrypted != "legacy-test" {
		t.Errorf("decrypted = %q, want 'legacy-test'", decrypted)
	}
}
