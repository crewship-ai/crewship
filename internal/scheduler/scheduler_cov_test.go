package scheduler

import (
	"context"
	"database/sql"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/chatbridge"
	"github.com/crewship-ai/crewship/internal/conversation"
	"github.com/crewship-ai/crewship/internal/logcollector"
	"github.com/crewship-ai/crewship/internal/orchestrator"
	"github.com/crewship-ai/crewship/internal/provider"
	_ "modernc.org/sqlite"
)

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

// warnRecorder captures slog records >= Warn so tests can assert on the
// degraded-path logging instead of just "didn't panic".
type warnRecorder struct {
	mu   sync.Mutex
	msgs []string
}

func (w *warnRecorder) Enabled(_ context.Context, level slog.Level) bool {
	return level >= slog.LevelWarn
}

func (w *warnRecorder) Handle(_ context.Context, r slog.Record) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.msgs = append(w.msgs, r.Message)
	return nil
}

func (w *warnRecorder) WithAttrs(_ []slog.Attr) slog.Handler { return w }
func (w *warnRecorder) WithGroup(_ string) slog.Handler      { return w }

func (w *warnRecorder) has(substr string) bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	for _, m := range w.msgs {
		if strings.Contains(m, substr) {
			return true
		}
	}
	return false
}

// waitUntil polls cond for up to 2s.
func waitUntil(t *testing.T, cond func() bool, what string) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

// streamContainer is a ContainerProvider whose Exec emits canned Claude Code
// stream-json output and whose ExecInspect reports a configurable exit code.
type streamContainer struct {
	mockContainer
	streamOutput string
	exitCode     int
}

func (s *streamContainer) Exec(_ context.Context, _ provider.ExecConfig) (*provider.ExecResult, error) {
	return &provider.ExecResult{
		ExecID: "exec-stream",
		Reader: io.NopCloser(strings.NewReader(s.streamOutput)),
	}, nil
}

func (s *streamContainer) ExecInspect(_ context.Context, _ string) (bool, int, error) {
	return false, s.exitCode, nil
}

// ---------------------------------------------------------------------------
// Start / loadSchedules error paths
// ---------------------------------------------------------------------------

func TestStart_LoadSchedulesQueryError(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	// No schema at all — the scheduled-agents query must fail.
	s := newTestScheduler(db, &mockResolver{}, nil, nil)
	err = s.Start(context.Background())
	if err == nil {
		t.Fatal("expected Start to fail without an agents table")
	}
	if !strings.Contains(err.Error(), "load schedules") {
		t.Errorf("error %q should wrap load schedules", err)
	}
}

func TestLoadSchedules_ScanErrorSkipsRow(t *testing.T) {
	db := testDB(t)
	db.SetMaxOpenConns(1)
	// workspace_id NULL → Scan into string fails → row skipped, not fatal.
	if _, err := db.Exec(`INSERT INTO agents (id, workspace_id, name, slug, schedule_cron, schedule_enabled)
		VALUES ('bad', NULL, 'Bad', 'bad', '0 8 * * MON', 1)`); err != nil {
		t.Fatal(err)
	}
	seedAgent(t, db, "good", "good", "Good", "", "ws1", "0 9 * * *", "", true)

	s := newTestScheduler(db, &mockResolver{}, nil, nil)
	if err := s.loadSchedules(context.Background()); err != nil {
		t.Fatalf("loadSchedules must tolerate scan errors: %v", err)
	}
	if len(s.entryMap) != 1 {
		t.Fatalf("entryMap len = %d, want 1 (bad row skipped)", len(s.entryMap))
	}
	if _, ok := s.entryMap["good"]; !ok {
		t.Error("good agent should be registered")
	}
}

// ---------------------------------------------------------------------------
// RegisterPlatformRoutine
// ---------------------------------------------------------------------------

func TestRegisterPlatformRoutine_NilFn(t *testing.T) {
	db := testDB(t)
	s := newTestScheduler(db, &mockResolver{}, nil, nil)
	err := s.RegisterPlatformRoutine("skill_review", "@daily", nil)
	if err == nil {
		t.Fatal("expected error for nil fn")
	}
	if !strings.Contains(err.Error(), "non-nil fn") {
		t.Errorf("error %q should mention non-nil fn", err)
	}
}

// ---------------------------------------------------------------------------
// Cron entry closures actually fire
// ---------------------------------------------------------------------------

// The closure registered by addEntry must invoke triggerAgent when cron fires.
func TestLoadSchedules_EntryFiresTriggerAgent(t *testing.T) {
	db := testDB(t)
	db.SetMaxOpenConns(1)
	seedAgent(t, db, "a1", "bob", "Bob", "", "ws1", "@every 25ms", "", true)

	resolver := &mockResolver{createChatErr: fmt.Errorf("stop early")}
	s := newTestScheduler(db, resolver, nil, nil)
	if err := s.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(s.Stop)

	waitUntil(t, func() bool {
		resolver.mu.Lock()
		defer resolver.mu.Unlock()
		return len(resolver.createdChats) >= 1
	}, "cron entry to fire triggerAgent")

	resolver.mu.Lock()
	defer resolver.mu.Unlock()
	if resolver.createdChats[0].AgentID != "a1" {
		t.Errorf("fired chat agent = %q, want a1", resolver.createdChats[0].AgentID)
	}
}

// The closure registered by UpdateSchedule must also fire.
func TestUpdateSchedule_EntryFiresTriggerAgent(t *testing.T) {
	db := testDB(t)
	db.SetMaxOpenConns(1)
	seedAgent(t, db, "a1", "bob", "Bob", "", "ws1", "", "", false)

	resolver := &mockResolver{createChatErr: fmt.Errorf("stop early")}
	s := newTestScheduler(db, resolver, nil, nil)
	if err := s.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(s.Stop)

	if err := s.UpdateSchedule(context.Background(), "a1", "@every 25ms", "go", true); err != nil {
		t.Fatalf("UpdateSchedule: %v", err)
	}

	waitUntil(t, func() bool {
		resolver.mu.Lock()
		defer resolver.mu.Unlock()
		return len(resolver.createdChats) >= 1
	}, "updated cron entry to fire")
}

// ---------------------------------------------------------------------------
// triggerAgent full pipeline (events, conversation store, log writer)
// ---------------------------------------------------------------------------

// claudeStreamJSON is canned Claude Code stream-json output: one text delta
// and a result line carrying cost/usage metadata.
const claudeStreamJSON = `{"type":"stream_event","event":{"delta":{"type":"text_delta","text":"hello from bob"}}}
{"type":"result","subtype":"success","total_cost_usd":0.42,"num_turns":3,"usage":{"input_tokens":11,"output_tokens":7}}
`

func TestTriggerAgent_StreamsEventsAndPersistsConversation(t *testing.T) {
	db := testDB(t)
	db.SetMaxOpenConns(1)
	seedCrew(t, db, "crew1", "ws1", "Alpha", "alpha")
	seedAgent(t, db, "a1", "bob", "Bob", "crew1", "ws1", "0 8 * * MON", "do work", true)

	resolver := &mockResolver{
		resolveInfo: &chatbridge.ChatInfo{
			AgentID:     "a1",
			AgentSlug:   "bob",
			AgentRole:   "AGENT",
			CrewID:      "crew1",
			CrewSlug:    "alpha",
			CLIAdapter:  "CLAUDE_CODE",
			WorkspaceID: "ws1",
		},
		// Both warn-only error paths must not abort the run.
		createRunErr: fmt.Errorf("create run unavailable"),
		updateRunErr: fmt.Errorf("update run unavailable"),
	}
	container := &streamContainer{streamOutput: claudeStreamJSON, exitCode: 0}
	container.ensureID = "container-events"
	orch := orchestrator.New(container, newMemState(), testLogger())

	rec := &warnRecorder{}
	convStore := conversation.NewStore(t.TempDir(), slog.New(rec))
	logWriter := logcollector.NewWriter(t.TempDir(), slog.New(rec))

	s := New(db, orch, container, resolver, logWriter, convStore, Config{}, slog.New(rec))

	ag := scheduledAgent{
		ID: "a1", Slug: "bob", Name: "Bob",
		CrewID: "crew1", CrewSlug: "alpha",
		Cron: "0 8 * * MON", Prompt: "do work", Workspace: "ws1",
	}
	s.triggerAgent(ag)

	resolver.mu.Lock()
	defer resolver.mu.Unlock()

	if len(resolver.updatedRuns) != 1 {
		t.Fatalf("expected 1 run update, got %d", len(resolver.updatedRuns))
	}
	up := resolver.updatedRuns[0]
	if up.Status != "COMPLETED" {
		t.Errorf("run status = %q, want COMPLETED", up.Status)
	}
	if up.ExitCode == nil || *up.ExitCode != 0 {
		t.Errorf("exit code = %v, want 0", up.ExitCode)
	}
	// Assistant text was streamed → conversation persisted → message count +2.
	if resolver.incrementCt != 1 {
		t.Errorf("IncrementMessageCount calls = %d, want 1", resolver.incrementCt)
	}
}

func TestTriggerAgent_RunFailureMarksRunFailed(t *testing.T) {
	db := testDB(t)
	db.SetMaxOpenConns(1)
	seedCrew(t, db, "crew1", "ws1", "Alpha", "alpha")
	seedAgent(t, db, "a1", "bob", "Bob", "crew1", "ws1", "0 8 * * MON", "", true)

	resolver := &mockResolver{
		resolveInfo: &chatbridge.ChatInfo{
			AgentID:     "a1",
			AgentSlug:   "bob",
			AgentRole:   "AGENT",
			CrewID:      "crew1",
			CrewSlug:    "alpha",
			CLIAdapter:  "CLAUDE_CODE",
			WorkspaceID: "ws1",
		},
		updateRunErr: fmt.Errorf("update run unavailable"), // FAILED-side warn branch
	}
	container := &streamContainer{streamOutput: "", exitCode: 7}
	container.ensureID = "container-fail"
	orch := orchestrator.New(container, newMemState(), testLogger())
	s := New(db, orch, container, resolver, nil, nil, Config{}, testLogger())

	ag := scheduledAgent{
		ID: "a1", Slug: "bob", Name: "Bob",
		CrewID: "crew1", CrewSlug: "alpha",
		Cron: "0 8 * * MON", Workspace: "ws1",
	}
	s.triggerAgent(ag)

	resolver.mu.Lock()
	defer resolver.mu.Unlock()
	if len(resolver.updatedRuns) != 1 {
		t.Fatalf("expected 1 run update, got %d", len(resolver.updatedRuns))
	}
	up := resolver.updatedRuns[0]
	if up.Status != "FAILED" {
		t.Errorf("run status = %q, want FAILED", up.Status)
	}
	if up.ErrorMsg == nil || *up.ErrorMsg == "" {
		t.Error("expected non-empty error message on FAILED run")
	}
}

// ---------------------------------------------------------------------------
// updateTimestamps degraded paths
// ---------------------------------------------------------------------------

func TestUpdateTimestamps_UnparsableCronClearsNextRun(t *testing.T) {
	db := testDB(t)
	seedAgent(t, db, "a1", "bob", "Bob", "", "ws1", "garbage", "", true)
	if _, err := db.Exec("UPDATE agents SET schedule_next_run = '2026-01-01T00:00:00Z' WHERE id = 'a1'"); err != nil {
		t.Fatal(err)
	}

	rec := &warnRecorder{}
	s := New(db, nil, nil, &mockResolver{}, nil, nil, Config{}, slog.New(rec))
	s.updateTimestamps("a1", "garbage", false)

	var lastRun, nextRun sql.NullString
	if err := db.QueryRow("SELECT schedule_last_run, schedule_next_run FROM agents WHERE id = 'a1'").Scan(&lastRun, &nextRun); err != nil {
		t.Fatal(err)
	}
	if nextRun.Valid {
		t.Errorf("schedule_next_run should be cleared for unparsable cron, got %q", nextRun.String)
	}
	if !lastRun.Valid || lastRun.String == "" {
		t.Error("schedule_last_run should still be recorded")
	}
	if !rec.has("schedule cron unparsable; clearing schedule_next_run") {
		t.Errorf("expected unparsable-cron warning, got %v", rec.msgs)
	}
}

func TestUpdateTimestamps_UnparsableCronErrorOnlySkipsLastRun(t *testing.T) {
	db := testDB(t)
	seedAgent(t, db, "a1", "bob", "Bob", "", "ws1", "garbage", "", true)

	s := New(db, nil, nil, &mockResolver{}, nil, nil, Config{}, testLogger())
	s.updateTimestamps("a1", "garbage", true)

	var lastRun, nextRun sql.NullString
	if err := db.QueryRow("SELECT schedule_last_run, schedule_next_run FROM agents WHERE id = 'a1'").Scan(&lastRun, &nextRun); err != nil {
		t.Fatal(err)
	}
	if lastRun.Valid {
		t.Error("errorOnly must not set schedule_last_run")
	}
	if nextRun.Valid {
		t.Error("schedule_next_run must stay cleared for unparsable cron")
	}
}

func TestUpdateTimestamps_DBErrorsAreLoggedNotFatal(t *testing.T) {
	db := testDB(t)
	seedAgent(t, db, "a1", "bob", "Bob", "", "ws1", "0 8 * * MON", "", true)

	rec := &warnRecorder{}
	s := New(db, nil, nil, &mockResolver{}, nil, nil, Config{}, slog.New(rec))
	db.Close() // every subsequent UPDATE fails

	s.updateTimestamps("a1", "0 8 * * MON", false) // last+next update fails
	s.updateTimestamps("a1", "0 8 * * MON", true)  // errorOnly next update fails
	s.updateTimestamps("a1", "garbage", false)     // clear fails, then last-only fails

	for _, want := range []string{
		"update schedule timestamps",
		"update schedule_next_run",
		"clear schedule_next_run",
		"update schedule_last_run",
	} {
		if !rec.has(want) {
			t.Errorf("expected warn %q, got %v", want, rec.msgs)
		}
	}
}
