package scheduler

import (
	"context"
	"database/sql"
	"fmt"
	"io"
	"log/slog"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/chatbridge"
	"github.com/crewship-ai/crewship/internal/orchestrator"
	"github.com/crewship-ai/crewship/internal/provider"
	_ "modernc.org/sqlite"
)

// ---------------------------------------------------------------------------
// Mocks
// ---------------------------------------------------------------------------

type mockResolver struct {
	mu sync.Mutex

	createChatErr error
	resolveInfo   *chatbridge.ChatInfo
	resolveErr    error
	createRunErr  error
	updateRunErr  error

	// tracking
	createdChats []chatbridge.CreateChatRequest
	createdRuns  []mockRun
	updatedRuns  []mockRunUpdate
	incrementCt  int
}

type mockRun struct {
	RunID, AgentID, ChatID, WorkspaceID, TriggerType string
}

type mockRunUpdate struct {
	RunID, Status string
	ExitCode      *int
	ErrorMsg      *string
}

func (m *mockResolver) CreateChat(_ context.Context, req chatbridge.CreateChatRequest) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.createdChats = append(m.createdChats, req)
	return m.createChatErr
}

func (m *mockResolver) ResolveChat(_ context.Context, _ string) (*chatbridge.ChatInfo, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.resolveInfo, m.resolveErr
}

func (m *mockResolver) ResolveAgent(_ context.Context, _, _ string) (*chatbridge.ChatInfo, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.resolveInfo, m.resolveErr
}

func (m *mockResolver) GetWebhookSecret(_ context.Context, _, _ string) (string, error) {
	return "", nil
}

func (m *mockResolver) CreateRun(_ context.Context, runID, agentID, chatID, wsID, triggerType string, _ map[string]interface{}) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.createdRuns = append(m.createdRuns, mockRun{runID, agentID, chatID, wsID, triggerType})
	return m.createRunErr
}

func (m *mockResolver) UpdateRun(_ context.Context, runID, status string, exitCode *int, errMsg *string, _ map[string]interface{}) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.updatedRuns = append(m.updatedRuns, mockRunUpdate{runID, status, exitCode, errMsg})
	return m.updateRunErr
}

func (m *mockResolver) IncrementMessageCount(_ context.Context, _ string, _ int) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.incrementCt++
	return nil
}

func (m *mockResolver) UpdateChatTitle(_ context.Context, _, _ string) error {
	return nil
}

// mockContainer implements provider.ContainerProvider
type mockContainer struct {
	ensureErr error
	ensureID  string
}

func (m *mockContainer) EnsureCrewRuntime(_ context.Context, _ provider.CrewConfig) (string, error) {
	return m.ensureID, m.ensureErr
}

func (m *mockContainer) StopCrewRuntime(_ context.Context, _ string) error   { return nil }
func (m *mockContainer) RemoveCrewRuntime(_ context.Context, _ string) error { return nil }
func (m *mockContainer) ContainerStatus(_ context.Context, _ string) (*provider.ContainerStatus, error) {
	return nil, nil
}
func (m *mockContainer) Exec(_ context.Context, _ provider.ExecConfig) (*provider.ExecResult, error) {
	return &provider.ExecResult{ExecID: "exec-123", Reader: io.NopCloser(strings.NewReader(""))}, nil
}
func (m *mockContainer) ExecInspect(_ context.Context, _ string) (bool, int, error) {
	return false, 0, nil
}
func (m *mockContainer) ContainerStats(_ context.Context, _ string) (*provider.ContainerMetrics, error) {
	return nil, nil
}
func (m *mockContainer) CrewContainerName(_ string) string { return "test-container" }
func (m *mockContainer) CopyToContainer(_ context.Context, _ string, _ string, _ io.Reader) error {
	return nil
}

// in-memory state mock for orchestrator
type memState struct {
	data map[string]map[string][]byte
}

func newMemState() *memState {
	return &memState{data: make(map[string]map[string][]byte)}
}

func (m *memState) Get(_ context.Context, bucket, key string) ([]byte, error) {
	if b, ok := m.data[bucket]; ok {
		return b[key], nil
	}
	return nil, nil
}

func (m *memState) Set(_ context.Context, bucket, key string, value []byte) error {
	if m.data[bucket] == nil {
		m.data[bucket] = make(map[string][]byte)
	}
	m.data[bucket][key] = value
	return nil
}

func (m *memState) Delete(_ context.Context, bucket, key string) error {
	if b, ok := m.data[bucket]; ok {
		delete(b, key)
	}
	return nil
}

func (m *memState) List(_ context.Context, bucket string) (map[string][]byte, error) {
	return m.data[bucket], nil
}

func (m *memState) ListByPrefix(_ context.Context, bucket, prefix string) (map[string][]byte, error) {
	result := make(map[string][]byte)
	for k, v := range m.data[bucket] {
		if strings.HasPrefix(k, prefix) {
			result[k] = v
		}
	}
	return result, nil
}

func (m *memState) Close() error { return nil }

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

func testLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
}

func testDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { db.Close() })

	schema := `
		CREATE TABLE crews (id TEXT PRIMARY KEY, workspace_id TEXT, name TEXT, slug TEXT);
		CREATE TABLE agents (
			id TEXT PRIMARY KEY,
			workspace_id TEXT,
			crew_id TEXT,
			name TEXT,
			slug TEXT,
			agent_role TEXT DEFAULT 'AGENT',
			deleted_at TEXT,
			schedule_cron TEXT,
			schedule_prompt TEXT,
			schedule_enabled INTEGER DEFAULT 0,
			schedule_last_run TEXT,
			schedule_next_run TEXT
		);
	`
	if _, err := db.Exec(schema); err != nil {
		t.Fatalf("create schema: %v", err)
	}
	return db
}

func seedAgent(t *testing.T, db *sql.DB, id, slug, name, crewID, workspace, cronExpr, prompt string, enabled bool) {
	t.Helper()
	enabledInt := 0
	if enabled {
		enabledInt = 1
	}
	var crewIDVal interface{} = nil
	if crewID != "" {
		crewIDVal = crewID
	}
	_, err := db.Exec(`INSERT INTO agents (id, workspace_id, crew_id, name, slug, schedule_cron, schedule_prompt, schedule_enabled)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)`, id, workspace, crewIDVal, name, slug, cronExpr, prompt, enabledInt)
	if err != nil {
		t.Fatalf("seed agent: %v", err)
	}
}

func seedCrew(t *testing.T, db *sql.DB, id, workspace, name, slug string) {
	t.Helper()
	_, err := db.Exec(`INSERT INTO crews (id, workspace_id, name, slug) VALUES (?, ?, ?, ?)`, id, workspace, name, slug)
	if err != nil {
		t.Fatalf("seed crew: %v", err)
	}
}

func newTestScheduler(db *sql.DB, resolver chatbridge.ChatResolver, container provider.ContainerProvider, orch *orchestrator.Orchestrator) *Scheduler {
	return New(db, orch, container, resolver, nil, nil, Config{}, testLogger())
}

// ---------------------------------------------------------------------------
// Tests
// ---------------------------------------------------------------------------

func TestNew_DefaultConfig(t *testing.T) {
	db := testDB(t)
	s := New(db, nil, nil, &mockResolver{}, nil, nil, Config{}, testLogger())
	if s.cfg.DefaultMemoryMB != 4096 {
		t.Errorf("DefaultMemoryMB = %d, want 4096", s.cfg.DefaultMemoryMB)
	}
	if s.cfg.DefaultCPUs != 2.0 {
		t.Errorf("DefaultCPUs = %f, want 2.0", s.cfg.DefaultCPUs)
	}
}

func TestNew_CustomConfig(t *testing.T) {
	db := testDB(t)
	s := New(db, nil, nil, &mockResolver{}, nil, nil, Config{DefaultMemoryMB: 8192, DefaultCPUs: 4.0}, testLogger())
	if s.cfg.DefaultMemoryMB != 8192 {
		t.Errorf("DefaultMemoryMB = %d, want 8192", s.cfg.DefaultMemoryMB)
	}
	if s.cfg.DefaultCPUs != 4.0 {
		t.Errorf("DefaultCPUs = %f, want 4.0", s.cfg.DefaultCPUs)
	}
}

func TestLoadSchedules_Empty(t *testing.T) {
	db := testDB(t)
	s := newTestScheduler(db, &mockResolver{}, nil, nil)

	if err := s.loadSchedules(context.Background()); err != nil {
		t.Fatalf("loadSchedules: %v", err)
	}
	if len(s.entryMap) != 0 {
		t.Errorf("entryMap len = %d, want 0", len(s.entryMap))
	}
}

func TestLoadSchedules_RegistersAgents(t *testing.T) {
	db := testDB(t)
	seedCrew(t, db, "crew1", "ws1", "Alpha", "alpha")
	seedAgent(t, db, "a1", "bob", "Bob", "crew1", "ws1", "0 8 * * MON", "do stuff", true)
	seedAgent(t, db, "a2", "alice", "Alice", "crew1", "ws1", "30 9 * * *", "daily check", true)

	s := newTestScheduler(db, &mockResolver{}, nil, nil)
	if err := s.loadSchedules(context.Background()); err != nil {
		t.Fatalf("loadSchedules: %v", err)
	}
	if len(s.entryMap) != 2 {
		t.Errorf("entryMap len = %d, want 2", len(s.entryMap))
	}
	if _, ok := s.entryMap["a1"]; !ok {
		t.Error("expected entry for agent a1")
	}
	if _, ok := s.entryMap["a2"]; !ok {
		t.Error("expected entry for agent a2")
	}
}

func TestLoadSchedules_SkipsDisabled(t *testing.T) {
	db := testDB(t)
	seedAgent(t, db, "a1", "bob", "Bob", "", "ws1", "0 8 * * MON", "", true)
	seedAgent(t, db, "a2", "alice", "Alice", "", "ws1", "0 9 * * *", "", false) // disabled

	s := newTestScheduler(db, &mockResolver{}, nil, nil)
	if err := s.loadSchedules(context.Background()); err != nil {
		t.Fatalf("loadSchedules: %v", err)
	}
	if len(s.entryMap) != 1 {
		t.Errorf("entryMap len = %d, want 1", len(s.entryMap))
	}
}

func TestLoadSchedules_SkipsDeletedAgents(t *testing.T) {
	db := testDB(t)
	seedAgent(t, db, "a1", "bob", "Bob", "", "ws1", "0 8 * * MON", "", true)
	if _, err := db.Exec("UPDATE agents SET deleted_at = '2026-01-01T00:00:00Z' WHERE id = 'a1'"); err != nil {
		t.Fatalf("mark deleted: %v", err)
	}

	s := newTestScheduler(db, &mockResolver{}, nil, nil)
	if err := s.loadSchedules(context.Background()); err != nil {
		t.Fatalf("loadSchedules: %v", err)
	}
	if len(s.entryMap) != 0 {
		t.Errorf("entryMap len = %d, want 0", len(s.entryMap))
	}
}

func TestLoadSchedules_InvalidCron(t *testing.T) {
	db := testDB(t)
	seedAgent(t, db, "a1", "bob", "Bob", "", "ws1", "not-a-cron", "", true)
	seedAgent(t, db, "a2", "alice", "Alice", "", "ws1", "0 8 * * MON", "", true)

	s := newTestScheduler(db, &mockResolver{}, nil, nil)
	if err := s.loadSchedules(context.Background()); err != nil {
		t.Fatalf("loadSchedules should not fail on invalid cron: %v", err)
	}
	// Only the valid one should be registered
	if len(s.entryMap) != 1 {
		t.Errorf("entryMap len = %d, want 1", len(s.entryMap))
	}
}

func TestUpdateSchedule_Enable(t *testing.T) {
	db := testDB(t)
	seedAgent(t, db, "a1", "bob", "Bob", "", "ws1", "", "", false)

	s := newTestScheduler(db, &mockResolver{}, nil, nil)
	err := s.UpdateSchedule(context.Background(), "a1", "0 8 * * MON", "weekly report", true)
	if err != nil {
		t.Fatalf("UpdateSchedule: %v", err)
	}
	if _, ok := s.entryMap["a1"]; !ok {
		t.Error("expected entry for agent a1 after enable")
	}

	// Check next_run was written to DB
	var nextRun sql.NullString
	if err := db.QueryRow("SELECT schedule_next_run FROM agents WHERE id = 'a1'").Scan(&nextRun); err != nil {
		t.Fatalf("scan schedule_next_run: %v", err)
	}
	if !nextRun.Valid || nextRun.String == "" {
		t.Error("expected schedule_next_run to be set")
	}
}

func TestUpdateSchedule_Disable(t *testing.T) {
	db := testDB(t)
	seedAgent(t, db, "a1", "bob", "Bob", "", "ws1", "0 8 * * MON", "", true)

	s := newTestScheduler(db, &mockResolver{}, nil, nil)
	// First enable
	if err := s.UpdateSchedule(context.Background(), "a1", "0 8 * * MON", "", true); err != nil {
		t.Fatalf("enable: %v", err)
	}
	if _, ok := s.entryMap["a1"]; !ok {
		t.Fatal("expected entry after enable")
	}

	// Now disable
	if err := s.UpdateSchedule(context.Background(), "a1", "", "", false); err != nil {
		t.Fatalf("disable: %v", err)
	}
	if _, ok := s.entryMap["a1"]; ok {
		t.Error("expected entry to be removed after disable")
	}
}

func TestUpdateSchedule_Replace(t *testing.T) {
	db := testDB(t)
	seedAgent(t, db, "a1", "bob", "Bob", "", "ws1", "0 8 * * MON", "", true)

	s := newTestScheduler(db, &mockResolver{}, nil, nil)

	// First schedule
	if err := s.UpdateSchedule(context.Background(), "a1", "0 8 * * MON", "", true); err != nil {
		t.Fatalf("first schedule: %v", err)
	}
	oldEntryID := s.entryMap["a1"]

	// Replace with new cron
	if err := s.UpdateSchedule(context.Background(), "a1", "30 9 * * *", "new prompt", true); err != nil {
		t.Fatalf("replace schedule: %v", err)
	}
	newEntryID := s.entryMap["a1"]
	if oldEntryID == newEntryID {
		t.Error("expected entry ID to change after replacement")
	}
}

func TestUpdateSchedule_InvalidCron(t *testing.T) {
	db := testDB(t)
	seedAgent(t, db, "a1", "bob", "Bob", "", "ws1", "0 8 * * MON", "", true)

	s := newTestScheduler(db, &mockResolver{}, nil, nil)

	// First set a valid schedule
	if err := s.UpdateSchedule(context.Background(), "a1", "0 8 * * MON", "", true); err != nil {
		t.Fatalf("valid schedule: %v", err)
	}

	// Try invalid cron — should fail, old entry stays
	err := s.UpdateSchedule(context.Background(), "a1", "not-valid", "", true)
	if err == nil {
		t.Fatal("expected error for invalid cron")
	}
	if _, ok := s.entryMap["a1"]; !ok {
		t.Error("old entry should remain after invalid update")
	}
}

func TestUpdateSchedule_AgentNotFound(t *testing.T) {
	db := testDB(t)
	s := newTestScheduler(db, &mockResolver{}, nil, nil)
	err := s.UpdateSchedule(context.Background(), "nonexistent", "0 8 * * MON", "", true)
	if err == nil {
		t.Fatal("expected error for nonexistent agent")
	}
}

func TestStartAndStop(t *testing.T) {
	db := testDB(t)
	seedAgent(t, db, "a1", "bob", "Bob", "", "ws1", "0 8 * * MON", "", true)

	s := newTestScheduler(db, &mockResolver{}, nil, nil)
	if err := s.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if len(s.entryMap) != 1 {
		t.Errorf("entryMap len = %d, want 1", len(s.entryMap))
	}

	// Stop should complete without hanging
	done := make(chan struct{})
	go func() {
		s.Stop()
		close(done)
	}()
	select {
	case <-done:
		// ok
	case <-time.After(5 * time.Second):
		t.Fatal("Stop() did not complete within 5s")
	}
}

func TestTriggerAgent_Success(t *testing.T) {
	db := testDB(t)
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
	}
	container := &mockContainer{ensureID: "container-123"}
	orch := orchestrator.New(container, newMemState(), testLogger())

	s := newTestScheduler(db, resolver, container, orch)

	ag := scheduledAgent{
		ID: "a1", Slug: "bob", Name: "Bob",
		CrewID: "crew1", CrewSlug: "alpha",
		Cron: "0 8 * * MON", Prompt: "do work", Workspace: "ws1",
	}

	s.triggerAgent(ag)

	resolver.mu.Lock()
	defer resolver.mu.Unlock()

	if len(resolver.createdChats) != 1 {
		t.Fatalf("expected 1 created chat, got %d", len(resolver.createdChats))
	}
	if resolver.createdChats[0].AgentID != "a1" {
		t.Errorf("chat agent_id = %s, want a1", resolver.createdChats[0].AgentID)
	}

	if len(resolver.createdRuns) != 1 {
		t.Fatalf("expected 1 created run, got %d", len(resolver.createdRuns))
	}
	if resolver.createdRuns[0].TriggerType != "SCHEDULED" {
		t.Errorf("trigger_type = %s, want SCHEDULED", resolver.createdRuns[0].TriggerType)
	}

	// Run should be updated (either COMPLETED or FAILED depending on orchestrator behavior)
	if len(resolver.updatedRuns) != 1 {
		t.Fatalf("expected 1 updated run, got %d", len(resolver.updatedRuns))
	}

	// Check schedule_last_run was updated in DB
	var lastRun sql.NullString
	if err := db.QueryRow("SELECT schedule_last_run FROM agents WHERE id = 'a1'").Scan(&lastRun); err != nil {
		t.Fatalf("scan schedule_last_run: %v", err)
	}
	if !lastRun.Valid || lastRun.String == "" {
		t.Error("expected schedule_last_run to be set after trigger")
	}
}

func TestTriggerAgent_CreateChatFails(t *testing.T) {
	db := testDB(t)
	seedAgent(t, db, "a1", "bob", "Bob", "", "ws1", "0 8 * * MON", "", true)

	resolver := &mockResolver{
		createChatErr: fmt.Errorf("db error"),
	}
	s := newTestScheduler(db, resolver, nil, nil)

	ag := scheduledAgent{
		ID: "a1", Slug: "bob", Name: "Bob",
		Cron: "0 8 * * MON", Workspace: "ws1",
	}

	s.triggerAgent(ag)

	resolver.mu.Lock()
	defer resolver.mu.Unlock()

	// Should NOT have created a run
	if len(resolver.createdRuns) != 0 {
		t.Errorf("expected 0 runs, got %d", len(resolver.createdRuns))
	}

	// next_run should still be updated (errorOnly=true path)
	var nextRun sql.NullString
	if err := db.QueryRow("SELECT schedule_next_run FROM agents WHERE id = 'a1'").Scan(&nextRun); err != nil {
		t.Fatalf("scan schedule_next_run: %v", err)
	}
	if !nextRun.Valid {
		t.Error("expected schedule_next_run to be set even on error")
	}
}

func TestTriggerAgent_ResolveChatFails(t *testing.T) {
	db := testDB(t)
	seedAgent(t, db, "a1", "bob", "Bob", "", "ws1", "0 8 * * MON", "", true)

	resolver := &mockResolver{
		resolveErr: fmt.Errorf("resolve error"),
	}
	s := newTestScheduler(db, resolver, nil, nil)

	ag := scheduledAgent{
		ID: "a1", Slug: "bob", Name: "Bob",
		Cron: "0 8 * * MON", Workspace: "ws1",
	}

	s.triggerAgent(ag)

	resolver.mu.Lock()
	defer resolver.mu.Unlock()

	if len(resolver.createdRuns) != 0 {
		t.Errorf("expected 0 runs, got %d", len(resolver.createdRuns))
	}
}

func TestTriggerAgent_ContainerFails(t *testing.T) {
	db := testDB(t)
	seedAgent(t, db, "a1", "bob", "Bob", "", "ws1", "0 8 * * MON", "", true)

	resolver := &mockResolver{
		resolveInfo: &chatbridge.ChatInfo{
			AgentID:     "a1",
			AgentSlug:   "bob",
			WorkspaceID: "ws1",
		},
	}
	container := &mockContainer{ensureErr: fmt.Errorf("docker error")}
	s := newTestScheduler(db, resolver, container, nil)

	ag := scheduledAgent{
		ID: "a1", Slug: "bob", Name: "Bob",
		Cron: "0 8 * * MON", Workspace: "ws1",
	}

	s.triggerAgent(ag)

	resolver.mu.Lock()
	defer resolver.mu.Unlock()

	if len(resolver.createdRuns) != 0 {
		t.Errorf("expected 0 runs when container fails, got %d", len(resolver.createdRuns))
	}
}

func TestTriggerAgent_SchedulerNilContainer(t *testing.T) {
	// When scheduler.container is nil, EnsureCrewRuntime is skipped
	// and containerID is "" — RunAgent still needs the orchestrator's container provider.
	db := testDB(t)
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
	}
	container := &mockContainer{ensureID: "container-456"}
	orch := orchestrator.New(container, newMemState(), testLogger())
	// Scheduler container is nil → skips EnsureCrewRuntime, containerID stays ""
	s := newTestScheduler(db, resolver, nil, orch)

	ag := scheduledAgent{
		ID: "a1", Slug: "bob", Name: "Bob",
		CrewID: "crew1", CrewSlug: "alpha",
		Cron: "0 8 * * MON", Workspace: "ws1",
	}

	s.triggerAgent(ag)

	resolver.mu.Lock()
	defer resolver.mu.Unlock()

	// Should proceed past container step — run is created
	if len(resolver.createdRuns) != 1 {
		t.Errorf("expected 1 run (scheduler nil container skips Ensure), got %d", len(resolver.createdRuns))
	}
}

func TestTriggerAgent_DefaultPrompt(t *testing.T) {
	db := testDB(t)
	seedCrew(t, db, "crew1", "ws1", "Alpha", "alpha")
	seedAgent(t, db, "a1", "bob", "Bob", "crew1", "ws1", "0 8 * * MON", "", true) // empty prompt

	resolver := &mockResolver{
		resolveInfo: &chatbridge.ChatInfo{
			AgentID:     "a1",
			AgentSlug:   "bob",
			AgentRole:   "AGENT",
			CLIAdapter:  "CLAUDE_CODE",
			WorkspaceID: "ws1",
		},
	}
	container := &mockContainer{ensureID: "container-789"}
	orch := orchestrator.New(container, newMemState(), testLogger())
	s := newTestScheduler(db, resolver, container, orch)

	ag := scheduledAgent{
		ID: "a1", Slug: "bob", Name: "Bob",
		Cron: "0 8 * * MON", Prompt: "", Workspace: "ws1",
	}

	s.triggerAgent(ag)

	// The test passes if triggerAgent doesn't panic — the default prompt is used internally
	resolver.mu.Lock()
	defer resolver.mu.Unlock()
	if len(resolver.createdChats) != 1 {
		t.Errorf("expected 1 chat, got %d", len(resolver.createdChats))
	}
}

func TestGenerateID(t *testing.T) {
	id1 := generateID()
	id2 := generateID()

	if !strings.HasPrefix(id1, "sched_") {
		t.Errorf("id %q does not have sched_ prefix", id1)
	}
	if !strings.HasPrefix(id2, "sched_") {
		t.Errorf("id %q does not have sched_ prefix", id2)
	}
	if id1 == id2 {
		t.Error("generated IDs should be unique")
	}
}

func TestUpdateTimestamps_Success(t *testing.T) {
	db := testDB(t)
	seedAgent(t, db, "a1", "bob", "Bob", "", "ws1", "0 8 * * MON", "", true)

	s := newTestScheduler(db, &mockResolver{}, nil, nil)
	s.updateTimestamps("a1", "0 8 * * MON", false)

	var lastRun, nextRun sql.NullString
	if err := db.QueryRow("SELECT schedule_last_run, schedule_next_run FROM agents WHERE id = 'a1'").Scan(&lastRun, &nextRun); err != nil {
		t.Fatalf("scan schedule timestamps: %v", err)
	}

	if !lastRun.Valid || lastRun.String == "" {
		t.Error("expected schedule_last_run to be set")
	}
	if !nextRun.Valid || nextRun.String == "" {
		t.Error("expected schedule_next_run to be set")
	}
}

func TestUpdateTimestamps_ErrorOnly(t *testing.T) {
	db := testDB(t)
	seedAgent(t, db, "a1", "bob", "Bob", "", "ws1", "0 8 * * MON", "", true)

	s := newTestScheduler(db, &mockResolver{}, nil, nil)
	s.updateTimestamps("a1", "0 8 * * MON", true)

	var lastRun, nextRun sql.NullString
	if err := db.QueryRow("SELECT schedule_last_run, schedule_next_run FROM agents WHERE id = 'a1'").Scan(&lastRun, &nextRun); err != nil {
		t.Fatalf("scan schedule timestamps: %v", err)
	}

	// errorOnly=true should only update next_run, not last_run
	if lastRun.Valid {
		t.Error("expected schedule_last_run to NOT be set on errorOnly")
	}
	if !nextRun.Valid || nextRun.String == "" {
		t.Error("expected schedule_next_run to be set")
	}
}
