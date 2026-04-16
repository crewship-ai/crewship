package encryption

import (
	"os"
	"testing"
)

const benchKey = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

// BenchmarkEncrypt measures end-to-end encryption of a typical small
// credential value. This path runs every time a credential is persisted.
func BenchmarkEncrypt(b *testing.B) {
	os.Setenv("ENCRYPTION_KEY", benchKey)
	b.Cleanup(func() { os.Unsetenv("ENCRYPTION_KEY") })
	plaintext := "sk-ant-api-key-example-1234567890abcdef"
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := Encrypt(plaintext); err != nil {
			b.Fatalf("encrypt: %v", err)
		}
	}
}

// BenchmarkDecrypt measures end-to-end decryption. This path runs on every
// agent start (credential inject), keeper execute, and other credential-
// consuming flows.
func BenchmarkDecrypt(b *testing.B) {
	os.Setenv("ENCRYPTION_KEY", benchKey)
	b.Cleanup(func() { os.Unsetenv("ENCRYPTION_KEY") })
	plaintext := "sk-ant-api-key-example-1234567890abcdef"
	ciphertext, err := Encrypt(plaintext)
	if err != nil {
		b.Fatalf("setup encrypt: %v", err)
	}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := Decrypt(ciphertext); err != nil {
			b.Fatalf("decrypt: %v", err)
		}
	}
}
