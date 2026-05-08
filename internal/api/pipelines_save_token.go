package api

// HMAC-signed save_token plumbing for routines.
//
// Threat model (per PIPELINES.md §17 + CodeRabbit flag on types.go:499):
// the save endpoint accepts last_test_run_at + last_test_run_passed in
// the body, and the freshness check ("within 5 minutes, not in future")
// only filters the obvious cases. A malicious in-process caller can
// still set last_test_run_passed=true + last_test_run_at=now() and
// bypass the gate without actually running test_run.
//
// The HMAC save_token closes that loophole: TestRun signs a token
// over (workspace_id, definition_hash, user_id, server-issued ts),
// the body Save passes the token back, the server re-derives the HMAC
// and verifies. No body-trust on the gate-pass — only proof of "we
// just ran test_run for THIS definition_hash for THIS user".
//
// Why HMAC over JWT/PASETO: the secret is process-local + the token
// is short-lived (5 min), so the signing primitive doesn't need
// rotation/key-id machinery. HMAC-SHA256 is the simplest thing that
// matches the threat surface.

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
)

const (
	// saveTokenMaxAge bounds the freshness window. Five minutes
	// matches the existing timestamp-based gate (testRunFreshness)
	// so behaviour is the same observable contract.
	saveTokenMaxAge = 5 * time.Minute

	// saveTokenSep is the in-token separator between issued-ts and
	// HMAC. Picked from a non-base64 / non-hex character so a token
	// is unambiguously parseable.
	saveTokenSep = "."
)

// signSaveToken returns "<unix_ts>.<hex_hmac>". Inputs are
// concatenated with pipe — the pipe is also non-base64/hex so a
// crafted definition_hash can't collide with the boundary.
func signSaveToken(secret []byte, workspaceID, definitionHash, userID string, issuedAt time.Time) string {
	if len(secret) == 0 {
		return ""
	}
	ts := issuedAt.Unix()
	msg := fmt.Sprintf("%s|%s|%s|%d", workspaceID, definitionHash, userID, ts)
	mac := hmac.New(sha256.New, secret)
	_, _ = mac.Write([]byte(msg))
	return strconv.FormatInt(ts, 10) + saveTokenSep + hex.EncodeToString(mac.Sum(nil))
}

// verifySaveToken checks that `token` is a valid HMAC over the same
// inputs the issuer signed and is within saveTokenMaxAge. Returns nil
// on success, a typed error otherwise. We deliberately don't expose
// the specific failure mode to the caller (timing-safe: an attacker
// guessing the HMAC shouldn't get oracle leaks via different errors)
// — the handler maps every non-nil return to a generic 422.
func verifySaveToken(secret []byte, token, workspaceID, definitionHash, userID string) error {
	if len(secret) == 0 {
		return errors.New("save_token: server has no signing secret configured")
	}
	if token == "" {
		return errors.New("save_token: empty")
	}
	parts := strings.SplitN(token, saveTokenSep, 2)
	if len(parts) != 2 {
		return errors.New("save_token: malformed")
	}
	ts, err := strconv.ParseInt(parts[0], 10, 64)
	if err != nil {
		return errors.New("save_token: bad timestamp")
	}
	issued := time.Unix(ts, 0)
	if age := time.Since(issued); age > saveTokenMaxAge || age < -1*time.Minute {
		return errors.New("save_token: expired or future-dated")
	}

	expected := signSaveToken(secret, workspaceID, definitionHash, userID, issued)
	if !hmac.Equal([]byte(token), []byte(expected)) {
		return errors.New("save_token: HMAC mismatch")
	}
	return nil
}
