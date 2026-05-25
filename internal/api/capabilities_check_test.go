package api

import (
	"context"
	"database/sql"
	"io"
	"log/slog"
	"net/http/httptest"
	"testing"
	"time"
)

// silentLogger is the *slog.Logger every capability_check test uses
// — we don't care about Warn output, but the helper signature wants
// something with a Warn method.
func silentLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// seedMemberWithCapabilities inserts a workspace_members row with an
// explicit capabilities JSON. Returns the user_id so callers can
// reference it. Uses a distinct user/membership id per caller so
// tests in the same DB don't collide.
func seedMemberWithCapabilities(t *testing.T, db *sql.DB, wsID, role, capsJSON, suffix string) string {
	t.Helper()
	userID := "test-user-" + suffix
	mID := "test-mem-" + suffix
	if _, err := db.Exec(`INSERT INTO users (id, email, full_name) VALUES (?, ?, 'Test')`,
		userID, userID+"@x"); err != nil {
		t.Fatalf("insert user: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO workspace_members (id, workspace_id, user_id, role, capabilities) VALUES (?, ?, ?, ?, ?)`,
		mID, wsID, userID, role, capsJSON); err != nil {
		t.Fatalf("insert member: %v", err)
	}
	return userID
}

// TestRequireCapability_AutonomousAgentBypasses asserts the empty-
// callerUserID path returns true unconditionally — autonomous agent
// calls hit the autonomy_level gate the handler runs separately, so
// the capability layer must not wrong-deny them.
func TestRequireCapability_AutonomousAgentBypasses(t *testing.T) {
	db := setupTestDB(t)
	w := httptest.NewRecorder()
	r := httptest.NewRequest("POST", "/x", nil)
	got := requireCapabilityOrForbid(w, r, silentLogger(), db,
		"any-ws", "" /* callerUserID empty = autonomous */,
		CapabilityRoutineCreate, "routine.create", "routine:new")
	if !got {
		t.Fatal("autonomous agent path must return true")
	}
	if w.Code != 200 {
		t.Errorf("status = %d, want 200 (no write)", w.Code)
	}
}

// TestRequireCapability_GrantedCapability covers the happy path —
// MEMBER with explicit routine.create grant passes the gate.
func TestRequireCapability_GrantedCapability(t *testing.T) {
	defaultCapabilityCache.Invalidate("", "") // start clean
	db := setupTestDB(t)
	ownerID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, ownerID)
	defaultCapabilityCache.Invalidate(wsID, "")

	ludmilaID := seedMemberWithCapabilities(t, db, wsID, "MEMBER",
		`["chat","routine.create"]`, "ludmila")
	defaultCapabilityCache.Invalidate(wsID, ludmilaID)

	w := httptest.NewRecorder()
	r := httptest.NewRequest("POST", "/x", nil)
	got := requireCapabilityOrForbid(w, r, silentLogger(), db,
		wsID, ludmilaID, CapabilityRoutineCreate, "routine.create", "routine:new")
	if !got {
		t.Fatalf("MEMBER with grant must pass; status=%d", w.Code)
	}
}

// TestRequireCapability_MissingCapability covers the deny path — a
// MEMBER without the explicit grant gets 403.
func TestRequireCapability_MissingCapability(t *testing.T) {
	db := setupTestDB(t)
	ownerID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, ownerID)

	ludmilaID := seedMemberWithCapabilities(t, db, wsID, "MEMBER",
		`["chat"]`, "ludmila-noroutine")
	defaultCapabilityCache.Invalidate(wsID, ludmilaID)

	w := httptest.NewRecorder()
	r := httptest.NewRequest("POST", "/x", nil)
	got := requireCapabilityOrForbid(w, r, silentLogger(), db,
		wsID, ludmilaID, CapabilityRoutineCreate, "routine.create", "routine:new")
	if got {
		t.Fatal("MEMBER without grant must be denied")
	}
	if w.Code != 403 {
		t.Errorf("status = %d, want 403", w.Code)
	}
}

// TestRequireCapability_NoMembershipRow covers the "caller pretends
// to be member" case — auth middleware put a user on the request but
// they have no workspace_members row for the targeted workspace.
// Must deny.
func TestRequireCapability_NoMembershipRow(t *testing.T) {
	db := setupTestDB(t)
	ownerID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, ownerID)

	// Create a second user but don't add them as a member.
	if _, err := db.Exec(`INSERT INTO users (id, email, full_name) VALUES ('outsider','o@x','Out')`); err != nil {
		t.Fatalf("insert outsider: %v", err)
	}
	defaultCapabilityCache.Invalidate(wsID, "outsider")

	w := httptest.NewRecorder()
	r := httptest.NewRequest("POST", "/x", nil)
	got := requireCapabilityOrForbid(w, r, silentLogger(), db,
		wsID, "outsider", CapabilityChat, "any.thing", "any:res")
	if got {
		t.Fatal("non-member must be denied even for chat capability")
	}
	if w.Code != 403 {
		t.Errorf("status = %d, want 403", w.Code)
	}
}

// TestRequireCapability_NullCapabilitiesFallsBackToRoleBundle covers
// the upgrade-in-progress window where a row exists with NULL
// capabilities (migration ran but no application write filled it).
// Must use FallbackCapabilitiesForRole semantics.
func TestRequireCapability_NullCapabilitiesFallsBackToRoleBundle(t *testing.T) {
	db := setupTestDB(t)
	ownerID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, ownerID)

	// Insert OWNER-role member with NULL capabilities — the
	// fallback bundle should grant the full surface.
	if _, err := db.Exec(`INSERT INTO users (id, email, full_name) VALUES ('owner2','o2@x','O2')`); err != nil {
		t.Fatalf("seed: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO workspace_members (id, workspace_id, user_id, role, capabilities) VALUES ('mNull', ?, 'owner2', 'OWNER', NULL)`, wsID); err != nil {
		t.Fatalf("seed null caps: %v", err)
	}
	defaultCapabilityCache.Invalidate(wsID, "owner2")

	w := httptest.NewRecorder()
	r := httptest.NewRequest("POST", "/x", nil)
	got := requireCapabilityOrForbid(w, r, silentLogger(), db,
		wsID, "owner2", CapabilityCredentialCreate, "credential.create", "cred:new")
	if !got {
		t.Fatalf("OWNER with NULL caps must fall back to admin bundle (which includes credential.create); status=%d", w.Code)
	}
}

// TestRequireCapability_CacheHit asserts the second call within the
// TTL doesn't touch the database. We prove it by mutating the row
// after the first call — the second call must still see the cached
// grant rather than the new deny.
func TestRequireCapability_CacheHit(t *testing.T) {
	db := setupTestDB(t)
	ownerID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, ownerID)

	ludmilaID := seedMemberWithCapabilities(t, db, wsID, "MEMBER",
		`["chat","routine.create"]`, "ludmila-cachehit")
	defaultCapabilityCache.Invalidate(wsID, ludmilaID)

	// First call populates cache and must grant.
	w1 := httptest.NewRecorder()
	r1 := httptest.NewRequest("POST", "/x", nil)
	if !requireCapabilityOrForbid(w1, r1, silentLogger(), db,
		wsID, ludmilaID, CapabilityRoutineCreate, "routine.create", "x") {
		t.Fatalf("first call expected grant; status=%d", w1.Code)
	}

	// Revoke at the row level — cache should still hold the grant
	// for the next 30 s.
	if _, err := db.Exec(`UPDATE workspace_members SET capabilities = '["chat"]' WHERE user_id = ?`, ludmilaID); err != nil {
		t.Fatalf("revoke: %v", err)
	}

	w2 := httptest.NewRecorder()
	r2 := httptest.NewRequest("POST", "/x", nil)
	if !requireCapabilityOrForbid(w2, r2, silentLogger(), db,
		wsID, ludmilaID, CapabilityRoutineCreate, "routine.create", "x") {
		t.Fatal("second call (within TTL) must still grant from cache despite DB revoke")
	}

	// Manual invalidation models what the admin grant/revoke handler
	// will do — after that, the next call must see the deny.
	InvalidateCapabilityCache(wsID, ludmilaID)
	w3 := httptest.NewRecorder()
	r3 := httptest.NewRequest("POST", "/x", nil)
	if requireCapabilityOrForbid(w3, r3, silentLogger(), db,
		wsID, ludmilaID, CapabilityRoutineCreate, "routine.create", "x") {
		t.Fatal("post-invalidate call must see revoke")
	}
	if w3.Code != 403 {
		t.Errorf("status = %d, want 403", w3.Code)
	}
}

// TestRequireCapability_CacheTTLExpiry verifies the cached entry
// expires after the configured TTL. We override timeNow rather than
// sleeping so the test stays sub-millisecond.
func TestRequireCapability_CacheTTLExpiry(t *testing.T) {
	originalNow := timeNow
	t.Cleanup(func() { timeNow = originalNow })
	clock := time.Now()
	timeNow = func() time.Time { return clock }

	db := setupTestDB(t)
	ownerID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, ownerID)

	ludmilaID := seedMemberWithCapabilities(t, db, wsID, "MEMBER",
		`["chat","routine.create"]`, "ludmila-ttl")
	InvalidateCapabilityCache(wsID, ludmilaID)

	// Prime cache.
	w1 := httptest.NewRecorder()
	r1 := httptest.NewRequest("POST", "/x", nil)
	if !requireCapabilityOrForbid(w1, r1, silentLogger(), db,
		wsID, ludmilaID, CapabilityRoutineCreate, "routine.create", "x") {
		t.Fatalf("priming call denied; status=%d", w1.Code)
	}

	// Revoke + advance time past TTL (30 s).
	if _, err := db.Exec(`UPDATE workspace_members SET capabilities = '["chat"]' WHERE user_id = ?`, ludmilaID); err != nil {
		t.Fatalf("revoke: %v", err)
	}
	clock = clock.Add(31 * time.Second)

	w2 := httptest.NewRecorder()
	r2 := httptest.NewRequest("POST", "/x", nil)
	if requireCapabilityOrForbid(w2, r2, silentLogger(), db,
		wsID, ludmilaID, CapabilityRoutineCreate, "routine.create", "x") {
		t.Fatal("post-TTL call must re-read DB and see the revoke")
	}
}

// TestCapabilitiesForMember_ReturnsRole verifies the public lookup
// returns both the cap set and the role so a handler can use both
// off one DB round-trip — important for layered gates (role check
// then capability check) on the same request.
func TestCapabilitiesForMember_ReturnsRole(t *testing.T) {
	db := setupTestDB(t)
	ownerID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, ownerID)

	ludmilaID := seedMemberWithCapabilities(t, db, wsID, "MEMBER",
		`["chat","issue.create"]`, "ludmila-role")
	InvalidateCapabilityCache(wsID, ludmilaID)

	caps, role, ok := CapabilitiesForMember(context.Background(), db, wsID, ludmilaID)
	if !ok {
		t.Fatal("expected lookup to succeed")
	}
	if role != "MEMBER" {
		t.Errorf("role = %q, want MEMBER", role)
	}
	if !HasCapability(caps, CapabilityIssueCreate) {
		t.Error("expected issue.create in returned set")
	}
}

// TestInvalidateCapabilityCache_Wildcard verifies that empty userID
// drops every entry in the workspace — admin "reset all members"
// flow needs this so a bulk SQL update is immediately visible.
func TestInvalidateCapabilityCache_Wildcard(t *testing.T) {
	db := setupTestDB(t)
	ownerID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, ownerID)

	u1 := seedMemberWithCapabilities(t, db, wsID, "MEMBER", `["chat"]`, "wild-a")
	u2 := seedMemberWithCapabilities(t, db, wsID, "MEMBER", `["chat"]`, "wild-b")

	// Prime both.
	_, _, _ = CapabilitiesForMember(context.Background(), db, wsID, u1)
	_, _, _ = CapabilitiesForMember(context.Background(), db, wsID, u2)

	// Wildcard invalidate.
	InvalidateCapabilityCache(wsID, "")

	// Both should be gone from cache; lookup re-reads DB. We verify
	// indirectly by checking the cache map size for the workspace
	// prefix is zero.
	defaultCapabilityCache.mu.RLock()
	remaining := 0
	prefix := wsID + "\x00"
	for k := range defaultCapabilityCache.store {
		if len(k) >= len(prefix) && k[:len(prefix)] == prefix {
			remaining++
		}
	}
	defaultCapabilityCache.mu.RUnlock()
	if remaining != 0 {
		t.Errorf("wildcard invalidate left %d entries", remaining)
	}
}
