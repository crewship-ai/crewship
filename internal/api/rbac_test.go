package api

import (
	"context"
	"net/http/httptest"
	"testing"
)

// TestEffectiveRole_TakesMax pins the Patch M1 contract: per-crew role
// can only elevate, never drop below the workspace floor. Anything
// unknown ranks 0 so a missing/typo'd value can't accidentally
// outrank a real one.
func TestEffectiveRole_TakesMax(t *testing.T) {
	cases := []struct {
		ws, crew, want string
	}{
		{"MEMBER", "MANAGER", "MANAGER"}, // crew elevates
		{"MANAGER", "MEMBER", "MANAGER"}, // crew can't demote
		{"OWNER", "MEMBER", "OWNER"},     // owner stays owner everywhere
		{"VIEWER", "ADMIN", "ADMIN"},     // crew can promote way up
		{"ADMIN", "", "ADMIN"},           // empty crew override falls through
		{"", "MANAGER", "MANAGER"},       // empty workspace falls through (unusual but defined)
		{"MEMBER", "BOGUS", "MEMBER"},    // typo'd override stays at floor
		{"", "", ""},                     // both empty → empty (no implicit role)
	}
	for _, tc := range cases {
		got := effectiveRole(tc.ws, tc.crew)
		if got != tc.want {
			t.Errorf("effectiveRole(%q, %q) = %q, want %q", tc.ws, tc.crew, got, tc.want)
		}
	}
}

// TestCrewRoleFromDB_ElevationPath: a workspace MEMBER becomes
// effective MANAGER inside a crew that has them promoted to MANAGER
// at the crew level. Validates the SQL join + the role merge.
func TestCrewRoleFromDB_ElevationPath(t *testing.T) {
	t.Parallel()
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	// seedTestWorkspace makes userID OWNER. Add a second user as
	// workspace MEMBER + crew MANAGER to test the elevation path.
	const u2 = "user-2"
	execOrFatal(t, db, `INSERT INTO users (id, email, full_name) VALUES (?, 'u2@x', 'U2')`, u2)
	execOrFatal(t, db, `INSERT INTO workspace_members (id, workspace_id, user_id, role) VALUES ('wm2', ?, ?, 'MEMBER')`, wsID, u2)
	execOrFatal(t, db, `INSERT INTO crews (id, workspace_id, name, slug) VALUES ('crew-1', ?, 'Crew', 'c1')`, wsID)
	execOrFatal(t, db, `INSERT INTO crew_members (id, crew_id, user_id, role) VALUES ('cm2', 'crew-1', ?, 'MANAGER')`, u2)

	got, err := CrewRoleFromDB(context.Background(), db, u2, "crew-1")
	if err != nil {
		t.Fatalf("CrewRoleFromDB: %v", err)
	}
	if got != "MANAGER" {
		t.Errorf("effective role = %q, want MANAGER (elevated from MEMBER)", got)
	}
}

// TestCrewRoleFromDB_NoOverrideFallsBackToWorkspace: a workspace
// MANAGER who is a crew member with NULL role override stays
// MANAGER inside the crew.
func TestCrewRoleFromDB_NoOverrideFallsBackToWorkspace(t *testing.T) {
	t.Parallel()
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	const u2 = "user-2-nofallback"
	execOrFatal(t, db, `INSERT INTO users (id, email, full_name) VALUES (?, 'u2@x', 'U2')`, u2)
	execOrFatal(t, db, `INSERT INTO workspace_members (id, workspace_id, user_id, role) VALUES ('wm2', ?, ?, 'MANAGER')`, wsID, u2)
	execOrFatal(t, db, `INSERT INTO crews (id, workspace_id, name, slug) VALUES ('crew-1', ?, 'Crew', 'c1')`, wsID)
	// NULL role on crew_members → effective role = workspace MANAGER.
	execOrFatal(t, db, `INSERT INTO crew_members (id, crew_id, user_id) VALUES ('cm2', 'crew-1', ?)`, u2)

	got, err := CrewRoleFromDB(context.Background(), db, u2, "crew-1")
	if err != nil {
		t.Fatalf("CrewRoleFromDB: %v", err)
	}
	if got != "MANAGER" {
		t.Errorf("effective role = %q, want MANAGER (NULL override = workspace floor)", got)
	}
}

// TestCrewRoleFromDB_NotAMember returns empty for a user who isn't a
// workspace member of the crew's workspace. Empty role → downstream
// canRole returns false → access denied. Important: never accidentally
// grants access when the user simply doesn't exist in the join.
func TestCrewRoleFromDB_NotAMember(t *testing.T) {
	t.Parallel()
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	execOrFatal(t, db, `INSERT INTO crews (id, workspace_id, name, slug) VALUES ('crew-1', ?, 'Crew', 'c1')`, wsID)

	got, err := CrewRoleFromDB(context.Background(), db, "ghost-user", "crew-1")
	if err != nil {
		t.Fatalf("CrewRoleFromDB: %v", err)
	}
	if got != "" {
		t.Errorf("effective role for non-member = %q, want empty", got)
	}
}

// TestCanScope_WildcardAndExact pins the Patch M2 scope-matching
// contract: exact match, wildcard "*", and per-resource wildcard
// "agents:*" all pass; unrelated scopes don't.
func TestCanScope_WildcardAndExact(t *testing.T) {
	mkCtx := func(scopes ...string) context.Context {
		set := make(stringSet, len(scopes))
		for _, s := range scopes {
			set[s] = struct{}{}
		}
		ctx := context.Background()
		return context.WithValue(ctx, ctxTokenScopes, set)
	}

	cases := []struct {
		name      string
		ctx       context.Context
		requested string
		want      bool
	}{
		{"jwt_no_scope_unrestricted", context.Background(), "agents:write", true},
		{"exact_match", mkCtx("agents:write"), "agents:write", true},
		{"unrelated_denied", mkCtx("agents:read"), "agents:write", false},
		{"wildcard_star", mkCtx("*"), "credentials:write", true},
		{"resource_wildcard", mkCtx("agents:*"), "agents:write", true},
		{"resource_wildcard_does_not_match_other_resource", mkCtx("agents:*"), "credentials:write", false},
		{"empty_scope_set_denies", mkCtx(), "agents:write", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := canScope(tc.ctx, tc.requested)
			if got != tc.want {
				t.Errorf("canScope(%q) = %v, want %v", tc.requested, got, tc.want)
			}
		})
	}
}

// TestParseScopes_GarbageReturnsNil — a corrupt scopes column must
// not lock the caller out; parseScopes returns nil so canScope
// treats the token as pre-v99 unrestricted instead of empty-set
// "deny everything".
func TestParseScopes_GarbageReturnsNil(t *testing.T) {
	cases := []string{
		"",
		"not-json",
		`{"not": "an array"}`,
		`[]`,
		`[""]`, // empty strings filtered out → empty set → nil
	}
	for _, c := range cases {
		if got := parseScopes(c); got != nil && len(got) > 0 {
			t.Errorf("parseScopes(%q) = %v, want nil/empty", c, got)
		}
	}
}

// TestParseScopes_HappyPath
func TestParseScopes_HappyPath(t *testing.T) {
	set := parseScopes(`["agents:write", "credentials:read", "*"]`)
	if len(set) != 3 {
		t.Errorf("expected 3 scopes, got %d", len(set))
	}
	for _, s := range []string{"agents:write", "credentials:read", "*"} {
		if _, ok := set[s]; !ok {
			t.Errorf("missing scope %q in parsed set", s)
		}
	}
}

// TestReplyForbidden_WritesAuditLine guards against silently
// dropping the audit emission — operator chasing a 403 needs the
// "who tried what" trail.
func TestReplyForbidden_WritesAuditLine(t *testing.T) {
	rec := httptest.NewRecorder()
	calls := 0
	stub := stubWarnLogger{onWarn: func(msg string, args ...any) {
		calls++
		if msg != "rbac: access denied" {
			t.Errorf("warn message = %q, want 'rbac: access denied'", msg)
		}
	}}
	replyForbidden(rec, &stub, "user-x", "MEMBER", "agent.create", "workspace:ws1")
	if rec.Code != 403 {
		t.Errorf("status = %d, want 403", rec.Code)
	}
	if calls != 1 {
		t.Errorf("Warn call count = %d, want 1", calls)
	}
}

type stubWarnLogger struct {
	onWarn func(msg string, args ...any)
}

func (s *stubWarnLogger) Warn(msg string, args ...any) {
	if s.onWarn != nil {
		s.onWarn(msg, args...)
	}
}
