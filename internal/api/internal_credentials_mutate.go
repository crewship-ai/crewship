package api

// Internal sidecar routes for credential Create + Rotate
// (PRD-SLASH-CAPABILITIES-2026 §6.4).
//
// Differs from internal_routines.go / internal_skills.go in one
// material way: the public CredentialHandler.Create + Rotate both
// require a non-nil UserFromContext (audit attribution writes the
// rotation_initiator_user_id column; the row literally cannot exist
// without it). So this adapter REQUIRES X-Caller-User-Id on the
// inbound and rejects with 401 when absent — autonomous-agent
// credential mutation is intentionally not supported.
//
// ID1 (PRD §11): because X-Caller-User-Id is forgeable from inside the
// agent container, the adapter ALSO requires a valid X-Caller-Signature
// — an HMAC over (workspace_id, caller_user_id) keyed by the
// workspace-bound internal token that only the sidecar process holds.
// A missing or invalid signature is rejected with 401 before any
// capability check, so a forged caller id can never escalate. If a future
// autonomous workflow needs to rotate a credential it can either
// (a) act on behalf of a real user via a delegated token (out of
// scope here) or (b) we add an explicit system-actor row in users
// and gate it behind a stricter capability than the human one.
//
// Why not the comfortable fallback to "system": rotation has a
// blast radius (active sessions get cut over to the new value
// across the workspace). The audit log MUST name a human; "system"
// is fine for issue.create where the worst case is a junk ticket,
// not for credential lifecycle.

import (
	"context"
	"net/http"

	"github.com/crewship-ai/crewship/internal/auth/internaltoken"
)

// CredentialInternalAdapter wraps CredentialHandler.Create + Rotate
// so the internal /api/v1/internal/credentials and
// /api/v1/internal/credentials/{credentialId}/rotate routes can
// dispatch into them with workspace + role + user context injected
// from query params and the X-Caller-User-Id header.
type CredentialInternalAdapter struct {
	creds *CredentialHandler
}

// NewCredentialInternalAdapter constructs the adapter at router-
// wiring time so it reuses the public CredentialHandler instance
// (shared *sql.DB, shared logger, no duplicate state).
func NewCredentialInternalAdapter(creds *CredentialHandler) *CredentialInternalAdapter {
	return &CredentialInternalAdapter{creds: creds}
}

// Create reads workspace_id + caller from the request envelope,
// gates on credential.create capability, injects context, then
// calls public CredentialHandler.Create. Rejects on missing
// X-Caller-User-Id at the envelope step — see file header.
func (h *CredentialInternalAdapter) Create(w http.ResponseWriter, r *http.Request) {
	if h == nil || h.creds == nil {
		replyError(w, http.StatusInternalServerError, "credential adapter not configured")
		return
	}
	wsID, callerID, ok := h.envelope(w, r)
	if !ok {
		return
	}
	if !requireCapabilityOrForbid(w, r, h.creds.logger, h.creds.db,
		wsID, callerID,
		CapabilityCredentialCreate, "credential.create", "credential:new") {
		return
	}
	r = h.injectContext(r, wsID, callerID)
	h.creds.Create(w, r)
}

// Rotate reads workspace_id + caller + credentialId from the
// request envelope. Public Rotate reads credentialId from
// r.PathValue("credentialId") — the internal route uses the same
// pattern so SetPathValue is unnecessary (the router will populate
// it from /credentials/{credentialId}/rotate).
//
// credential.rotate is a distinct capability from credential.create
// so an admin can let an oncall user rotate a leaked token without
// also granting them blanket vault-add reach.
func (h *CredentialInternalAdapter) Rotate(w http.ResponseWriter, r *http.Request) {
	if h == nil || h.creds == nil {
		replyError(w, http.StatusInternalServerError, "credential adapter not configured")
		return
	}
	wsID, callerID, ok := h.envelope(w, r)
	if !ok {
		return
	}
	credID := r.PathValue("credentialId")
	if !requireCapabilityOrForbid(w, r, h.creds.logger, h.creds.db,
		wsID, callerID,
		CapabilityCredentialRotate, "credential.rotate", "credential:"+credID) {
		return
	}
	r = h.injectContext(r, wsID, callerID)
	h.creds.Rotate(w, r)
}

// envelope unpacks workspace_id (query) + caller user id (header)
// and returns them, or writes a 4xx and returns ok=false. Centralised
// so Create and Rotate share the same validation surface; future
// internal credential routes (delete, test, ...) can reuse it.
func (h *CredentialInternalAdapter) envelope(w http.ResponseWriter, r *http.Request) (string, string, bool) {
	wsID := r.URL.Query().Get("workspace_id")
	if wsID == "" {
		replyError(w, http.StatusBadRequest, "workspace_id required")
		return "", "", false
	}
	callerID := r.Header.Get("X-Caller-User-Id")
	if callerID == "" {
		// Autonomous-agent path — explicitly rejected for credential
		// mutation. See file header for rationale.
		replyError(w, http.StatusUnauthorized, "credential mutation requires user attribution (X-Caller-User-Id)")
		return "", "", false
	}
	// ID1: X-Caller-User-Id is attacker-influenceable — an agent inside the
	// container can reach the sidecar over loopback and set any user id. Before
	// we trust it for a privileged credential mutation it MUST carry a valid
	// HMAC signature over (workspace_id, caller_user_id) keyed by a holder of
	// the workspace-bound internal token. Crucially, the agent-reachable sidecar
	// hop deliberately does NOT sign agent-supplied caller ids (that would be a
	// signing oracle — see internal/sidecar/coordinator.go), so an agent's
	// forged caller id arrives unsigned and is rejected here. Autonomous-agent
	// credential mutation is therefore unsupported by construction; a future
	// trusted out-of-container signer could attach a real signature. We re-derive
	// the MAC from X-Internal-Token (already validated against the master secret
	// by the internal-auth middleware) and constant-time compare. Missing/invalid
	// → reject. The DB capability re-derivation below (requireCapabilityOrForbid)
	// stays as defense-in-depth.
	if !verifyCallerSignature(r, wsID, callerID) {
		replyError(w, http.StatusUnauthorized, "caller identity is unsigned or its signature is invalid (X-Caller-Signature)")
		return "", "", false
	}
	return wsID, callerID, true
}

// verifyCallerSignature checks the X-Caller-Signature header against an
// HMAC of (workspaceID, callerID) keyed by the internal token the
// caller authenticated with. The token is read from X-Internal-Token —
// the internal-auth middleware already proved it is genuine (it
// re-derives the MAC from the in-memory master), so only a real token
// holder could have produced a matching signature. workspaceID is the
// middleware-enforced query workspace, which for a workspace-bound
// token equals the token's own workspace, so a signature can only be
// honoured inside the tenant it was minted for.
func verifyCallerSignature(r *http.Request, workspaceID, callerID string) bool {
	token := r.Header.Get("X-Internal-Token")
	sig := r.Header.Get("X-Caller-Signature")
	return internaltoken.VerifyCaller(token, workspaceID, callerID, sig)
}

// injectContext stamps the workspace + role + synthetic AuthUser
// onto the request so the public handler's UserFromContext /
// WorkspaceIDFromContext / RoleFromContext lookups satisfy. The
// synthetic AuthUser carries ONLY the id; downstream code paths
// that need name/email already query the users table by id, so the
// minimum is sufficient. (Email is populated as a debug-friendly
// "x-internal" placeholder so a log line that prints it doesn't
// confuse an operator into thinking the user has no email.)
//
// Role injection is ADMIN (not MANAGER like the other internal
// mirrors) because CredentialHandler.Rotate gates on canRole("manage")
// — manage requires ADMIN+. The capability gate added in commit 6 is
// the actual security boundary for slash-initiated credential
// mutation; this role injection just clears the pre-capability
// role check that predates the dual-path design.
func (h *CredentialInternalAdapter) injectContext(r *http.Request, wsID, callerID string) *http.Request {
	ctx := context.WithValue(r.Context(), ctxWorkspaceID, wsID)
	ctx = context.WithValue(ctx, ctxRole, "ADMIN")
	ctx = context.WithValue(ctx, ctxUser, &AuthUser{ID: callerID, Email: "x-internal"})
	return r.WithContext(ctx)
}
