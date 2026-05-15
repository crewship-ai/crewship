package server

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/config"
	"github.com/crewship-ai/crewship/internal/database"
	"github.com/crewship-ai/crewship/internal/llmproxy"
	"github.com/crewship-ai/crewship/internal/logging"
	"github.com/crewship-ai/crewship/internal/provider"
)

// execMockContainer extends mockContainer to return canned Exec output so we
// can drive handleContainerFileList / handleContainerGitLog through their
// happy paths.
type execMockContainer struct {
	*mockContainer
	output string
	mu     sync.Mutex
	calls  int
}

func (e *execMockContainer) Exec(_ context.Context, _ provider.ExecConfig) (*provider.ExecResult, error) {
	e.mu.Lock()
	e.calls++
	e.mu.Unlock()
	return &provider.ExecResult{
		ExecID: "x",
		Reader: io.NopCloser(strings.NewReader(e.output)),
	}, nil
}

func TestHandleContainerFileList_ParsesFindOutput(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	db, err := database.Open("file:" + filepath.Join(dir, "fl.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	if err := database.Migrate(context.Background(), db.DB, newSilentLogger()); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Format(time.RFC3339)
	mustExec(t, db.DB, `INSERT INTO workspaces (id, name, slug, created_at, updated_at) VALUES ('w1','W','w',?,?)`, now, now)
	mustExec(t, db.DB, `INSERT INTO crews (id, workspace_id, name, slug, created_at, updated_at) VALUES ('c1','w1','C','crew-x',?,?)`, now, now)

	cfg := config.Default()
	cfg.Auth.JWTSecret = "test-secret-for-server-tests-32-chars"
	logger := logging.New("error", "json", nil)
	mock := &execMockContainer{
		mockContainer: &mockContainer{},
		output:        "d 4096 1234567890 /home\nf 128 1234567891 /home/notes.txt\nf 256 1234567892 /home/x.go\n",
	}
	s := New(cfg, logger, &Deps{Container: mock, DB: db.DB})
	s.startedAt = time.Now()
	t.Cleanup(func() {
		s.StopBackground()
		if s.fileWatcher != nil {
			s.fileWatcher.Close()
		}
	})

	req := httptest.NewRequest("GET", "/crews/c1/container-files", nil)
	rec := httptest.NewRecorder()
	s.ipcMux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body %s", rec.Code, rec.Body.String())
	}
	var body map[string]interface{}
	_ = json.Unmarshal(rec.Body.Bytes(), &body)
	files, ok := body["files"].([]interface{})
	if !ok || len(files) < 2 {
		t.Errorf("expected ≥2 files, got %v", body["files"])
	}
}

func TestHandleContainerFileList_InvalidSubdir(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	db, err := database.Open("file:" + filepath.Join(dir, "fl2.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	if err := database.Migrate(context.Background(), db.DB, newSilentLogger()); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Format(time.RFC3339)
	mustExec(t, db.DB, `INSERT INTO workspaces (id, name, slug, created_at, updated_at) VALUES ('w1','W','w',?,?)`, now, now)
	mustExec(t, db.DB, `INSERT INTO crews (id, workspace_id, name, slug, created_at, updated_at) VALUES ('c1','w1','C','crew-x',?,?)`, now, now)

	cfg := config.Default()
	cfg.Auth.JWTSecret = "test-secret-for-server-tests-32-chars"
	logger := logging.New("error", "json", nil)
	s := New(cfg, logger, &Deps{Container: &mockContainer{}, DB: db.DB})
	s.startedAt = time.Now()
	t.Cleanup(func() {
		s.StopBackground()
		if s.fileWatcher != nil {
			s.fileWatcher.Close()
		}
	})

	req := httptest.NewRequest("GET", "/crews/c1/container-files?subdir=../../etc", nil)
	rec := httptest.NewRecorder()
	s.ipcMux.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", rec.Code)
	}
}

func TestHandleContainerGitLog_ParsesCommits(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	db, err := database.Open("file:" + filepath.Join(dir, "gl.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	if err := database.Migrate(context.Background(), db.DB, newSilentLogger()); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Format(time.RFC3339)
	mustExec(t, db.DB, `INSERT INTO workspaces (id, name, slug, created_at, updated_at) VALUES ('w1','W','w',?,?)`, now, now)
	mustExec(t, db.DB, `INSERT INTO crews (id, workspace_id, name, slug, created_at, updated_at) VALUES ('c1','w1','C','crew-x',?,?)`, now, now)

	cfg := config.Default()
	cfg.Auth.JWTSecret = "test-secret-for-server-tests-32-chars"
	logger := logging.New("error", "json", nil)
	mock := &execMockContainer{
		mockContainer: &mockContainer{},
		output:        "abc1234|first commit|alice|2026-04-01T12:00:00Z\nfeed5678|second commit|bob|2026-04-02T13:00:00Z\n",
	}
	s := New(cfg, logger, &Deps{Container: mock, DB: db.DB})
	s.startedAt = time.Now()
	t.Cleanup(func() {
		s.StopBackground()
		if s.fileWatcher != nil {
			s.fileWatcher.Close()
		}
	})

	req := httptest.NewRequest("GET", "/crews/c1/git-log", nil)
	rec := httptest.NewRecorder()
	s.ipcMux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body %s", rec.Code, rec.Body.String())
	}
	var body map[string]interface{}
	_ = json.Unmarshal(rec.Body.Bytes(), &body)
	commits, ok := body["commits"].([]interface{})
	if !ok || len(commits) != 2 {
		t.Fatalf("expected 2 commits, got %v", body["commits"])
	}
	first, _ := commits[0].(map[string]interface{})
	if first["hash"] != "abc1234" || first["message"] != "first commit" {
		t.Errorf("first commit parsed wrong: %+v", first)
	}
}

// (handleAgentStart success path skipped: the spawned goroutine calls
// orchestrator.RunAgent which requires too many real subsystems to fake.)

func TestHandleChatMessages_ReadStore(t *testing.T) {
	t.Parallel()
	s := newTestServer()
	// Trigger error path by passing a chat ID that doesn't exist — store returns
	// nil messages. The handler swallows errors and returns 200 with empty list.
	req := httptest.NewRequest("GET", "/chats/missing/messages", nil)
	rec := httptest.NewRecorder()
	s.ipcMux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
}

// TestHandleCredentialToken_FoundReturnsToken seeds the pool and confirms the
// token-fetch endpoint hands back the full credential record.
func TestHandleCredentialToken_FoundReturnsToken(t *testing.T) {
	t.Parallel()
	s := newTestServer()
	s.tokenPool.Update([]llmproxy.ProviderConnection{
		{
			ID: "c1", WorkspaceID: "w1", Provider: llmproxy.ProviderAnthropic,
			AccessToken: "tok-XYZ", Status: llmproxy.StatusActive,
		},
	})
	req := httptest.NewRequest("GET", "/credentials/w1/token?provider=ANTHROPIC", nil)
	rec := httptest.NewRecorder()
	s.ipcMux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body %s", rec.Code, rec.Body.String())
	}
	var body map[string]interface{}
	_ = json.Unmarshal(rec.Body.Bytes(), &body)
	if body["access_token"] != "tok-XYZ" {
		t.Errorf("got %v", body["access_token"])
	}
}
