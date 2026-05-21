package api

// Tests for the PR-D F5 Hire endpoint. Cover the four cells of the
// (autonomy_level × outcome) matrix the PRD calls out:
//
//	strict   → 403 rejected
//	guided   → 202 accepted + pending_review=true + blocking inbox
//	trusted  → 201 created + non-blocking inbox
//	full     → 201 created + journal-only (no inbox row)
//
// Plus the structural rules: ephemeral=true persists, quota cuts off at
// the configured max, cross-workspace crews are 404'd, missing template
// is 404, missing reason is 400.

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	"github.com/crewship-ai/crewship/internal/policy"
)

// seedHireCrew builds a workspace + crew + a built-in template so the
// hire path has something to resolve. Returns the crew id; the
// workspace id is fixed by the helper to keep call sites short.
func seedHireCrew(t *testing.T, db *sql.DB, autonomyLevel string, maxEphemeral int) (userID, wsID, crewID string) {
	t.Helper()
	userID = seedTestUser(t, db)
	wsID = seedTestWorkspace(t, db, userID)
	crewID = "crew-hire-1"
	_, err := db.Exec(`INSERT INTO crews (id, workspace_id, name, slug, autonomy_level, behavior_mode, max_ephemeral_agents)
		VALUES (?, ?, 'Hire Crew', 'hire-crew', ?, 'warn', ?)`,
		crewID, wsID, autonomyLevel, maxEphemeral)
	if err != nil {
		t.Fatalf("seed crew: %v", err)
	}
	// Minimal built-in template so lookupCrewTemplate finds it.
	_, err = db.Exec(`INSERT INTO crew_templates (id, name, slug, agents_json, is_builtin)
		VALUES ('tmpl-1', 'Docs Writer', 'docs-writer', '[]', 1)`)
	if err != nil {
		t.Fatalf("seed template: %v", err)
	}
	return userID, wsID, crewID
}

func newHireHandler(t *testing.T, db *sql.DB) *AgentHandler {
	t.Helper()
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))
	h := NewAgentHandler(db, logger)
	h.SetPolicyResolver(policy.NewResolver(db))
	return h
}

func postHire(t *testing.T, h *AgentHandler, userID, wsID, role string, body any) *httptest.ResponseRecorder {
	t.Helper()
	b, _ := json.Marshal(body)
	req := httptest.NewRequest("POST", "/api/v1/agents/hire", bytes.NewReader(b))
	req.Header.Set("Content-Type", "application/json")
	ctx := withUser(req.Context(), &AuthUser{ID: userID})
	ctx = withWorkspace(ctx, wsID, role)
	req = req.WithContext(ctx)
	rr := httptest.NewRecorder()
	h.Hire(rr, req)
	return rr
}

func TestHire_HappyPath_TrustedAutoLogInbox(t *testing.T) {
	db := setupTestDB(t)
	userID, wsID, crewID := seedHireCrew(t, db, "trusted", 5)
	h := newHireHandler(t, db)

	rr := postHire(t, h, userID, wsID, "MANAGER", map[string]any{
		"crew_id":       crewID,
		"template_slug": "docs-writer",
		"reason":        "ship the docs site",
		"ttl_minutes":   60,
	})
	if rr.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201; body: %s", rr.Code, rr.Body.String())
	}

	var resp hireResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if !resp.Ephemeral {
		t.Errorf("ephemeral=false, want true")
	}
	if resp.PendingReview {
		t.Errorf("pending_review=true on trusted; want false (auto-log)")
	}
	if resp.ExpiresAt == nil || *resp.ExpiresAt == "" {
		t.Errorf("expires_at empty, want RFC3339 timestamp")
	}
	if resp.Decision != string(policy.DecisionAutoLogJournal) {
		// Trusted hires use auto_log_journal per the PR-B decision
		// matrix. If the matrix changes the test should track it.
		t.Errorf("decision = %q, want %q", resp.Decision, policy.DecisionAutoLogJournal)
	}

	// Verify ephemeral=1 + expires_at populated in the row itself.
	var eph int
	var expiresAt sql.NullString
	err := db.QueryRow(`SELECT ephemeral, expires_at FROM agents WHERE id = ?`, resp.ID).Scan(&eph, &expiresAt)
	if err != nil {
		t.Fatalf("verify row: %v", err)
	}
	if eph != 1 {
		t.Errorf("agents.ephemeral = %d, want 1", eph)
	}
	if !expiresAt.Valid || expiresAt.String == "" {
		t.Errorf("agents.expires_at not set")
	}

	// Trusted path = auto_log_journal → NO inbox row.
	var inboxCount int
	_ = db.QueryRow(`SELECT COUNT(*) FROM inbox_items WHERE source_id = ?`, resp.ID).Scan(&inboxCount)
	if inboxCount != 0 {
		t.Errorf("inbox rows = %d on trusted; want 0", inboxCount)
	}
}

func TestHire_GuidedRequiresInboxApproval(t *testing.T) {
	db := setupTestDB(t)
	userID, wsID, crewID := seedHireCrew(t, db, "guided", 5)
	h := newHireHandler(t, db)

	rr := postHire(t, h, userID, wsID, "MANAGER", map[string]any{
		"crew_id":       crewID,
		"template_slug": "docs-writer",
		"reason":        "guided crew needs approval",
	})
	if rr.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want 202; body: %s", rr.Code, rr.Body.String())
	}
	var resp hireResponse
	_ = json.Unmarshal(rr.Body.Bytes(), &resp)
	if !resp.PendingReview {
		t.Errorf("pending_review=false on guided; want true")
	}
	if resp.InboxItemID == "" {
		t.Errorf("inbox_item_id empty; want a blocking inbox row")
	}

	// Inbox kind must be 'waitpoint' (blocking) on guided.
	var kind string
	var blocking int
	err := db.QueryRow(`SELECT kind, blocking FROM inbox_items WHERE source_id = ?`, resp.ID).Scan(&kind, &blocking)
	if err != nil {
		t.Fatalf("inbox lookup: %v", err)
	}
	if kind != "waitpoint" || blocking != 1 {
		t.Errorf("inbox row = (%q, blocking=%d), want (waitpoint, 1)", kind, blocking)
	}
}

// TestHire_Guided_CreatesAgentInPendingReviewStatus asserts the
// PENDING_REVIEW status sentinel is written to the agents row when
// the policy returns InboxApprove. This is what actually blocks the
// chatbridge — without it, the inbox waitpoint is purely
// informational and the agent would serve messages the instant after
// the 202 lands.
func TestHire_Guided_CreatesAgentInPendingReviewStatus(t *testing.T) {
	db := setupTestDB(t)
	userID, wsID, crewID := seedHireCrew(t, db, "guided", 5)
	h := newHireHandler(t, db)

	rr := postHire(t, h, userID, wsID, "MANAGER", map[string]any{
		"crew_id":       crewID,
		"template_slug": "docs-writer",
		"reason":        "blocked until approve",
	})
	if rr.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want 202; body: %s", rr.Code, rr.Body.String())
	}
	var resp hireResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if resp.Status != "PENDING_REVIEW" {
		t.Errorf("response status = %q, want PENDING_REVIEW", resp.Status)
	}

	// Source of truth is the DB row, not the response shape — assert
	// the column directly.
	var status string
	if err := db.QueryRow(`SELECT status FROM agents WHERE id = ?`, resp.ID).Scan(&status); err != nil {
		t.Fatalf("verify status: %v", err)
	}
	if status != "PENDING_REVIEW" {
		t.Errorf("agents.status = %q, want PENDING_REVIEW", status)
	}
}

// TestHire_Trusted_CreatesAgentInIdleStatus asserts non-guided
// hires (trusted, full) keep the legacy IDLE status — the
// PENDING_REVIEW sentinel must be scoped to InboxApprove only,
// otherwise the chatbridge would refuse to start any ephemeral
// regardless of policy.
func TestHire_Trusted_CreatesAgentInIdleStatus(t *testing.T) {
	db := setupTestDB(t)
	userID, wsID, crewID := seedHireCrew(t, db, "trusted", 5)
	h := newHireHandler(t, db)

	rr := postHire(t, h, userID, wsID, "MANAGER", map[string]any{
		"crew_id":       crewID,
		"template_slug": "docs-writer",
		"reason":        "should start immediately",
	})
	if rr.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201; body: %s", rr.Code, rr.Body.String())
	}
	var resp hireResponse
	_ = json.Unmarshal(rr.Body.Bytes(), &resp)
	var status string
	if err := db.QueryRow(`SELECT status FROM agents WHERE id = ?`, resp.ID).Scan(&status); err != nil {
		t.Fatalf("verify status: %v", err)
	}
	if status != "IDLE" {
		t.Errorf("agents.status = %q, want IDLE on trusted hire", status)
	}
}

func TestHire_StrictRejected(t *testing.T) {
	db := setupTestDB(t)
	userID, wsID, crewID := seedHireCrew(t, db, "strict", 5)
	h := newHireHandler(t, db)

	rr := postHire(t, h, userID, wsID, "MANAGER", map[string]any{
		"crew_id":       crewID,
		"template_slug": "docs-writer",
		"reason":        "strict crew, should bounce",
	})
	if rr.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403; body: %s", rr.Code, rr.Body.String())
	}

	// No agent row should have been written. A regression that did
	// the insert before the policy check would leak rows here.
	var n int
	_ = db.QueryRow(`SELECT COUNT(*) FROM agents WHERE crew_id = ?`, crewID).Scan(&n)
	if n != 0 {
		t.Errorf("agent rows = %d on strict reject; want 0", n)
	}

	// 403 body should carry the autonomy_level so the CLI can
	// suggest a fix instead of just printing "Forbidden".
	var body map[string]string
	_ = json.Unmarshal(rr.Body.Bytes(), &body)
	if body["autonomy_level"] != "strict" {
		t.Errorf("body.autonomy_level = %q, want strict", body["autonomy_level"])
	}
}

func TestHire_QuotaCutoffReturns429(t *testing.T) {
	db := setupTestDB(t)
	userID, wsID, crewID := seedHireCrew(t, db, "trusted", 2)
	h := newHireHandler(t, db)

	for i := 0; i < 2; i++ {
		rr := postHire(t, h, userID, wsID, "MANAGER", map[string]any{
			"crew_id":       crewID,
			"template_slug": "docs-writer",
			"reason":        fmt.Sprintf("hire %d", i),
		})
		if rr.Code != http.StatusCreated {
			t.Fatalf("hire %d: status %d, want 201; body: %s", i, rr.Code, rr.Body.String())
		}
	}

	rr := postHire(t, h, userID, wsID, "MANAGER", map[string]any{
		"crew_id":       crewID,
		"template_slug": "docs-writer",
		"reason":        "should bounce",
	})
	if rr.Code != http.StatusTooManyRequests {
		t.Fatalf("status = %d, want 429; body: %s", rr.Code, rr.Body.String())
	}
}

func TestHire_GhostsDoNotCountTowardQuota(t *testing.T) {
	db := setupTestDB(t)
	userID, wsID, crewID := seedHireCrew(t, db, "trusted", 2)
	h := newHireHandler(t, db)

	// Two live + one ghost = still allowed up to two live.
	for i := 0; i < 2; i++ {
		rr := postHire(t, h, userID, wsID, "MANAGER", map[string]any{
			"crew_id":       crewID,
			"template_slug": "docs-writer",
			"reason":        fmt.Sprintf("hire %d", i),
		})
		if rr.Code != http.StatusCreated {
			t.Fatalf("setup hire %d failed: %s", i, rr.Body.String())
		}
	}
	// Mark the first as ghost.
	_, err := db.Exec(`UPDATE agents SET expired_at = '2024-01-01T00:00:00Z' WHERE crew_id = ? LIMIT 1`, crewID)
	if err != nil {
		// Older SQLite builds reject LIMIT on UPDATE without a
		// compile flag; fall back to a row-id targeted update.
		_, err = db.Exec(`UPDATE agents SET expired_at = '2024-01-01T00:00:00Z' WHERE id = (SELECT id FROM agents WHERE crew_id = ? LIMIT 1)`, crewID)
		if err != nil {
			t.Fatalf("ghost setup: %v", err)
		}
	}

	rr := postHire(t, h, userID, wsID, "MANAGER", map[string]any{
		"crew_id":       crewID,
		"template_slug": "docs-writer",
		"reason":        "should fit because one is ghost",
	})
	if rr.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201; body: %s", rr.Code, rr.Body.String())
	}
}

func TestHire_CrossWorkspaceCrewIs404(t *testing.T) {
	db := setupTestDB(t)
	userID, wsID, _ := seedHireCrew(t, db, "trusted", 5)
	h := newHireHandler(t, db)

	// Create a second workspace + crew the caller does NOT belong
	// to. Hire must 404 — never leak existence of cross-tenant
	// crews to a caller scoped at wsID.
	_, _ = db.Exec(`INSERT INTO workspaces (id, name, slug) VALUES ('ws-other', 'Other', 'other')`)
	_, _ = db.Exec(`INSERT INTO crews (id, workspace_id, name, slug) VALUES ('crew-other', 'ws-other', 'X', 'x')`)

	rr := postHire(t, h, userID, wsID, "MANAGER", map[string]any{
		"crew_id":       "crew-other",
		"template_slug": "docs-writer",
		"reason":        "cross-tenant probe",
	})
	if rr.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404; body: %s", rr.Code, rr.Body.String())
	}
}

func TestHire_MissingReasonIs400(t *testing.T) {
	db := setupTestDB(t)
	userID, wsID, crewID := seedHireCrew(t, db, "trusted", 5)
	h := newHireHandler(t, db)

	rr := postHire(t, h, userID, wsID, "MANAGER", map[string]any{
		"crew_id":       crewID,
		"template_slug": "docs-writer",
	})
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body: %s", rr.Code, rr.Body.String())
	}
}

func TestHire_RequiresManagerOrAbove(t *testing.T) {
	db := setupTestDB(t)
	userID, wsID, crewID := seedHireCrew(t, db, "trusted", 5)
	h := newHireHandler(t, db)

	for _, role := range []string{"VIEWER", "MEMBER"} {
		rr := postHire(t, h, userID, wsID, role, map[string]any{
			"crew_id":       crewID,
			"template_slug": "docs-writer",
			"reason":        "rbac probe",
		})
		if rr.Code != http.StatusForbidden {
			t.Errorf("role=%s: status = %d, want 403", role, rr.Code)
		}
	}
}

func TestHire_NilPolicyResolverFallsBackToInboxApprove(t *testing.T) {
	db := setupTestDB(t)
	userID, wsID, crewID := seedHireCrew(t, db, "trusted", 5)
	// Deliberately do NOT call SetPolicyResolver — the nil path
	// must hold the documented default (guided ⇒ inbox approve)
	// regardless of what the crew row says, because the resolver
	// is the only thing that reads autonomy_level.
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))
	h := NewAgentHandler(db, logger)

	rr := postHire(t, h, userID, wsID, "MANAGER", map[string]any{
		"crew_id":       crewID,
		"template_slug": "docs-writer",
		"reason":        "nil resolver fallback",
	})
	if rr.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want 202 (guided fallback); body: %s", rr.Code, rr.Body.String())
	}
}

// TestBuildEphemeralSlug pins the slug shape so a future template-slug
// normalisation doesn't silently break referrers (CLI deep-links rely
// on this format). The suffix is the trailing 6 chars of the CUID
// (its random-byte tail) — that's load-bearing for collision avoidance
// under rapid back-to-back hires in the same millisecond, where the
// leading "c<ts>" prefix is shared.
func TestBuildEphemeralSlug(t *testing.T) {
	got := buildEphemeralSlug("docs-writer", "cm1abc23456789xyz")
	if want := "docs-writer-eph-789xyz"; got != want {
		t.Errorf("buildEphemeralSlug = %q, want %q (trailing 6 of CUID)", got, want)
	}
	// Empty template falls back to "agent" so an inbound spawn
	// without a slug still produces something queryable. Short
	// agentID (<= 6 chars) passes through whole; longer agentIDs
	// are trimmed to the trailing 6 chars.
	if got := buildEphemeralSlug("", "xy"); got != "agent-eph-xy" {
		t.Errorf("short CUID passthrough = %q, want agent-eph-xy", got)
	}
	if got := buildEphemeralSlug("", "abcdefghij"); got != "agent-eph-efghij" {
		t.Errorf("long CUID trim = %q, want agent-eph-efghij", got)
	}
}

// TestBuildInitialReason verifies the leading-timestamp shape rehire
// will later append to. Stable format is load-bearing — the CLI is
// going to splitlines on the column.
func TestBuildInitialReason(t *testing.T) {
	got := buildInitialReason("ship the docs", "2026-05-21T11:00:00Z")
	want := "[2026-05-21T11:00:00Z] hire: ship the docs"
	if got != want {
		t.Errorf("buildInitialReason = %q, want %q", got, want)
	}
}

// TestProvideForModel pins the provider inference so a regression that
// silently changed it (e.g. swapping the prefix check direction) shows
// up as a test failure rather than at provisioning time.
func TestProvideForModel(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{"claude-opus-4-7", "ANTHROPIC"},
		{"gpt-5", "OPENAI"},
		{"o3-mini", "OPENAI"},
		{"gemini-2.5-pro", "GOOGLE"},
		{"", ""},
		{"llama-3", ""},
	} {
		if got := provideForModel(tc.in); got != tc.want {
			t.Errorf("provideForModel(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

// Silence "unused" warning on context.Background — Go vet's import
// hygiene rule trips otherwise when the test file imports context for
// the resolver fixtures but no test directly references it.
var _ = context.Background
