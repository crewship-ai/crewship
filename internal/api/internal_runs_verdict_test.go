package api

import (
	"context"
	"database/sql"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"log/slog"

	"github.com/crewship-ai/crewship/internal/llm"
)

// stubVerdictProvider is a minimal llm.Provider returning a canned
// verdict JSON, for exercising UpdateRun's post-run verdict wiring
// (#1403) end-to-end against a real (in-memory) journal.
type stubVerdictProvider struct {
	content string
	calls   int
}

func (s *stubVerdictProvider) Complete(ctx context.Context, req llm.Request) (*llm.Response, error) {
	s.calls++
	return &llm.Response{Content: s.content}, nil
}

func (s *stubVerdictProvider) Stream(ctx context.Context, req llm.Request, handler func(llm.StreamEvent) error) (*llm.Response, error) {
	return nil, nil
}

func (s *stubVerdictProvider) Name() string { return "stub" }

const stubVerdictJSON = `{"outcome":"goal_met","verdict":"Agent completed the task successfully.","summary":"The agent ran the requested steps and finished without error."}`

// verdictEntryPayload looks up the summary.generated entry (if any) for
// runID and returns its outcome + verdict fields, or ("", "", false) if
// none exists.
func verdictEntryPayload(t *testing.T, db *sql.DB, runID string) (outcome, verdict string, found bool) {
	t.Helper()
	var payload string
	err := db.QueryRow(`SELECT payload FROM journal_entries WHERE trace_id = ? AND entry_type = 'summary.generated' LIMIT 1`, runID).Scan(&payload)
	if err == sql.ErrNoRows {
		return "", "", false
	}
	if err != nil {
		t.Fatalf("query verdict entry: %v", err)
	}
	if strings.Contains(payload, `"outcome":"goal_met"`) {
		outcome = "goal_met"
	}
	if strings.Contains(payload, "Agent completed the task successfully.") {
		verdict = "Agent completed the task successfully."
	}
	return outcome, verdict, true
}

func createAndCompleteRun(t *testing.T, h *InternalHandler, db *sql.DB, wsID, agentID, runID, status string) {
	t.Helper()
	// Seed run.started directly (same fixture internal_runs_trigger_test.go
	// uses) rather than going through the CreateRun HTTP path — CreateRun's
	// journal emit is async (queued, background-flushed) so an immediate
	// follow-up UpdateRun can race ahead of the write becoming visible.
	seedRunFixture(t, db, runID, agentID, wsID, "", "USER", "")

	updateBody := strings.NewReader(`{"status":"` + status + `"}`)
	updateReq := httptest.NewRequest("PATCH", "/api/v1/internal/runs/"+runID, updateBody)
	updateReq.SetPathValue("runId", runID)
	updateRR := httptest.NewRecorder()
	h.UpdateRun(updateRR, updateReq)
	if updateRR.Code != http.StatusOK {
		t.Fatalf("UpdateRun: got %d, body=%s", updateRR.Code, updateRR.Body.String())
	}
	h.verdictWG.Wait()
}

func TestUpdateRun_EmitsVerdict_WhenCompletedAndProviderWired(t *testing.T) {
	db := setupTestDB(t)
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))

	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	if _, err := db.Exec(`INSERT INTO agents (id, workspace_id, name, slug, status) VALUES ('a-verdict', ?, 'Bot', 'bot', 'IDLE')`, wsID); err != nil {
		t.Fatalf("insert agent: %v", err)
	}

	h := NewInternalHandler(db, "test-token", logger)
	_ = wireTestJournalForHandler(t, db, h)
	provider := &stubVerdictProvider{content: stubVerdictJSON}
	h.SetRunVerdict(provider, "claude-haiku-4-5")

	createAndCompleteRun(t, h, db, wsID, "a-verdict", "run-verdict-1", "COMPLETED")

	if provider.calls != 1 {
		t.Fatalf("provider.Complete calls = %d, want 1", provider.calls)
	}
	outcome, verdict, found := verdictEntryPayload(t, db, "run-verdict-1")
	if !found {
		t.Fatal("expected a summary.generated journal entry, found none")
	}
	if outcome != "goal_met" {
		t.Errorf("outcome = %q, want goal_met", outcome)
	}
	if verdict == "" {
		t.Error("expected verdict one-liner in payload")
	}
}

func TestUpdateRun_NoVerdict_WhenCancelled(t *testing.T) {
	db := setupTestDB(t)
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))

	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	if _, err := db.Exec(`INSERT INTO agents (id, workspace_id, name, slug, status) VALUES ('a-cancel', ?, 'Bot', 'bot', 'IDLE')`, wsID); err != nil {
		t.Fatalf("insert agent: %v", err)
	}

	h := NewInternalHandler(db, "test-token", logger)
	_ = wireTestJournalForHandler(t, db, h)
	provider := &stubVerdictProvider{content: stubVerdictJSON}
	h.SetRunVerdict(provider, "claude-haiku-4-5")

	createAndCompleteRun(t, h, db, wsID, "a-cancel", "run-cancel-1", "CANCELLED")

	if provider.calls != 0 {
		t.Errorf("provider.Complete calls = %d, want 0 (cancelled runs get no verdict)", provider.calls)
	}
	if _, _, found := verdictEntryPayload(t, db, "run-cancel-1"); found {
		t.Error("expected no summary.generated entry for a cancelled run")
	}
}

func TestUpdateRun_NoVerdict_WhenNoProviderWired(t *testing.T) {
	db := setupTestDB(t)
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))

	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	if _, err := db.Exec(`INSERT INTO agents (id, workspace_id, name, slug, status) VALUES ('a-noprov', ?, 'Bot', 'bot', 'IDLE')`, wsID); err != nil {
		t.Fatalf("insert agent: %v", err)
	}

	h := NewInternalHandler(db, "test-token", logger)
	_ = wireTestJournalForHandler(t, db, h)
	// No SetRunVerdict call — mirrors a boot where the run_summary aux
	// slot has no buildable provider (e.g. missing ANTHROPIC_API_KEY).

	createAndCompleteRun(t, h, db, wsID, "a-noprov", "run-noprov-1", "COMPLETED")

	if _, _, found := verdictEntryPayload(t, db, "run-noprov-1"); found {
		t.Error("expected no summary.generated entry when no verdict provider is wired")
	}
}

func TestUpdateRun_NoVerdict_WhenWorkspaceFlagOverriddenOff(t *testing.T) {
	db := setupTestDB(t)
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))

	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	if _, err := db.Exec(`INSERT INTO agents (id, workspace_id, name, slug, status) VALUES ('a-off', ?, 'Bot', 'bot', 'IDLE')`, wsID); err != nil {
		t.Fatalf("insert agent: %v", err)
	}
	// Migration v164 seeds the run_verdict_summaries flag enabled=1
	// instance-wide; override it off for this workspace only.
	if _, err := db.Exec(`INSERT INTO feature_flag_overrides (id, flag_id, workspace_id, enabled) VALUES (?, 'ffl_run_verdict_summaries', ?, 0)`,
		"ov-"+wsID, wsID); err != nil {
		t.Fatalf("insert override: %v", err)
	}

	h := NewInternalHandler(db, "test-token", logger)
	_ = wireTestJournalForHandler(t, db, h)
	provider := &stubVerdictProvider{content: stubVerdictJSON}
	h.SetRunVerdict(provider, "claude-haiku-4-5")

	createAndCompleteRun(t, h, db, wsID, "a-off", "run-off-1", "COMPLETED")

	if provider.calls != 0 {
		t.Errorf("provider.Complete calls = %d, want 0 (workspace opted out via flag override)", provider.calls)
	}
	if _, _, found := verdictEntryPayload(t, db, "run-off-1"); found {
		t.Error("expected no summary.generated entry when the workspace flag override is off")
	}
}
