package api

// Route-table authorization chokepoint (#809 / #811).
//
// Before this, every mutation route registered as `authed(wsCtx(handler))` —
// authentication + workspace *membership* only, no role — and the actual
// authorization was a hand-placed `requireRole`/`canRole` call INSIDE each
// handler. There were ~135 such inline calls against ~200 mutation routes, so
// a single forgotten call left a state-changing endpoint silently open to any
// workspace member (it fails OPEN — the mutation just succeeds). No test
// walked the route table, so the next omission shipped ungated — which is
// exactly what happened to `checkpoint Create` in #792.
//
// The fix is Saltzer & Schroeder *complete mediation*: declare the required
// role AT REGISTRATION and enforce it from that declaration in one place.
// http.ServeMux is not walkable, so authedMut records {method, pattern, role}
// into Router.mutationRoutes AND mounts the route behind a single role-
// enforcing middleware. The enumeration test then asserts every recorded
// route declares a role, and a source guard asserts no mutation route is
// registered the old `authed(...)` way — making a forgotten gate a build
// failure, not a review catch.
//
// The pre-existing inline requireRole/canRole checks are intentionally kept as
// belt-and-suspenders; the middleware is the declarative chokepoint layered in
// front of them. This is a registration refactor only — it does not change the
// 5-tier role model or the v109 capability layer.

import "net/http"

// mutRoute is one entry in the walkable mutation route table.
type mutRoute struct {
	Method  string
	Pattern string
	Role    string
}

// Mutation role declarations. The value is the *capability class* a route
// requires; requireRoleMW resolves the concrete-role gates through canRole,
// which already maps these onto the 5-tier model:
//
//   - roleCreate  → OWNER / ADMIN / MANAGER  (canRole "create"/"update" tier)
//   - roleManage  → OWNER / ADMIN            (canRole "manage"/"delete" tier)
//
// Two sentinels mark routes whose finer gate cannot be reduced to a single
// workspace-role check and so is enforced INSIDE the handler. The middleware
// only guarantees workspace membership for them (that is unchanged from the
// old `authed(wsCtx(...))` behaviour); recording them keeps the route
// *declared* rather than silently skipped:
//
//   - roleSelf   → self-scoped: ownership / user_id / visibility is enforced
//     in the handler (e.g. own preferences, reactions, notifications, a
//     saved-view the caller owns, inbox items the caller can see).
//   - roleInline → the handler runs a layered gate the middleware must not
//     pre-empt: the v109 role-OR-capability gate
//     (requireRoleOrCapabilityOrForbid — a MEMBER holding an explicit
//     capability passes) or the per-agent owner / per-crew-elevation gate
//     (canEditAgent / effectiveRole). Gating these on the plain workspace
//     role in middleware would wrongly refuse a legitimately-elevated MEMBER,
//     so the middleware passes through and the handler decides.
const (
	roleCreate = "create"
	roleManage = "manage"
	roleSelf   = "self"
	roleInline = "inline"
)

// isDeclaredRole reports whether role is one of the recognised declarations.
// The enumeration test uses it so a typo'd or empty role fails the build.
func isDeclaredRole(role string) bool {
	switch role {
	case roleCreate, roleManage, roleSelf, roleInline:
		return true
	default:
		return false
	}
}

// recordMut appends a route's declared policy to the walkable table.
func (r *Router) recordMut(method, pattern, role string) {
	r.mutationRoutes = append(r.mutationRoutes, mutRoute{Method: method, Pattern: pattern, Role: role})
}

// requireRoleMW enforces a declared role from the route registration. For the
// concrete-role classes it runs the same canRole gate the handlers used to run
// inline; for the roleSelf / roleInline sentinels it passes through to the
// handler, which owns the finer-grained decision (membership is already
// established by RequireWorkspace upstream).
func (r *Router) requireRoleMW(role string, h http.HandlerFunc) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		switch role {
		case roleSelf, roleInline:
			// Handler-authoritative — see the role-const doc comment.
		default:
			if !canRole(RoleFromContext(req.Context()), role) {
				writeProblem(w, req, http.StatusForbidden, "Forbidden")
				return
			}
		}
		h(w, req)
	})
}

// authedMut registers a workspace-scoped mutation route: authentication +
// workspace membership + the declared role, enforced from the declaration.
// It records the policy so the enumeration invariant can see it. This is the
// single mediation point every state-changing, workspace-scoped endpoint flows
// through.
func (r *Router) authedMut(method, pattern, role string, h http.HandlerFunc) {
	r.recordMut(method, pattern, role)
	r.mux.Handle(method+" "+pattern,
		r.authMw.RequireAuth(r.authMw.RequireWorkspace(r.requireRoleMW(role, h))))
}

// authedSelfMut registers a session-scoped mutation route that has no
// workspace in scope (creating a workspace, testing a credential value in the
// body, own preferences, chat reactions/participants, feedback, session/token
// self-management). Authentication is required; the resource is scoped by the
// caller's user id inside the handler, so it is recorded with the roleSelf
// sentinel — declared, not skipped. Preserves the prior `authed(...)` chain
// exactly (RequireAuth only, no RequireWorkspace).
func (r *Router) authedSelfMut(method, pattern string, h http.HandlerFunc) {
	r.recordMut(method, pattern, roleSelf)
	r.mux.Handle(method+" "+pattern, r.authMw.RequireAuth(h))
}
