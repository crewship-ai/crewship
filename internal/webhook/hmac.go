package webhook

import (
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
)

// ValidateSecret performs a constant-time comparison of the provided and
// expected webhook secrets to prevent timing attacks.
func ValidateSecret(provided, expected string) bool {
	if provided == "" || expected == "" {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(provided), []byte(expected)) == 1
}

// ComputeHMAC returns the hex-encoded HMAC-SHA256 of message using the given secret.
func ComputeHMAC(message []byte, secret string) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(message)
	return hex.EncodeToString(mac.Sum(nil))
}

// ValidateHMAC verifies that the given hex-encoded signature matches the
// HMAC-SHA256 of message computed with the provided secret.
func ValidateHMAC(message []byte, signature, secret string) bool {
	expected := ComputeHMAC(message, secret)
	return hmac.Equal([]byte(signature), []byte(expected))
}
