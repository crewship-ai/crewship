// Package internaltoken derives and validates workspace-bound
// X-Internal-Token values (PR-F24, token-to-workspace binding).
//
// The master internal token (cfg.Auth.InternalToken) is a single
// process-wide secret. Handing it to every sidecar meant any agent
// that captured it from inside its container (UID escalation, memory
// dump, shared file) could call /api/v1/internal/* on behalf of ANY
// workspace just by picking the ?workspace_id it wanted — the
// documented symmetric cross-tenant bypass (CHANGELOG Unreleased /
// docs/security/threat-model.mdx "Known exception"). Sidecars now
// receive a token derived per workspace instead:
//
//	wsv1.<workspace_id>.<hex(HMAC-SHA256(master, context || workspace_id))>
//
// Properties this buys:
//
//   - A captured sidecar token only authorizes the workspace baked
//     into it. The API middleware re-derives the MAC from the embedded
//     workspace_id and the in-memory master, so a tampered workspace
//     segment fails the constant-time compare.
//   - Derivation is deterministic and stateless — no issuance table,
//     no persistence. Tokens are minted at sidecar start and stay
//     valid for the lifetime of one server boot (the master is
//     regenerated when unset in config, so a restart naturally rolls
//     every derived token together with the master).
//   - The master token itself never enters a container. It remains
//     valid for host-side trusted callers (chatbridge resolver,
//     llmproxy cost monitor) that talk to the internal API over
//     loopback from inside the trust boundary.
//
// The derivation context string is versioned ("v1") so a future
// format change can coexist with old tokens during a migration
// window without ambiguity.
package internaltoken

import (
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"strings"
)

// Prefix marks a workspace-bound internal token on the wire. The
// middleware branches on it to pick the validation path; it is not a
// secret.
const Prefix = "wsv1"

// derivationContext domain-separates this HMAC use from any other
// future HMAC over the same master secret. The trailing NUL keeps the
// context unambiguously delimited from the workspace ID.
const derivationContext = "crewship internal-token workspace binding v1\x00"

// callerSigContext domain-separates the caller-identity HMAC (SignCaller
// / VerifyCaller) from the workspace-binding HMAC above so the two can
// never be confused even though both run over the same key material.
// The trailing NUL delimits the context from the signed payload.
const callerSigContext = "crewship internal-token caller-identity v1\x00"

// DeriveWorkspaceToken returns the workspace-bound internal token for
// workspaceID, derived from the master internal token. Returns ""
// when either input is empty — an empty master means internal auth is
// unconfigured (every call is rejected anyway), and a token bound to
// an empty workspace must never exist (it could otherwise act as a
// wildcard). Callers must treat "" as "do not issue".
func DeriveWorkspaceToken(master, workspaceID string) string {
	if master == "" || workspaceID == "" {
		return ""
	}
	return Prefix + "." + workspaceID + "." + mac(master, workspaceID)
}

// IsWorkspaceToken reports whether token is shaped like a
// workspace-bound token (prefix match only — call
// ValidateWorkspaceToken to verify it).
func IsWorkspaceToken(token string) bool {
	return strings.HasPrefix(token, Prefix+".")
}

// ValidateWorkspaceToken verifies token against the master secret and
// returns the workspace ID it is bound to. ok is false when the
// token is malformed, the MAC doesn't verify, the bound workspace is
// empty, or master is empty (fail closed — never authorize anything
// without a configured master).
//
// The MAC comparison is constant-time. The workspace ID segment is
// parsed with LastIndex so workspace IDs containing "." would still
// round-trip (the hex MAC can never contain one).
func ValidateWorkspaceToken(master, token string) (workspaceID string, ok bool) {
	if master == "" {
		return "", false
	}
	rest, found := strings.CutPrefix(token, Prefix+".")
	if !found {
		return "", false
	}
	i := strings.LastIndexByte(rest, '.')
	if i <= 0 {
		// No separator, or empty workspace segment ("wsv1..<mac>").
		return "", false
	}
	wsID, sig := rest[:i], rest[i+1:]
	expected := mac(master, wsID)
	if subtle.ConstantTimeCompare([]byte(sig), []byte(expected)) != 1 {
		return "", false
	}
	return wsID, true
}

func mac(master, workspaceID string) string {
	m := hmac.New(sha256.New, []byte(master))
	m.Write([]byte(derivationContext))
	m.Write([]byte(workspaceID))
	return hex.EncodeToString(m.Sum(nil))
}

// SignCaller returns a hex HMAC that binds an acting user id to a
// workspace, keyed by the internal token the caller authenticates with
// (the workspace-bound token a sidecar holds, or the master token for
// host-side callers). It is the signature the sidecar attaches as
// X-Caller-Signature alongside the forwarded X-Caller-User-Id so the
// backend can prove the identity was vouched for by a holder of the
// token — and not forged by the agent process inside the container,
// which never sees the token (ID1, PRD §11).
//
// Returns "" when any input is empty (an unsigned identity, which
// VerifyCaller will reject). The workspaceID and callerUserID are
// length-prefix-free but unambiguously delimited by a NUL byte; the
// MAC over the domain-separated context makes cross-use forgery
// infeasible.
func SignCaller(token, workspaceID, callerUserID string) string {
	if token == "" || workspaceID == "" || callerUserID == "" {
		return ""
	}
	m := hmac.New(sha256.New, []byte(token))
	m.Write([]byte(callerSigContext))
	m.Write([]byte(workspaceID))
	m.Write([]byte{0})
	m.Write([]byte(callerUserID))
	return hex.EncodeToString(m.Sum(nil))
}

// VerifyCaller reports whether signature is a valid SignCaller output
// for (token, workspaceID, callerUserID). The comparison is
// constant-time. It fails closed when any input — including the
// signature itself — is empty, so a missing X-Caller-Signature never
// authorizes a caller id.
func VerifyCaller(token, workspaceID, callerUserID, signature string) bool {
	expected := SignCaller(token, workspaceID, callerUserID)
	if expected == "" || signature == "" {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(signature), []byte(expected)) == 1
}
