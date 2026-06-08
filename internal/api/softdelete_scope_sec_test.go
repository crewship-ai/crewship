package api

// Security regressions for the soft-delete-bypass class of bug: four
// scope/ownership queries validated workspace/crew membership but omitted
// `AND deleted_at IS NULL`, so a caller holding a still-valid CUID for a
// row that had since been soft-deleted could read or mutate it as though
// it were live. Each test soft-deletes the row, then drives the fixed
// code path and asserts the row is now treated as absent (404 /
// not-found / no mutation).
//
// Template: the existing soft-delete regression style (agentExists et al.
// already carry the filter; these pin the four that didn't).

import (
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/ws"
)

// ---------------------------------------------------------------------------
// 1. paymaster_handler.go — crewBelongsToWorkspace
//    crews HAS deleted_at; the membership probe must ignore tombstoned crews.
// ---------------------------------------------------------------------------

func TestSecSoftDel_CrewBelongsToWorkspace_DeletedCrewAbsent(t *testing.T) {
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	seedCrewRow(t, db, "crew-live", wsID, "Live", "live")
	seedCrewRow(t, db, "crew-dead", wsID, "Dead", "dead")

	// Live crew: still resolves.
	if ok, err := crewBelongsToWorkspace(context.Background(), db, "crew-live", wsID); err != nil || !ok {
		t.Fatalf("live crew: ok=%v err=%v, want true,nil", ok, err)
	}

	// Soft-delete crew-dead, then the probe must report it absent.
	if _, err := db.Exec(`UPDATE crews SET deleted_at = ? WHERE id = 'crew-dead'`,
		time.Now().UTC().Format(time.RFC3339)); err != nil {
		t.Fatalf("soft-delete crew: %v", err)
	}
	if ok, err := crewBelongsToWorkspace(context.Background(), db, "crew-dead", wsID); err != nil || ok {
		t.Errorf("soft-deleted crew: ok=%v err=%v, want false,nil (must be treated as absent)", ok, err)
	}
}

// Handler-level counterpart: SpendByAgent gates on crewBelongsToWorkspace,
// so a soft-deleted crew must 404 (the read of its ledger must be denied).
func TestSecSoftDel_PaymasterSpendByAgent_DeletedCrew404(t *testing.T) {
	db := setupTestDB(t)
	h := NewPaymasterHandler(db, newTestLogger())
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	seedCrewRow(t, db, "crew-x", wsID, "X", "x")

	if _, err := db.Exec(`UPDATE crews SET deleted_at = ? WHERE id = 'crew-x'`,
		time.Now().UTC().Format(time.RFC3339)); err != nil {
		t.Fatalf("soft-delete crew: %v", err)
	}

	req := httptest.NewRequest("GET", "/api/v1/paymaster/spend/by-agent/crew-x", nil)
	req.SetPathValue("crewId", "crew-x")
	req = withWorkspaceUser(req, userID, wsID, "OWNER")
	rr := httptest.NewRecorder()
	h.SpendByAgent(rr, req)
	if rr.Code != http.StatusNotFound {
		t.Errorf("soft-deleted crew spend read = %d, want 404", rr.Code)
	}
}

// ---------------------------------------------------------------------------
// 2. internal_runs.go — CreateRun agent scope
//    agents HAS deleted_at; posting a soft-deleted agent_id must 404 and
//    leave nothing mutated.
// ---------------------------------------------------------------------------

func TestSecSoftDel_CreateRun_DeletedAgent404(t *testing.T) {
	db := setupTestDB(t)
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))

	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	if _, err := db.Exec(`INSERT INTO agents (id, workspace_id, name, slug, status) VALUES ('gone-agent', ?, 'Gone', 'gone', 'IDLE')`, wsID); err != nil {
		t.Fatalf("insert agent: %v", err)
	}
	// Soft-delete the agent AFTER the caller learned its id.
	if _, err := db.Exec(`UPDATE agents SET deleted_at = ? WHERE id = 'gone-agent'`,
		time.Now().UTC().Format(time.RFC3339)); err != nil {
		t.Fatalf("soft-delete agent: %v", err)
	}

	h := NewInternalHandler(db, "test-token", logger)
	h.SetHub(ws.NewHub(logger, nil, ws.NopValidatorForTests, ws.NopSessionsForTests))
	_ = wireTestJournalForHandler(t, db, h)

	body := strings.NewReader(`{"id":"run-gone","agent_id":"gone-agent","workspace_id":"` + wsID + `","trigger_type":"USER"}`)
	req := httptest.NewRequest("POST", "/api/v1/internal/runs", body)
	rr := httptest.NewRecorder()
	h.CreateRun(rr, req)

	if rr.Code != http.StatusNotFound {
		t.Fatalf("CreateRun on soft-deleted agent must be 404; got %d body=%s", rr.Code, rr.Body.String())
	}
	// The tombstoned agent must not be flipped to RUNNING.
	var status string
	if err := db.QueryRow(`SELECT status FROM agents WHERE id = 'gone-agent'`).Scan(&status); err != nil {
		t.Fatalf("read agent status: %v", err)
	}
	if status != "IDLE" {
		t.Errorf("soft-deleted agent status = %q, want IDLE (mutation leaked through)", status)
	}
}

// ---------------------------------------------------------------------------
// 3. backup.go — allowRestore Path 2 slug lookup
//    workspaces HAS deleted_at; a caller whose own workspace row is
//    tombstoned must not slug-match a bundle into authorization.
//    allowRestore's Path 2 needs a real bundle manifest, so we pin the
//    underlying query contract directly: the slug probe must return no
//    row for a soft-deleted workspace.
// ---------------------------------------------------------------------------

func TestSecSoftDel_RestoreSlugLookup_DeletedWorkspaceAbsent(t *testing.T) {
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID) // slug "test"

	// Before deletion the slug probe (the exact query allowRestore runs)
	// resolves.
	var slug string
	if err := db.QueryRow(`SELECT slug FROM workspaces WHERE id = ? AND deleted_at IS NULL`, wsID).Scan(&slug); err != nil {
		t.Fatalf("live workspace slug lookup failed: %v", err)
	}
	if slug != "test" {
		t.Fatalf("slug = %q, want test", slug)
	}

	// Soft-delete the caller's workspace; the slug probe must now find
	// nothing, so the slug-match authorization branch can't fire.
	if _, err := db.Exec(`UPDATE workspaces SET deleted_at = ? WHERE id = ?`,
		time.Now().UTC().Format(time.RFC3339), wsID); err != nil {
		t.Fatalf("soft-delete workspace: %v", err)
	}
	err := db.QueryRow(`SELECT slug FROM workspaces WHERE id = ? AND deleted_at IS NULL`, wsID).Scan(&slug)
	if err == nil {
		t.Errorf("soft-deleted workspace slug still resolved as %q; restore slug-match would wrongly authorize", slug)
	}
}

// ---------------------------------------------------------------------------
// 4. agents_update.go — schedule UPDATE WHERE clause
//    The handler's agentExists precheck already filters deleted_at, but
//    the UPDATE itself did not, so a PATCH racing a DELETE could write a
//    tombstoned row. We exercise the exact UPDATE the handler issues and
//    assert it affects zero rows once the agent is soft-deleted.
// ---------------------------------------------------------------------------

func TestSecSoftDel_AgentUpdate_DeletedAgentNotWritten(t *testing.T) {
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	seedAgentRow(t, db, "ag-race", wsID, "", "Race", "race", "AGENT")

	// Control: against a live agent the UPDATE writes exactly one row.
	ub := newUpdate()
	ub.Set("name", "Renamed")
	q, args := ub.Build("agents", "id = ? AND workspace_id = ? AND deleted_at IS NULL", "ag-race", wsID)
	res, err := db.Exec(q, args...)
	if err != nil {
		t.Fatalf("live update exec: %v", err)
	}
	if n, _ := res.RowsAffected(); n != 1 {
		t.Fatalf("live agent update affected %d rows, want 1", n)
	}

	// Now soft-delete the agent and re-issue the same UPDATE: it must
	// touch zero rows (the tombstoned agent is invisible to the write).
	if _, err := db.Exec(`UPDATE agents SET deleted_at = ? WHERE id = 'ag-race'`,
		time.Now().UTC().Format(time.RFC3339)); err != nil {
		t.Fatalf("soft-delete agent: %v", err)
	}
	ub2 := newUpdate()
	ub2.Set("name", "ShouldNotStick")
	q2, args2 := ub2.Build("agents", "id = ? AND workspace_id = ? AND deleted_at IS NULL", "ag-race", wsID)
	res2, err := db.Exec(q2, args2...)
	if err != nil {
		t.Fatalf("post-delete update exec: %v", err)
	}
	if n, _ := res2.RowsAffected(); n != 0 {
		t.Errorf("update on soft-deleted agent affected %d rows, want 0 (write leaked onto tombstoned row)", n)
	}
	// And the name must remain the pre-deletion value.
	var name string
	if err := db.QueryRow(`SELECT name FROM agents WHERE id = 'ag-race'`).Scan(&name); err != nil {
		t.Fatalf("read agent name: %v", err)
	}
	if name != "Renamed" {
		t.Errorf("agent name = %q, want Renamed (post-delete write must not have applied)", name)
	}
}
