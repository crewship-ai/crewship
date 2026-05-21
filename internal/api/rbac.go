package api

// RBAC plumbing — effective-role lookups that consider per-crew
// overrides, scope-based gates for CLI tokens, and the canEdit*
// helpers per-resource handlers call before mutating.
//
// Why a dedicated file: the pre-Patch-M canRole / RoleFromContext
// pair lived next to other helpers because there was nothing else
// to say. v99 introduces three new dimensions (per-crew role,
// per-agent owner, token scopes) and a uniform place to look for
// "does this caller pass the gate?" makes the patchwork of inline
// checks across ~40 handlers easier to evolve.

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"strings"
)

// CrewRoleFromDB returns the workspace_role of the user for the
// workspace owning the crew, and the per-crew role override if
// any. Empty strings when the user isn't a member of the crew or
// the crew doesn't exist. A NULL crew_members.role column (the
// post-v99 default for legacy rows + the workspace-only mode for
// new memberships) returns "" so effectiveRole falls back to the
// workspace role.
//
// Returns the effective role (max of workspace + crew) directly
// so handlers don't have to repeat the rank comparison. Pre-
// existing code paths that only know workspace role can keep
// using RoleFromContext — RBAC is opt-in per-handler.
func CrewRoleFromDB(ctx context.Context, db *sql.DB, userID, crewID string) (string, error) {
	if db == nil || userID == "" || crewID == "" {
		return "", nil
	}
	var workspaceRole string
	var crewRole sql.NullString
	err := db.QueryRowContext(ctx, `
		SELECT wm.role, cm.role
		FROM crews c
		JOIN workspace_members wm
		  ON wm.workspace_id = c.workspace_id AND wm.user_id = ?
		LEFT JOIN crew_members cm
		  ON cm.crew_id = c.id AND cm.user_id = ?
		WHERE c.id = ? AND c.deleted_at IS NULL
	`, userID, userID, crewID).Scan(&workspaceRole, &crewRole)
	if err != nil {
		if err == sql.ErrNoRows {
			return "", nil // not a member; gate downstream returns false
		}
		return "", err
	}
	override := ""
	if crewRole.Valid {
		override = crewRole.String
	}
	return effectiveRole(workspaceRole, override), nil
}

// (canRoleForCrew helper deferred — was drafted as a wrapper around
// CrewRoleFromDB + canRole but no in-tree handler calls it yet, so
// landing it now would trip `unused` linter. canEditAgent already
// uses CrewRoleFromDB directly; the next per-crew handler that
// needs the same pattern will introduce the helper for real.)

// TokenScopesFromContext returns the scope list attached to the
// caller's CLI token, if any. Empty slice means "no scope
// restriction" — the token carries the full user role and any
// canRole(...) check is sufficient. A non-empty slice means the
// caller's token was issued with a specific scope set; canScope
// MUST be satisfied IN ADDITION TO canRole.
//
// Context key is set by AuthMiddleware after successful CLI token
// validation. JWT-authed callers never have scopes set; their
// session represents the full user role.
func TokenScopesFromContext(ctx context.Context) []string {
	v, _ := ctx.Value(ctxTokenScopes).(stringSet)
	if v == nil {
		return nil
	}
	out := make([]string, 0, len(v))
	for s := range v {
		out = append(out, s)
	}
	return out
}

// canScope reports whether the caller's CLI token (if any) carries
// permission for the given scope. Returns true when:
//
//   - The caller authenticated via JWT (no scope restriction in
//     play; full role applies).
//   - The caller's CLI token has no scope list (pre-Patch-M2
//     tokens, unrestricted).
//   - The caller's CLI token scope list contains the requested
//     scope, OR the wildcard "*", OR a parent scope that subsumes
//     the requested one (e.g. token holds "agents:*" and the
//     handler asked for "agents:write").
//
// Scopes use the shape "<resource>:<action>" with a flat
// resource namespace. Wildcards are recognised at the action level
// ("agents:*") and at the top ("*"). No nested resource wildcards
// — "agents:*" matches "agents:write" but not "agents.foo:write"
// (we don't have nested resources, but the rule is explicit so a
// future change is a deliberate decision).
func canScope(ctx context.Context, requested string) bool {
	scopes := ctx.Value(ctxTokenScopes)
	if scopes == nil {
		return true // JWT-authed or pre-v100 token — no restriction
	}
	set, ok := scopes.(stringSet)
	if !ok || len(set) == 0 {
		// Empty stringSet means "explicitly no scopes" which we
		// treat as "no permission" — distinct from "key not set
		// at all" above. Refuses by default for safety.
		return false
	}
	if _, hasWildcard := set["*"]; hasWildcard {
		return true
	}
	if _, exact := set[requested]; exact {
		return true
	}
	// Resource-level wildcard: "agents:*" matches "agents:write".
	if colon := strings.IndexByte(requested, ':'); colon > 0 {
		resourceWildcard := requested[:colon] + ":*"
		if _, ok := set[resourceWildcard]; ok {
			return true
		}
	}
	return false
}

// canEditAgent gates write operations on a specific agent. The
// pre-Patch-M3 rule was just canRole(role, "create") which let any
// workspace MANAGER edit any agent — fine when the workspace had
// one human but accident-prone the moment two MANAGERs share a
// fleet. The post-v99 rule:
//
//   - OWNER / ADMIN: edit everything (unchanged).
//   - MANAGER: edit agents they created OR agents where they have
//     ADMIN/OWNER role on the crew (per-crew elevation).
//   - MEMBER / VIEWER: refused outright.
//
// callerUserID is the authenticated user. agentID is the target.
// The function loads the agent once to read its workspace_id and
// created_by_user_id; callers that already have those values can
// inline the logic instead.
func canEditAgent(ctx context.Context, db *sql.DB, callerUserID, callerRole, agentID string) (bool, error) {
	if callerRole == "OWNER" || callerRole == "ADMIN" {
		return true, nil
	}
	if !canRole(callerRole, "create") {
		return false, nil // MEMBER / VIEWER never edit
	}
	// MANAGER: ownership check.
	var agentWS, agentCrew sql.NullString
	var createdBy sql.NullString
	err := db.QueryRowContext(ctx, `
		SELECT workspace_id, crew_id, created_by_user_id
		FROM agents
		WHERE id = ? AND deleted_at IS NULL
	`, agentID).Scan(&agentWS, &agentCrew, &createdBy)
	if err != nil {
		if err == sql.ErrNoRows {
			return false, nil
		}
		return false, err
	}
	if createdBy.Valid && createdBy.String == callerUserID {
		return true, nil
	}
	// Per-crew elevation: if this MANAGER is ADMIN/OWNER on the
	// crew the agent belongs to, they get edit rights.
	if agentCrew.Valid {
		crewRole, err := CrewRoleFromDB(ctx, db, callerUserID, agentCrew.String)
		if err != nil {
			return false, err
		}
		if crewRole == "OWNER" || crewRole == "ADMIN" {
			return true, nil
		}
	}
	return false, nil
}

// stringSet is a tiny generic-free set used for scope lookups so
// canScope can do O(1) membership tests without importing a third-
// party slices package or paying for the slice-walk on every call.
type stringSet map[string]struct{}

// parseScopes parses a JSON array string from the cli_tokens.scopes
// column into a normalised stringSet. Empty input (NULL column),
// invalid JSON, or non-array shapes all return nil — the caller
// then treats the token as "unrestricted" (pre-v100 behaviour). The
// shape is validated at issue time, so a value that fails to parse
// at validation time indicates DB corruption, not an attack — but
// returning nil here keeps the auth path moving so a corrupt scope
// list can't lock a user out entirely.
func parseScopes(jsonValue string) stringSet {
	jsonValue = strings.TrimSpace(jsonValue)
	if jsonValue == "" {
		return nil
	}
	var arr []string
	if err := json.Unmarshal([]byte(jsonValue), &arr); err != nil {
		return nil
	}
	if len(arr) == 0 {
		return nil
	}
	set := make(stringSet, len(arr))
	for _, s := range arr {
		s = strings.TrimSpace(s)
		if s != "" {
			set[s] = struct{}{}
		}
	}
	// Empty after trim (input was ["", " "] etc.) — return nil so
	// canScope treats the token as pre-v100 unrestricted instead of
	// "explicitly no scopes = deny everything". Matches the
	// jsonValue=="[]" early-return above and keeps the fail-safe
	// behaviour consistent across both no-entries paths.
	if len(set) == 0 {
		return nil
	}
	return set
}

// ctxTokenScopes is the context key under which AuthMiddleware
// stashes the caller's CLI token scope set. Distinct contextKey
// type so we don't collide with other packages stashing strings
// under "scopes".
var ctxTokenScopes = contextKey("token_scopes")

// replyForbidden is the structured 403 helper used by all RBAC
// gates post-Patch-M4. Emits a single audit-friendly journal-style
// log line so an operator chasing a 403 can answer "what did this
// user actually try, with what role" without grep'ing a wall of
// generic 403s. The wire body stays minimal — the caller only
// needs to know the request was refused; the reason audit lives
// server-side.
//
// Pre-M4 deny paths used a mix of `replyError(w, 403, "Forbidden")`
// and inline logger.Warn calls with inconsistent field names. M4
// standardises on the (subject_user_id, role, action, resource)
// quartet so log queries / SIEM rules can rely on a single shape
// across every RBAC gate in the codebase.
func replyForbidden(w http.ResponseWriter, logger interface {
	Warn(msg string, args ...any)
}, callerUserID, callerRole, action, resource string) {
	if logger != nil {
		logger.Warn("rbac: access denied",
			"user_id", callerUserID,
			"role", callerRole,
			"action", action,
			"resource", resource,
		)
	}
	replyError(w, http.StatusForbidden, "Forbidden")
}

// requireRoleOrForbid is a convenience wrapper that combines the
// pre-M4 canRole gate with the M4 audit emit. Saves the four-line
// boilerplate every handler used to write:
//
//	if !canRole(role, "create") {
//	    replyError(w, http.StatusForbidden, "Forbidden")
//	    return
//	}
//
// becomes:
//
//	if !requireRoleOrForbid(w, h.logger, callerID, role, "create", "agent.update", "agent:"+id) {
//	    return
//	}
//
// Returns true on success so the caller can early-return on false.
// Picks the FIRST failing action for the audit so the line names
// the concrete missing privilege rather than a vague "or".
func requireRoleOrForbid(w http.ResponseWriter, logger interface {
	Warn(msg string, args ...any)
}, callerUserID, callerRole, action, resource string, requiredActions ...string) bool {
	if canRole(callerRole, requiredActions...) {
		return true
	}
	replyForbidden(w, logger, callerUserID, callerRole, action, resource)
	return false
}
