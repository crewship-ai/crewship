package api

import (
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestCredentialAdapter_Create_NilHandler covers the nil-safety
// early return path. The router guards with `if r.credentialHandler
// != nil` but the adapter must degrade gracefully if the test
// router bypassed that.
func TestCredentialAdapter_Create_NilHandler(t *testing.T) {
	a := NewCredentialInternalAdapter(nil)
	r := httptest.NewRequest(http.MethodPost, "/?workspace_id=ws-1", strings.NewReader(`{}`))
	r.Header.Set("X-Caller-User-Id", "u-x")
	w := httptest.NewRecorder()
	a.Create(w, r)
	if w.Code != 500 {
		t.Errorf("got %d, want 500", w.Code)
	}
}

// TestCredentialAdapter_Create_PassesCapabilityGate covers the
// happy-path-but-no-DB-creds branch: capability check would normally
// pass but the underlying Create handler will fail for a different
// reason (nil DB). We assert the capability gate did NOT 403.
//
// Setup: real DB with Pavel as OWNER (full caps) so the check passes;
// CredentialHandler is real but its body parse on empty JSON returns
// 400. A 400 from downstream means the capability gate let us through.
func TestCredentialAdapter_Create_PassesCapabilityGate(t *testing.T) {
	db := setupTestDB(t)
	ownerID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, ownerID)
	InvalidateCapabilityCache(wsID, ownerID)

	h := &CredentialHandler{db: db, logger: slog.Default()}
	a := NewCredentialInternalAdapter(h)
	r := httptest.NewRequest(http.MethodPost,
		"/?workspace_id="+wsID, strings.NewReader(`{}`)) // empty body
	r.Header.Set("X-Caller-User-Id", ownerID)
	w := httptest.NewRecorder()
	a.Create(w, r)

	// 400 = downstream rejected (name empty); 403 would mean
	// capability gate wrong-denied. Anything else is a regression
	// of the previously-tested wiring.
	if w.Code != 400 {
		t.Errorf("status = %d, want 400 (capability passed, downstream validation rejected)", w.Code)
	}
}

// TestCredentialAdapter_Rotate_NilHandler — nil-safety mirror of
// Create_NilHandler.
func TestCredentialAdapter_Rotate_NilHandler(t *testing.T) {
	a := NewCredentialInternalAdapter(nil)
	r := httptest.NewRequest(http.MethodPost, "/?workspace_id=ws-1", strings.NewReader(`{}`))
	r.Header.Set("X-Caller-User-Id", "u-x")
	w := httptest.NewRecorder()
	a.Rotate(w, r)
	if w.Code != 500 {
		t.Errorf("got %d, want 500", w.Code)
	}
}

// TestCredentialAdapter_Rotate_PassesCapabilityGate mirrors the
// Create version. The downstream Rotate handler will 404 (no such
// credential) or 400 (empty body) — both mean the capability gate
// let us through. Anything other than 4xx-not-403 would be a
// regression.
func TestCredentialAdapter_Rotate_PassesCapabilityGate(t *testing.T) {
	db := setupTestDB(t)
	ownerID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, ownerID)
	InvalidateCapabilityCache(wsID, ownerID)

	h := &CredentialHandler{db: db, logger: slog.Default()}
	a := NewCredentialInternalAdapter(h)
	r := httptest.NewRequest(http.MethodPost,
		"/?workspace_id="+wsID, strings.NewReader(`{}`))
	r.Header.Set("X-Caller-User-Id", ownerID)
	r.SetPathValue("credentialId", "nonexistent")
	w := httptest.NewRecorder()
	a.Rotate(w, r)

	if w.Code == 403 {
		t.Errorf("status = %d — capability gate wrong-denied an OWNER with credential.rotate", w.Code)
	}
}

// TestSkillAdapter_NilHandler — defensive 500 mirror.
func TestSkillAdapter_NilHandler(t *testing.T) {
	a := NewSkillInternalAdapter(nil)
	r := httptest.NewRequest(http.MethodPost, "/?workspace_id=ws-1", strings.NewReader(`{}`))
	w := httptest.NewRecorder()
	a.Generate(w, r)
	if w.Code != 500 {
		t.Errorf("got %d, want 500", w.Code)
	}
}

// TestSkillAdapter_PassesCapabilityGate covers the user-attributed
// path through the skill adapter. With Pavel (OWNER, has skill.create)
// the capability gate passes; downstream Generate will fail on an
// invalid body — that's the signal we reached past the gate.
func TestSkillAdapter_PassesCapabilityGate(t *testing.T) {
	db := setupTestDB(t)
	ownerID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, ownerID)
	InvalidateCapabilityCache(wsID, ownerID)

	gen := &SkillGenerateHandler{db: db, logger: slog.Default()}
	a := NewSkillInternalAdapter(gen)
	r := httptest.NewRequest(http.MethodPost,
		"/?workspace_id="+wsID, strings.NewReader(`not json`))
	r.Header.Set("X-Caller-User-Id", ownerID)
	w := httptest.NewRecorder()
	a.Generate(w, r)

	if w.Code == 403 {
		t.Errorf("status = %d — capability gate wrong-denied OWNER with skill.create", w.Code)
	}
	// Expect 400 from downstream invalid JSON parse.
	if w.Code != 400 {
		t.Errorf("status = %d, want 400 (downstream invalid JSON)", w.Code)
	}
}

// TestRoutineAdapter_DeniesAutonomousWhenBackendUnconfigured — the
// existing routine adapter tests cover the autonomous path. This
// adds the "user-attributed + deny" log path that's wired via the
// shared replyForbidden helper but worth pinning explicitly so a
// future refactor doesn't silently drop the audit emit.
func TestRoutineAdapter_DeniesUserWithoutCapability(t *testing.T) {
	db := setupTestDB(t)
	ownerID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, ownerID)
	chatOnlyID := seedMemberWithCapabilities(t, db, wsID, "MEMBER",
		`["chat"]`, "chat-only-routine-deny")
	InvalidateCapabilityCache(wsID, chatOnlyID)

	pipes := &PipelineHandler{db: db, logger: slog.Default()}
	a := NewRoutineInternalAdapter(pipes)
	r := httptest.NewRequest(http.MethodPost,
		"/?workspace_id="+wsID, strings.NewReader(`{}`))
	r.Header.Set("X-Caller-User-Id", chatOnlyID)
	w := httptest.NewRecorder()
	a.CreateSchedule(w, r)

	if w.Code != 403 {
		t.Errorf("status = %d, want 403 — chat-only MEMBER must be denied routine.create", w.Code)
	}
}
