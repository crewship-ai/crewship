package backup

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"strings"
)

// NextAuthSecretEnv holds the JWE session-key derivation secret used
// by internal/auth. An instance backup needs it so a disaster
// recovery into a fresh host keeps the admin's existing auth tokens
// valid — without this, restore lands a DB full of users whose
// sessions all expire immediately.
const NextAuthSecretEnv = "NEXTAUTH_SECRET"

// CollectedAuthKeys is the tuple written into the payload under the
// authkeys/ prefix. Flat struct keeps the JSON stable across versions;
// new fields must be additive.
type CollectedAuthKeys struct {
	NextAuthSecret string `json:"nextauth_secret"`
}

// CollectAuthKeys reads the authentication secrets from the running
// process env. On a correctly-configured instance the env must be
// set before crewshipd starts; an absent secret produces an error so
// instance backup never silently writes an empty authkeys section.
//
// The returned NextAuthSecret is the RAW env value — TrimSpace is
// used only to detect an unset / whitespace-only env. Mutating the
// secret would change the HKDF inputs and silently invalidate every
// existing JWE on restore.
func CollectAuthKeys() (*CollectedAuthKeys, error) {
	raw := os.Getenv(NextAuthSecretEnv)
	if strings.TrimSpace(raw) == "" {
		return nil, fmt.Errorf("backup: %s not set — refuse to backup auth keys section", NextAuthSecretEnv)
	}
	return &CollectedAuthKeys{NextAuthSecret: raw}, nil
}

// RotateAuthKeys generates a fresh 64-byte NEXTAUTH_SECRET, persists
// it via the caller-supplied writer, and returns the new value so a
// CLI can print it for the operator to paste into the service
// definition. Writer signature is intentionally pluggable: in
// production the caller writes it to .env / systemd drop-in /
// kubernetes secret; tests supply a noop.
//
// Returning the new secret is a DELIBERATE trade: the caller MUST
// persist it (or the operator prints it) before the next process
// start, otherwise all sessions silently break. The alternative —
// mutating the env of the current process — is useless because
// children (sidecars) already forked with the old value.
func RotateAuthKeys(writer func(newSecret string) error) (string, error) {
	if writer == nil {
		return "", errors.New("backup: RotateAuthKeys requires a writer")
	}
	var buf [64]byte
	if _, err := rand.Read(buf[:]); err != nil {
		return "", fmt.Errorf("backup: rotate: rand: %w", err)
	}
	newSecret := hex.EncodeToString(buf[:])
	if err := writer(newSecret); err != nil {
		return "", fmt.Errorf("backup: rotate: persist: %w", err)
	}
	return newSecret, nil
}

// ShouldRotateAuthKeysOnRestore returns true when a restore must
// force an auth-key rotation because the bundle is being loaded onto
// a different host than the one that produced it. The rotation is
// MANDATORY — leaving the original keys in place on a foreign host
// would let anyone who ever held a session token on the source host
// impersonate that user here.
func ShouldRotateAuthKeysOnRestore(ctx context.Context, db *sql.DB, m *Manifest) bool {
	if m == nil || m.Scope != ScopeInstance {
		return false
	}
	return IsCrossInstanceRestore(ctx, db, m)
}
