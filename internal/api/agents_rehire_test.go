package api

// Tests for the PR-D F5 Rehire endpoint + the live-first list ordering
// it depends on. Rehire is the "resurrect a ghost" half of the
// ephemeral lifecycle — the list order change is what makes those
// ghosts visible in operator UIs without scrolling past every active
// agent first.

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"log/slog"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"
)

// seedEphemeralAgent inserts a row matching what Hire would have
// produced. Returns the agent id so tests can target it.
func seedEphemeralAgent(t *testing.T, db *sql.DB, wsID, crewID, slug string, expiresAt, expiredAt *string, hireReason string) string {
	t.Helper()
	id := "agent-" + slug
	now := time.Now().UTC().Format(time.RFC3339)
	_, err := db.Exec(`
		INSERT INTO agents (id, crew_id, workspace_id, name, slug, agent_role, status,
		    cli_adapter, tool_profile, memory_enabled,
		    ephemeral, expires_at, expired_at, hire_reason, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, 'AGENT', 'IDLE',
		        'CLAUDE_CODE', 'CODING', 1,
		        1, ?, ?, ?, ?, ?)`,
		id, crewID, wsID, slug, slug, expiresAt, expiredAt, hireReason, now, now)
	if err != nil {
		t.Fatalf("seed ephemeral %s: %v", slug, err)
	}
	return id
}

func postRehire(t *testing.T, h *AgentHandler, userID, wsID, role, agentID string, body any) *httptest.ResponseRecorder {
	t.Helper()
	b, _ := json.Marshal(body)
	req := httptest.NewRequest("POST", "/api/v1/agents/"+agentID+"/rehire", bytes.NewReader(b))
	req.SetPathValue("agentId", agentID)
	req.Header.Set("Content-Type", "application/json")
	ctx := withUser(req.Context(), &AuthUser{ID: userID})
	ctx = withWorkspace(ctx, wsID, role)
	req = req.WithContext(ctx)
	rr := httptest.NewRecorder()
	h.Rehire(rr, req)
	return rr
}

func TestRehire_GhostResetsExpiresAtAndClearsExpiredAt(t *testing.T) {
	db := setupTestDB(t)
	userID, wsID, crewID := seedHireCrew(t, db, "trusted", 5)
	h := newHireHandler(t, db)

	past := "2024-01-01T00:00:00Z"
	agentID := seedEphemeralAgent(t, db, wsID, crewID, "docs-eph-aaaaaa", &past, &past, "[2024-01-01T00:00:00Z] hire: first")

	rr := postRehire(t, h, userID, wsID, "MANAGER", agentID, map[string]any{
		"ttl_minutes": 90,
		"reason":      "second pass",
	})
	if rr.Code != 200 {
		t.Fatalf("status = %d, want 200; body: %s", rr.Code, rr.Body.String())
	}

	var expiresAt sql.NullString
	var expiredAt sql.NullString
	var reason sql.NullString
	err := db.QueryRow(`SELECT expires_at, expired_at, hire_reason FROM agents WHERE id = ?`, agentID).
		Scan(&expiresAt, &expiredAt, &reason)
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	if expiredAt.Valid {
		t.Errorf("expired_at = %q, want NULL after rehire", expiredAt.String)
	}
	if !expiresAt.Valid || expiresAt.String == "" {
		t.Errorf("expires_at empty after rehire")
	}
	// expires_at must be in the future. Parse + compare against now.
	parsed, perr := time.Parse(time.RFC3339, expiresAt.String)
	if perr != nil {
		t.Fatalf("parse expires_at: %v", perr)
	}
	if !parsed.After(time.Now().Add(80 * time.Minute)) {
		t.Errorf("expires_at = %v; expected ~now+90m", parsed)
	}
	// History must be appended, not replaced.
	if !strings.Contains(reason.String, "[2024-01-01T00:00:00Z] hire: first") {
		t.Errorf("history dropped prior hire line: %q", reason.String)
	}
	if !strings.Contains(reason.String, "] rehire: second pass") {
		t.Errorf("history missing rehire line: %q", reason.String)
	}
}

func TestRehire_AppendsHistoryWithNewline(t *testing.T) {
	db := setupTestDB(t)
	userID, wsID, crewID := seedHireCrew(t, db, "trusted", 5)
	h := newHireHandler(t, db)

	past := "2024-01-01T00:00:00Z"
	prior := "[2024-01-01T00:00:00Z] hire: original"
	agentID := seedEphemeralAgent(t, db, wsID, crewID, "docs-eph-bbbbbb", &past, &past, prior)

	rr := postRehire(t, h, userID, wsID, "MANAGER", agentID, map[string]any{
		"ttl_minutes": 30,
		"reason":      "extend once",
	})
	if rr.Code != 200 {
		t.Fatalf("status = %d, want 200", rr.Code)
	}

	// Rehire a second time so we exercise the multi-line append path.
	// Each rehire moves expired_at back to NULL so the seed-then-rehire
	// loop below mimics the "rehire a still-live ephemeral" case.
	_, _ = db.Exec(`UPDATE agents SET expired_at = ? WHERE id = ?`, past, agentID)
	rr2 := postRehire(t, h, userID, wsID, "MANAGER", agentID, map[string]any{
		"ttl_minutes": 30,
		"reason":      "extend twice",
	})
	if rr2.Code != 200 {
		t.Fatalf("status2 = %d, want 200", rr2.Code)
	}

	var reason sql.NullString
	_ = db.QueryRow(`SELECT hire_reason FROM agents WHERE id = ?`, agentID).Scan(&reason)
	if want := "[2024-01-01T00:00:00Z] hire: original"; !strings.Contains(reason.String, want) {
		t.Errorf("missing initial: %q", reason.String)
	}
	if !strings.Contains(reason.String, "rehire: extend once") {
		t.Errorf("missing first rehire: %q", reason.String)
	}
	if !strings.Contains(reason.String, "rehire: extend twice") {
		t.Errorf("missing second rehire: %q", reason.String)
	}
	if strings.Count(reason.String, "\n") < 2 {
		t.Errorf("expected at least 2 newlines, got: %q", reason.String)
	}
}

func TestRehire_NonEphemeralRejected(t *testing.T) {
	db := setupTestDB(t)
	userID, wsID, crewID := seedHireCrew(t, db, "trusted", 5)
	h := newHireHandler(t, db)

	now := time.Now().UTC().Format(time.RFC3339)
	_, err := db.Exec(`
		INSERT INTO agents (id, crew_id, workspace_id, name, slug, agent_role, status,
		    cli_adapter, tool_profile, memory_enabled, ephemeral, created_at, updated_at)
		VALUES ('perm-1', ?, ?, 'Perm', 'perm', 'AGENT', 'IDLE',
		        'CLAUDE_CODE', 'CODING', 1, 0, ?, ?)`,
		crewID, wsID, now, now)
	if err != nil {
		t.Fatalf("seed permanent: %v", err)
	}

	rr := postRehire(t, h, userID, wsID, "MANAGER", "perm-1", map[string]any{
		"ttl_minutes": 30,
		"reason":      "should reject",
	})
	if rr.Code != 400 {
		t.Fatalf("status = %d, want 400; body: %s", rr.Code, rr.Body.String())
	}
}

func TestRehire_StrictPolicyRejected(t *testing.T) {
	db := setupTestDB(t)
	userID, wsID, crewID := seedHireCrew(t, db, "strict", 5)
	h := newHireHandler(t, db)

	past := "2024-01-01T00:00:00Z"
	agentID := seedEphemeralAgent(t, db, wsID, crewID, "docs-eph-cccccc", &past, &past, "[old] hire: x")

	rr := postRehire(t, h, userID, wsID, "MANAGER", agentID, map[string]any{
		"ttl_minutes": 30,
		"reason":      "strict should bounce",
	})
	if rr.Code != 403 {
		t.Fatalf("status = %d, want 403; body: %s", rr.Code, rr.Body.String())
	}

	// State must be untouched on policy reject — no partial flip.
	var expiredAt sql.NullString
	_ = db.QueryRow(`SELECT expired_at FROM agents WHERE id = ?`, agentID).Scan(&expiredAt)
	if !expiredAt.Valid || expiredAt.String != past {
		t.Errorf("expired_at flipped to %v on rejected rehire; want untouched %q", expiredAt, past)
	}
}

func TestRehire_QuotaEnforcedWhenResurrectingGhost(t *testing.T) {
	db := setupTestDB(t)
	userID, wsID, crewID := seedHireCrew(t, db, "trusted", 1)
	h := newHireHandler(t, db)

	// 1 live + 1 ghost. Quota=1; rehiring the ghost would push us to
	// 2 live which exceeds. Must 429.
	future := time.Now().Add(time.Hour).Format(time.RFC3339)
	past := "2024-01-01T00:00:00Z"
	_ = seedEphemeralAgent(t, db, wsID, crewID, "live-eph-aaa", &future, nil, "[old] hire: live")
	ghostID := seedEphemeralAgent(t, db, wsID, crewID, "ghost-eph-bbb", &past, &past, "[old] hire: ghost")

	rr := postRehire(t, h, userID, wsID, "MANAGER", ghostID, map[string]any{
		"ttl_minutes": 30,
		"reason":      "rehire should bounce on quota",
	})
	if rr.Code != 429 {
		t.Fatalf("status = %d, want 429; body: %s", rr.Code, rr.Body.String())
	}
}

func TestRehire_NotFoundIsCleanly404(t *testing.T) {
	db := setupTestDB(t)
	userID, wsID, _ := seedHireCrew(t, db, "trusted", 5)
	h := newHireHandler(t, db)

	rr := postRehire(t, h, userID, wsID, "MANAGER", "no-such-agent", map[string]any{
		"ttl_minutes": 30,
		"reason":      "ghost hunt",
	})
	if rr.Code != 404 {
		t.Fatalf("status = %d, want 404", rr.Code)
	}
}

// TestList_LiveAgentsOrderedBeforeGhosts verifies the PR-D F5 list
// query reorder: live (expired_at IS NULL) come first, ghosts last.
// Without this, a workspace that accumulates 50 ghosts pushes the
// active agents off the first page and operators have no way to see
// who's actually working without filtering.
func TestList_LiveAgentsOrderedBeforeGhosts(t *testing.T) {
	db := setupTestDB(t)
	userID, wsID, crewID := seedHireCrew(t, db, "trusted", 50)
	h := newHireHandler(t, db)

	// Seed in the order ghost, live so the natural created_at sort
	// would put ghost first. The reorder MUST flip it.
	past := "2024-01-01T00:00:00Z"
	future := time.Now().Add(time.Hour).Format(time.RFC3339)
	ghostID := seedEphemeralAgent(t, db, wsID, crewID, "old-ghost", &past, &past, "[old] hire: ghost")
	liveID := seedEphemeralAgent(t, db, wsID, crewID, "new-live", &future, nil, "[new] hire: live")

	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))
	_ = logger
	_ = h

	req := httptest.NewRequest("GET", "/api/v1/agents", nil)
	ctx := withUser(req.Context(), &AuthUser{ID: userID})
	ctx = withWorkspace(ctx, wsID, "OWNER")
	req = req.WithContext(ctx)
	rr := httptest.NewRecorder()
	h.List(rr, req)

	if rr.Code != 200 {
		t.Fatalf("list status = %d, want 200; body: %s", rr.Code, rr.Body.String())
	}
	var rows []map[string]any
	if err := json.Unmarshal(rr.Body.Bytes(), &rows); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(rows) < 2 {
		t.Fatalf("want >=2 rows, got %d", len(rows))
	}
	// The first row must be the live agent even though it was
	// inserted second; the ghost must be ordered after.
	firstID, _ := rows[0]["id"].(string)
	if firstID != liveID {
		t.Errorf("first row id = %q, want live agent %q", firstID, liveID)
	}
	// Find the ghost: it must appear after the live one. Linear
	// scan is fine for a 2-row test.
	var ghostIdx, liveIdx = -1, -1
	for i, r := range rows {
		id, _ := r["id"].(string)
		if id == ghostID {
			ghostIdx = i
		}
		if id == liveID {
			liveIdx = i
		}
	}
	if liveIdx < 0 || ghostIdx < 0 {
		t.Fatalf("missing rows: live=%d ghost=%d", liveIdx, ghostIdx)
	}
	if liveIdx >= ghostIdx {
		t.Errorf("live idx %d should be < ghost idx %d", liveIdx, ghostIdx)
	}
}

func TestAppendRehireReason(t *testing.T) {
	// Empty prior: a rehire-only line is the only sensible output;
	// we deliberately do NOT promote it to a hire: line because
	// that would mis-attribute the audit trail.
	got := appendRehireReason("", "first", "2026-05-21T10:00:00Z")
	if want := "[2026-05-21T10:00:00Z] rehire: first"; got != want {
		t.Errorf("empty prior: got %q, want %q", got, want)
	}
	// Existing history: newline separator preserves both lines.
	prior := "[2026-05-20T10:00:00Z] hire: original"
	got = appendRehireReason(prior, "second pass", "2026-05-21T10:00:00Z")
	want := prior + "\n[2026-05-21T10:00:00Z] rehire: second pass"
	if got != want {
		t.Errorf("append: got %q, want %q", got, want)
	}
	// Trailing newline on prior must be trimmed so we don't emit a
	// double newline that breaks the splitlines-style consumer.
	got = appendRehireReason(prior+"\n", "third", "2026-05-22T10:00:00Z")
	if strings.Contains(got, "\n\n") {
		t.Errorf("double newline emitted: %q", got)
	}
}
