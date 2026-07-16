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
	"sync"
)

const defaultKeyVersion = "v1"

// KeyVersionEnvVar selects which key version NEW envelopes are minted with.
// Unset (the default) means "v1" → ENCRYPTION_KEY. During a master-key
// rotation the operator sets it to "v2" (and provides ENCRYPTION_KEY_V2);
// Encrypt then stamps "v2:" envelopes while Decrypt keeps reading every older
// generation via its per-envelope version prefix.
const KeyVersionEnvVar = "CREWSHIP_ENCRYPTION_KEY_VERSION"

// Bounded at v99: Decrypt recognizes a version prefix only up to 3 chars
// before the ':' — an unbounded pattern here would let Encrypt mint
// envelopes (e.g. "v2026:…") that Decrypt misreads as legacy base64.
var versionPattern = regexp.MustCompile(`^v[1-9]\d?$`)

// CurrentKeyVersion returns the version new envelopes are minted with:
// "v1" unless CREWSHIP_ENCRYPTION_KEY_VERSION overrides it. A malformed
// override is an error — silently falling back to v1 would stamp envelopes
// with a version whose key they were not encrypted with.
func CurrentKeyVersion() (string, error) {
	v := strings.TrimSpace(os.Getenv(KeyVersionEnvVar))
	if v == "" {
		return defaultKeyVersion, nil
	}
	v = strings.ToLower(v)
	if !versionPattern.MatchString(v) {
		return "", fmt.Errorf("%s=%q is not a valid key version (expected v2, v3, … up to v99)", KeyVersionEnvVar, v)
	}
	return v, nil
}

// ParseEnvelopeVersion extracts the version prefix from a stored envelope
// ("v1:…" → "v1", true). Legacy non-versioned values (raw base64 from the
// pre-envelope era) and plaintext return ("", false).
func ParseEnvelopeVersion(s string) (string, bool) {
	idx := strings.Index(s, ":")
	if idx <= 0 {
		return "", false
	}
	prefix := s[:idx]
	if !versionPattern.MatchString(prefix) {
		return "", false
	}
	return prefix, true
}

// IsEncrypted reports whether s carries an encryption envelope prefix
// ("v1:…"). It's the read-side discriminator for columns that may hold a mix
// of encrypted and legacy-plaintext values during/after an at-rest-encryption
// rollout (e.g. webhook secrets, #1072/#1029): an enveloped value is decrypted,
// a bare value is used as-is. generateWebhookSecret() emits 64 hex chars with
// no ':' so it can never be mistaken for an envelope; a user-supplied value
// that happens to start with "vN:" is disambiguated by attempting Decrypt and
// falling back to plaintext on failure.
func IsEncrypted(s string) bool {
	_, ok := ParseEnvelopeVersion(s)
	return ok
}

// KeyConfigured reports whether a usable AES-256 encryption key is present for
// the current key version. Callers on fail-open paths (webhook secrets) gate
// encryption on this: with a key they Encrypt at rest, without one they store
// plaintext and warn, preserving key-less deployments.
func KeyConfigured() bool {
	return VerifyCurrentKey() == nil
}

// EncryptAtRest encrypts plaintext for storage when a usable key is configured;
// otherwise it returns the plaintext unchanged with encrypted=false so the
// caller can warn (fail-open — preserves key-less deployments per #1072). An
// empty value is a no-op. A configured-but-failing Encrypt returns the error.
func EncryptAtRest(plaintext string) (stored string, encrypted bool, err error) {
	if plaintext == "" || !KeyConfigured() {
		return plaintext, false, nil
	}
	ct, err := Encrypt(plaintext)
	if err != nil {
		return "", false, err
	}
	return ct, true, nil
}

// DecryptIfEncrypted returns the plaintext for a possibly-encrypted at-rest
// value. A bare (non-enveloped) value is returned unchanged — legacy plaintext
// or a key-less deployment. An enveloped value is decrypted; on a decrypt
// error the raw value is returned as a best-effort fallback AND the error is
// returned so the caller can warn (for an HMAC webhook secret a wrong value
// simply fails verification, so this never grants access).
func DecryptIfEncrypted(stored string) (string, error) {
	if !IsEncrypted(stored) {
		return stored, nil
	}
	pt, err := Decrypt(stored)
	if err != nil {
		return stored, err
	}
	return pt, nil
}

// keyEnvVarFor returns the env var holding the key for a version:
// ENCRYPTION_KEY for v1, ENCRYPTION_KEY_V<N> for later generations.
func keyEnvVarFor(version string) string {
	if version == defaultKeyVersion {
		return "ENCRYPTION_KEY"
	}
	return "ENCRYPTION_KEY_" + strings.ToUpper(version)
}

// getEncryptionKeyStrict resolves the key for a version WITHOUT the
// fall-back-to-ENCRYPTION_KEY behavior getEncryptionKey applies on decrypt.
// Used on the mint path: stamping a "v2:" envelope with the v1 key because
// ENCRYPTION_KEY_V2 was missing would make the envelope undecryptable the
// moment the real v2 key appears.
func getEncryptionKeyStrict(version string) ([]byte, error) {
	envVar := keyEnvVarFor(version)
	key := os.Getenv(envVar)
	if key == "" {
		return nil, fmt.Errorf("%s is not set (required to mint %s envelopes)", envVar, version)
	}
	raw, err := hex.DecodeString(key)
	if err != nil {
		return nil, fmt.Errorf("%s is not valid hex: %w", envVar, err)
	}
	return raw, nil
}

// VerifyCurrentKey confirms the active key version resolves to a usable
// AES-256 key. Called at startup and before a re-encryption run so a
// misconfigured rotation fails loudly up front instead of 500ing on the
// first Encrypt call.
func VerifyCurrentKey() error {
	version, err := CurrentKeyVersion()
	if err != nil {
		return err
	}
	key, err := getEncryptionKeyStrict(version)
	if err != nil {
		return err
	}
	if len(key) != 32 {
		return fmt.Errorf("%s must decode to 32 bytes (AES-256), got %d", keyEnvVarFor(version), len(key))
	}
	return nil
}

// aeadCache caches AES-256-GCM AEADs keyed by the raw key bytes so Encrypt /
// Decrypt don't rebuild the cipher + GCM precomputed tables on every call.
// Keying on the raw bytes (not the version) means a caller rotating
// ENCRYPTION_KEY at runtime transparently gets a fresh AEAD — callers that
// re-use the same key benefit from the cache.
var (
	aeadCacheMu sync.RWMutex
	aeadCache   = map[string]cipher.AEAD{}
)

func aeadForKey(key []byte) (cipher.AEAD, error) {
	// Zero-alloc lookup: Go elides the string(b) conversion for map indexing.
	aeadCacheMu.RLock()
	if a, ok := aeadCache[string(key)]; ok {
		aeadCacheMu.RUnlock()
		return a, nil
	}
	aeadCacheMu.RUnlock()

	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("create cipher: %w", err)
	}
	gcm, err := cipher.NewGCMWithNonceSize(block, 16)
	if err != nil {
		return nil, fmt.Errorf("create gcm: %w", err)
	}

	aeadCacheMu.Lock()
	if a, ok := aeadCache[string(key)]; ok {
		aeadCacheMu.Unlock()
		return a, nil
	}
	aeadCache[string(key)] = gcm
	aeadCacheMu.Unlock()
	return gcm, nil
}

func getEncryptionKey(version string) ([]byte, error) {
	envVar := keyEnvVarFor(version)

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
	version, err := CurrentKeyVersion()
	if err != nil {
		return "", err
	}
	key, err := getEncryptionKeyStrict(version)
	if err != nil {
		return "", err
	}

	gcm, err := aeadForKey(key)
	if err != nil {
		return "", err
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

	return version + ":" + base64.StdEncoding.EncodeToString(combined), nil
}

// Decrypt decrypts ciphertext with AES-256-GCM. Supports key versioning.
// Accepts both "v1:base64data" (versioned) and plain "base64data" (legacy).
// Compatible with the TypeScript decrypt() in lib/encryption.ts.
func Decrypt(ciphertextStr string) (string, error) {
	version := defaultKeyVersion
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

	// Try standard base64 first, fall back to raw (no padding) for TS compat
	data, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		data, err = base64.RawStdEncoding.DecodeString(encoded)
		if err != nil {
			return "", fmt.Errorf("decode base64: %w", err)
		}
	}

	if len(data) < 32 {
		return "", errors.New("ciphertext too short")
	}

	iv := data[:16]
	authTag := data[16:32]
	ciphertext := data[32:]

	gcm, err := aeadForKey(key)
	if err != nil {
		return "", err
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
