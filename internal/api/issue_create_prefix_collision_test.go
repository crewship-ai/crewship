package api

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
)

// issue_create_prefix_collision_test.go — #1797.
//
// Issue identifiers are `<prefix>-<n>`. The prefix is crews.issue_prefix, or,
// when that is empty, the first three letters of the crew slug upper-cased. The
// NUMBER came from issue_counters, which was keyed by crew_id — so every crew
// counted from 1 privately, while the namespace those identifiers land in is
// UNIQUE(workspace_id, identifier) (idx_mission_workspace_identifier, #1733).
// Two crews in one workspace whose effective prefix collides therefore both
// mint ENG-1.
//
// `engineering` and `engine` is the collision that arrives without anybody
// typing a prefix at all: both derive ENG.
//
// The bug is not a one-off 500. The counter upsert and the mission INSERT share
// ONE transaction, and the handler returns on the failed insert without
// committing — so the counter increment rolls back with it. The losing crew's
// next_number never advances, it retries the identical identifier on the next
// create, and 500s again, forever. TestIssuePrefixCollision_SecondCrewIsNotWedged
// asserts the REPEAT, which is what a "retry the insert once" fix would still
// fail.

// seedPrefixCollisionWorkspace builds one workspace holding two crews whose
// effective prefixes collide by slug derivation alone — no issue_prefix set on
// either. Returns (userID, wsID, crewA, crewB).
func seedPrefixCollisionWorkspace(t *testing.T, db *sql.DB) (string, string, string, string) {
	t.Helper()
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)

	crews := []struct{ id, name, slug string }{
		{"crew-prefix-a", "Engineering", "engineering"},
		{"crew-prefix-b", "Engine", "engine"},
	}
	for _, c := range crews {
		if _, err := db.ExecContext(context.Background(),
			`INSERT INTO crews (id, workspace_id, name, slug) VALUES (?, ?, ?, ?)`,
			c.id, wsID, c.name, c.slug); err != nil {
			t.Fatalf("insert crew %s: %v", c.slug, err)
		}
		if _, err := db.ExecContext(context.Background(),
			`INSERT INTO agents (id, workspace_id, crew_id, name, slug, agent_role, status, cli_adapter, temperature, timeout_seconds, tool_profile, memory_enabled)
			 VALUES (?, ?, ?, 'Lead', ?, 'LEAD', 'IDLE', 'CLAUDE_CODE', 0.7, 1800, 'CODING', 0)`,
			"agent-prefix-lead-"+c.slug, wsID, c.id, "lead-"+c.slug); err != nil {
			t.Fatalf("insert lead for %s: %v", c.slug, err)
		}
	}
	return userID, wsID, crews[0].id, crews[1].id
}

// createIssueVia posts one issue through the REST handler and returns the
// recorder plus the identifier it minted (empty when the create failed).
func createIssueVia(t *testing.T, h *IssueHandler, userID, wsID, crewID, title string) (*httptest.ResponseRecorder, string) {
	t.Helper()
	body, err := json.Marshal(map[string]string{"title": title})
	if err != nil {
		t.Fatalf("marshal body: %v", err)
	}
	req := httptest.NewRequest("POST", "/api/v1/crews/"+crewID+"/issues", bytes.NewReader(body))
	req.SetPathValue("crewId", crewID)
	ctx := withUser(req.Context(), &AuthUser{ID: userID})
	ctx = withWorkspace(ctx, wsID, "OWNER")
	req = req.WithContext(ctx)
	rr := httptest.NewRecorder()
	h.Create(rr, req)

	if rr.Code != http.StatusCreated {
		return rr, ""
	}
	var resp struct {
		Identifier *string `json:"identifier"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode create response: %v", err)
	}
	if resp.Identifier == nil {
		t.Fatalf("create returned no identifier: %s", rr.Body.String())
	}
	return rr, *resp.Identifier
}

// TestIssuePrefixCollision_SecondCrewIsNotWedged is the bug. `engineering`
// creates ENG-1; `engine` must then be able to create an issue at all — and
// must still be able to on the attempt AFTER that, which is the part that
// distinguishes a real fix from one that merely retries once.
func TestIssuePrefixCollision_SecondCrewIsNotWedged(t *testing.T) {
	db := setupTestDB(t)
	userID, wsID, crewA, crewB := seedPrefixCollisionWorkspace(t, db)
	h := NewIssueHandler(db, nil, nil, newTestLogger())

	rrA, identA := createIssueVia(t, h, userID, wsID, crewA, "first from engineering")
	if rrA.Code != http.StatusCreated {
		t.Fatalf("engineering's first issue failed: %d %s", rrA.Code, rrA.Body.String())
	}

	// Both of engine's attempts run before anything is asserted: a Fatalf on the
	// first would hide the evidence that the SECOND fails too, and the repeat is
	// the whole point — it is what makes this permanent rather than transient.
	rrB1, identB1 := createIssueVia(t, h, userID, wsID, crewB, "first from engine")
	rrB2, identB2 := createIssueVia(t, h, userID, wsID, crewB, "second from engine")

	if rrB1.Code != http.StatusCreated {
		t.Errorf("engine's first issue returned %d (%s)\n"+
			"`engineering` and `engine` both derive the prefix ENG, and the counter was keyed "+
			"per crew, so both minted %s and the second insert hit "+
			"UNIQUE(workspace_id, identifier) — surfaced as a bare 500 (#1797)",
			rrB1.Code, rrB1.Body.String(), identA)
	}
	// The wedge. The failed insert rolled its own transaction back, taking the
	// counter increment with it, so the retry asks for the SAME number again.
	if rrB2.Code != http.StatusCreated {
		t.Fatalf("engine's SECOND attempt returned %d (%s)\n"+
			"this is the permanent half of #1797: the counter upsert shares the "+
			"transaction with the mission insert, so a rejected insert rolls the "+
			"increment back and every subsequent create retries the identical "+
			"identifier — the crew can never create an issue again",
			rrB2.Code, rrB2.Body.String())
	}

	if identB1 == identA {
		t.Errorf("engine minted %q, the identifier engineering already holds", identB1)
	}
	if identB2 == identB1 {
		t.Errorf("engine minted %q twice — the counter did not advance", identB2)
	}

	var n int
	if err := db.QueryRow(
		`SELECT COUNT(*) FROM missions WHERE workspace_id = ? AND mission_type = 'issue'`,
		wsID).Scan(&n); err != nil {
		t.Fatalf("count issues: %v", err)
	}
	if n != 3 {
		t.Errorf("workspace holds %d issues, want 3", n)
	}
}

// TestIssuePrefixCollision_SeedsAboveExistingIdentifiers is the residual hazard
// the re-key opens: the counter is now keyed on the prefix, and issue_prefix is
// mutable, so a crew that changes its prefix opens a NEW counter row. Starting
// that row at 1 walks straight back into the bug when identifiers under that
// prefix already exist — which they do whenever the prefix was in use before, or
// whenever a restore brought missions back without their counter (the shape
// #1973 produced for real). The allocator seeds from the high-water mark.
func TestIssuePrefixCollision_SeedsAboveExistingIdentifiers(t *testing.T) {
	db := setupTestDB(t)
	userID, wsID, crewA, _ := seedPrefixCollisionWorkspace(t, db)
	h := NewIssueHandler(db, nil, nil, newTestLogger())

	// Identifiers in the ground with no counter row behind them.
	for i := 1; i <= 7; i++ {
		seedIssue(t, db, wsID, crewA, "agent-prefix-lead-engineering", "ENG-"+strconv.Itoa(i), "BACKLOG")
	}
	var counters int
	if err := db.QueryRow(`SELECT COUNT(*) FROM issue_counters WHERE workspace_id = ?`, wsID).Scan(&counters); err != nil {
		t.Fatalf("count counters: %v", err)
	}
	if counters != 0 {
		t.Fatalf("fixture seeded %d counter rows; it is meant to have none", counters)
	}

	rr, identifier := createIssueVia(t, h, userID, wsID, crewA, "after a restore")
	if rr.Code != http.StatusCreated {
		t.Fatalf("create returned %d (%s)\nthe first create for a prefix must start above the "+
			"identifiers that prefix already holds, not at 1", rr.Code, rr.Body.String())
	}
	if identifier != "ENG-8" {
		t.Errorf("identifier = %q, want ENG-8 — the counter was seeded from 1 instead of from the "+
			"highest ENG number already minted", identifier)
	}
}

// TestIssuePrefixCollision_AgentPathSharesTheCounter covers the OTHER generator.
// insertIssueTx (agent-tool and recurring-issue creates) had its own copy of the
// prefix derivation and its own copy of the counter upsert; a fix applied to one
// path only would leave the other minting duplicates against it.
func TestIssuePrefixCollision_AgentPathSharesTheCounter(t *testing.T) {
	db := setupTestDB(t)
	userID, wsID, crewA, crewB := seedPrefixCollisionWorkspace(t, db)
	h := NewIssueHandler(db, nil, nil, newTestLogger())

	if rr, _ := createIssueVia(t, h, userID, wsID, crewA, "REST create"); rr.Code != http.StatusCreated {
		t.Fatalf("REST create failed: %d %s", rr.Code, rr.Body.String())
	}

	ctx := context.Background()
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	defer func() { _ = tx.Rollback() }()

	_, identifier, err := insertIssueTx(ctx, tx, newTestLogger(), issueSpec{
		WorkspaceID: wsID,
		CrewID:      crewB,
		Title:       "agent create",
		AuthoredVia: "agent_tool_call",
	})
	if err != nil {
		t.Fatalf("insertIssueTx for the colliding crew failed: %v\n"+
			"the agent path must draw from the same (workspace, prefix) sequence as the "+
			"REST path, or the two generators mint the same identifier (#1797)", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("commit: %v", err)
	}
	if identifier == "ENG-1" {
		t.Errorf("agent path minted %q, which the REST path already used", identifier)
	}
}
