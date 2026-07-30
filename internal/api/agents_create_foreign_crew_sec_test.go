package api

// Security regression tests for a Create/Update asymmetry: agents_update.go
// validates that a PATCHed crew_id belongs to the caller's workspace
// (crewExists, see TestSecAgentCrewIDForeignWorkspace in
// agents_security_sec_test.go), but agents_create.go never did the same
// check. Create only computes an *effective role* from CrewRoleFromDB,
// which returns "" when the caller isn't a member of the crew's workspace
// — and "" is intentionally treated as "fall back to the caller's real
// workspace role" so it can't outrank a legitimate role. That fallback is
// correct on its own, but nothing then confirms the crew actually belongs
// to the caller's workspace: an OWNER of workspace A can pass a crew_id
// from workspace B and canRole(effective, "create") still passes, because
// effective just became the caller's OWNER role from ws A. req.CrewID is
// then used verbatim in the license check, the LEAD-per-crew uniqueness
// query, and the INSERT — writing a row where workspace_id (A) and
// crew_id (belongs to B) disagree.
//
// TestSecAgentCreateCrewIDForeignWorkspace400 and its LEAD variant pin the
// fix. TestSecAgentCreateCrewIDSameWorkspace is the positive control: a
// crew_id in the caller's own workspace must keep working.

import (
	"net/http"
	"testing"
)

// TestSecAgentCreateCrewIDForeignWorkspace400 — seed a crew in workspace B,
// then Create an agent as OWNER of workspace A with that crew_id. Before
// the fix: CrewRoleFromDB returns "" (caller isn't a member of B), effective
// falls back to the caller's ws-A OWNER role, canRole passes, and the
// handler proceeds to INSERT a cross-tenant row (workspace_id=A,
// crew_id=<B's crew>). Must be rejected with 400, matching the "Invalid
// crew_id" contract agents_update.go already enforces via crewExists.
func TestSecAgentCreateCrewIDForeignWorkspace400(t *testing.T) {
	h := NewAgentHandler(setupTestDB(t), newTestLogger())
	userID := seedTestUser(t, h.db)
	wsA := seedTestWorkspace(t, h.db, userID)

	wsB := "ws-foreign-create-sec"
	if _, err := h.db.Exec(`INSERT INTO workspaces (id, name, slug) VALUES (?, 'Foreign Create', 'foreign-create-sec')`, wsB); err != nil {
		t.Fatalf("seed ws B: %v", err)
	}
	foreignCrew := seedCrewRow(t, h.db, "crew-b-create", wsB, "B Crew", "b-crew-create")

	rr := secAgentCreate(t, h, userID, wsA, "OWNER",
		`{"name":"Cross Tenant","slug":"cross-tenant","crew_id":"`+foreignCrew+`"}`)

	if rr.Code != http.StatusBadRequest {
		t.Fatalf("cross-workspace crew_id Create = %d, want 400; body: %s", rr.Code, rr.Body.String())
	}

	// No agent row must have been written under either workspace.
	var count int
	if err := h.db.QueryRow(`SELECT COUNT(*) FROM agents WHERE slug = 'cross-tenant'`).Scan(&count); err != nil {
		t.Fatalf("count agents: %v", err)
	}
	if count != 0 {
		t.Fatalf("cross-workspace crew_id Create wrote %d agent row(s), want 0", count)
	}
}

// TestSecAgentCreateCrewIDForeignWorkspaceLead400 — same cross-tenant
// crew_id, but agent_role: LEAD. The LEAD branch (agents_create.go ~L132)
// runs its own DB query keyed only on crew_id, independent of the RBAC
// fallback — it must not be reachable with a foreign crew_id either.
func TestSecAgentCreateCrewIDForeignWorkspaceLead400(t *testing.T) {
	h := NewAgentHandler(setupTestDB(t), newTestLogger())
	userID := seedTestUser(t, h.db)
	wsA := seedTestWorkspace(t, h.db, userID)

	wsB := "ws-foreign-create-lead-sec"
	if _, err := h.db.Exec(`INSERT INTO workspaces (id, name, slug) VALUES (?, 'Foreign Create Lead', 'foreign-create-lead-sec')`, wsB); err != nil {
		t.Fatalf("seed ws B: %v", err)
	}
	foreignCrew := seedCrewRow(t, h.db, "crew-b-create-lead", wsB, "B Crew Lead", "b-crew-create-lead")

	rr := secAgentCreate(t, h, userID, wsA, "OWNER",
		`{"name":"Cross Tenant Lead","slug":"cross-tenant-lead","agent_role":"LEAD","crew_id":"`+foreignCrew+`"}`)

	if rr.Code != http.StatusBadRequest {
		t.Fatalf("cross-workspace crew_id LEAD Create = %d, want 400; body: %s", rr.Code, rr.Body.String())
	}

	var count int
	if err := h.db.QueryRow(`SELECT COUNT(*) FROM agents WHERE slug = 'cross-tenant-lead'`).Scan(&count); err != nil {
		t.Fatalf("count agents: %v", err)
	}
	if count != 0 {
		t.Fatalf("cross-workspace crew_id LEAD Create wrote %d agent row(s), want 0", count)
	}
}

// TestSecAgentCreateCrewIDSameWorkspace — positive control for the fix
// above: a crew_id that DOES belong to the caller's own workspace must
// keep working. Without this, a fix that's too aggressive (e.g. rejecting
// every crew_id) would pass the negative tests while breaking the
// legitimate case.
func TestSecAgentCreateCrewIDSameWorkspace(t *testing.T) {
	h := NewAgentHandler(setupTestDB(t), newTestLogger())
	userID := seedTestUser(t, h.db)
	wsID := seedTestWorkspace(t, h.db, userID)
	crewID := seedCrewRow(t, h.db, "crew-ok-create", wsID, "OK Crew", "ok-crew-create")

	rr := secAgentCreate(t, h, userID, wsID, "OWNER",
		`{"name":"Same Workspace","slug":"same-workspace","crew_id":"`+crewID+`"}`)

	if rr.Code != http.StatusCreated {
		t.Fatalf("same-workspace crew_id Create = %d, want 201; body: %s", rr.Code, rr.Body.String())
	}
}
