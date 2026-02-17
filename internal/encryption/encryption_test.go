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
