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

// loadCapabilitiesForMember reads the capabilities + role from the
// workspace_members row, falling back to role-derived defaults when
// the column is NULL (legacy row that hasn't been backfilled yet
// because the migration ran but the application-layer write that
// would fill the column hasn't happened).
//
// Returns (nil, "", false) if no membership row exists — the caller
// treats that as "not a member" and denies. Distinct from the
// "member with no granted capabilities" case (which returns
// chat-only via the defensive HasCapability path).
func loadCapabilitiesForMember(ctx context.Context, db *sql.DB, workspaceID, userID string) (map[string]struct{}, string, bool) {
	if db == nil || workspaceID == "" || userID == "" {
		return nil, "", false
	}
	var capsJSON sql.NullString
	var role string
	err := db.QueryRowContext(ctx, `
		SELECT capabilities, role
		FROM workspace_members
		WHERE workspace_id = ? AND user_id = ?
	`, workspaceID, userID).Scan(&capsJSON, &role)
	if err != nil {
		return nil, "", false
	}
	if capsJSON.Valid {
		caps := ParseCapabilities(capsJSON.String)
		if caps != nil {
			return caps, role, true
		}
	}
	// NULL or unparseable: degrade to role-derived defaults so the
	// upgrade-in-progress window doesn't lock anyone out.
	return FallbackCapabilitiesForRole(role), role, true
}

// CapabilitiesForMember is the cached lookup that handlers call
// before requireCapabilityOrForbid (or to inspect the set for UI
// filtering, e.g. the /slash-commands handler). Returns the
// capability set + the underlying role. (role is returned alongside
// caps so a single DB round-trip serves both the capability gate
// and any layered role gate the same handler runs.)
//
// Returns (nil, "", false) when no membership row exists.
func CapabilitiesForMember(ctx context.Context, db *sql.DB, workspaceID, userID string) (map[string]struct{}, string, bool) {
	if e, ok := defaultCapabilityCache.get(workspaceID, userID); ok {
		return e.caps, e.role, true
	}
	caps, role, ok := loadCapabilitiesForMember(ctx, db, workspaceID, userID)
	if !ok {
		return nil, "", false
	}
	defaultCapabilityCache.put(workspaceID, userID, caps, role)
	return caps, role, true
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
//   - membership row missing: deny + audit (caller pretended to be
//     a member of a workspace they aren't in).
//   - capability not granted: deny + audit, action recorded for the
//     SIEM trail.
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
	caps, role, ok := CapabilitiesForMember(r.Context(), db, workspaceID, callerUserID)
	if !ok {
		// No membership row — caller has no business in this
		// workspace at all. Audit as a role-level deny rather than
		// a capability deny because the absence of membership is
		// the underlying reason; capability would be misleading.
		replyForbidden(w, logger, callerUserID, "", action, resource)
		return false
	}
	if HasCapability(caps, capability) {
		return true
	}
	// Capability missing — audit with the role so the operator
	// reviewing logs can decide between "grant capability" and
	// "promote role".
	replyForbidden(w, logger, callerUserID, role, action, resource)
	return false
}
