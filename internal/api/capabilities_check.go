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
// workspace_members row. Returns:
//
//   - (caps, role, nil)   — row exists; caps populated per the rules
//     below.
//   - (nil, "", nil)      — no membership row (caller treats as
//     "not a member").
//   - (nil, "", err)      — real DB error (caller surfaces 500 instead
//     of conflating with not-a-member).
//
// Capability resolution rules for an existing row:
//
//   - capabilities IS NULL  → role-derived fallback bundle. This is
//     the legacy / upgrade-in-progress path: the migration ran but no
//     application write has filled the column, so we'd rather grant
//     role-equivalent defaults than lock the user out.
//
//   - capabilities IS valid JSON with at least one known entry →
//     return that set verbatim. Operator intent honored.
//
//   - capabilities IS present but ParseCapabilities returns nil (the
//     value parsed to an empty array, was malformed JSON, or only
//     contained unknown future-version strings) → return the
//     chat-only baseline, NOT the role fallback. An explicit-but-
//     drained value reflects deliberate operator intent ("strip this
//     user back to chat") and the runtime must not silently restore
//     role-derived privileges. (CodeRabbit CR-1.)
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
		// genuine permission deny. (CodeRabbit CR-2.)
		return nil, "", err
	}
	if !capsJSON.Valid {
		// NULL: legacy / upgrade-in-progress. Role fallback is the
		// safe degrade so a fresh INSERT that didn't fill the column
		// doesn't lock the user out before the backfill catches up.
		return FallbackCapabilitiesForRole(role), role, nil
	}
	caps := ParseCapabilities(capsJSON.String)
	if caps == nil {
		// Explicit-empty / malformed / all-unknown-strings. Operator
		// intent: chat-only. NOT role fallback — that would silently
		// restore privileges the operator just stripped.
		return map[string]struct{}{CapabilityChat: {}}, role, nil
	}
	return caps, role, nil
}

// CapabilitiesForMember is the cached lookup that handlers call
// before requireCapabilityOrForbid (or to inspect the set for UI
// filtering, e.g. the /slash-commands handler). Returns the
// capability set + the underlying role. (role is returned alongside
// caps so a single DB round-trip serves both the capability gate
// and any layered role gate the same handler runs.)
//
// Returns (nil, "", false) when no membership row exists. DB errors
// are NOT swallowed — they propagate via the loader's error return
// and surface as (nil, "", false) too BUT the caller can re-query
// via CapabilitiesForMemberE to get the underlying error and surface
// a 500 instead of conflating with not-a-member. Existing callers
// keep working unchanged.
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
