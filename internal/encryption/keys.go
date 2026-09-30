package encryption

import (
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"sort"
	"strconv"
	"strings"
)

// Key material for the instance backup's recovery kit and for a drill that
// must unlock sealed values with keys it was handed rather than the process
// environment.

// KeyEnvVar returns the env var holding the key for version:
// ENCRYPTION_KEY for v1, ENCRYPTION_KEY_V<N> for later generations.
func KeyEnvVar(version string) string { return keyEnvVarFor(version) }

// ValidKeyVersion reports whether version is an envelope version ("v1"…"v99").
func ValidKeyVersion(version string) bool { return versionPattern.MatchString(version) }

// ResolvedKey is the key an envelope version decrypts with on this server,
// and where it came from.
type ResolvedKey struct {
	Version string
	// Env is the variable the key was read from. For a version whose own
	// variable is unset, Decrypt falls back to ENCRYPTION_KEY — Env then says
	// ENCRYPTION_KEY, and FellBack is true.
	Env      string
	FellBack bool
	Key      []byte
	// Value is the variable's exact string. Keep it byte for byte: the
	// journal chain key is derived from the ENCRYPTION_KEY string itself, so
	// re-encoding the hex (upper to lower case) would break chain
	// verification on the restored server.
	Value string
}

// ResolveKey returns the key Decrypt would use for version, following the
// same fallback Decrypt applies (a missing ENCRYPTION_KEY_V<N> reads
// ENCRYPTION_KEY). An error means envelopes of that version cannot be opened
// on this server.
func ResolveKey(version string) (ResolvedKey, error) {
	if !ValidKeyVersion(version) {
		return ResolvedKey{}, fmt.Errorf("encryption: %q is not a key version", version)
	}
	env := keyEnvVarFor(version)
	raw := os.Getenv(env)
	fell := false
	if raw == "" && env != "ENCRYPTION_KEY" {
		raw = os.Getenv("ENCRYPTION_KEY")
		env, fell = "ENCRYPTION_KEY", true
	}
	if raw == "" {
		return ResolvedKey{}, fmt.Errorf("%s is not set", keyEnvVarFor(version))
	}
	key, err := hex.DecodeString(raw)
	if err != nil {
		return ResolvedKey{}, fmt.Errorf("%s is not valid hex: %w", env, err)
	}
	if len(key) != 32 {
		return ResolvedKey{}, fmt.Errorf("%s must decode to 32 bytes, got %d", env, len(key))
	}
	return ResolvedKey{Version: version, Env: env, FellBack: fell, Key: key, Value: raw}, nil
}

// ConfiguredKeyVersions lists every version with its own key variable set in
// the environment (v1 for ENCRYPTION_KEY, vN for ENCRYPTION_KEY_VN), sorted.
func ConfiguredKeyVersions() []string {
	var out []string
	if os.Getenv("ENCRYPTION_KEY") != "" {
		out = append(out, "v1")
	}
	for _, kv := range os.Environ() {
		name, val, ok := strings.Cut(kv, "=")
		if !ok || val == "" || !strings.HasPrefix(name, "ENCRYPTION_KEY_V") {
			continue
		}
		v := "v" + strings.TrimPrefix(name, "ENCRYPTION_KEY_V")
		if ValidKeyVersion(v) && v != "v1" {
			out = append(out, v)
		}
	}
	SortVersions(out)
	return out
}

// SortVersions orders key versions numerically (v2 before v10).
func SortVersions(vs []string) {
	sort.Slice(vs, func(i, j int) bool {
		a, _ := strconv.Atoi(strings.TrimPrefix(vs[i], "v"))
		b, _ := strconv.Atoi(strings.TrimPrefix(vs[j], "v"))
		return a < b
	})
}

// ErrNoKeyForVersion is returned by DecryptWithKeys when the envelope's
// version has no key in the map.
var ErrNoKeyForVersion = errors.New("encryption: no key for this envelope version")

// DecryptWithKeys decrypts an envelope with an explicit version → key map
// instead of the environment. No fallback: an envelope whose version is not
// in keys is ErrNoKeyForVersion, so a drill can tell "this key is missing"
// from "this key is wrong". A bare (pre-envelope) value is read as v1, as
// Decrypt does.
func DecryptWithKeys(ciphertextStr string, keys map[string][]byte) (string, error) {
	version := defaultKeyVersion
	encoded := ciphertextStr
	if idx := strings.Index(ciphertextStr, ":"); idx > 0 && idx <= 3 {
		prefix := ciphertextStr[:idx]
		if versionPattern.MatchString(prefix) {
			version = prefix
			encoded = ciphertextStr[idx+1:]
		}
	}
	key, ok := keys[version]
	if !ok || len(key) == 0 {
		return "", fmt.Errorf("%w: %s", ErrNoKeyForVersion, version)
	}
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
	iv, authTag, ciphertext := data[:16], data[16:32], data[32:]
	gcm, err := aeadForKey(key)
	if err != nil {
		return "", err
	}
	sealed := make([]byte, len(data)-len(iv))
	copy(sealed, ciphertext)
	copy(sealed[len(ciphertext):], authTag)
	plaintext, err := gcm.Open(nil, iv, sealed, nil)
	if err != nil {
		return "", fmt.Errorf("decrypt: %w", err)
	}
	return string(plaintext), nil
}
