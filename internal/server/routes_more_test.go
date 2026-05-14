package server

import (
	"context"
	"database/sql"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/config"
	"github.com/crewship-ai/crewship/internal/conversation"
	"github.com/crewship-ai/crewship/internal/database"
	"github.com/crewship-ai/crewship/internal/journal"
	"github.com/crewship-ai/crewship/internal/logging"
	"github.com/crewship-ai/crewship/internal/orchestrator"
	"github.com/crewship-ai/crewship/internal/provider"
	"github.com/crewship-ai/crewship/internal/provider/localfs"
)

func TestHandleAgentStop_RunningInState(t *testing.T) {
	t.Parallel()
	s := newTestServerWithDeps(t)

	// Pre-load a "running" run.
	stateData := `{"agent_id":"a1","status":"running","started_at":"2026-04-01T00:00:00Z"}`
	_ = s.state.Set(context.Background(), "agent_runs", "a1", []byte(stateData))

	req := httptest.NewRequest("POST", "/agents/a1/stop", nil)
	rec := httptest.NewRecorder()
	s.ipcMux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}

	// State should now read "stopped".
	got, _ := s.state.Get(context.Background(), "agent_runs", "a1")
	var run orchestrator.RunState
	if err := json.Unmarshal(got, &run); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if run.Status != "stopped" {
		t.Errorf("status not updated, got %q", run.Status)
	}
}

func TestHandleAgentStart_InvalidJSON(t *testing.T) {
	t.Parallel()
	s := newTestServerWithDeps(t)
	req := httptest.NewRequest("POST", "/agents/a1/start", strings.NewReader("not json"))
	rec := httptest.NewRecorder()
	s.ipcMux.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", rec.Code)
	}
}

func TestHandleFileList_RecursiveAndSubdir(t *testing.T) {
	t.Parallel()
	cfg := config.Default()
	cfg.Auth.JWTSecret = "test-secret-for-routes-more-test-32ch"
	dir := t.TempDir()
	cfg.Storage.BasePath = dir
	logger := logging.New("error", "json", nil)
	stor, _ := localfs.New(dir)
	s := New(cfg, logger, &Deps{Storage: stor, DB: openTestDB(t)})
	t.Cleanup(s.StopBackground)
	s.startedAt = time.Now()

	// Seed files: crewA/agentX/notes.txt, crewA/root.txt
	for path, body := range map[string]string{
		"crewA/agentX/notes.txt": "n1",
		"crewA/root.txt":         "root",
	} {
		_ = stor.Write(context.Background(), path, strings.NewReader(body))
	}

	t.Run("crew root", func(t *testing.T) {
		req := httptest.NewRequest("GET", "/crews/crewA/files", nil)
		rec := httptest.NewRecorder()
		s.ipcMux.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d", rec.Code)
		}
	})

	t.Run("agent namespace + recursive", func(t *testing.T) {
		req := httptest.NewRequest("GET", "/crews/crewA/files?agent_slug=agentX&recursive=true", nil)
		rec := httptest.NewRecorder()
		s.ipcMux.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d", rec.Code)
		}
		var body map[string]interface{}
		_ = json.Unmarshal(rec.Body.Bytes(), &body)
		files, ok := body["files"].([]interface{})
		if !ok || len(files) == 0 {
			t.Fatalf("expected files, got %v", body["files"])
		}
	})

	t.Run("invalid agent slug", func(t *testing.T) {
		req := httptest.NewRequest("GET", "/crews/crewA/files?agent_slug=../..", nil)
		rec := httptest.NewRecorder()
		s.ipcMux.ServeHTTP(rec, req)
		if rec.Code != http.StatusBadRequest {
			t.Errorf("status = %d, want 400", rec.Code)
		}
	})

	t.Run("invalid subdir", func(t *testing.T) {
		req := httptest.NewRequest("GET", "/crews/crewA/files?subdir=../..", nil)
		rec := httptest.NewRecorder()
		s.ipcMux.ServeHTTP(rec, req)
		if rec.Code != http.StatusBadRequest {
			t.Errorf("status = %d, want 400", rec.Code)
		}
	})

	t.Run("subdir param", func(t *testing.T) {
		req := httptest.NewRequest("GET", "/crews/crewA/files?subdir=agentX", nil)
		rec := httptest.NewRecorder()
		s.ipcMux.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Errorf("status = %d", rec.Code)
		}
	})
}

func TestRecoverOrphanedRuns_MarksRunningCancelled(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	db, err := database.Open("file:" + filepath.Join(dir, "rec.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	logger := logging.New("error", "json", nil)
	if err := database.Migrate(context.Background(), db.DB, logger); err != nil {
		t.Fatal(err)
	}

	now := time.Now().UTC().Format(time.RFC3339)
	mustExec(t, db.DB, `INSERT INTO users (id, email, created_at, updated_at) VALUES ('u1','u@x',?,?)`, now, now)
	mustExec(t, db.DB, `INSERT INTO workspaces (id, name, slug, created_at, updated_at) VALUES ('w1','W','w',?,?)`, now, now)
	mustExec(t, db.DB, `INSERT INTO agents (id, workspace_id, name, slug, status, created_at, updated_at) VALUES ('a1','w1','A','a','RUNNING',?,?)`, now, now)
	mustExec(t, db.DB, `INSERT INTO chats (id, workspace_id, agent_id, created_at, updated_at) VALUES ('c1','w1','a1',?,?)`, now, now)
	// Post Phase J: a "running" run is a journal trace with run.started
	// and no terminal entry. recoverOrphanedRuns emits run.cancelled and
	// flips the agent back to IDLE.
	mustExec(t, db.DB, `INSERT INTO journal_entries
		(id, workspace_id, agent_id, ts, entry_type, severity, actor_type, summary, payload, refs, trace_id, priority)
		VALUES ('je1','w1','a1', strftime('%Y-%m-%dT%H:%M:%fZ','now'),
		        'run.started','info','sidecar','run r1 started','{"trigger_type":"USER"}','{}','r1','normal')`)

	cfg := config.Default()
	cfg.Auth.JWTSecret = "test-secret-for-routes-more-test-32ch"
	s := New(cfg, logger, &Deps{DB: db.DB})
	s.startedAt = time.Now()
	// recoverOrphanedRuns needs a journal writer to emit cancel entries;
	// without one it logs and falls through, and the agent reset still
	// runs. Wire the production writer so the full path executes.
	s.journalWriter = journal.NewWriter(db.DB, logger, journal.WriterOptions{FlushSize: 1})
	defer s.journalWriter.Close()

	s.recoverOrphanedRuns(context.Background())
	_ = s.journalWriter.Flush(context.Background())
	time.Sleep(50 * time.Millisecond)

	// Agent must be IDLE after recovery.
	var status string
	if err := db.QueryRow("SELECT status FROM agents WHERE id='a1'").Scan(&status); err != nil {
		t.Fatal(err)
	}
	if status != "IDLE" {
		t.Errorf("agent status = %q, want IDLE", status)
	}

	// The trace must now have a run.cancelled terminal entry.
	var terminal string
	if err := db.QueryRow(`SELECT entry_type FROM journal_entries
		WHERE trace_id = 'r1' AND entry_type IN ('run.completed','run.failed','run.cancelled','run.timeout')
		LIMIT 1`).Scan(&terminal); err != nil {
		t.Fatalf("expected terminal run entry: %v", err)
	}
	if terminal != "run.cancelled" {
		t.Errorf("terminal entry = %q, want run.cancelled", terminal)
	}
}

func TestConvStoreAdapter_Read(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	logger := logging.New("error", "json", nil)
	store := conversation.NewStore(dir, logger)
	ctx := context.Background()
	_ = store.Append(ctx, "chat-1", conversation.Message{
		ID: "m1", Role: "user", Content: "hi", Timestamp: time.Now(),
	})
	_ = store.Append(ctx, "chat-1", conversation.Message{
		ID: "m2", Role: "assistant", Content: "hello", Timestamp: time.Now(),
	})

	a := &convStoreAdapter{store: store}
	out, err := a.Read(ctx, "chat-1", 0, 10)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if len(out) != 2 || out[0].Role != "user" || out[1].Content != "hello" {
		t.Errorf("unexpected messages: %+v", out)
	}
}

func TestEnsureFileWatcher_NoOpIfNil(t *testing.T) {
	t.Parallel()
	s := &Server{}
	s.ensureFileWatcher("crew-1") // must not panic
}

func TestSetChatHandler_AndChannelAuthorizer(t *testing.T) {
	t.Parallel()
	s := newTestServer()
	// These are pure setters — call them and verify no panic.
	s.SetChatHandler(nil)
	s.SetChannelAuthorizer(nil)
}

func TestDeps_CloseIsNilSafe(t *testing.T) {
	t.Parallel()
	var d *Deps
	d.Close()
	d = &Deps{}
	d.Close() // no state provider — must not panic
}

// closableState wraps mockState with a Close that records the call.
type closableState struct {
	*mockState
	closed bool
}

func (c *closableState) Close() error {
	c.closed = true
	return nil
}

func TestDeps_ClosesStateProvider(t *testing.T) {
	t.Parallel()
	cs := &closableState{mockState: newMockState()}
	d := &Deps{State: cs}
	d.Close()
	if !cs.closed {
		t.Error("expected state Close() to be called")
	}
}

// TestNew_WithJWTSecret_MountsAPI verifies the API router actually gets
// mounted when a JWT secret is provided in config.
func TestNew_WithJWTSecret_MountsAPI(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	db, err := database.Open("file:" + filepath.Join(dir, "api.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })

	logger := logging.New("error", "json", nil)
	if err := database.Migrate(context.Background(), db.DB, logger); err != nil {
		t.Fatal(err)
	}

	cfg := config.Default()
	cfg.Auth.JWTSecret = "supersecretkeythatisatleast32chars!!"
	cfg.Auth.InternalToken = "internal-tok"
	cfg.Storage.BasePath = dir

	s := New(cfg, logger, &Deps{DB: db.DB})
	t.Cleanup(func() {
		if s.fileWatcher != nil {
			s.fileWatcher.Close()
		}
	})
	if s.apiRouter == nil {
		t.Error("expected apiRouter mounted with JWT secret")
	}
}

// mustExec is a small helper that fails the test on a SQL error.
func mustExec(t *testing.T, db *sql.DB, q string, args ...any) {
	t.Helper()
	if _, err := db.Exec(q, args...); err != nil {
		t.Fatalf("exec %q: %v", q, err)
	}
}

// failingStorage forces Write to fail.
type failingStorage struct{ provider.StorageProvider }

func (failingStorage) Write(_ context.Context, _ string, _ io.Reader) error {
	return io.ErrShortWrite
}

func TestHandleFileSave_StorageWriteFailure(t *testing.T) {
	t.Parallel()
	cfg := config.Default()
	cfg.Auth.JWTSecret = "test-secret-for-routes-more-test-32ch"
	cfg.Storage.BasePath = t.TempDir()
	logger := logging.New("error", "json", nil)
	s := New(cfg, logger, &Deps{DB: openTestDB(t)})
	s.startedAt = time.Now()
	s.storage = failingStorage{}
	t.Cleanup(func() {
		if s.fileWatcher != nil {
			s.fileWatcher.Close()
		}
	})

	req := httptest.NewRequest("PUT", "/crews/crewA/files/save?path=crewA/x", strings.NewReader("data"))
	rec := httptest.NewRecorder()
	s.ipcMux.ServeHTTP(rec, req)
	if rec.Code != http.StatusInternalServerError {
		t.Errorf("status = %d, want 500", rec.Code)
	}
}
