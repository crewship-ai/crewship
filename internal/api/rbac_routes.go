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

import (
	"net/http"
	"strings"
)

// mutRoute is one entry in the walkable mutation route table.
type mutRoute struct {
	Method  string
	Pattern string
	Role    string
	// Scope is the CLI-token scope required to call the route (#864). It is
	// the scope analogue of Role: declared at registration, enforced from the
	// declaration in requireRoleScopeMW, and asserted present by the
	// enumeration invariant. scopeSelf marks an ownership-gated route where a
	// resource scope does not apply.
	Scope string
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

// scopeSelf marks a mutation route as scope-exempt: the route acts on the
// caller's OWN resources (own preferences, own notifications, own inbox, own
// saved views, a chat the caller participates in), so it is gated by
// ownership in the handler, not by a resource capability. A CLI token acting
// on its own principal cannot exceed any resource scope, so canScope is not
// consulted for these. Recorded (not skipped) so the scope enumeration
// invariant still sees the route as declared. Every roleSelf / authedSelfMut
// route resolves to scopeSelf.
const scopeSelf = "self"

// scopeForRoute maps a workspace-scoped mutation route to the CLI-token scope
// required to call it, drawn from the mintable vocabulary (knownScopes). The
// policy lives in this one readable table so the enumeration invariant can
// prove every mutation route resolves to a real scope, exactly as the Role
// declaration does for #824.
//
// Granularity (MVP, #864): the five resource families that have a first-class
// scope — agents, crews, credentials, skills, webhooks — map to their
// <resource>:write scope. The broad workspace-management surface (projects,
// issues, pipelines, integrations, admin, feature-flags, …) has no dedicated
// scope in the current vocabulary and maps to workspace:admin — the honest
// "this is workspace administration" gate. Finer scopes for that surface are
// a deliberate follow-up (would expand knownScopes + the New Token dialog).
// Reads are not scope-gated in this pass: GET routes register through a
// separate wrapper outside the mutation route table, so read-scoping is the
// other tracked follow-up. An unmapped resource returns "" — fail-closed: the
// enumeration test fails the build, and requireRoleScopeMW denies scoped
// tokens at runtime.
func scopeForRoute(pattern string) string {
	segs := strings.Split(strings.TrimPrefix(pattern, "/api/v1/"), "/")
	if len(segs) == 0 || segs[0] == "" {
		return ""
	}
	has := func(want string) bool {
		for _, seg := range segs {
			if seg == want {
				return true
			}
		}
		return false
	}
	switch segs[0] {
	case "agents":
		// Agent sub-resources borrow the target resource's scope so a token
		// scoped to credentials/skills can manage them on an agent.
		if has("credentials") {
			return "credentials:write"
		}
		if has("skills") {
			return "skills:write"
		}
		return "agents:write"
	case "crews", "crew-connections", "crew-templates", "crew-ai-suggest":
		return "crews:write"
	case "credentials", "credential-rotations":
		return "credentials:write"
	case "skills":
		return "skills:write"
	case "notification-channels":
		return "webhooks:write"
	case "notification-providers":
		// Instance-wide provider enable/disable toggle (#1412) — an
		// administration action, not a webhook-delivery-target write.
		return "workspace:admin"
	case "workspaces":
		// Nested resources under /workspaces/{id}/… carry their own scope;
		// everything else at the workspace level is administration.
		if has("skills") {
			return "skills:write"
		}
		if has("pipeline-webhooks") {
			return "webhooks:write"
		}
		return "workspace:admin"
	case "admin", "integrations", "connectors", "recipes", "templates",
		"projects", "milestones", "labels", "relations", "feature-flags",
		"instance", "issues", "journal", "checkpoints", "missions",
		"approvals", "inbox", "hooks", "consolidate", "eval", "mcp-registry",
		"oauth", "escalations", "cache", "recurring-issues", "triage",
		"triage-rules", "workflow-templates", "saved-views", "memory",
		"notifications", "users", "conversations":
		return "workspace:admin"
	default:
		return ""
	}
}

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
func (r *Router) recordMut(method, pattern, role, scope string) {
	r.mutationRoutes = append(r.mutationRoutes, mutRoute{Method: method, Pattern: pattern, Role: role, Scope: scope})
}

// requireRoleScopeMW enforces a declared role AND scope from the route
// registration. For the concrete-role classes it runs the same canRole gate
// the handlers used to run inline; for the roleSelf / roleInline sentinels it
// passes the role decision through to the handler. The scope gate (#864) is
// orthogonal to the role: a CLI token issued with a restricted scope set must
// additionally carry the route's scope, regardless of the caller's role.
// scopeSelf routes are ownership-gated and scope-exempt, and canScope returns
// true for unrestricted / JWT callers — so the scope gate only ever bites a
// scoped CLI token, closing the fail-open hole where scopes did nothing.
func (r *Router) requireRoleScopeMW(role, scope string, h http.HandlerFunc) http.Handler {
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
		if scope != scopeSelf && !canScope(req.Context(), scope) {
			writeProblem(w, req, http.StatusForbidden, "Forbidden")
			return
		}
		h(w, req)
	})
}

// authedMut registers a workspace-scoped mutation route: authentication +
// workspace membership + the declared role + the route's scope, all enforced
// from the declaration. The scope is resolved from the route pattern
// (scopeForRoute); roleSelf routes are ownership-gated and recorded scopeSelf.
// It records the policy so the enumeration invariants can see it. This is the
// single mediation point every state-changing, workspace-scoped endpoint flows
// through.
func (r *Router) authedMut(method, pattern, role string, h http.HandlerFunc) {
	scope := scopeForRoute(pattern)
	if role == roleSelf {
		// Self-scoped: gated by ownership in the handler, not a resource
		// capability. Mark scope-exempt regardless of the path's family.
		scope = scopeSelf
	}
	r.recordMut(method, pattern, role, scope)
	r.mux.Handle(method+" "+pattern,
		r.authMw.RequireAuth(r.authMw.RequireWorkspace(r.requireRoleScopeMW(role, scope, h))))
}

// authedSelfMut registers a session-scoped mutation route that has no
// workspace in scope (creating a workspace, testing a credential value in the
// body, own preferences, chat reactions/participants, feedback, session/token
// self-management). Authentication is required; the resource is scoped by the
// caller's user id inside the handler, so it is recorded with the roleSelf
// sentinel — declared, not skipped. Preserves the prior `authed(...)` chain
// exactly (RequireAuth only, no RequireWorkspace).
func (r *Router) authedSelfMut(method, pattern string, h http.HandlerFunc) {
	r.recordMut(method, pattern, roleSelf, scopeSelf)
	r.mux.Handle(method+" "+pattern, r.authMw.RequireAuth(h))
}

// adminRoute is one entry in the walkable admin-console read route table.
type adminRoute struct {
	Method  string
	Pattern string
}

// authedAdmin registers an admin-console READ route behind the ADMIN+ floor
// (#865). The admin surface exposes cross-user / cross-workspace operational
// data (stats, user/workspace listings, keeper audit, backups, memory
// versions); before this it registered as authed(wsCtx(...)) with no role, so
// any workspace MEMBER could read it while the destructive mutations behind
// the same console were already ADMIN+. authedAdmin gates the reads at
// roleManage (OWNER/ADMIN) from the registration — reusing the mutation
// chokepoint (requireRoleScopeMW) with scopeSelf, since reads are not
// scope-gated — and records the route so the floor invariant can enumerate it
// and a forgotten gate is a build failure, not a review catch.
func (r *Router) authedAdmin(method, pattern string, h http.HandlerFunc) {
	r.adminRoutes = append(r.adminRoutes, adminRoute{Method: method, Pattern: pattern})
	r.mux.Handle(method+" "+pattern,
		r.authMw.RequireAuth(r.authMw.RequireWorkspace(r.requireRoleScopeMW(roleManage, scopeSelf, h))))
}
