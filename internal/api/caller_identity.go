package api

// Caller-identity resolution — the unified lookup that lets a
// handler ask "who is acting on this request?" without caring
// whether the request came in via JWT (dashboard), CLI token, or
// X-Internal-Token-vouched sidecar proxy.
//
// The three call paths put identity in three different places:
//
//   - JWT-authed dashboard call:   AuthMiddleware writes *AuthUser
//                                  into ctxUser (UserFromContext).
//   - CLI token call:              same as JWT — AuthMiddleware
//                                  resolves the token to its owning
//                                  user and writes *AuthUser too.
//   - Sidecar internal-token call: AuthMiddleware doesn't run on the
//                                  /api/v1/internal/* routes (they
//                                  use the internalAuth wrapper
//                                  instead, which doesn't carry user
//                                  identity). The sidecar's
//                                  proxyToAPIFiltered (coordinator.go)
//                                  propagates the end-user's id from
//                                  the originating chat-bridge / CLI
//                                  repl request as X-Caller-User-Id.
//
// CallerUserIDFromRequest checks both. The header path returns empty
// for autonomous-agent tool calls (the chat-bridge omits the header
// when the agent itself, not a human, initiated the action), which
// is the discriminator the dual-path handlers use to decide between
// capability check (user-attributed) and autonomy_level check
// (autonomous).
//
// CallerSourceFromRequest exposes the parallel X-Caller-Source header
// for audit logging — "chat-ui", "cli-repl", or empty. It is never
// used for authorization decisions; the user_id absence/presence is
// the only gate.

import "net/http"

// Caller source labels — sent by chat-bridge / CLI when proxying a
// user-initiated action to the sidecar, propagated through to the
// backend by coordinator.go. Audit log records this verbatim so
// operators can split capability denies by surface origin without
// re-deriving from User-Agent strings.
const (
	CallerSourceChatUI  = "chat-ui"
	CallerSourceCLIRepl = "cli-repl"
)

// CallerUserIDFromRequest returns the acting user's id, preferring
// the AuthMiddleware-populated context value over the header path.
// Returns empty string when:
//
//   - The route has no auth middleware (rare — system routes only).
//   - The route is /api/v1/internal/* and the sidecar didn't
//     propagate X-Caller-User-Id (autonomous-agent tool call —
//     the handler's autonomy_level gate is the authoritative check).
//
// Handlers that gate on capability call requireCapabilityOrForbid
// with this return value as callerUserID — the empty-string branch
// there bypasses the capability gate and delegates to the
// handler's autonomy check.
func CallerUserIDFromRequest(r *http.Request) string {
	if u := UserFromContext(r.Context()); u != nil && u.ID != "" {
		return u.ID
	}
	return r.Header.Get("X-Caller-User-Id")
}

// CallerSourceFromRequest returns the X-Caller-Source header. Empty
// string for autonomous-agent tool calls or for paths that don't
// propagate (e.g. direct JWT-authed dashboard calls — the source is
// implicit in the auth method, so we don't double-stamp).
//
// Use for audit log entries, never for authorization. Treat
// unknown values as opaque strings; the constants above are the
// values we expect today but the field is intentionally open so a
// new surface (e.g. "mobile-app") can land without a coordinated
// rollout.
func CallerSourceFromRequest(r *http.Request) string {
	return r.Header.Get("X-Caller-Source")
}
