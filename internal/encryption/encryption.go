package encryption

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"regexp"
	"strings"
)

const currentKeyVersion = "v1"

var versionPattern = regexp.MustCompile(`^v\d+$`)

func getEncryptionKey(version string) ([]byte, error) {
	envVar := "ENCRYPTION_KEY"
	if version != "v1" {
		envVar = "ENCRYPTION_KEY_" + strings.ToUpper(version)
	}

	key := os.Getenv(envVar)
	if key == "" {
		key = os.Getenv("ENCRYPTION_KEY")
	}
	if key == "" {
		return nil, fmt.Errorf("%s is not set", envVar)
	}

	return hex.DecodeString(key)
}

// Encrypt encrypts plaintext with AES-256-GCM.
// Output format: "v1:base64(IV + AuthTag + Ciphertext)"
// Compatible with the TypeScript encrypt() in lib/encryption.ts.
func Encrypt(plaintext string) (string, error) {
	key, err := getEncryptionKey(currentKeyVersion)
	if err != nil {
		return "", err
	}

	block, err := aes.NewCipher(key)
	if err != nil {
		return "", fmt.Errorf("create cipher: %w", err)
	}

	gcm, err := cipher.NewGCMWithNonceSize(block, 16)
	if err != nil {
		return "", fmt.Errorf("create gcm: %w", err)
	}

	iv := make([]byte, 16)
	if _, err := io.ReadFull(rand.Reader, iv); err != nil {
		return "", fmt.Errorf("generate iv: %w", err)
	}

	// GCM Seal appends: ciphertext + authTag (last 16 bytes)
	sealed := gcm.Seal(nil, iv, []byte(plaintext), nil)

	// Split into ciphertext and authTag for our format: IV + AuthTag + Ciphertext
	authTagStart := len(sealed) - 16
	ciphertext := sealed[:authTagStart]
	authTag := sealed[authTagStart:]

	combined := make([]byte, 0, len(iv)+len(authTag)+len(ciphertext))
	combined = append(combined, iv...)
	combined = append(combined, authTag...)
	combined = append(combined, ciphertext...)

	return currentKeyVersion + ":" + base64.StdEncoding.EncodeToString(combined), nil
}

// Decrypt decrypts ciphertext with AES-256-GCM. Supports key versioning.
// Accepts both "v1:base64data" (versioned) and plain "base64data" (legacy).
// Compatible with the TypeScript decrypt() in lib/encryption.ts.
func Decrypt(ciphertextStr string) (string, error) {
	version := currentKeyVersion
	encoded := ciphertextStr

	if idx := strings.Index(ciphertextStr, ":"); idx > 0 && idx <= 3 {
		prefix := ciphertextStr[:idx]
		if versionPattern.MatchString(prefix) {
			version = prefix
			encoded = ciphertextStr[idx+1:]
		}
	}

	key, err := getEncryptionKey(version)
	if err != nil {
		return "", err
	}

	data, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		return "", fmt.Errorf("decode base64: %w", err)
	}

	if len(data) < 32 {
		return "", errors.New("ciphertext too short")
	}

	iv := data[:16]
	authTag := data[16:32]
	ciphertext := data[32:]

	block, err := aes.NewCipher(key)
	if err != nil {
		return "", fmt.Errorf("create cipher: %w", err)
	}

	gcm, err := cipher.NewGCMWithNonceSize(block, 16)
	if err != nil {
		return "", fmt.Errorf("create gcm: %w", err)
	}

	// Reconstruct sealed data: ciphertext + authTag (Go GCM expects this format)
	sealed := make([]byte, 0, len(ciphertext)+len(authTag))
	sealed = append(sealed, ciphertext...)
	sealed = append(sealed, authTag...)

	plaintext, err := gcm.Open(nil, iv, sealed, nil)
	if err != nil {
		return "", fmt.Errorf("decrypt: %w", err)
	}

	return string(plaintext), nil
}
