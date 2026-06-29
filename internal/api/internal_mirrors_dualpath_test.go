package api

import (
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestRoutineAdapter_DualPath_UserWithoutCapabilityDenied is the
// central dual-path test: the slash-action path (X-Caller-User-Id
// present) hits the capability gate. A MEMBER without
// routine.create grant gets 403 — even though the adapter would
// otherwise inject MANAGER role and clear canRole("create").
func TestRoutineAdapter_DualPath_UserWithoutCapabilityDenied(t *testing.T) {
	db := setupTestDB(t)
	ownerID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, ownerID)
	ludmilaID := seedMemberWithCapabilities(t, db, wsID, "MEMBER",
		`["chat"]`, "ludmila-routine-deny")
	InvalidateCapabilityCache(wsID, ludmilaID)

	// Construct a PipelineHandler with just db + logger — the
	// adapter never reaches the schedules backend because the
	// capability check short-circuits.
	pipes := &PipelineHandler{db: db, logger: slog.Default()}
	adapter := NewRoutineInternalAdapter(pipes)

	r := httptest.NewRequest(http.MethodPost,
		"/?workspace_id="+wsID, strings.NewReader(`{}`))
	r.Header.Set("X-Caller-User-Id", ludmilaID)
	w := httptest.NewRecorder()
	adapter.CreateSchedule(w, r)

	if w.Code != http.StatusForbidden {
		t.Errorf("status = %d, want 403 — MEMBER without routine.create must be denied", w.Code)
	}
}

// TestRoutineAdapter_DualPath_AutonomousAgentFallsThrough: with no
// X-Caller-User-Id header (autonomous-agent path), the capability
// gate is skipped and we fall through to the underlying handler.
// The underlying CreateSchedule will fail for a different reason
// (no schedules backend wired in this test setup), but the failure
// mode tells us the capability gate let us through — that's the
// "fall-through" assertion.
func TestRoutineAdapter_DualPath_AutonomousAgentFallsThrough(t *testing.T) {
	db := setupTestDB(t)
	ownerID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, ownerID)

	pipes := &PipelineHandler{db: db, logger: slog.Default()}
	adapter := NewRoutineInternalAdapter(pipes)

	r := httptest.NewRequest(http.MethodPost,
		"/?workspace_id="+wsID, strings.NewReader(`{}`))
	// No X-Caller-User-Id — autonomous-agent path.
	w := httptest.NewRecorder()
	adapter.CreateSchedule(w, r)

	// CreateSchedule's first action with a nil schedules backend is
	// to 503 (pipeline_schedules.go:83-85). If the capability gate
	// had wrong-denied we'd see 403 instead. 503 = fell through.
	if w.Code != http.StatusServiceUnavailable {
		t.Errorf("status = %d, want 503 (fall-through to nil backend); a 403 would mean the capability gate wrong-denied autonomous", w.Code)
	}
}

// TestRoutineAdapter_DualPath_UserWithCapabilityFallsThrough: the
// happy path. MEMBER with grant clears the capability gate; the
// underlying handler runs and 503s (no backend) — that's the
// "passed the gate" signal.
func TestRoutineAdapter_DualPath_UserWithCapabilityFallsThrough(t *testing.T) {
	db := setupTestDB(t)
	ownerID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, ownerID)
	ludmilaID := seedMemberWithCapabilities(t, db, wsID, "MEMBER",
		`["chat","routine.create"]`, "ludmila-routine-allow")
	InvalidateCapabilityCache(wsID, ludmilaID)

	pipes := &PipelineHandler{db: db, logger: slog.Default()}
	adapter := NewRoutineInternalAdapter(pipes)

	r := httptest.NewRequest(http.MethodPost,
		"/?workspace_id="+wsID, strings.NewReader(`{}`))
	r.Header.Set("X-Caller-User-Id", ludmilaID)
	w := httptest.NewRecorder()
	adapter.CreateSchedule(w, r)

	if w.Code != http.StatusServiceUnavailable {
		t.Errorf("status = %d, want 503 (granted user fell through to nil backend)", w.Code)
	}
}

// TestSkillAdapter_DualPath_DenyOnMissingSkillCapability: parallel
// to the routine deny test — skill.create is the gate.
func TestSkillAdapter_DualPath_DenyOnMissingSkillCapability(t *testing.T) {
	db := setupTestDB(t)
	ownerID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, ownerID)
	// Note: grant routine.create but NOT skill.create — the
	// capability is action-specific, not "any high-value action".
	ludmilaID := seedMemberWithCapabilities(t, db, wsID, "MEMBER",
		`["chat","routine.create"]`, "ludmila-skill-deny")
	InvalidateCapabilityCache(wsID, ludmilaID)

	gen := &SkillGenerateHandler{db: db, logger: slog.Default()}
	adapter := NewSkillInternalAdapter(gen)

	r := httptest.NewRequest(http.MethodPost,
		"/?workspace_id="+wsID, strings.NewReader(`{}`))
	r.Header.Set("X-Caller-User-Id", ludmilaID)
	w := httptest.NewRecorder()
	adapter.Generate(w, r)

	if w.Code != http.StatusForbidden {
		t.Errorf("status = %d, want 403 — routine.create does NOT imply skill.create", w.Code)
	}
}

// TestCredentialAdapter_DualPath_DenyOnMissingCredentialCapability:
// credential.create is the gate. credential.rotate is a separate
// capability (different test).
func TestCredentialAdapter_DualPath_DenyOnMissingCredentialCapability(t *testing.T) {
	db := setupTestDB(t)
	ownerID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, ownerID)
	ludmilaID := seedMemberWithCapabilities(t, db, wsID, "MEMBER",
		`["chat"]`, "ludmila-cred-deny")
	InvalidateCapabilityCache(wsID, ludmilaID)

	creds := &CredentialHandler{db: db, logger: slog.Default()}
	adapter := NewCredentialInternalAdapter(creds)

	r := httptest.NewRequest(http.MethodPost,
		"/?workspace_id="+wsID, strings.NewReader(`{}`))
	// ID1: sign the caller id so the request clears the identity gate
	// and the capability gate is what actually decides (403 here).
	stampSignedCaller(r, forgedTestMaster, wsID, ludmilaID)
	w := httptest.NewRecorder()
	adapter.Create(w, r)

	if w.Code != http.StatusForbidden {
		t.Errorf("status = %d, want 403 — chat-only MEMBER must not create credentials", w.Code)
	}
}

// TestCredentialAdapter_DualPath_RotateRequiresRotateNotCreate
// asserts the rotate/create split: a user with credential.create
// but NOT credential.rotate cannot rotate. The adapter must check
// the right capability for the right action.
func TestCredentialAdapter_DualPath_RotateRequiresRotateNotCreate(t *testing.T) {
	db := setupTestDB(t)
	ownerID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, ownerID)
	ludmilaID := seedMemberWithCapabilities(t, db, wsID, "MEMBER",
		`["chat","credential.create"]`, "ludmila-cred-rotate-deny")
	InvalidateCapabilityCache(wsID, ludmilaID)

	creds := &CredentialHandler{db: db, logger: slog.Default()}
	adapter := NewCredentialInternalAdapter(creds)

	r := httptest.NewRequest(http.MethodPost,
		"/?workspace_id="+wsID, strings.NewReader(`{}`))
	stampSignedCaller(r, forgedTestMaster, wsID, ludmilaID)
	w := httptest.NewRecorder()
	adapter.Rotate(w, r)

	if w.Code != http.StatusForbidden {
		t.Errorf("status = %d, want 403 — credential.create does NOT imply credential.rotate", w.Code)
	}
}
