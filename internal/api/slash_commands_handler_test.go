package api

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"testing"
)

// TestSlashCommandsList_FiltersByCapability is the central
// behaviour test: the catalog returned matches exactly the slash
// actions the caller's capability set permits. A MEMBER with only
// chat sees an empty array; a MEMBER granted routine.create sees
// the one routine entry; an ADMIN sees the full catalog.
func TestSlashCommandsList_FiltersByCapability(t *testing.T) {
	db := setupTestDB(t)
	ownerID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, ownerID)

	// Three members at different capability tiers.
	chatOnly := seedMemberWithCapabilities(t, db, wsID, "MEMBER",
		`["chat"]`, "slashcat-chatonly")
	withRoutine := seedMemberWithCapabilities(t, db, wsID, "MEMBER",
		`["chat","routine.create"]`, "slashcat-routine")
	adminFull := seedMemberWithCapabilities(t, db, wsID, "ADMIN",
		`["chat","routine.create","skill.create","credential.create","credential.rotate","issue.create","memory.write"]`,
		"slashcat-admin")

	// Set up the handler against a router that has just the DB
	// wired — minimal scaffolding, enough for the List handler to
	// reach CapabilitiesForMember.
	router := &Router{db: db}
	h := NewSlashCommandsHandler(router)

	tests := []struct {
		name    string
		userID  string
		wantIDs []string
	}{
		{
			name:    "chat-only member sees empty actions",
			userID:  chatOnly,
			wantIDs: []string{}, // chat is implied; no actions in the catalog match
		},
		{
			name:    "routine-grant member sees only routine action",
			userID:  withRoutine,
			wantIDs: []string{"routine"},
		},
		{
			name:    "admin sees full catalog",
			userID:  adminFull,
			wantIDs: []string{"routine", "issue", "remember", "skill", "credential"},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			InvalidateCapabilityCache(wsID, tc.userID)

			r := httptest.NewRequest("GET", "/api/v1/slash-commands", nil)
			ctx := context.WithValue(r.Context(), ctxWorkspaceID, wsID)
			ctx = context.WithValue(ctx, ctxUser, &AuthUser{ID: tc.userID})
			r = r.WithContext(ctx)
			w := httptest.NewRecorder()

			h.List(w, r)

			if w.Code != 200 {
				t.Fatalf("status = %d, body = %s", w.Code, w.Body.String())
			}
			var got []slashCommand
			if err := json.NewDecoder(w.Body).Decode(&got); err != nil {
				t.Fatalf("decode: %v", err)
			}
			gotIDs := make([]string, 0, len(got))
			for _, sc := range got {
				gotIDs = append(gotIDs, sc.ID)
			}
			if !stringSliceEqual(gotIDs, tc.wantIDs) {
				t.Errorf("got IDs %v, want %v", gotIDs, tc.wantIDs)
			}
		})
	}
}

// TestSlashCommandsList_RejectsNoUserContext asserts the handler
// doesn't accidentally return the full catalog when AuthMiddleware
// failed to populate ctxUser. Defence-in-depth — the registrar
// wraps the route in authed so this path shouldn't fire in
// production, but a future routing typo shouldn't open a hole.
func TestSlashCommandsList_RejectsNoUserContext(t *testing.T) {
	router := &Router{}
	h := NewSlashCommandsHandler(router)
	r := httptest.NewRequest("GET", "/api/v1/slash-commands", nil)
	w := httptest.NewRecorder()
	h.List(w, r)
	if w.Code != 401 {
		t.Errorf("status = %d, want 401 without user context", w.Code)
	}
}

// TestSlashCommandsList_RejectsNoWorkspace covers the path where
// the user is authed but no workspace context is present —
// capabilities are workspace-scoped so the filter has no meaning
// without one.
func TestSlashCommandsList_RejectsNoWorkspace(t *testing.T) {
	router := &Router{}
	h := NewSlashCommandsHandler(router)
	r := httptest.NewRequest("GET", "/api/v1/slash-commands", nil)
	r = r.WithContext(context.WithValue(r.Context(), ctxUser, &AuthUser{ID: "u1"}))
	w := httptest.NewRecorder()
	h.List(w, r)
	if w.Code != 400 {
		t.Errorf("status = %d, want 400 without workspace_id", w.Code)
	}
}

// TestSlashCommandsList_NonMemberReturnsEmpty: a caller with valid
// session but no membership in the queried workspace gets an empty
// array (not 403). UX-friendly — palette opens, just has nothing in
// it; no special-case handling on the client.
func TestSlashCommandsList_NonMemberReturnsEmpty(t *testing.T) {
	db := setupTestDB(t)
	ownerID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, ownerID)

	if _, err := db.Exec(`INSERT INTO users (id, email, full_name) VALUES ('outsider','o@x','Out')`); err != nil {
		t.Fatalf("seed: %v", err)
	}
	InvalidateCapabilityCache(wsID, "outsider")

	router := &Router{db: db}
	h := NewSlashCommandsHandler(router)
	r := httptest.NewRequest("GET", "/api/v1/slash-commands", nil)
	ctx := context.WithValue(r.Context(), ctxWorkspaceID, wsID)
	ctx = context.WithValue(ctx, ctxUser, &AuthUser{ID: "outsider"})
	r = r.WithContext(ctx)
	w := httptest.NewRecorder()

	h.List(w, r)
	if w.Code != 200 {
		t.Fatalf("status = %d", w.Code)
	}
	var got []slashCommand
	_ = json.NewDecoder(w.Body).Decode(&got)
	if len(got) != 0 {
		t.Errorf("non-member got %d entries, want 0", len(got))
	}
}

// TestSlashCommandsCatalog_EveryEntryHasKnownCapability is a
// developer-aimed test: an entry pointing at a capability the
// runtime doesn't recognize would silently appear in the catalog
// but never get granted to anyone. Catches drift between
// capabilities.go constants and the catalog.
func TestSlashCommandsCatalog_EveryEntryHasKnownCapability(t *testing.T) {
	for _, sc := range slashCommandCatalog {
		if !IsValidCapability(sc.Capability) {
			t.Errorf("slash command %q references unknown capability %q", sc.ID, sc.Capability)
		}
		if sc.Label == "" {
			t.Errorf("slash command %q has empty label", sc.ID)
		}
		if sc.ID == "" {
			t.Error("catalog has entry with empty ID")
		}
	}
}

func stringSliceEqual(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
