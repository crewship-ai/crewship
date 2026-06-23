package api

// Second coverage pass for agents_hire.go: the Hire DB-error ladder
// (parent-lead load, template load, policy resolve, agent insert), the
// inbox-write failure modes for both blocking (guided → 500 + compensating
// delete) and non-blocking (trusted → 201 with inbox dropped) decisions,
// the template-name fallback, and Rehire's policy/quota/update failures.

import (
	"database/sql"
	"net/http"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/policy"
)

// covHire2BrokenResolver returns a policy resolver bound to a closed DB so
// Resolve always errors (cache is empty on first call).
func covHire2BrokenResolver(t *testing.T) *policy.Resolver {
	t.Helper()
	broken, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	broken.Close()
	return policy.NewResolver(broken)
}

func covHire2Body(extra map[string]any) map[string]any {
	body := map[string]any{
		"crew_id":       "crew-hire-1",
		"template_slug": "docs-writer",
		"ttl_minutes":   30,
		"reason":        "cov2",
	}
	for k, v := range extra {
		body[k] = v
	}
	return body
}

func TestHire2_ParentLeadLoadDBError500(t *testing.T) {
	db := setupTestDB(t)
	userID, wsID, _ := seedHireCrew(t, db, "trusted", 5)
	h := newHireHandler(t, db)
	if _, err := db.Exec(`ALTER TABLE agents RENAME TO agents_hidden_h2`); err != nil {
		t.Fatalf("rename agents: %v", err)
	}
	t.Cleanup(func() { _, _ = db.Exec(`ALTER TABLE agents_hidden_h2 RENAME TO agents`) })

	rr := postHire(t, h, userID, wsID, "OWNER", covHire2Body(map[string]any{"parent_lead_id": "lead-x"}))
	if rr.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500; body=%s", rr.Code, rr.Body.String())
	}
}

func TestHire2_TemplateLoadDBError500(t *testing.T) {
	db := setupTestDB(t)
	userID, wsID, _ := seedHireCrew(t, db, "trusted", 5)
	h := newHireHandler(t, db)
	if _, err := db.Exec(`ALTER TABLE crew_templates RENAME TO ct_hidden_h2`); err != nil {
		t.Fatalf("rename crew_templates: %v", err)
	}
	t.Cleanup(func() { _, _ = db.Exec(`ALTER TABLE ct_hidden_h2 RENAME TO crew_templates`) })

	rr := postHire(t, h, userID, wsID, "OWNER", covHire2Body(nil))
	if rr.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500; body=%s", rr.Code, rr.Body.String())
	}
}

func TestHire2_PolicyResolveError500(t *testing.T) {
	db := setupTestDB(t)
	userID, wsID, _ := seedHireCrew(t, db, "trusted", 5)
	h := newHireHandler(t, db)
	h.SetPolicyResolver(covHire2BrokenResolver(t))

	rr := postHire(t, h, userID, wsID, "OWNER", covHire2Body(nil))
	if rr.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500; body=%s", rr.Code, rr.Body.String())
	}
}

func TestHire2_AgentInsertFails500(t *testing.T) {
	db := setupTestDB(t)
	userID, wsID, _ := seedHireCrew(t, db, "trusted", 5)
	h := newHireHandler(t, db)
	if _, err := db.Exec(`
		CREATE TRIGGER h2_block_ephemeral BEFORE INSERT ON agents
		WHEN NEW.ephemeral = 1
		BEGIN SELECT RAISE(ABORT, 'h2 no ephemerals'); END`); err != nil {
		t.Fatalf("create trigger: %v", err)
	}

	rr := postHire(t, h, userID, wsID, "OWNER", covHire2Body(nil))
	if rr.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500; body=%s", rr.Code, rr.Body.String())
	}
}

func TestHire2_BlockingInboxFailure_CompensatingDelete(t *testing.T) {
	db := setupTestDB(t)
	// guided + nil resolver default to DecisionInboxApprove (blocking).
	userID, wsID, _ := seedHireCrew(t, db, "guided", 5)
	h := newHireHandler(t, db)
	if _, err := db.Exec(`
		CREATE TRIGGER h2_block_inbox BEFORE INSERT ON inbox_items
		BEGIN SELECT RAISE(ABORT, 'h2 no inbox'); END`); err != nil {
		t.Fatalf("create trigger: %v", err)
	}

	rr := postHire(t, h, userID, wsID, "OWNER", covHire2Body(nil))
	if rr.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500; body=%s", rr.Code, rr.Body.String())
	}
	// The compensating delete must have removed the bricked agent row.
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM agents WHERE ephemeral = 1`).Scan(&n); err != nil {
		t.Fatalf("count: %v", err)
	}
	if n != 0 {
		t.Errorf("ephemeral agents = %d, want 0 after compensating delete", n)
	}
}

func TestHire2_NonBlockingInboxFailure_AgentStaysLive(t *testing.T) {
	db := setupTestDB(t)
	userID, wsID, _ := seedHireCrew(t, db, "trusted", 5)
	h := newHireHandler(t, db)
	h.SetPolicyResolver(policy.NewResolver(db)) // trusted → auto + non-blocking inbox
	if _, err := db.Exec(`
		CREATE TRIGGER h2_block_inbox_nb BEFORE INSERT ON inbox_items
		BEGIN SELECT RAISE(ABORT, 'h2 no inbox'); END`); err != nil {
		t.Fatalf("create trigger: %v", err)
	}

	rr := postHire(t, h, userID, wsID, "OWNER", covHire2Body(nil))
	if rr.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201; body=%s", rr.Code, rr.Body.String())
	}
	// Inbox row was dropped, agent still live.
	if strings.Contains(rr.Body.String(), `"inbox_id":"c`) {
		t.Errorf("inbox_id should be empty after failed non-blocking write: %s", rr.Body.String())
	}
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM agents WHERE ephemeral = 1 AND expired_at IS NULL`).Scan(&n); err != nil {
		t.Fatalf("count: %v", err)
	}
	if n != 1 {
		t.Errorf("live ephemerals = %d, want 1", n)
	}
}

func TestHire2_EmptyTemplateName_FallsBackToSlug(t *testing.T) {
	db := setupTestDB(t)
	userID, wsID, _ := seedHireCrew(t, db, "trusted", 5)
	if _, err := db.Exec(`INSERT INTO crew_templates (id, name, slug, agents_json, is_builtin)
		VALUES ('tmpl-h2', '', 'nameless-tmpl', '[]', 1)`); err != nil {
		t.Fatalf("seed template: %v", err)
	}
	h := newHireHandler(t, db)
	h.SetPolicyResolver(policy.NewResolver(db))

	rr := postHire(t, h, userID, wsID, "OWNER", covHire2Body(map[string]any{"template_slug": "nameless-tmpl"}))
	if rr.Code != http.StatusCreated {
		t.Fatalf("status = %d; body=%s", rr.Code, rr.Body.String())
	}
	var name string
	if err := db.QueryRow(`SELECT name FROM agents WHERE ephemeral = 1`).Scan(&name); err != nil {
		t.Fatalf("query agent: %v", err)
	}
	if name != "nameless-tmpl" {
		t.Errorf("name = %q, want template slug fallback", name)
	}
}

// ---- Rehire ----

func TestHire2_Rehire_PolicyResolveError500(t *testing.T) {
	db := setupTestDB(t)
	userID, wsID, crewID := seedHireCrew(t, db, "trusted", 5)
	h := newHireHandler(t, db)
	h.SetPolicyResolver(covHire2BrokenResolver(t))

	past := "2024-01-01T00:00:00Z"
	agentID := seedEphemeralAgent(t, db, wsID, crewID, "rehire-h2a", &past, &past, "r")
	rr := postRehire(t, h, userID, wsID, "OWNER", agentID, map[string]any{"ttl_minutes": 30, "reason": "cov2 rehire"})
	if rr.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500; body=%s", rr.Code, rr.Body.String())
	}
}

func TestHire2_Rehire_GhostQuotaExhausted429(t *testing.T) {
	db := setupTestDB(t)
	userID, wsID, crewID := seedHireCrew(t, db, "trusted", 1)
	h := newHireHandler(t, db)

	// One live ephemeral fills the quota; the ghost cannot come back.
	live := "2030-01-01T00:00:00Z"
	seedEphemeralAgent(t, db, wsID, crewID, "rehire-h2-live", &live, nil, "live")
	past := "2024-01-01T00:00:00Z"
	ghost := seedEphemeralAgent(t, db, wsID, crewID, "rehire-h2-ghost", &past, &past, "ghost")

	rr := postRehire(t, h, userID, wsID, "OWNER", ghost, map[string]any{"ttl_minutes": 30, "reason": "cov2 rehire"})
	if rr.Code != http.StatusTooManyRequests {
		t.Fatalf("status = %d, want 429; body=%s", rr.Code, rr.Body.String())
	}
	if !strings.Contains(rr.Body.String(), "Ephemeral quota reached") {
		t.Errorf("body = %q", rr.Body.String())
	}
}

func TestHire2_Rehire_QuotaLookupDBError500(t *testing.T) {
	db := setupTestDB(t)
	userID, wsID, crewID := seedHireCrew(t, db, "trusted", 5)
	h := newHireHandler(t, db)
	past := "2024-01-01T00:00:00Z"
	ghost := seedEphemeralAgent(t, db, wsID, crewID, "rehire-h2-q", &past, &past, "g")

	// Rehire only touches crews inside the quota branch; renaming it
	// leaves the earlier agent loads intact.
	if _, err := db.Exec(`ALTER TABLE crews RENAME TO crews_hidden_h2`); err != nil {
		t.Fatalf("rename crews: %v", err)
	}
	t.Cleanup(func() { _, _ = db.Exec(`ALTER TABLE crews_hidden_h2 RENAME TO crews`) })

	rr := postRehire(t, h, userID, wsID, "OWNER", ghost, map[string]any{"ttl_minutes": 30, "reason": "cov2 rehire"})
	if rr.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500; body=%s", rr.Code, rr.Body.String())
	}
}

func TestHire2_Rehire_UpdateFails500(t *testing.T) {
	db := setupTestDB(t)
	userID, wsID, crewID := seedHireCrew(t, db, "trusted", 5)
	h := newHireHandler(t, db)
	past := "2024-01-01T00:00:00Z"
	ghost := seedEphemeralAgent(t, db, wsID, crewID, "rehire-h2-u", &past, &past, "g")

	if _, err := db.Exec(`
		CREATE TRIGGER h2_block_rehire BEFORE UPDATE ON agents
		WHEN NEW.expired_at IS NULL AND OLD.expired_at IS NOT NULL
		BEGIN SELECT RAISE(ABORT, 'h2 no unghost'); END`); err != nil {
		t.Fatalf("create trigger: %v", err)
	}

	rr := postRehire(t, h, userID, wsID, "OWNER", ghost, map[string]any{"ttl_minutes": 30, "reason": "cov2 rehire"})
	if rr.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500; body=%s", rr.Code, rr.Body.String())
	}
}
