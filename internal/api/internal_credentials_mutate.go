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
// credential mutation is intentionally not supported. If a future
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
// injects them into the context, then calls public
// CredentialHandler.Create. Rejects on missing X-Caller-User-Id —
// see file header.
func (h *CredentialInternalAdapter) Create(w http.ResponseWriter, r *http.Request) {
	if h == nil || h.creds == nil {
		replyError(w, http.StatusInternalServerError, "credential adapter not configured")
		return
	}
	wsID, callerID, ok := h.envelope(w, r)
	if !ok {
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
func (h *CredentialInternalAdapter) Rotate(w http.ResponseWriter, r *http.Request) {
	if h == nil || h.creds == nil {
		replyError(w, http.StatusInternalServerError, "credential adapter not configured")
		return
	}
	wsID, callerID, ok := h.envelope(w, r)
	if !ok {
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
	return wsID, callerID, true
}

// injectContext stamps the workspace + role + synthetic AuthUser
// onto the request so the public handler's UserFromContext /
// WorkspaceIDFromContext / RoleFromContext lookups satisfy. The
// synthetic AuthUser carries ONLY the id; downstream code paths
// that need name/email already query the users table by id, so the
// minimum is sufficient. (Email is populated as a debug-friendly
// "x-internal" placeholder so a log line that prints it doesn't
// confuse an operator into thinking the user has no email.)
func (h *CredentialInternalAdapter) injectContext(r *http.Request, wsID, callerID string) *http.Request {
	ctx := context.WithValue(r.Context(), ctxWorkspaceID, wsID)
	ctx = context.WithValue(ctx, ctxRole, "MANAGER")
	ctx = context.WithValue(ctx, ctxUser, &AuthUser{ID: callerID, Email: "x-internal"})
	return r.WithContext(ctx)
}
