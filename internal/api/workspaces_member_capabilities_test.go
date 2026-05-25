package api

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http/httptest"
	"strings"
	"testing"
)

// newWsHandlerForTest builds a WorkspaceHandler against a freshly
// migrated test DB. Used by every test in this file — capability
// admin handlers only need db + logger.
func newWsHandlerForTest(t *testing.T) *WorkspaceHandler {
	t.Helper()
	db := setupTestDB(t)
	return &WorkspaceHandler{db: db, logger: slog.Default()}
}

// withAdminCtx stamps the four context values every capability-
// admin handler reads: workspace, user, role, plus the request
// path value the route uses for memberId.
func withAdminCtx(req interface{}, wsID, adminID, role, targetMemberID string) {
	if r, ok := req.(*requestWithSetters); ok {
		r.set(wsID, adminID, role, targetMemberID)
	}
}

type requestWithSetters struct{}

func (r *requestWithSetters) set(wsID, adminID, role, targetMemberID string) {}

// TestPatchCapabilities_SetReplacesEntireRow exercises the canonical
// shape the Members grid posts: the post-edit state of the row.
// Asserts the row is replaced exactly, with chat always implied.
func TestPatchCapabilities_SetReplacesEntireRow(t *testing.T) {
	h := newWsHandlerForTest(t)
	adminID := seedTestUser(t, h.db)
	wsID := seedTestWorkspace(t, h.db, adminID)
	ludmilaID := seedMemberWithCapabilities(t, h.db, wsID, "MEMBER",
		`["chat","routine.create"]`, "ludmila-set")
	InvalidateCapabilityCache(wsID, ludmilaID)

	req := httptest.NewRequest("PATCH", "/x", strings.NewReader(
		`{"set":["chat","issue.create","memory.write"]}`))
	req.SetPathValue("memberId", ludmilaID)
	ctx := context.WithValue(req.Context(), ctxWorkspaceID, wsID)
	ctx = context.WithValue(ctx, ctxUser, &AuthUser{ID: adminID})
	ctx = context.WithValue(ctx, ctxRole, "ADMIN")
	req = req.WithContext(ctx)
	w := httptest.NewRecorder()

	h.PatchMemberCapabilities(w, req)

	if w.Code != 200 {
		t.Fatalf("status = %d, body = %s", w.Code, w.Body.String())
	}
	var got capabilitiesGetResponse
	_ = json.NewDecoder(w.Body).Decode(&got)
	want := []string{"chat", "issue.create", "memory.write"}
	if !stringSliceEqual(got.Capabilities, want) {
		t.Errorf("got %v, want %v", got.Capabilities, want)
	}
}

// TestPatchCapabilities_GrantAdds adds without touching existing
// grants — the CLI's incremental edit shape.
func TestPatchCapabilities_GrantAdds(t *testing.T) {
	h := newWsHandlerForTest(t)
	adminID := seedTestUser(t, h.db)
	wsID := seedTestWorkspace(t, h.db, adminID)
	ludmilaID := seedMemberWithCapabilities(t, h.db, wsID, "MEMBER",
		`["chat","routine.create"]`, "ludmila-grant")
	InvalidateCapabilityCache(wsID, ludmilaID)

	req := httptest.NewRequest("PATCH", "/x", strings.NewReader(
		`{"grant":["issue.create"]}`))
	req.SetPathValue("memberId", ludmilaID)
	ctx := context.WithValue(req.Context(), ctxWorkspaceID, wsID)
	ctx = context.WithValue(ctx, ctxUser, &AuthUser{ID: adminID})
	ctx = context.WithValue(ctx, ctxRole, "ADMIN")
	req = req.WithContext(ctx)
	w := httptest.NewRecorder()

	h.PatchMemberCapabilities(w, req)

	if w.Code != 200 {
		t.Fatalf("status = %d, body = %s", w.Code, w.Body.String())
	}
	var got capabilitiesGetResponse
	_ = json.NewDecoder(w.Body).Decode(&got)
	want := []string{"chat", "issue.create", "routine.create"}
	if !stringSliceEqual(got.Capabilities, want) {
		t.Errorf("got %v, want %v", got.Capabilities, want)
	}
}

// TestPatchCapabilities_RevokeRemoves.
func TestPatchCapabilities_RevokeRemoves(t *testing.T) {
	h := newWsHandlerForTest(t)
	adminID := seedTestUser(t, h.db)
	wsID := seedTestWorkspace(t, h.db, adminID)
	ludmilaID := seedMemberWithCapabilities(t, h.db, wsID, "MEMBER",
		`["chat","routine.create","issue.create"]`, "ludmila-revoke")
	InvalidateCapabilityCache(wsID, ludmilaID)

	req := httptest.NewRequest("PATCH", "/x", strings.NewReader(
		`{"revoke":["issue.create"]}`))
	req.SetPathValue("memberId", ludmilaID)
	ctx := context.WithValue(req.Context(), ctxWorkspaceID, wsID)
	ctx = context.WithValue(ctx, ctxUser, &AuthUser{ID: adminID})
	ctx = context.WithValue(ctx, ctxRole, "ADMIN")
	req = req.WithContext(ctx)
	w := httptest.NewRecorder()

	h.PatchMemberCapabilities(w, req)

	if w.Code != 200 {
		t.Fatalf("status = %d, body = %s", w.Code, w.Body.String())
	}
	var got capabilitiesGetResponse
	_ = json.NewDecoder(w.Body).Decode(&got)
	if !stringSliceEqual(got.Capabilities, []string{"chat", "routine.create"}) {
		t.Errorf("got %v", got.Capabilities)
	}
}

// TestPatchCapabilities_RejectsRevokeOfChat: chat is always implied,
// revoking it is meaningless — reject so the admin knows their
// click did nothing rather than silently no-oping.
func TestPatchCapabilities_RejectsRevokeOfChat(t *testing.T) {
	h := newWsHandlerForTest(t)
	adminID := seedTestUser(t, h.db)
	wsID := seedTestWorkspace(t, h.db, adminID)
	ludmilaID := seedMemberWithCapabilities(t, h.db, wsID, "MEMBER",
		`["chat"]`, "ludmila-no-chat-revoke")

	req := httptest.NewRequest("PATCH", "/x", strings.NewReader(
		`{"revoke":["chat"]}`))
	req.SetPathValue("memberId", ludmilaID)
	ctx := context.WithValue(req.Context(), ctxWorkspaceID, wsID)
	ctx = context.WithValue(ctx, ctxUser, &AuthUser{ID: adminID})
	ctx = context.WithValue(ctx, ctxRole, "ADMIN")
	req = req.WithContext(ctx)
	w := httptest.NewRecorder()

	h.PatchMemberCapabilities(w, req)

	if w.Code != 400 {
		t.Errorf("status = %d, want 400 for revoke of chat", w.Code)
	}
}

// TestPatchCapabilities_PresetApplies maps to BundleCapabilities.
func TestPatchCapabilities_PresetApplies(t *testing.T) {
	h := newWsHandlerForTest(t)
	adminID := seedTestUser(t, h.db)
	wsID := seedTestWorkspace(t, h.db, adminID)
	ludmilaID := seedMemberWithCapabilities(t, h.db, wsID, "MEMBER",
		`["chat"]`, "ludmila-preset")
	InvalidateCapabilityCache(wsID, ludmilaID)

	req := httptest.NewRequest("PATCH", "/x", strings.NewReader(
		`{"preset":"power"}`))
	req.SetPathValue("memberId", ludmilaID)
	ctx := context.WithValue(req.Context(), ctxWorkspaceID, wsID)
	ctx = context.WithValue(ctx, ctxUser, &AuthUser{ID: adminID})
	ctx = context.WithValue(ctx, ctxRole, "ADMIN")
	req = req.WithContext(ctx)
	w := httptest.NewRecorder()

	h.PatchMemberCapabilities(w, req)

	if w.Code != 200 {
		t.Fatalf("status = %d, body = %s", w.Code, w.Body.String())
	}
	var got capabilitiesGetResponse
	_ = json.NewDecoder(w.Body).Decode(&got)
	want := []string{"chat", "issue.create", "memory.write", "routine.create"}
	if !stringSliceEqual(got.Capabilities, want) {
		t.Errorf("got %v, want %v", got.Capabilities, want)
	}
}

// TestPatchCapabilities_RejectsMultipleShapes guards against
// ambiguous intent — body must contain exactly one shape.
func TestPatchCapabilities_RejectsMultipleShapes(t *testing.T) {
	h := newWsHandlerForTest(t)
	adminID := seedTestUser(t, h.db)
	wsID := seedTestWorkspace(t, h.db, adminID)
	ludmilaID := seedMemberWithCapabilities(t, h.db, wsID, "MEMBER",
		`["chat"]`, "ludmila-multi-shape")

	req := httptest.NewRequest("PATCH", "/x", strings.NewReader(
		`{"grant":["issue.create"],"revoke":["routine.create"]}`))
	req.SetPathValue("memberId", ludmilaID)
	ctx := context.WithValue(req.Context(), ctxWorkspaceID, wsID)
	ctx = context.WithValue(ctx, ctxUser, &AuthUser{ID: adminID})
	ctx = context.WithValue(ctx, ctxRole, "ADMIN")
	req = req.WithContext(ctx)
	w := httptest.NewRecorder()

	h.PatchMemberCapabilities(w, req)

	if w.Code != 400 {
		t.Errorf("status = %d, want 400 for multiple shapes", w.Code)
	}
}

// TestPatchCapabilities_RejectsOwnerTarget: OWNER capabilities are
// immutable — even another OWNER can't mutate them.
func TestPatchCapabilities_RejectsOwnerTarget(t *testing.T) {
	h := newWsHandlerForTest(t)
	adminID := seedTestUser(t, h.db)
	wsID := seedTestWorkspace(t, h.db, adminID)
	// Second OWNER row.
	if _, err := h.db.Exec(`INSERT INTO users (id, email, full_name) VALUES ('owner2','o2@x','O2')`); err != nil {
		t.Fatalf("seed: %v", err)
	}
	if _, err := h.db.Exec(`INSERT INTO workspace_members (id, workspace_id, user_id, role, capabilities) VALUES ('mOwner2', ?, 'owner2', 'OWNER', '["chat"]')`, wsID); err != nil {
		t.Fatalf("seed: %v", err)
	}

	req := httptest.NewRequest("PATCH", "/x", strings.NewReader(
		`{"set":["chat"]}`))
	req.SetPathValue("memberId", "owner2")
	ctx := context.WithValue(req.Context(), ctxWorkspaceID, wsID)
	ctx = context.WithValue(ctx, ctxUser, &AuthUser{ID: adminID})
	ctx = context.WithValue(ctx, ctxRole, "ADMIN")
	req = req.WithContext(ctx)
	w := httptest.NewRecorder()

	h.PatchMemberCapabilities(w, req)

	if w.Code != 403 {
		t.Errorf("status = %d, want 403 for OWNER target", w.Code)
	}
}

// TestPatchCapabilities_RejectsSelfMutation: admin can't mutate own
// row — defence against the downgrade-then-restore stunt.
func TestPatchCapabilities_RejectsSelfMutation(t *testing.T) {
	h := newWsHandlerForTest(t)
	adminID := seedTestUser(t, h.db)
	wsID := seedTestWorkspace(t, h.db, adminID)

	req := httptest.NewRequest("PATCH", "/x", strings.NewReader(
		`{"set":["chat"]}`))
	req.SetPathValue("memberId", adminID)
	ctx := context.WithValue(req.Context(), ctxWorkspaceID, wsID)
	ctx = context.WithValue(ctx, ctxUser, &AuthUser{ID: adminID})
	ctx = context.WithValue(ctx, ctxRole, "ADMIN")
	req = req.WithContext(ctx)
	w := httptest.NewRecorder()

	h.PatchMemberCapabilities(w, req)

	if w.Code != 403 {
		t.Errorf("status = %d, want 403 for self-mutation", w.Code)
	}
}

// TestPatchCapabilities_RejectsNonAdmin: MANAGER (let alone MEMBER)
// can't grant capabilities to anyone. Role check is the first gate
// at the handler.
func TestPatchCapabilities_RejectsNonAdmin(t *testing.T) {
	h := newWsHandlerForTest(t)
	adminID := seedTestUser(t, h.db)
	wsID := seedTestWorkspace(t, h.db, adminID)
	managerID := seedMemberWithCapabilities(t, h.db, wsID, "MANAGER",
		`["chat","routine.create"]`, "manager")
	ludmilaID := seedMemberWithCapabilities(t, h.db, wsID, "MEMBER",
		`["chat"]`, "ludmila-non-admin-target")

	req := httptest.NewRequest("PATCH", "/x", strings.NewReader(
		`{"grant":["routine.create"]}`))
	req.SetPathValue("memberId", ludmilaID)
	ctx := context.WithValue(req.Context(), ctxWorkspaceID, wsID)
	ctx = context.WithValue(ctx, ctxUser, &AuthUser{ID: managerID})
	ctx = context.WithValue(ctx, ctxRole, "MANAGER")
	req = req.WithContext(ctx)
	w := httptest.NewRecorder()

	h.PatchMemberCapabilities(w, req)

	if w.Code != 403 {
		t.Errorf("status = %d, want 403 — MANAGER cannot manage capabilities", w.Code)
	}
}

// TestPatchCapabilities_InvalidCapabilityName: typo rejection.
func TestPatchCapabilities_InvalidCapabilityName(t *testing.T) {
	h := newWsHandlerForTest(t)
	adminID := seedTestUser(t, h.db)
	wsID := seedTestWorkspace(t, h.db, adminID)
	ludmilaID := seedMemberWithCapabilities(t, h.db, wsID, "MEMBER",
		`["chat"]`, "ludmila-typo")

	req := httptest.NewRequest("PATCH", "/x", strings.NewReader(
		`{"grant":["routine.creat"]}`)) // typo
	req.SetPathValue("memberId", ludmilaID)
	ctx := context.WithValue(req.Context(), ctxWorkspaceID, wsID)
	ctx = context.WithValue(ctx, ctxUser, &AuthUser{ID: adminID})
	ctx = context.WithValue(ctx, ctxRole, "ADMIN")
	req = req.WithContext(ctx)
	w := httptest.NewRecorder()

	h.PatchMemberCapabilities(w, req)

	if w.Code != 400 {
		t.Errorf("status = %d, want 400 for typo'd capability", w.Code)
	}
}

// TestPatchCapabilities_InvalidatesCacheOnWrite: after a PATCH the
// next CapabilitiesForMember call sees the new set without waiting
// for the 30 s TTL. We prove it by reading-before, writing, reading-
// after — the after-value should match the post-write state.
func TestPatchCapabilities_InvalidatesCacheOnWrite(t *testing.T) {
	h := newWsHandlerForTest(t)
	adminID := seedTestUser(t, h.db)
	wsID := seedTestWorkspace(t, h.db, adminID)
	ludmilaID := seedMemberWithCapabilities(t, h.db, wsID, "MEMBER",
		`["chat"]`, "ludmila-cache-invalidate")
	InvalidateCapabilityCache(wsID, ludmilaID)

	// Prime cache via the same helper the runtime path uses.
	_, _, _ = CapabilitiesForMember(context.Background(), h.db, wsID, ludmilaID)

	// PATCH grants routine.create.
	req := httptest.NewRequest("PATCH", "/x", strings.NewReader(
		`{"grant":["routine.create"]}`))
	req.SetPathValue("memberId", ludmilaID)
	ctx := context.WithValue(req.Context(), ctxWorkspaceID, wsID)
	ctx = context.WithValue(ctx, ctxUser, &AuthUser{ID: adminID})
	ctx = context.WithValue(ctx, ctxRole, "ADMIN")
	req = req.WithContext(ctx)
	w := httptest.NewRecorder()
	h.PatchMemberCapabilities(w, req)
	if w.Code != 200 {
		t.Fatalf("PATCH failed: status = %d", w.Code)
	}

	// Cached lookup must see the new capability immediately — no
	// 30 s wait, no flake-prone time.Sleep in this test.
	caps, _, _ := CapabilitiesForMember(context.Background(), h.db, wsID, ludmilaID)
	if !HasCapability(caps, CapabilityRoutineCreate) {
		t.Error("post-PATCH lookup didn't see new routine.create grant — cache wasn't invalidated")
	}
}
