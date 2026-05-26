package api

// Capability enforcement helper — paralelní k requireRoleOrForbid
// (rbac.go:283), ale gates na per-membership capability set místo
// na 5-tier role rank.
//
// Layered usage: handlers that want both gates call requireRoleOrForbid
// first (broad surface gate by role) and then requireCapabilityOrForbid
// (specific action gate by capability). Belt + braces during the
// rollout window so a configuration error in one layer can't bypass
// both. Once capabilities are the authoritative gate, the role check
// can be relaxed to "any non-VIEWER" or removed per-route at the
// PRD §6 graduation milestone.
//
// Cache: 30 s TTL keyed by (workspace_id, user_id). Hot-path target
// — every slash-initiated API call hits this once. 30 s strikes the
// balance between freshness (admin revoke takes effect within half a
// minute) and DB load (saves the join from running on every request
// from the same chat session).

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"sync"
	"time"
)

// capabilityCache is a workspace-scoped membership capability cache.
// Singleton per process — RBAC decisions must not diverge across
// router instances in the same binary, so we deliberately don't
// allow multiple instances.
//
// Eviction is lazy: entries are checked for expiry on read; a
// background sweep is intentionally absent because the working set
// is bounded by active members × workspaces (low cardinality) and
// process restart already clears it.
//
// HA / multi-process invalidation is out of scope — see
// PRD-SLASH-CAPABILITIES-2026.md §11. A future Redis-backed cache
// with pub/sub invalidate is the upgrade path.
type capabilityCache struct {
	mu    sync.RWMutex
	ttl   time.Duration
	store map[string]capabilityCacheEntry
}

type capabilityCacheEntry struct {
	caps      map[string]struct{}
	role      string
	expiresAt time.Time
}

var defaultCapabilityCache = &capabilityCache{
	ttl:   30 * time.Second,
	store: map[string]capabilityCacheEntry{},
}

// capabilityCacheKey is workspace + user joined by NUL — both
// components come from server-controlled identifiers, so collision
// is impossible and cheap to compute.
func capabilityCacheKey(workspaceID, userID string) string {
	return workspaceID + "\x00" + userID
}

// get returns the cached entry if present and unexpired.
func (c *capabilityCache) get(workspaceID, userID string) (capabilityCacheEntry, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	e, ok := c.store[capabilityCacheKey(workspaceID, userID)]
	if !ok {
		return capabilityCacheEntry{}, false
	}
	if timeNow().After(e.expiresAt) {
		return capabilityCacheEntry{}, false
	}
	return e, true
}

func (c *capabilityCache) put(workspaceID, userID string, caps map[string]struct{}, role string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.store[capabilityCacheKey(workspaceID, userID)] = capabilityCacheEntry{
		caps:      caps,
		role:      role,
		expiresAt: timeNow().Add(c.ttl),
	}
}

// Invalidate drops a cached entry so the next lookup re-reads the
// row from DB. Called by the admin grant/revoke handlers after a
// successful UPDATE so the change is visible within the same
// request the operator just made (sub-30s wait window collapses to
// zero for the workspace admin themselves).
//
// Empty userID invalidates every entry in the workspace — useful
// when bulk-updating capabilities via SQL migration / fixture seed.
func (c *capabilityCache) Invalidate(workspaceID, userID string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if userID != "" {
		delete(c.store, capabilityCacheKey(workspaceID, userID))
		return
	}
	prefix := workspaceID + "\x00"
	for k := range c.store {
		if len(k) >= len(prefix) && k[:len(prefix)] == prefix {
			delete(c.store, k)
		}
	}
}

// InvalidateCapabilityCache exposes the package-level cache so admin
// handlers in other files (member grant/revoke, preset apply) can
// drop stale entries after mutating the underlying row.
func InvalidateCapabilityCache(workspaceID, userID string) {
	defaultCapabilityCache.Invalidate(workspaceID, userID)
}

// resolveCapabilitiesFromRow turns the raw (capsJSON, role) pair from
// a workspace_members SELECT into the authoritative capability set
// the runtime acts on. Single source of truth so the read API
// (GetMemberCapabilities) and the runtime gate (requireCapabilityOrForbid)
// can't diverge on how they interpret edge cases.
//
// Resolution rules:
//
//   - capsJSON IS NULL → role-derived fallback. Legacy /
//     upgrade-in-progress path: a row exists but the application
//     write that should fill the column hasn't happened. Falling back
//     to the role bundle keeps a fresh INSERT working before the
//     backfill catches up.
//   - capsJSON IS valid JSON with at least one known entry → that set
//     verbatim. Operator intent honoured.
//   - capsJSON IS present but ParseCapabilities returns nil (empty
//     array, malformed JSON, only future-unknown strings) →
//     chat-only baseline, NOT the role fallback. An explicit-but-
//     drained value reflects deliberate operator intent ("strip this
//     user back to chat") and the runtime must not silently restore
//     role-derived privileges.
func resolveCapabilitiesFromRow(capsJSON sql.NullString, role string) map[string]struct{} {
	if !capsJSON.Valid {
		return FallbackCapabilitiesForRole(role)
	}
	caps := ParseCapabilities(capsJSON.String)
	if caps == nil {
		return map[string]struct{}{CapabilityChat: {}}
	}
	return caps
}

// loadCapabilitiesForMember reads the capabilities + role from the
// workspace_members row, then runs resolveCapabilitiesFromRow on the
// result. Returns:
//
//   - (caps, role, nil)   — row exists; caps populated per the rules
//     in resolveCapabilitiesFromRow.
//   - (nil, "", nil)      — no membership row (caller treats as
//     "not a member").
//   - (nil, "", err)      — real DB error (caller surfaces 500 instead
//     of conflating with not-a-member).
func loadCapabilitiesForMember(ctx context.Context, db *sql.DB, workspaceID, userID string) (map[string]struct{}, string, error) {
	if db == nil || workspaceID == "" || userID == "" {
		return nil, "", nil
	}
	var capsJSON sql.NullString
	var role string
	err := db.QueryRowContext(ctx, `
		SELECT capabilities, role
		FROM workspace_members
		WHERE workspace_id = ? AND user_id = ?
	`, workspaceID, userID).Scan(&capsJSON, &role)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, "", nil
	}
	if err != nil {
		// Real DB error — surface to caller so it can 500. Collapsing
		// this into "not a member" used to mask transient SQLITE_BUSY
		// behind a 403 that an operator couldn't distinguish from a
		// genuine permission deny.
		return nil, "", err
	}
	return resolveCapabilitiesFromRow(capsJSON, role), role, nil
}

// CapabilitiesForMember is the cached lookup that handlers call
// before requireCapabilityOrForbid (or to inspect the set for UI
// filtering, e.g. the /slash-commands handler). Returns the
// capability set + the underlying role.
//
// Returns (nil, "", false) when no membership row exists OR when the
// DB lookup failed. The bool collapses both into "false" for
// backwards compatibility; new callers wanting to distinguish should
// use CapabilitiesForMemberE.
func CapabilitiesForMember(ctx context.Context, db *sql.DB, workspaceID, userID string) (map[string]struct{}, string, bool) {
	caps, role, _, ok := CapabilitiesForMemberE(ctx, db, workspaceID, userID)
	return caps, role, ok
}

// CapabilitiesForMemberE is the same lookup as CapabilitiesForMember
// but also returns the underlying error so callers that need to
// distinguish "not a member" from "DB failed" can do so. The bool
// return stays "true on real hit" for symmetry with the existing
// helper — false means either no row OR error; the error tells the
// caller which.
//
// Cache is only populated on a successful load (no row → no cache;
// error → no cache), so repeated calls on a transient SQLITE_BUSY
// won't pin a wrong answer for the TTL window.
func CapabilitiesForMemberE(ctx context.Context, db *sql.DB, workspaceID, userID string) (map[string]struct{}, string, error, bool) {
	if e, ok := defaultCapabilityCache.get(workspaceID, userID); ok {
		return e.caps, e.role, nil, true
	}
	caps, role, err := loadCapabilitiesForMember(ctx, db, workspaceID, userID)
	if err != nil {
		return nil, "", err, false
	}
	if role == "" {
		// No membership row.
		return nil, "", nil, false
	}
	defaultCapabilityCache.put(workspaceID, userID, caps, role)
	return caps, role, nil, true
}

// auditRoleNonMember is the role literal recorded in audit logs when
// the deny reason is "caller has no membership row in the workspace".
// Distinct from empty so an operator scanning the log can tell the
// non-member case apart from a real role check (and from a future
// audit-emit bug that drops the role field).
const auditRoleNonMember = "non-member"

// requireRoleOrCapabilityOrForbid is the OR-combined gate that
// public end-user endpoints (POST /credentials, POST /pipeline-
// schedules, POST /skills/generate, POST /crews/{id}/issues) use
// to grant access to BOTH the legacy MANAGER+ path AND the new
// per-capability path. Without this layer the public endpoints
// stayed MANAGER-only — slash actions from the dashboard / CLI
// 403'd for any MEMBER with an explicit capability grant, defeating
// the whole point of the PR (the internal API mirrors gate
// correctly but only the agent-via-sidecar path reached them).
//
// Behaviour:
//
//   - role passes canRole(...requiredActions)  → grant. Existing
//     MANAGER+ callers keep working with zero change.
//   - role fails but capability is granted     → grant + audit as
//     a capability-gated access (role recorded so operators can
//     tell which dimension let the call through).
//   - both miss                                → 403 with audit.
//
// The capability check fires only when role check would have
// denied — that keeps the audit log signal clean (MANAGER+
// successes don't spam capability audits) and avoids a redundant
// DB lookup on the hot path.
//
// callerUserID empty (autonomous agent) defers to the existing
// requireRoleOrForbid behaviour — autonomous calls don't have a
// capability set, the autonomy_level gate (handled separately by
// the agent-facing routes) is the authoritative path there.
func requireRoleOrCapabilityOrForbid(
	w http.ResponseWriter,
	r *http.Request,
	logger interface {
		Warn(msg string, args ...any)
	},
	db *sql.DB,
	workspaceID, callerUserID, role, capability, action, resource string,
	requiredRoleActions ...string,
) bool {
	// Role check first — preserves existing behaviour exactly for
	// MANAGER+ callers and skips the DB lookup on their hot path.
	if canRole(role, requiredRoleActions...) {
		return true
	}
	// Role check failed. If we have a caller id, try the capability
	// path. Autonomous calls (no caller id) fall straight through
	// to the existing forbid emit.
	if callerUserID != "" {
		caps, _, err, ok := CapabilitiesForMemberE(r.Context(), db, workspaceID, callerUserID)
		if err != nil {
			if logger != nil {
				logger.Warn("rbac: capability lookup failed",
					"user_id", callerUserID,
					"workspace_id", workspaceID,
					"action", action,
					"resource", resource,
					"error", err.Error(),
				)
			}
			replyError(w, http.StatusInternalServerError, "Internal server error")
			return false
		}
		if ok && HasCapability(caps, capability) {
			return true
		}
	}
	replyForbidden(w, logger, callerUserID, role, action, resource)
	return false
}

// requireCapabilityOrForbid is the capability-gate variant of
// requireRoleOrForbid. Returns true on grant so the caller can
// early-return on false:
//
//	if !requireCapabilityOrForbid(w, r, h.logger, h.db, wsID, userID,
//	    CapabilityRoutineCreate, "routine.create", "routine:new") {
//	    return
//	}
//
// Behaviour matrix:
//
//   - userID == "": treated as autonomous-agent call. Returns true
//     unconditionally; the autonomy_level check in the handler is
//     the authoritative gate for that path. This branch is what
//     keeps the sidecar-vouched / X-Caller-User-Id-absent path
//     working — never wrong-deny a legit autonomous agent.
//   - DB error during lookup: 500 + log. NOT 403 — transient
//     SQLITE_BUSY masquerading as "Forbidden" is the bug that
//     drove the E-variant lookup.
//   - membership row missing: 403 + audit with role="non-member"
//     (distinct literal so the log line isn't ambiguous about
//     whether the role field was simply unset).
//   - capability not granted: 403 + audit, role recorded.
//   - capability granted: true (cached for next 30 s).
//
// Audit emit matches replyForbidden — same (subject_user_id, role,
// action, resource) quartet so capability denies and role denies
// stream into the same log query.
func requireCapabilityOrForbid(
	w http.ResponseWriter,
	r *http.Request,
	logger interface {
		Warn(msg string, args ...any)
	},
	db *sql.DB,
	workspaceID, callerUserID, capability, action, resource string,
) bool {
	// Autonomous agent path: the handler caller decided this isn't
	// user-attributed. We grant and rely on the autonomy_level gate
	// the handler runs separately.
	if callerUserID == "" {
		return true
	}
	caps, role, err, ok := CapabilitiesForMemberE(r.Context(), db, workspaceID, callerUserID)
	if err != nil {
		// Surface the failure so the caller can see it's
		// infrastructure, not a permission deny. We don't expose
		// the SQL error to the wire — operators read /var/log.
		if logger != nil {
			logger.Warn("rbac: capability lookup failed",
				"user_id", callerUserID,
				"workspace_id", workspaceID,
				"action", action,
				"resource", resource,
				"error", err.Error(),
			)
		}
		replyError(w, http.StatusInternalServerError, "Internal server error")
		return false
	}
	if !ok {
		// No membership row — caller has no business in this
		// workspace at all. Audit with the non-member role literal
		// so the log line is unambiguous.
		replyForbidden(w, logger, callerUserID, auditRoleNonMember, action, resource)
		return false
	}
	if HasCapability(caps, capability) {
		return true
	}
	// Capability missing — audit with the real role so the operator
	// reviewing logs can decide between "grant capability" and
	// "promote role".
	replyForbidden(w, logger, callerUserID, role, action, resource)
	return false
}
