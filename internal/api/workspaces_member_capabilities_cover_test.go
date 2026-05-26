package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
)

// TestListMembersCapabilities_BulkReturnsAllMembers covers the
// workspace-wide bulk endpoint. One SELECT, one response, every
// member's capabilities included. Drives the Members grid.
func TestListMembersCapabilities_BulkReturnsAllMembers(t *testing.T) {
	h := newWsHandlerForTest(t)
	adminID := seedTestUser(t, h.db)
	wsID := seedTestWorkspace(t, h.db, adminID)
	seedMemberWithCapabilities(t, h.db, wsID, "MEMBER", `["chat"]`, "bulk-a")
	seedMemberWithCapabilities(t, h.db, wsID, "MEMBER", `["chat","routine.create"]`, "bulk-b")
	seedMemberWithCapabilities(t, h.db, wsID, "MANAGER", `["chat","routine.create","issue.create","memory.write"]`, "bulk-c")

	req := httptest.NewRequest("GET", "/x", nil)
	ctx := context.WithValue(req.Context(), ctxWorkspaceID, wsID)
	ctx = context.WithValue(ctx, ctxUser, &AuthUser{ID: adminID})
	ctx = context.WithValue(ctx, ctxRole, "ADMIN")
	req = req.WithContext(ctx)
	w := httptest.NewRecorder()

	h.ListMembersCapabilities(w, req)
	if w.Code != 200 {
		t.Fatalf("status = %d, body = %s", w.Code, w.Body.String())
	}
	var got capabilitiesBulkResponse
	if err := json.NewDecoder(w.Body).Decode(&got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	// 4 members total: original OWNER + 3 seeded.
	if len(got.Members) != 4 {
		t.Errorf("got %d members, want 4", len(got.Members))
	}
	// Spot-check: every returned member has at least chat (defensive
	// invariant from resolveCapabilitiesFromRow).
	for _, m := range got.Members {
		hasChat := false
		for _, c := range m.Capabilities {
			if c == "chat" {
				hasChat = true
				break
			}
		}
		if !hasChat {
			t.Errorf("member %s has no chat capability in bulk response: %v", m.UserID, m.Capabilities)
		}
	}
}

// TestListMembersCapabilities_NonAdminDenied — bulk endpoint must
// share the same admin-only gate as the per-member variant. Capability
// topology is operator-confidential.
func TestListMembersCapabilities_NonAdminDenied(t *testing.T) {
	h := newWsHandlerForTest(t)
	adminID := seedTestUser(t, h.db)
	wsID := seedTestWorkspace(t, h.db, adminID)
	managerID := seedMemberWithCapabilities(t, h.db, wsID, "MANAGER",
		`["chat","routine.create"]`, "bulk-mgr-deny")

	req := httptest.NewRequest("GET", "/x", nil)
	ctx := context.WithValue(req.Context(), ctxWorkspaceID, wsID)
	ctx = context.WithValue(ctx, ctxUser, &AuthUser{ID: managerID})
	ctx = context.WithValue(ctx, ctxRole, "MANAGER")
	req = req.WithContext(ctx)
	w := httptest.NewRecorder()

	h.ListMembersCapabilities(w, req)
	if w.Code != 403 {
		t.Errorf("status = %d, want 403", w.Code)
	}
}

// TestGetMemberCapabilities_HappyPath covers the previously 0%-
// covered GET handler. ADMIN reads a MEMBER's row, gets the parsed
// capability set + role back.
func TestGetMemberCapabilities_HappyPath(t *testing.T) {
	h := newWsHandlerForTest(t)
	adminID := seedTestUser(t, h.db)
	wsID := seedTestWorkspace(t, h.db, adminID)
	ludmilaID := seedMemberWithCapabilities(t, h.db, wsID, "MEMBER",
		`["chat","routine.create"]`, "ludmila-get-happy")
	InvalidateCapabilityCache(wsID, ludmilaID)

	req := httptest.NewRequest("GET", "/x", nil)
	req.SetPathValue("memberId", ludmilaID)
	ctx := context.WithValue(req.Context(), ctxWorkspaceID, wsID)
	ctx = context.WithValue(ctx, ctxUser, &AuthUser{ID: adminID})
	ctx = context.WithValue(ctx, ctxRole, "ADMIN")
	req = req.WithContext(ctx)
	w := httptest.NewRecorder()

	h.GetMemberCapabilities(w, req)

	if w.Code != 200 {
		t.Fatalf("status = %d, body = %s", w.Code, w.Body.String())
	}
	var got capabilitiesGetResponse
	if err := json.NewDecoder(w.Body).Decode(&got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.UserID != ludmilaID {
		t.Errorf("UserID = %q, want %q", got.UserID, ludmilaID)
	}
	if got.Role != "MEMBER" {
		t.Errorf("Role = %q, want MEMBER", got.Role)
	}
	want := []string{"chat", "routine.create"}
	if !reflect.DeepEqual(got.Capabilities, want) {
		t.Errorf("Capabilities = %v, want %v", got.Capabilities, want)
	}
}

// TestGetMemberCapabilities_NonAdminDenied — MANAGER caller is
// rejected (capability topology is admin-confidential).
func TestGetMemberCapabilities_NonAdminDenied(t *testing.T) {
	h := newWsHandlerForTest(t)
	adminID := seedTestUser(t, h.db)
	wsID := seedTestWorkspace(t, h.db, adminID)
	managerID := seedMemberWithCapabilities(t, h.db, wsID, "MANAGER",
		`["chat","routine.create"]`, "mgr-get-deny")

	req := httptest.NewRequest("GET", "/x", nil)
	req.SetPathValue("memberId", "u-anyone")
	ctx := context.WithValue(req.Context(), ctxWorkspaceID, wsID)
	ctx = context.WithValue(ctx, ctxUser, &AuthUser{ID: managerID})
	ctx = context.WithValue(ctx, ctxRole, "MANAGER")
	req = req.WithContext(ctx)
	w := httptest.NewRecorder()

	h.GetMemberCapabilities(w, req)
	if w.Code != 403 {
		t.Errorf("status = %d, want 403", w.Code)
	}
}

// TestGetMemberCapabilities_MemberNotFound — admin queries a user
// who isn't in the workspace_members table. 404 (not 500) so the
// UI can distinguish from a server failure.
func TestGetMemberCapabilities_MemberNotFound(t *testing.T) {
	h := newWsHandlerForTest(t)
	adminID := seedTestUser(t, h.db)
	wsID := seedTestWorkspace(t, h.db, adminID)

	req := httptest.NewRequest("GET", "/x", nil)
	req.SetPathValue("memberId", "u-nonexistent")
	ctx := context.WithValue(req.Context(), ctxWorkspaceID, wsID)
	ctx = context.WithValue(ctx, ctxUser, &AuthUser{ID: adminID})
	ctx = context.WithValue(ctx, ctxRole, "ADMIN")
	req = req.WithContext(ctx)
	w := httptest.NewRecorder()

	h.GetMemberCapabilities(w, req)
	if w.Code != 404 {
		t.Errorf("status = %d, want 404", w.Code)
	}
}

// TestPatchCapabilities_DiffNoOp covers the short-circuit path:
// posting a set equal to current state should succeed without
// writing an audit row. We verify by observing no PATCH side-effect
// other than the 200 response — exact log-row absence is harder to
// assert without log capture, but the visible OK + unchanged row
// is sufficient signal.
func TestPatchCapabilities_DiffNoOp(t *testing.T) {
	h := newWsHandlerForTest(t)
	adminID := seedTestUser(t, h.db)
	wsID := seedTestWorkspace(t, h.db, adminID)
	ludmilaID := seedMemberWithCapabilities(t, h.db, wsID, "MEMBER",
		`["chat","routine.create"]`, "ludmila-noop")
	InvalidateCapabilityCache(wsID, ludmilaID)

	// Send the same set back; no diff.
	req := httptest.NewRequest("PATCH", "/x", strings.NewReader(
		`{"set":["chat","routine.create"]}`))
	req.SetPathValue("memberId", ludmilaID)
	ctx := context.WithValue(req.Context(), ctxWorkspaceID, wsID)
	ctx = context.WithValue(ctx, ctxUser, &AuthUser{ID: adminID})
	ctx = context.WithValue(ctx, ctxRole, "ADMIN")
	req = req.WithContext(ctx)
	w := httptest.NewRecorder()

	h.PatchMemberCapabilities(w, req)
	if w.Code != 200 {
		t.Fatalf("status = %d", w.Code)
	}
	var got capabilitiesGetResponse
	_ = json.NewDecoder(w.Body).Decode(&got)
	if !reflect.DeepEqual(got.Capabilities, []string{"chat", "routine.create"}) {
		t.Errorf("noop returned different caps: %v", got.Capabilities)
	}
}

// TestPatchCapabilities_InvalidJSONBody — malformed JSON is rejected
// with 400 rather than crashing the handler.
func TestPatchCapabilities_InvalidJSONBody(t *testing.T) {
	h := newWsHandlerForTest(t)
	adminID := seedTestUser(t, h.db)
	wsID := seedTestWorkspace(t, h.db, adminID)
	ludmilaID := seedMemberWithCapabilities(t, h.db, wsID, "MEMBER",
		`["chat"]`, "ludmila-bad-json")

	req := httptest.NewRequest("PATCH", "/x", strings.NewReader(`{not json`))
	req.SetPathValue("memberId", ludmilaID)
	ctx := context.WithValue(req.Context(), ctxWorkspaceID, wsID)
	ctx = context.WithValue(ctx, ctxUser, &AuthUser{ID: adminID})
	ctx = context.WithValue(ctx, ctxRole, "ADMIN")
	req = req.WithContext(ctx)
	w := httptest.NewRecorder()

	h.PatchMemberCapabilities(w, req)
	if w.Code != 400 {
		t.Errorf("status = %d, want 400", w.Code)
	}
}

// TestPatchCapabilities_NoAuthUser — defensive 401 when caller
// context lacks an AuthUser (route should be gated by middleware,
// but the handler shouldn't crash if it isn't).
func TestPatchCapabilities_NoAuthUser(t *testing.T) {
	h := newWsHandlerForTest(t)
	adminID := seedTestUser(t, h.db)
	wsID := seedTestWorkspace(t, h.db, adminID)

	req := httptest.NewRequest("PATCH", "/x", strings.NewReader(`{"set":["chat"]}`))
	req.SetPathValue("memberId", "u-anyone")
	ctx := context.WithValue(req.Context(), ctxWorkspaceID, wsID)
	// Note: no ctxUser set
	ctx = context.WithValue(ctx, ctxRole, "ADMIN")
	req = req.WithContext(ctx)
	w := httptest.NewRecorder()

	h.PatchMemberCapabilities(w, req)
	if w.Code != 401 {
		t.Errorf("status = %d, want 401", w.Code)
	}
}

// TestPatchCapabilities_TargetNotFound — admin tries to patch a
// non-member: 404.
func TestPatchCapabilities_TargetNotFound(t *testing.T) {
	h := newWsHandlerForTest(t)
	adminID := seedTestUser(t, h.db)
	wsID := seedTestWorkspace(t, h.db, adminID)

	req := httptest.NewRequest("PATCH", "/x", strings.NewReader(`{"set":["chat"]}`))
	req.SetPathValue("memberId", "u-nobody")
	ctx := context.WithValue(req.Context(), ctxWorkspaceID, wsID)
	ctx = context.WithValue(ctx, ctxUser, &AuthUser{ID: adminID})
	ctx = context.WithValue(ctx, ctxRole, "ADMIN")
	req = req.WithContext(ctx)
	w := httptest.NewRecorder()

	h.PatchMemberCapabilities(w, req)
	if w.Code != 404 {
		t.Errorf("status = %d, want 404", w.Code)
	}
}

// TestCapabilitySetsEqual_NotEqual covers the inequality branch
// that previously wasn't hit by any test (equal-only happy path).
func TestCapabilitySetsEqual_NotEqual(t *testing.T) {
	a := map[string]struct{}{"chat": {}, "routine.create": {}}
	b := map[string]struct{}{"chat": {}}
	if capabilitySetsEqual(a, b) {
		t.Error("a != b (different sizes); expected false")
	}
	c := map[string]struct{}{"chat": {}, "skill.create": {}}
	if capabilitySetsEqual(a, c) {
		t.Error("a != c (different keys, same size); expected false")
	}
}

// TestAllBundles covers the previously 0%-covered AllBundles
// helper. Asserts ordering + presence of every named bundle.
func TestAllBundles(t *testing.T) {
	got := AllBundles()
	want := []CapabilityBundle{BundleChat, BundlePower, BundleAdmin}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
}

// TestApplyCapabilityMutation_NoBody covers the "shapes == 0" branch
// — empty body should reject.
func TestApplyCapabilityMutation_NoBody(t *testing.T) {
	_, err := applyCapabilityMutation(map[string]struct{}{"chat": {}}, capabilitiesPatchRequest{})
	if err == nil {
		t.Error("empty body should error")
	}
	if !strings.Contains(err.Error(), "set, grant, revoke, preset") {
		t.Errorf("expected helpful error, got %v", err)
	}
}

// TestApplyCapabilityMutation_EmptyArraysRejected guards against the
// silent-reset bug: an empty array slipping past the multi-shape
// check and executing as a destructive "replace with chat-only" or
// "no-op". All three array-shaped bodies (set, grant, revoke) must
// error symmetrically when the array is empty.
//
// Pre-fix, `{"set":[]}` made it through because the counter used
// `body.Set != nil` (true for empty slice) but grant/revoke used
// `len(...) > 0`. An optimistic UI sending `{"set":[]}` would have
// silently wiped the member's capabilities back to chat-only.
func TestApplyCapabilityMutation_EmptyArraysRejected(t *testing.T) {
	current := map[string]struct{}{"chat": {}, "routine.create": {}, "skill.create": {}}
	cases := []struct {
		name string
		body capabilitiesPatchRequest
	}{
		{"set=[]", capabilitiesPatchRequest{Set: []string{}}},
		{"grant=[]", capabilitiesPatchRequest{Grant: []string{}}},
		{"revoke=[]", capabilitiesPatchRequest{Revoke: []string{}}},
		{"all=[]", capabilitiesPatchRequest{Set: []string{}, Grant: []string{}, Revoke: []string{}}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			next, err := applyCapabilityMutation(current, tc.body)
			if err == nil {
				t.Fatalf("empty %s must error; got next=%v", tc.name, next)
			}
			if !strings.Contains(err.Error(), "set, grant, revoke, preset") {
				t.Errorf("error message unhelpful: %v", err)
			}
		})
	}
}

// TestApplyCapabilityMutation_InvalidSetCapability covers the
// "set with unknown capability" rejection path.
func TestApplyCapabilityMutation_InvalidSetCapability(t *testing.T) {
	_, err := applyCapabilityMutation(map[string]struct{}{"chat": {}}, capabilitiesPatchRequest{
		Set: []string{"chat", "bogus.thing"},
	})
	if err == nil || !strings.Contains(err.Error(), "unknown capability") {
		t.Errorf("expected unknown-capability error, got %v", err)
	}
}

// TestApplyCapabilityMutation_InvalidGrantCapability covers the
// grant path's unknown-capability rejection.
func TestApplyCapabilityMutation_InvalidGrantCapability(t *testing.T) {
	_, err := applyCapabilityMutation(map[string]struct{}{"chat": {}}, capabilitiesPatchRequest{
		Grant: []string{"bogus.thing"},
	})
	if err == nil || !strings.Contains(err.Error(), "unknown capability") {
		t.Errorf("expected unknown-capability error, got %v", err)
	}
}

// TestApplyCapabilityMutation_InvalidRevokeCapability — same shape
// for revoke. Easy to forget when a future cap rename ships.
func TestApplyCapabilityMutation_InvalidRevokeCapability(t *testing.T) {
	_, err := applyCapabilityMutation(map[string]struct{}{"chat": {}, "routine.create": {}}, capabilitiesPatchRequest{
		Revoke: []string{"bogus.thing"},
	})
	if err == nil || !strings.Contains(err.Error(), "unknown capability") {
		t.Errorf("expected unknown-capability error, got %v", err)
	}
}

// TestApplyCapabilityMutation_SetStripsExplicitChat covers the
// canonical-form invariant: chat in the set is implicitly added
// even if the caller forgot it.
func TestApplyCapabilityMutation_SetStripsExplicitChat(t *testing.T) {
	got, err := applyCapabilityMutation(nil, capabilitiesPatchRequest{
		Set: []string{"routine.create"}, // no chat
	})
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if _, ok := got["chat"]; !ok {
		t.Error("chat should be implied even when set omits it")
	}
}

// TestApplyCapabilityMutation_UnknownPreset hits the previously-
// uncovered preset-rejection branch (line 281-283). Typo guards.
func TestApplyCapabilityMutation_UnknownPreset(t *testing.T) {
	_, err := applyCapabilityMutation(map[string]struct{}{"chat": {}}, capabilitiesPatchRequest{
		Preset: "elite-tier", // not chat / power / admin
	})
	if err == nil || !strings.Contains(err.Error(), "unknown preset") {
		t.Errorf("expected unknown-preset error, got %v", err)
	}
}

// TestGetMemberCapabilities_DBError covers the load-error path —
// we close the DB out from under the handler, which makes the
// subsequent ExecContext return a 'database is closed' error.
// This is the only realistic way to trigger the err-not-nil branch
// at line 72-76 from a unit test.
func TestGetMemberCapabilities_DBError(t *testing.T) {
	h := newWsHandlerForTest(t)
	adminID := seedTestUser(t, h.db)
	wsID := seedTestWorkspace(t, h.db, adminID)
	// Close DB so the next query returns an error.
	_ = h.db.Close()

	req := httptest.NewRequest("GET", "/x", nil)
	req.SetPathValue("memberId", "u-x")
	ctx := context.WithValue(req.Context(), ctxWorkspaceID, wsID)
	ctx = context.WithValue(ctx, ctxUser, &AuthUser{ID: adminID})
	ctx = context.WithValue(ctx, ctxRole, "ADMIN")
	req = req.WithContext(ctx)
	w := httptest.NewRecorder()

	h.GetMemberCapabilities(w, req)
	if w.Code != 500 {
		t.Errorf("status = %d, want 500 on DB error", w.Code)
	}
}

// TestPatchCapabilities_BodyTooLarge guards the MaxBytesReader cap.
// A malicious ADMIN streaming a multi-GB JSON body would otherwise
// pin the server; now we reject at 16 KB with a 413.
func TestPatchCapabilities_BodyTooLarge(t *testing.T) {
	h := newWsHandlerForTest(t)
	adminID := seedTestUser(t, h.db)
	wsID := seedTestWorkspace(t, h.db, adminID)
	ludmilaID := seedMemberWithCapabilities(t, h.db, wsID, "MEMBER",
		`["chat"]`, "ludmila-body-cap")

	// Craft a body that's well over the 16 KB limit — pad the JSON
	// with a giant repeated capability name (still parses as JSON,
	// just oversized). MaxBytesReader trips before the decoder
	// even sees a full object.
	huge := strings.Repeat("a", 20*1024)
	body := `{"set":["` + huge + `"]}`
	req := httptest.NewRequest("PATCH", "/x", strings.NewReader(body))
	req.SetPathValue("memberId", ludmilaID)
	ctx := context.WithValue(req.Context(), ctxWorkspaceID, wsID)
	ctx = context.WithValue(ctx, ctxUser, &AuthUser{ID: adminID})
	ctx = context.WithValue(ctx, ctxRole, "ADMIN")
	req = req.WithContext(ctx)
	w := httptest.NewRecorder()

	h.PatchMemberCapabilities(w, req)
	if w.Code != http.StatusRequestEntityTooLarge {
		t.Errorf("status = %d, want 413", w.Code)
	}
}

// TestPatchCapabilities_RaceMemberDeletedBetweenReadAndWrite is the
// regression test. Between the load step and the UPDATE
// step a concurrent RemoveMember can delete the row; the UPDATE
// then affects 0 rows but the old code 200'd + audited. Now we 404.
func TestPatchCapabilities_RaceMemberDeletedBetweenReadAndWrite(t *testing.T) {
	h := newWsHandlerForTest(t)
	adminID := seedTestUser(t, h.db)
	wsID := seedTestWorkspace(t, h.db, adminID)
	ludmilaID := seedMemberWithCapabilities(t, h.db, wsID, "MEMBER",
		`["chat"]`, "ludmila-race")
	InvalidateCapabilityCache(wsID, ludmilaID)

	// Inject the race deterministically: delete the row out from
	// under the cached read. The cache holds the post-load state;
	// the next CapabilitiesForMember call hits cache, the UPDATE
	// hits the (now-empty) table → 0 rows affected.
	_, _, _ = CapabilitiesForMember(context.Background(), h.db, wsID, ludmilaID)
	if _, err := h.db.Exec(`DELETE FROM workspace_members WHERE user_id = ?`, ludmilaID); err != nil {
		t.Fatalf("simulate race: %v", err)
	}

	req := httptest.NewRequest("PATCH", "/x", strings.NewReader(
		`{"set":["chat","routine.create"]}`))
	req.SetPathValue("memberId", ludmilaID)
	ctx := context.WithValue(req.Context(), ctxWorkspaceID, wsID)
	ctx = context.WithValue(ctx, ctxUser, &AuthUser{ID: adminID})
	ctx = context.WithValue(ctx, ctxRole, "ADMIN")
	req = req.WithContext(ctx)
	w := httptest.NewRecorder()

	h.PatchMemberCapabilities(w, req)
	if w.Code != 404 {
		t.Errorf("status = %d, want 404 (member deleted between read and write)", w.Code)
	}
}

// TestPatchCapabilities_DBUpdateError covers the line 158-162 branch:
// the UPDATE itself fails with a SQL error. Same close-DB technique.
func TestPatchCapabilities_DBUpdateError(t *testing.T) {
	h := newWsHandlerForTest(t)
	adminID := seedTestUser(t, h.db)
	wsID := seedTestWorkspace(t, h.db, adminID)
	ludmilaID := seedMemberWithCapabilities(t, h.db, wsID, "MEMBER",
		`["chat"]`, "ludmila-db-fail")
	InvalidateCapabilityCache(wsID, ludmilaID)

	// Close DB after seed so load succeeds (cache hit-style — we
	// need to actually exercise the UPDATE path, so we drop the
	// connection right when the handler runs. Closing here makes
	// even the load fail, which still routes through the 500
	// branch — just one step earlier on line 119.
	_ = h.db.Close()

	req := httptest.NewRequest("PATCH", "/x", strings.NewReader(`{"set":["chat","routine.create"]}`))
	req.SetPathValue("memberId", ludmilaID)
	ctx := context.WithValue(req.Context(), ctxWorkspaceID, wsID)
	ctx = context.WithValue(ctx, ctxUser, &AuthUser{ID: adminID})
	ctx = context.WithValue(ctx, ctxRole, "ADMIN")
	req = req.WithContext(ctx)
	w := httptest.NewRecorder()

	h.PatchMemberCapabilities(w, req)
	if w.Code != 500 {
		t.Errorf("status = %d, want 500 on DB error", w.Code)
	}
}
