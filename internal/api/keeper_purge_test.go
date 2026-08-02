package api

import (
	"database/sql"
	"net/http"
	"net/http/httptest"
	"testing"
)

// `crewship seed --nuke` says it deletes all workspace contents, and it left
// keeper_requests behind. 115 rows survived a nuke on dev2.
//
// That is not untidiness. Those rows carry `intent` — agent-authored free text —
// and `ollama_prompt`, which is the conversation history the judge was shown. A
// workspace wipe that leaves the full record of what every agent asked for, and
// the conversations around it, has not done what it promised.
//
// It falls through both nets by construction: keeper_requests has NO
// workspace_id (the scope comes from the requesting agent), and its
// `requesting_agent_id REFERENCES agents(id)` carries no ON DELETE CASCADE — so
// deleting the agents orphans the rows rather than removing them. The ledger has
// a workspace_id with a cascade, but that cascade fires on deleting the
// WORKSPACE, and a contents-nuke keeps the workspace.
//
// Ordering matters the same way escalations and crew runtimes already do in
// nukeAll: this has to run while the agents still exist, because they are the
// only route from a workspace to its keeper requests.

func TestKeeperPurge_RefusesARoleThatCannotManage(t *testing.T) {
	db := setupTestDB(t)
	h := NewKeeperHandler(db, "tok", nil, newTestLogger())

	for _, role := range []string{"MANAGER", "MEMBER", "VIEWER", ""} {
		rr := httptest.NewRecorder()
		h.HandlePurge(rr, resolveReq(t, "ws1", role, "u1", "", nil))
		if rr.Code != http.StatusForbidden {
			t.Errorf("role %q got %d, want 403 — this erases the decision history", role, rr.Code)
		}
	}
}

func TestKeeperPurge_RequiresAWorkspace(t *testing.T) {
	db := setupTestDB(t)
	h := NewKeeperHandler(db, "tok", nil, newTestLogger())

	rr := httptest.NewRecorder()
	h.HandlePurge(rr, resolveReq(t, "", "ADMIN", "u1", "", nil))
	if rr.Code != http.StatusBadRequest {
		t.Errorf("got %d, want 400 without a workspace — an unscoped purge is every tenant's", rr.Code)
	}
}

// The whole point: the requests go, and so does the ledger beside them. Leaving
// the ledger would keep the intents and the reasons, which is most of what the
// wipe was for.
func TestKeeperPurge_RemovesTheRequestsAndTheirLedger(t *testing.T) {
	db := setupTestDB(t)
	wsID, crewID, agentID, credID := seedKeeperFixture(t, db)
	h := newKeeperHandler(t, db)

	execOrFatal(t, db, `
		INSERT INTO keeper_requests
			(id, request_type, requesting_agent_id, requesting_crew_id, credential_id, intent, decision, ollama_prompt)
		VALUES ('kr-a', 'access', ?, ?, ?, 'rotate the certs', 'ALLOW', 'PROMPT'),
		       ('kr-b', 'execute', ?, ?, ?, 'run the migration', 'DENY', 'PROMPT')`,
		agentID, crewID, credID, agentID, crewID, credID)
	execOrFatal(t, db, `
		INSERT INTO keeper_request_events (id, request_id, workspace_id, seq, state, actor_type, recorded_at)
		VALUES ('ev-a', 'kr-a', ?, 1, 'ALLOW', 'keeper', '2026-01-01T00:00:00.000000000Z'),
		       ('ev-b', 'kr-b', ?, 1, 'DENY',  'keeper', '2026-01-01T00:00:01.000000000Z')`,
		wsID, wsID)

	rr := httptest.NewRecorder()
	h.HandlePurge(rr, resolveReq(t, wsID, "ADMIN", "u1", "", nil))
	if rr.Code != http.StatusOK {
		t.Fatalf("purge returned %d: %s", rr.Code, rr.Body.String())
	}

	if n := purgeCount(t, db, `SELECT COUNT(*) FROM keeper_requests`); n != 0 {
		t.Errorf("%d keeper_requests survived the wipe, each carrying an intent and a prompt", n)
	}
	if n := purgeCount(t, db, `SELECT COUNT(*) FROM keeper_request_events`); n != 0 {
		t.Errorf("%d ledger rows survived; they carry the same intents and reasons", n)
	}
}

// A request whose agent is already gone — the shape dev2 was in, from nukes that
// predate this endpoint — is still reachable through its ledger row, which does
// carry a workspace_id. Anything with neither an agent nor a ledger row is
// unreachable from a workspace and this says so rather than pretending.
func TestKeeperPurge_ReachesOrphansThroughTheLedger(t *testing.T) {
	db := setupTestDB(t)
	wsID, _, _, _ := seedKeeperFixture(t, db)
	h := newKeeperHandler(t, db)

	// No requesting_agent_id at all: the agent was deleted by an earlier nuke.
	execOrFatal(t, db, `
		INSERT INTO keeper_requests (id, request_type, intent, decision, ollama_prompt)
		VALUES ('kr-orphan', 'access', 'a stale intent nobody can trace', 'DENY', 'PROMPT')`)
	execOrFatal(t, db, `
		INSERT INTO keeper_request_events (id, request_id, workspace_id, seq, state, actor_type, recorded_at)
		VALUES ('ev-orphan', 'kr-orphan', ?, 1, 'DENY', 'keeper', '2026-01-01T00:00:00.000000000Z')`, wsID)

	rr := httptest.NewRecorder()
	h.HandlePurge(rr, resolveReq(t, wsID, "ADMIN", "u1", "", nil))
	if rr.Code != http.StatusOK {
		t.Fatalf("purge returned %d: %s", rr.Code, rr.Body.String())
	}

	if n := purgeCount(t, db, `SELECT COUNT(*) FROM keeper_requests WHERE id = 'kr-orphan'`); n != 0 {
		t.Error("the orphaned request survived; its ledger row named the workspace it belonged to")
	}
}

// Scoping. keeper_requests has no workspace of its own, so this endpoint derives
// one from two places at once — and a purge that reached past its tenant would
// be the worst possible bug in a delete route.
func TestKeeperPurge_DoesNotCrossTenants(t *testing.T) {
	db := setupTestDB(t)
	wsID, crewID, agentID, credID := seedKeeperFixture(t, db)
	h := newKeeperHandler(t, db)

	execOrFatal(t, db, `
		INSERT INTO keeper_requests
			(id, request_type, requesting_agent_id, requesting_crew_id, credential_id, intent, decision, ollama_prompt)
		VALUES ('kr-mine', 'access', ?, ?, ?, 'mine', 'ALLOW', 'PROMPT')`,
		agentID, crewID, credID)

	// Another tenant, with its own agent and its own request + ledger row.
	execOrFatal(t, db, `INSERT INTO users (id, email, full_name) VALUES ('xt-u', 'xt@ex.com', 'XT')`)
	execOrFatal(t, db, `INSERT INTO workspaces (id, name, slug) VALUES ('xt-ws', 'Other', 'other')`)
	execOrFatal(t, db, `INSERT INTO crews (id, workspace_id, name, slug) VALUES ('xt-c', 'xt-ws', 'C', 'c')`)
	execOrFatal(t, db, `INSERT INTO agents (id, workspace_id, crew_id, name, slug) VALUES ('xt-a', 'xt-ws', 'xt-c', 'A', 'a')`)
	execOrFatal(t, db, `
		INSERT INTO keeper_requests (id, request_type, requesting_agent_id, requesting_crew_id, intent, decision, ollama_prompt)
		VALUES ('kr-theirs', 'access', 'xt-a', 'xt-c', 'theirs', 'ALLOW', 'PROMPT')`)
	execOrFatal(t, db, `
		INSERT INTO keeper_request_events (id, request_id, workspace_id, seq, state, actor_type, recorded_at)
		VALUES ('ev-theirs', 'kr-theirs', 'xt-ws', 1, 'ALLOW', 'keeper', '2026-01-01T00:00:00.000000000Z')`)

	rr := httptest.NewRecorder()
	h.HandlePurge(rr, resolveReq(t, wsID, "ADMIN", "u1", "", nil))
	if rr.Code != http.StatusOK {
		t.Fatalf("purge returned %d: %s", rr.Code, rr.Body.String())
	}

	if n := purgeCount(t, db, `SELECT COUNT(*) FROM keeper_requests WHERE id = 'kr-theirs'`); n != 1 {
		t.Fatal("the purge deleted another tenant's keeper request")
	}
	if n := purgeCount(t, db, `SELECT COUNT(*) FROM keeper_request_events WHERE id = 'ev-theirs'`); n != 1 {
		t.Fatal("the purge deleted another tenant's ledger row")
	}
	if n := purgeCount(t, db, `SELECT COUNT(*) FROM keeper_requests WHERE id = 'kr-mine'`); n != 0 {
		t.Error("...and missed its own")
	}
}

func purgeCount(t *testing.T, db *sql.DB, q string) int {
	t.Helper()
	var n int
	if err := db.QueryRow(q).Scan(&n); err != nil {
		t.Fatalf("count %.50q: %v", q, err)
	}
	return n
}
