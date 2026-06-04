package api

// Branch-coverage tests for the sidecar-facing internal/IPC handlers in:
//   internal_chat.go, internal_runs.go, internal_status.go,
//   internal_skills.go, internal_routines.go, missions_internal.go,
//   internal_credentials.go, internal_credentials_mutate.go
//
// These endpoints sit behind X-Internal-Token auth (validated upstream),
// so the handler bodies trust the caller and read from the request body /
// query + DB. We exercise the uncovered branches: invalid JSON (400),
// missing-required-field (400), not-found (404), happy paths asserting DB
// state, and DB-error 500 branches via fault injection (db.Close() before
// invoking with an otherwise-valid request).
//
// SKIPPED (orchestrator / Docker / LLM paths, per task scope):
//   - SkillInternalAdapter.Generate beyond the nil-adapter + missing-ws
//     guards (the success path fires a real LLM call via the public
//     SkillGenerateHandler).
//   - RoutineInternalAdapter.CreateSchedule beyond nil-adapter +
//     missing-ws (success path delegates to PipelineHandler with cron
//     parsing + store wiring not built here).
//   - CredentialInternalAdapter.Create/Rotate beyond nil-adapter +
//     envelope 400/401 (success path delegates to the public
//     CredentialHandler with capability gating + encryption wiring).
//   - InternalMissionHandler.Start MissionEngine kick-off (nil engine is
//     a tolerated no-op; the DB transition is still asserted).
//   - UpdateRun postRunTrigger / hub broadcast side effects (nil hub +
//     nil trigger are no-ops; journal emit + agent status are asserted).

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/encryption"
)

// covIISeedChat inserts a chat row (requires agent + workspace to satisfy FKs).
func covIISeedChat(t *testing.T, db *sql.DB, id, agentID, wsID, createdBy, title string) {
	t.Helper()
	var by interface{} = createdBy
	if createdBy == "" {
		by = nil
	}
	var ti interface{} = title
	if title == "" {
		ti = nil
	}
	if _, err := db.Exec(
		`INSERT INTO chats (id, agent_id, workspace_id, created_by, title, mode, status)
		 VALUES (?, ?, ?, ?, ?, 'CHAT', 'ACTIVE')`,
		id, agentID, wsID, by, ti); err != nil {
		t.Fatalf("seed chat %s: %v", id, err)
	}
}

// covIISeedAICred inserts an AI_CLI_TOKEN credential that ListCredentials
// will surface (type + provider pass the WHERE filter). The encrypted_value
// is a real ciphertext so include_values decryption succeeds.
func covIISeedAICred(t *testing.T, db *sql.DB, id, wsID, userID, name, plaintext string) {
	t.Helper()
	enc, err := encryption.Encrypt(plaintext)
	if err != nil {
		t.Fatalf("encrypt cred value: %v", err)
	}
	if _, err := db.Exec(
		`INSERT INTO credentials (id, workspace_id, name, encrypted_value, type, provider, scope, status, created_by)
		 VALUES (?, ?, ?, ?, 'AI_CLI_TOKEN', 'ANTHROPIC', 'WORKSPACE', 'ACTIVE', ?)`,
		id, wsID, name, enc, userID); err != nil {
		t.Fatalf("seed AI cred %s: %v", id, err)
	}
}

// ---------------------------------------------------------------------------
// internal_chat.go
// ---------------------------------------------------------------------------

func TestCovIICreateChat(t *testing.T) {
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	seedCrewRow(t, db, "crewC", wsID, "Crew", "crew")
	seedAgentRow(t, db, "agentC", wsID, "crewC", "Ann", "ann", "AGENT")

	em := &emitRecorder{}
	h := &InternalHandler{db: db, logger: newTestLogger(), journal: em}

	t.Run("invalid JSON → 400", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/api/v1/internal/chats", strings.NewReader("{bad"))
		rec := httptest.NewRecorder()
		h.CreateChat(rec, req)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("code=%d want 400; body=%s", rec.Code, rec.Body.String())
		}
	})

	t.Run("missing required → 400", func(t *testing.T) {
		body, _ := json.Marshal(map[string]any{"chat_id": "c1"})
		req := httptest.NewRequest(http.MethodPost, "/api/v1/internal/chats", bytes.NewReader(body))
		rec := httptest.NewRecorder()
		h.CreateChat(rec, req)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("code=%d want 400", rec.Code)
		}
	})

	t.Run("happy path → 201 and DB row", func(t *testing.T) {
		body, _ := json.Marshal(map[string]any{
			"chat_id": "chatNew", "agent_id": "agentC", "workspace_id": wsID,
		})
		req := httptest.NewRequest(http.MethodPost, "/api/v1/internal/chats", bytes.NewReader(body))
		rec := httptest.NewRecorder()
		h.CreateChat(rec, req)
		if rec.Code != http.StatusCreated {
			t.Fatalf("code=%d want 201; body=%s", rec.Code, rec.Body.String())
		}
		var n int
		if err := db.QueryRow("SELECT COUNT(*) FROM chats WHERE id = 'chatNew'").Scan(&n); err != nil {
			t.Fatalf("scan chat count: %v", err)
		}
		if n != 1 {
			t.Fatalf("expected chat row, got %d", n)
		}
	})

	t.Run("already exists → 200 already_exists", func(t *testing.T) {
		body, _ := json.Marshal(map[string]any{
			"chat_id": "chatNew", "agent_id": "agentC", "workspace_id": wsID,
		})
		req := httptest.NewRequest(http.MethodPost, "/api/v1/internal/chats", bytes.NewReader(body))
		rec := httptest.NewRecorder()
		h.CreateChat(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("code=%d want 200; body=%s", rec.Code, rec.Body.String())
		}
		if !strings.Contains(rec.Body.String(), "already_exists") {
			t.Fatalf("body=%s want already_exists", rec.Body.String())
		}
	})
}

func TestCovIICreateChatDBError(t *testing.T) {
	db := setupTestDB(t)
	em := &emitRecorder{}
	h := &InternalHandler{db: db, logger: newTestLogger(), journal: em}
	db.Close() // fault injection

	body, _ := json.Marshal(map[string]any{
		"chat_id": "x", "agent_id": "a", "workspace_id": "w",
	})
	req := httptest.NewRequest(http.MethodPost, "/api/v1/internal/chats", bytes.NewReader(body))
	rec := httptest.NewRecorder()
	h.CreateChat(rec, req)
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("code=%d want 500", rec.Code)
	}
}

func TestCovIIResolveChatNotFound(t *testing.T) {
	db := setupTestDB(t)
	h := &InternalHandler{db: db, logger: newTestLogger(), journal: &emitRecorder{}}

	req := httptest.NewRequest(http.MethodGet, "/api/v1/internal/chats/nope/resolve", nil)
	req.SetPathValue("chatId", "nope")
	rec := httptest.NewRecorder()
	h.ResolveChat(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("code=%d want 404; body=%s", rec.Code, rec.Body.String())
	}
}

func TestCovIIResolveChatDBError(t *testing.T) {
	db := setupTestDB(t)
	h := &InternalHandler{db: db, logger: newTestLogger(), journal: &emitRecorder{}}
	db.Close()

	req := httptest.NewRequest(http.MethodGet, "/api/v1/internal/chats/x/resolve", nil)
	req.SetPathValue("chatId", "x")
	rec := httptest.NewRecorder()
	h.ResolveChat(rec, req)
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("code=%d want 500", rec.Code)
	}
}

func TestCovIIIncrementMessageCount(t *testing.T) {
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	seedCrewRow(t, db, "crewI", wsID, "Crew", "crew")
	seedAgentRow(t, db, "agentI", wsID, "crewI", "Ivy", "ivy", "AGENT")
	covIISeedChat(t, db, "chatI", "agentI", wsID, userID, "")

	h := &InternalHandler{db: db, logger: newTestLogger(), journal: &emitRecorder{}}

	t.Run("invalid delta → 400", func(t *testing.T) {
		body, _ := json.Marshal(map[string]any{"delta": 0})
		req := httptest.NewRequest(http.MethodPost, "/x", bytes.NewReader(body))
		req.SetPathValue("chatId", "chatI")
		rec := httptest.NewRecorder()
		h.IncrementMessageCount(rec, req)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("code=%d want 400", rec.Code)
		}
	})

	t.Run("unknown chat → 404", func(t *testing.T) {
		body, _ := json.Marshal(map[string]any{"delta": 2})
		req := httptest.NewRequest(http.MethodPost, "/x", bytes.NewReader(body))
		req.SetPathValue("chatId", "ghost")
		rec := httptest.NewRecorder()
		h.IncrementMessageCount(rec, req)
		if rec.Code != http.StatusNotFound {
			t.Fatalf("code=%d want 404", rec.Code)
		}
	})

	t.Run("happy path increments", func(t *testing.T) {
		body, _ := json.Marshal(map[string]any{"delta": 3})
		req := httptest.NewRequest(http.MethodPost, "/x", bytes.NewReader(body))
		req.SetPathValue("chatId", "chatI")
		rec := httptest.NewRecorder()
		h.IncrementMessageCount(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("code=%d want 200; body=%s", rec.Code, rec.Body.String())
		}
		var n int
		if err := db.QueryRow("SELECT message_count FROM chats WHERE id = 'chatI'").Scan(&n); err != nil {
			t.Fatalf("scan message_count: %v", err)
		}
		if n != 3 {
			t.Fatalf("message_count=%d want 3", n)
		}
	})
}

func TestCovIIUpdateChatTitle(t *testing.T) {
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	seedCrewRow(t, db, "crewT", wsID, "Crew", "crew")
	seedAgentRow(t, db, "agentT", wsID, "crewT", "Tom", "tom", "AGENT")
	covIISeedChat(t, db, "chatT", "agentT", wsID, userID, "")         // untitled
	covIISeedChat(t, db, "chatTitled", "agentT", wsID, userID, "Set") // already titled

	h := &InternalHandler{db: db, logger: newTestLogger(), journal: &emitRecorder{}}

	t.Run("empty title → 400", func(t *testing.T) {
		body, _ := json.Marshal(map[string]any{"title": ""})
		req := httptest.NewRequest(http.MethodPatch, "/x", bytes.NewReader(body))
		req.SetPathValue("chatId", "chatT")
		rec := httptest.NewRecorder()
		h.UpdateChatTitle(rec, req)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("code=%d want 400", rec.Code)
		}
	})

	t.Run("already titled → 404", func(t *testing.T) {
		body, _ := json.Marshal(map[string]any{"title": "New"})
		req := httptest.NewRequest(http.MethodPatch, "/x", bytes.NewReader(body))
		req.SetPathValue("chatId", "chatTitled")
		rec := httptest.NewRecorder()
		h.UpdateChatTitle(rec, req)
		if rec.Code != http.StatusNotFound {
			t.Fatalf("code=%d want 404", rec.Code)
		}
	})

	t.Run("happy path sets title", func(t *testing.T) {
		body, _ := json.Marshal(map[string]any{"title": "Hello"})
		req := httptest.NewRequest(http.MethodPatch, "/x", bytes.NewReader(body))
		req.SetPathValue("chatId", "chatT")
		rec := httptest.NewRecorder()
		h.UpdateChatTitle(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("code=%d want 200; body=%s", rec.Code, rec.Body.String())
		}
		var title string
		if err := db.QueryRow("SELECT title FROM chats WHERE id = 'chatT'").Scan(&title); err != nil {
			t.Fatalf("scan title: %v", err)
		}
		if title != "Hello" {
			t.Fatalf("title=%q want Hello", title)
		}
	})
}

// ---------------------------------------------------------------------------
// internal_runs.go
// ---------------------------------------------------------------------------

func TestCovIICreateRun(t *testing.T) {
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	seedCrewRow(t, db, "crewR", wsID, "Crew", "crew")
	seedAgentRow(t, db, "agentR", wsID, "crewR", "Rob", "rob", "AGENT")

	em := &emitRecorder{}
	h := &InternalHandler{db: db, logger: newTestLogger(), journal: em}

	t.Run("invalid JSON → 400", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/x", strings.NewReader("{"))
		rec := httptest.NewRecorder()
		h.CreateRun(rec, req)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("code=%d want 400", rec.Code)
		}
	})

	t.Run("missing required → 400", func(t *testing.T) {
		body, _ := json.Marshal(map[string]any{"id": "r1"})
		req := httptest.NewRequest(http.MethodPost, "/x", bytes.NewReader(body))
		rec := httptest.NewRecorder()
		h.CreateRun(rec, req)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("code=%d want 400", rec.Code)
		}
	})

	t.Run("happy path → 201, emits run.started, flips agent RUNNING", func(t *testing.T) {
		body, _ := json.Marshal(map[string]any{
			"id": "run1", "agent_id": "agentR", "workspace_id": wsID,
			"chat_id": "chatX", "metadata": map[string]any{"k": "v"},
		})
		req := httptest.NewRequest(http.MethodPost, "/x", bytes.NewReader(body))
		rec := httptest.NewRecorder()
		h.CreateRun(rec, req)
		if rec.Code != http.StatusCreated {
			t.Fatalf("code=%d want 201; body=%s", rec.Code, rec.Body.String())
		}
		if len(em.entries) != 1 {
			t.Fatalf("expected 1 journal entry, got %d", len(em.entries))
		}
		var status string
		if err := db.QueryRow("SELECT status FROM agents WHERE id = 'agentR'").Scan(&status); err != nil {
			t.Fatalf("scan agent status: %v", err)
		}
		if status != "RUNNING" {
			t.Fatalf("agent status=%q want RUNNING", status)
		}
	})
}

func TestCovIIUpdateRun(t *testing.T) {
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	seedCrewRow(t, db, "crewU", wsID, "Crew", "crew")
	seedAgentRow(t, db, "agentU", wsID, "crewU", "Uma", "uma", "AGENT")

	em := &emitRecorder{}
	h := &InternalHandler{db: db, logger: newTestLogger(), journal: em}

	// Seed a run.started journal entry so the terminal lookup succeeds.
	if _, err := db.Exec(
		`INSERT INTO journal_entries (id, workspace_id, agent_id, entry_type, severity, actor_type, summary, trace_id)
		 VALUES ('je1', ?, 'agentU', 'run.started', 'info', 'sidecar', 'started', 'runU')`,
		wsID); err != nil {
		t.Fatalf("seed journal: %v", err)
	}

	t.Run("invalid JSON → 400", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPatch, "/x", strings.NewReader("{"))
		req.SetPathValue("runId", "runU")
		rec := httptest.NewRecorder()
		h.UpdateRun(rec, req)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("code=%d want 400", rec.Code)
		}
	})

	t.Run("invalid status → 400", func(t *testing.T) {
		body, _ := json.Marshal(map[string]any{"status": "BOGUS"})
		req := httptest.NewRequest(http.MethodPatch, "/x", bytes.NewReader(body))
		req.SetPathValue("runId", "runU")
		rec := httptest.NewRecorder()
		h.UpdateRun(rec, req)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("code=%d want 400", rec.Code)
		}
	})

	t.Run("non-terminal RUNNING → 200 no-op", func(t *testing.T) {
		body, _ := json.Marshal(map[string]any{"status": "RUNNING"})
		req := httptest.NewRequest(http.MethodPatch, "/x", bytes.NewReader(body))
		req.SetPathValue("runId", "runU")
		rec := httptest.NewRecorder()
		h.UpdateRun(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("code=%d want 200", rec.Code)
		}
	})

	t.Run("terminal lookup miss → 404", func(t *testing.T) {
		body, _ := json.Marshal(map[string]any{"status": "COMPLETED"})
		req := httptest.NewRequest(http.MethodPatch, "/x", bytes.NewReader(body))
		req.SetPathValue("runId", "noSuchRun")
		rec := httptest.NewRecorder()
		h.UpdateRun(rec, req)
		if rec.Code != http.StatusNotFound {
			t.Fatalf("code=%d want 404; body=%s", rec.Code, rec.Body.String())
		}
	})

	t.Run("happy COMPLETED → 200, emits terminal entry, agent IDLE", func(t *testing.T) {
		exit := 0
		before := len(em.entries)
		body, _ := json.Marshal(map[string]any{"status": "COMPLETED", "exit_code": exit})
		req := httptest.NewRequest(http.MethodPatch, "/x", bytes.NewReader(body))
		req.SetPathValue("runId", "runU")
		rec := httptest.NewRecorder()
		h.UpdateRun(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("code=%d want 200; body=%s", rec.Code, rec.Body.String())
		}
		// The terminal write goes through the journal Emitter (recorder),
		// not directly to journal_entries — assert the recorded entry type.
		if len(em.entries) != before+1 {
			t.Fatalf("expected 1 new journal entry, got %d", len(em.entries)-before)
		}
		if got := em.entries[len(em.entries)-1].Type; string(got) != "run.completed" {
			t.Fatalf("emitted type=%q want run.completed", got)
		}
		var status string
		if err := db.QueryRow("SELECT status FROM agents WHERE id='agentU'").Scan(&status); err != nil {
			t.Fatalf("scan agent status: %v", err)
		}
		if status != "IDLE" {
			t.Fatalf("agent status=%q want IDLE", status)
		}
	})

	t.Run("idempotent retry → 200 already-terminal echo", func(t *testing.T) {
		// The idempotency guard reads journal_entries directly, so seed the
		// terminal row the recorder didn't persist.
		if _, err := db.Exec(
			`INSERT INTO journal_entries (id, workspace_id, agent_id, entry_type, severity, actor_type, summary, trace_id)
			 VALUES ('je-term', ?, 'agentU', 'run.completed', 'info', 'sidecar', 'done', 'runU')`,
			wsID); err != nil {
			t.Fatalf("seed terminal entry: %v", err)
		}
		body, _ := json.Marshal(map[string]any{"status": "FAILED"})
		req := httptest.NewRequest(http.MethodPatch, "/x", bytes.NewReader(body))
		req.SetPathValue("runId", "runU")
		rec := httptest.NewRecorder()
		h.UpdateRun(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("code=%d want 200", rec.Code)
		}
		// Already recorded as COMPLETED — retry must echo that, not FAILED.
		if !strings.Contains(rec.Body.String(), "COMPLETED") {
			t.Fatalf("body=%s want COMPLETED echo", rec.Body.String())
		}
	})
}

// ---------------------------------------------------------------------------
// internal_status.go
// ---------------------------------------------------------------------------

func TestCovIIListCrews(t *testing.T) {
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	seedCrewRow(t, db, "crewL", wsID, "Alpha", "alpha")
	h := &InternalHandler{db: db, logger: newTestLogger(), journal: &emitRecorder{}}

	t.Run("missing workspace_id → 400", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/api/v1/internal/crews", nil)
		rec := httptest.NewRecorder()
		h.ListCrews(rec, req)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("code=%d want 400", rec.Code)
		}
	})

	t.Run("happy path lists", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/api/v1/internal/crews?workspace_id="+wsID, nil)
		rec := httptest.NewRecorder()
		h.ListCrews(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("code=%d want 200", rec.Code)
		}
		if !strings.Contains(rec.Body.String(), "alpha") {
			t.Fatalf("body=%s missing crew", rec.Body.String())
		}
	})

	t.Run("DB error → 500", func(t *testing.T) {
		db2 := setupTestDB(t)
		h2 := &InternalHandler{db: db2, logger: newTestLogger(), journal: &emitRecorder{}}
		db2.Close()
		req := httptest.NewRequest(http.MethodGet, "/api/v1/internal/crews?workspace_id=x", nil)
		rec := httptest.NewRecorder()
		h2.ListCrews(rec, req)
		if rec.Code != http.StatusInternalServerError {
			t.Fatalf("code=%d want 500", rec.Code)
		}
	})
}

func TestCovIICreateCrew(t *testing.T) {
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	h := &InternalHandler{db: db, logger: newTestLogger(), journal: &emitRecorder{}}

	t.Run("missing workspace_id → 400", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/api/v1/internal/crews", strings.NewReader("{}"))
		rec := httptest.NewRecorder()
		h.CreateCrew(rec, req)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("code=%d want 400", rec.Code)
		}
	})

	t.Run("invalid JSON → 400", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/x?workspace_id="+wsID, strings.NewReader("{bad"))
		rec := httptest.NewRecorder()
		h.CreateCrew(rec, req)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("code=%d want 400", rec.Code)
		}
	})

	t.Run("missing name → 400", func(t *testing.T) {
		body, _ := json.Marshal(map[string]any{"slug": "x"})
		req := httptest.NewRequest(http.MethodPost, "/x?workspace_id="+wsID, bytes.NewReader(body))
		rec := httptest.NewRecorder()
		h.CreateCrew(rec, req)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("code=%d want 400", rec.Code)
		}
	})

	t.Run("happy path → 201", func(t *testing.T) {
		body, _ := json.Marshal(map[string]any{"name": "Bravo", "icon": "x", "color": "#fff"})
		req := httptest.NewRequest(http.MethodPost, "/x?workspace_id="+wsID, bytes.NewReader(body))
		rec := httptest.NewRecorder()
		h.CreateCrew(rec, req)
		if rec.Code != http.StatusCreated {
			t.Fatalf("code=%d want 201; body=%s", rec.Code, rec.Body.String())
		}
		var n int
		if err := db.QueryRow("SELECT COUNT(*) FROM crews WHERE slug='bravo' AND workspace_id=?", wsID).Scan(&n); err != nil {
			t.Fatalf("scan crew count: %v", err)
		}
		if n != 1 {
			t.Fatalf("crew not inserted, count=%d", n)
		}
	})

	t.Run("duplicate slug → 409", func(t *testing.T) {
		body, _ := json.Marshal(map[string]any{"name": "Bravo"})
		req := httptest.NewRequest(http.MethodPost, "/x?workspace_id="+wsID, bytes.NewReader(body))
		rec := httptest.NewRecorder()
		h.CreateCrew(rec, req)
		if rec.Code != http.StatusConflict {
			t.Fatalf("code=%d want 409", rec.Code)
		}
	})
}

func TestCovIICreateAgent(t *testing.T) {
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	seedCrewRow(t, db, "crewA", wsID, "Crew", "crew")
	h := &InternalHandler{db: db, logger: newTestLogger(), journal: &emitRecorder{}}

	t.Run("missing workspace_id → 400", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/x", strings.NewReader("{}"))
		rec := httptest.NewRecorder()
		h.CreateAgent(rec, req)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("code=%d want 400", rec.Code)
		}
	})

	t.Run("invalid JSON → 400", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/x?workspace_id="+wsID, strings.NewReader("{bad"))
		rec := httptest.NewRecorder()
		h.CreateAgent(rec, req)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("code=%d want 400", rec.Code)
		}
	})

	t.Run("missing name/crew_id → 400", func(t *testing.T) {
		body, _ := json.Marshal(map[string]any{"name": "X"})
		req := httptest.NewRequest(http.MethodPost, "/x?workspace_id="+wsID, bytes.NewReader(body))
		rec := httptest.NewRecorder()
		h.CreateAgent(rec, req)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("code=%d want 400", rec.Code)
		}
	})

	t.Run("happy path → 201, slug suffixed with crew", func(t *testing.T) {
		body, _ := json.Marshal(map[string]any{"name": "Nora", "crew_id": "crewA"})
		req := httptest.NewRequest(http.MethodPost, "/x?workspace_id="+wsID, bytes.NewReader(body))
		rec := httptest.NewRecorder()
		h.CreateAgent(rec, req)
		if rec.Code != http.StatusCreated {
			t.Fatalf("code=%d want 201; body=%s", rec.Code, rec.Body.String())
		}
		var n int
		if err := db.QueryRow("SELECT COUNT(*) FROM agents WHERE slug='nora-crew' AND workspace_id=?", wsID).Scan(&n); err != nil {
			t.Fatalf("scan agent count: %v", err)
		}
		if n != 1 {
			t.Fatalf("agent not inserted with suffixed slug, count=%d", n)
		}
	})

	t.Run("duplicate slug → 409", func(t *testing.T) {
		body, _ := json.Marshal(map[string]any{"name": "Nora", "crew_id": "crewA"})
		req := httptest.NewRequest(http.MethodPost, "/x?workspace_id="+wsID, bytes.NewReader(body))
		rec := httptest.NewRecorder()
		h.CreateAgent(rec, req)
		if rec.Code != http.StatusConflict {
			t.Fatalf("code=%d want 409", rec.Code)
		}
	})
}

func TestCovIIListCrewConnections(t *testing.T) {
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	h := &InternalHandler{db: db, logger: newTestLogger(), journal: &emitRecorder{}}

	t.Run("missing workspace_id → 400", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/x", nil)
		rec := httptest.NewRecorder()
		h.ListCrewConnections(rec, req)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("code=%d want 400", rec.Code)
		}
	})

	t.Run("happy path empty list", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/x?workspace_id="+wsID+"&crew_id=c1", nil)
		rec := httptest.NewRecorder()
		h.ListCrewConnections(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("code=%d want 200; body=%s", rec.Code, rec.Body.String())
		}
		if strings.TrimSpace(rec.Body.String()) != "[]" {
			t.Fatalf("body=%q want []", rec.Body.String())
		}
	})

	t.Run("DB error → 500", func(t *testing.T) {
		db2 := setupTestDB(t)
		h2 := &InternalHandler{db: db2, logger: newTestLogger(), journal: &emitRecorder{}}
		db2.Close()
		req := httptest.NewRequest(http.MethodGet, "/x?workspace_id=w", nil)
		rec := httptest.NewRecorder()
		h2.ListCrewConnections(rec, req)
		if rec.Code != http.StatusInternalServerError {
			t.Fatalf("code=%d want 500", rec.Code)
		}
	})
}

func TestCovIIRecordMCPToolCall(t *testing.T) {
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	h := &InternalHandler{db: db, logger: newTestLogger(), journal: &emitRecorder{}}

	t.Run("invalid JSON → 400", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/x", strings.NewReader("{"))
		rec := httptest.NewRecorder()
		h.RecordMCPToolCall(rec, req)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("code=%d want 400", rec.Code)
		}
	})

	t.Run("missing required → 400", func(t *testing.T) {
		body, _ := json.Marshal(map[string]any{"workspace_id": wsID})
		req := httptest.NewRequest(http.MethodPost, "/x", bytes.NewReader(body))
		rec := httptest.NewRecorder()
		h.RecordMCPToolCall(rec, req)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("code=%d want 400", rec.Code)
		}
	})

	t.Run("happy path → 201 (default scope)", func(t *testing.T) {
		body, _ := json.Marshal(map[string]any{
			"workspace_id": wsID, "agent_id": "a1", "mcp_server_id": "srv1",
			"tool_name": "search", "status": "success", "duration_ms": 12,
		})
		req := httptest.NewRequest(http.MethodPost, "/x", bytes.NewReader(body))
		rec := httptest.NewRecorder()
		h.RecordMCPToolCall(rec, req)
		if rec.Code != http.StatusCreated {
			t.Fatalf("code=%d want 201; body=%s", rec.Code, rec.Body.String())
		}
		var scope string
		if err := db.QueryRow("SELECT mcp_server_scope FROM mcp_tool_calls WHERE mcp_server_id='srv1'").Scan(&scope); err != nil {
			t.Fatalf("scan scope: %v", err)
		}
		if scope != "workspace" {
			t.Fatalf("scope=%q want workspace (default)", scope)
		}
	})

	t.Run("DB error → 500", func(t *testing.T) {
		db2 := setupTestDB(t)
		h2 := &InternalHandler{db: db2, logger: newTestLogger(), journal: &emitRecorder{}}
		db2.Close()
		body, _ := json.Marshal(map[string]any{
			"workspace_id": "w", "agent_id": "a", "mcp_server_id": "s",
			"tool_name": "t", "status": "success",
		})
		req := httptest.NewRequest(http.MethodPost, "/x", bytes.NewReader(body))
		rec := httptest.NewRecorder()
		h2.RecordMCPToolCall(rec, req)
		if rec.Code != http.StatusInternalServerError {
			t.Fatalf("code=%d want 500", rec.Code)
		}
	})
}

// ---------------------------------------------------------------------------
// internal_credentials.go
// ---------------------------------------------------------------------------

func TestCovIIListCredentials(t *testing.T) {
	setTestEncryptionKey(t)
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	covIISeedAICred(t, db, "credL", wsID, userID, "anthropic-key", "sk-ant-secret")

	h := &InternalHandler{db: db, logger: newTestLogger(), journal: &emitRecorder{}}

	t.Run("metadata only when non-loopback (no token leaked)", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/x?workspace_id="+wsID+"&include_values=true", nil)
		req.RemoteAddr = "192.0.2.10:5000" // non-loopback
		rec := httptest.NewRecorder()
		h.ListCredentials(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("code=%d want 200; body=%s", rec.Code, rec.Body.String())
		}
		if strings.Contains(rec.Body.String(), "sk-ant-secret") {
			t.Fatalf("plaintext token leaked to non-loopback caller: %s", rec.Body.String())
		}
	})

	t.Run("loopback + include_values → token present", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/x?workspace_id="+wsID+"&include_values=true", nil)
		req.RemoteAddr = "127.0.0.1:5000"
		rec := httptest.NewRecorder()
		h.ListCredentials(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("code=%d want 200; body=%s", rec.Code, rec.Body.String())
		}
		if !strings.Contains(rec.Body.String(), "sk-ant-secret") {
			t.Fatalf("expected decrypted token for loopback caller: %s", rec.Body.String())
		}
	})

	t.Run("DB error → 500", func(t *testing.T) {
		db2 := setupTestDB(t)
		h2 := &InternalHandler{db: db2, logger: newTestLogger(), journal: &emitRecorder{}}
		db2.Close()
		req := httptest.NewRequest(http.MethodGet, "/x?workspace_id=w", nil)
		rec := httptest.NewRecorder()
		h2.ListCredentials(rec, req)
		if rec.Code != http.StatusInternalServerError {
			t.Fatalf("code=%d want 500", rec.Code)
		}
	})
}

func TestCovIIUpdateCredentialStatus(t *testing.T) {
	setTestEncryptionKey(t)
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	covIISeedAICred(t, db, "credS", wsID, userID, "key", "plain")

	h := &InternalHandler{db: db, logger: newTestLogger(), journal: &emitRecorder{}}

	t.Run("invalid JSON → 400", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPatch, "/x", strings.NewReader("{"))
		req.SetPathValue("credentialId", "credS")
		rec := httptest.NewRecorder()
		h.UpdateCredentialStatus(rec, req)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("code=%d want 400", rec.Code)
		}
	})

	t.Run("invalid status → 400", func(t *testing.T) {
		body, _ := json.Marshal(map[string]any{"status": "BOGUS"})
		req := httptest.NewRequest(http.MethodPatch, "/x", bytes.NewReader(body))
		req.SetPathValue("credentialId", "credS")
		rec := httptest.NewRecorder()
		h.UpdateCredentialStatus(rec, req)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("code=%d want 400", rec.Code)
		}
	})

	t.Run("unknown credential → 404", func(t *testing.T) {
		body, _ := json.Marshal(map[string]any{"status": "ACTIVE"})
		req := httptest.NewRequest(http.MethodPatch, "/x", bytes.NewReader(body))
		req.SetPathValue("credentialId", "ghost")
		rec := httptest.NewRecorder()
		h.UpdateCredentialStatus(rec, req)
		if rec.Code != http.StatusNotFound {
			t.Fatalf("code=%d want 404", rec.Code)
		}
	})

	t.Run("happy path updates status + tokens", func(t *testing.T) {
		last := "boom"
		exp := "2030-01-01T00:00:00Z"
		at := "new-access"
		rt := "new-refresh"
		body, _ := json.Marshal(map[string]any{
			"status": "ERROR", "last_error": last,
			"access_token": at, "refresh_token": rt, "token_expires_at": exp,
		})
		req := httptest.NewRequest(http.MethodPatch, "/x?workspace_id="+wsID, bytes.NewReader(body))
		req.SetPathValue("credentialId", "credS")
		rec := httptest.NewRecorder()
		h.UpdateCredentialStatus(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("code=%d want 200; body=%s", rec.Code, rec.Body.String())
		}
		var status, lastErr, encVal string
		db.QueryRow("SELECT status, last_error, encrypted_value FROM credentials WHERE id='credS'").
			Scan(&status, &lastErr, &encVal)
		if status != "ERROR" || lastErr != "boom" {
			t.Fatalf("status=%q last_error=%q", status, lastErr)
		}
		dec, err := encryption.Decrypt(encVal)
		if err != nil || dec != "new-access" {
			t.Fatalf("access token not re-encrypted: dec=%q err=%v", dec, err)
		}
	})

	t.Run("DB error → 500", func(t *testing.T) {
		db2 := setupTestDB(t)
		h2 := &InternalHandler{db: db2, logger: newTestLogger(), journal: &emitRecorder{}}
		db2.Close()
		body, _ := json.Marshal(map[string]any{"status": "ACTIVE"})
		req := httptest.NewRequest(http.MethodPatch, "/x", bytes.NewReader(body))
		req.SetPathValue("credentialId", "x")
		rec := httptest.NewRecorder()
		h2.UpdateCredentialStatus(rec, req)
		if rec.Code != http.StatusInternalServerError {
			t.Fatalf("code=%d want 500", rec.Code)
		}
	})
}

func TestCovIIGetWebhookSecret(t *testing.T) {
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	seedCrewRow(t, db, "crewW", wsID, "Crew", "crew")
	seedAgentRow(t, db, "agentW", wsID, "crewW", "Wes", "wes", "AGENT")
	// agentW seeded without a webhook_secret → NULL.
	if _, err := db.Exec(
		`INSERT INTO agents (id, workspace_id, crew_id, name, slug, agent_role, status, cli_adapter, tool_profile, timeout_seconds, memory_enabled, webhook_secret)
		 VALUES ('agentWS', ?, 'crewW', 'Wsec', 'wsec', 'AGENT', 'IDLE', 'CLAUDE_CODE', 'CODING', 1800, 0, 'whsec-123')`,
		wsID); err != nil {
		t.Fatalf("seed agent with secret: %v", err)
	}
	h := &InternalHandler{db: db, logger: newTestLogger(), journal: &emitRecorder{}}

	t.Run("unknown agent → 404", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/x", nil)
		req.SetPathValue("agentId", "ghost")
		rec := httptest.NewRecorder()
		h.GetWebhookSecret(rec, req)
		if rec.Code != http.StatusNotFound {
			t.Fatalf("code=%d want 404", rec.Code)
		}
	})

	t.Run("agent without secret → 404", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/x", nil)
		req.SetPathValue("agentId", "agentW")
		rec := httptest.NewRecorder()
		h.GetWebhookSecret(rec, req)
		if rec.Code != http.StatusNotFound {
			t.Fatalf("code=%d want 404", rec.Code)
		}
	})

	t.Run("happy path → 200 with secret", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/x", nil)
		req.SetPathValue("agentId", "agentWS")
		rec := httptest.NewRecorder()
		h.GetWebhookSecret(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("code=%d want 200; body=%s", rec.Code, rec.Body.String())
		}
		if !strings.Contains(rec.Body.String(), "whsec-123") {
			t.Fatalf("body=%s missing secret", rec.Body.String())
		}
	})

	t.Run("DB error → 500", func(t *testing.T) {
		db2 := setupTestDB(t)
		h2 := &InternalHandler{db: db2, logger: newTestLogger(), journal: &emitRecorder{}}
		db2.Close()
		req := httptest.NewRequest(http.MethodGet, "/x", nil)
		req.SetPathValue("agentId", "a")
		rec := httptest.NewRecorder()
		h2.GetWebhookSecret(rec, req)
		if rec.Code != http.StatusInternalServerError {
			t.Fatalf("code=%d want 500", rec.Code)
		}
	})
}

// ---------------------------------------------------------------------------
// missions_internal.go
// ---------------------------------------------------------------------------

func TestCovIIMissionCreate(t *testing.T) {
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	seedCrewRow(t, db, "crewM", wsID, "Crew", "crew")
	seedAgentRow(t, db, "lead1", wsID, "crewM", "Lead", "lead", "LEAD")

	h := NewInternalMissionHandler(db, nil, nil, newTestLogger())

	t.Run("invalid JSON → 400", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/x", strings.NewReader("{"))
		rec := httptest.NewRecorder()
		h.Create(rec, req)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("code=%d want 400", rec.Code)
		}
	})

	t.Run("missing required → 400", func(t *testing.T) {
		body, _ := json.Marshal(map[string]any{"title": "X"})
		req := httptest.NewRequest(http.MethodPost, "/x", bytes.NewReader(body))
		rec := httptest.NewRecorder()
		h.Create(rec, req)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("code=%d want 400", rec.Code)
		}
	})

	t.Run("happy path with tasks → 201", func(t *testing.T) {
		body, _ := json.Marshal(map[string]any{
			"title": "Ship it", "lead_agent_id": "lead1", "crew_id": "crewM", "workspace_id": wsID,
			"tasks": []map[string]any{
				{"title": "T1", "task_order": 0},
				{"title": "T2", "task_order": 1, "depends_on": []string{"T1"}},
			},
		})
		req := httptest.NewRequest(http.MethodPost, "/x", bytes.NewReader(body))
		rec := httptest.NewRecorder()
		h.Create(rec, req)
		if rec.Code != http.StatusCreated {
			t.Fatalf("code=%d want 201; body=%s", rec.Code, rec.Body.String())
		}
		var nm, nt int
		if err := db.QueryRow("SELECT COUNT(*) FROM missions WHERE title='Ship it'").Scan(&nm); err != nil {
			t.Fatalf("scan mission count: %v", err)
		}
		if err := db.QueryRow("SELECT COUNT(*) FROM mission_tasks").Scan(&nt); err != nil {
			t.Fatalf("scan task count: %v", err)
		}
		if nm != 1 || nt != 2 {
			t.Fatalf("missions=%d tasks=%d want 1/2", nm, nt)
		}
		// The second task depends on another → BLOCKED.
		var blocked int
		if err := db.QueryRow("SELECT COUNT(*) FROM mission_tasks WHERE status='BLOCKED'").Scan(&blocked); err != nil {
			t.Fatalf("scan blocked count: %v", err)
		}
		if blocked != 1 {
			t.Fatalf("blocked=%d want 1", blocked)
		}
	})

	t.Run("DB error → 500", func(t *testing.T) {
		db2 := setupTestDB(t)
		h2 := NewInternalMissionHandler(db2, nil, nil, newTestLogger())
		db2.Close()
		body, _ := json.Marshal(map[string]any{
			"title": "X", "lead_agent_id": "l", "crew_id": "c", "workspace_id": "w",
		})
		req := httptest.NewRequest(http.MethodPost, "/x", bytes.NewReader(body))
		rec := httptest.NewRecorder()
		h2.Create(rec, req)
		if rec.Code != http.StatusInternalServerError {
			t.Fatalf("code=%d want 500", rec.Code)
		}
	})
}

func TestCovIIMissionStartAndGet(t *testing.T) {
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	seedCrewRow(t, db, "crewMS", wsID, "Crew", "crew")
	seedAgentRow(t, db, "leadMS", wsID, "crewMS", "Lead", "lead", "LEAD")
	if _, err := db.Exec(
		`INSERT INTO missions (id, workspace_id, crew_id, lead_agent_id, trace_id, title, status)
		 VALUES ('m1', ?, 'crewMS', 'leadMS', 'tr-m1', 'M1', 'PLANNING')`, wsID); err != nil {
		t.Fatalf("seed mission: %v", err)
	}
	if _, err := db.Exec(
		`INSERT INTO missions (id, workspace_id, crew_id, lead_agent_id, trace_id, title, status)
		 VALUES ('m2', ?, 'crewMS', 'leadMS', 'tr-m2', 'M2', 'IN_PROGRESS')`, wsID); err != nil {
		t.Fatalf("seed mission2: %v", err)
	}

	h := NewInternalMissionHandler(db, nil, nil, newTestLogger())

	t.Run("Start: not found → 404", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/x", nil)
		req.SetPathValue("missionId", "ghost")
		rec := httptest.NewRecorder()
		h.Start(rec, req)
		if rec.Code != http.StatusNotFound {
			t.Fatalf("code=%d want 404", rec.Code)
		}
	})

	t.Run("Start: wrong state → 400", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/x", nil)
		req.SetPathValue("missionId", "m2")
		rec := httptest.NewRecorder()
		h.Start(rec, req)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("code=%d want 400", rec.Code)
		}
	})

	t.Run("Start: PLANNING → 200 IN_PROGRESS", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/x", nil)
		req.SetPathValue("missionId", "m1")
		rec := httptest.NewRecorder()
		h.Start(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("code=%d want 200; body=%s", rec.Code, rec.Body.String())
		}
		var st string
		if err := db.QueryRow("SELECT status FROM missions WHERE id='m1'").Scan(&st); err != nil {
			t.Fatalf("scan mission status: %v", err)
		}
		if st != "IN_PROGRESS" {
			t.Fatalf("status=%q want IN_PROGRESS", st)
		}
	})

	t.Run("Get: not found → 404", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/x", nil)
		req.SetPathValue("missionId", "ghost")
		rec := httptest.NewRecorder()
		h.Get(rec, req)
		if rec.Code != http.StatusNotFound {
			t.Fatalf("code=%d want 404", rec.Code)
		}
	})

	t.Run("Get: happy path → 200 with tasks key", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/x", nil)
		req.SetPathValue("missionId", "m1")
		rec := httptest.NewRecorder()
		h.Get(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("code=%d want 200; body=%s", rec.Code, rec.Body.String())
		}
		if !strings.Contains(rec.Body.String(), "\"mission\"") {
			t.Fatalf("body=%s missing mission key", rec.Body.String())
		}
	})
}

// ---------------------------------------------------------------------------
// internal_skills.go / internal_routines.go / internal_credentials_mutate.go
// (adapter validation branches only — success paths delegate to public
//  handlers that need LLM / cron / encryption wiring, skipped per scope)
// ---------------------------------------------------------------------------

func TestCovIISkillAdapterGuards(t *testing.T) {
	t.Run("nil adapter → 500", func(t *testing.T) {
		var h *SkillInternalAdapter
		req := httptest.NewRequest(http.MethodPost, "/x", nil)
		rec := httptest.NewRecorder()
		h.Generate(rec, req)
		if rec.Code != http.StatusInternalServerError {
			t.Fatalf("code=%d want 500", rec.Code)
		}
	})

	t.Run("missing workspace_id → 400", func(t *testing.T) {
		h := &SkillInternalAdapter{gen: &SkillGenerateHandler{}}
		req := httptest.NewRequest(http.MethodPost, "/x", nil)
		rec := httptest.NewRecorder()
		h.Generate(rec, req)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("code=%d want 400", rec.Code)
		}
	})
}

func TestCovIIRoutineAdapterGuards(t *testing.T) {
	t.Run("nil adapter → 500", func(t *testing.T) {
		var h *RoutineInternalAdapter
		req := httptest.NewRequest(http.MethodPost, "/x", nil)
		rec := httptest.NewRecorder()
		h.CreateSchedule(rec, req)
		if rec.Code != http.StatusInternalServerError {
			t.Fatalf("code=%d want 500", rec.Code)
		}
	})

	t.Run("missing workspace_id → 400", func(t *testing.T) {
		h := &RoutineInternalAdapter{pipes: &PipelineHandler{}}
		req := httptest.NewRequest(http.MethodPost, "/x", nil)
		rec := httptest.NewRecorder()
		h.CreateSchedule(rec, req)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("code=%d want 400", rec.Code)
		}
	})
}

func TestCovIICredentialAdapterGuards(t *testing.T) {
	t.Run("nil adapter Create → 500", func(t *testing.T) {
		var h *CredentialInternalAdapter
		req := httptest.NewRequest(http.MethodPost, "/x", nil)
		rec := httptest.NewRecorder()
		h.Create(rec, req)
		if rec.Code != http.StatusInternalServerError {
			t.Fatalf("code=%d want 500", rec.Code)
		}
	})

	t.Run("Create missing workspace_id → 400", func(t *testing.T) {
		h := &CredentialInternalAdapter{creds: &CredentialHandler{}}
		req := httptest.NewRequest(http.MethodPost, "/x", nil)
		rec := httptest.NewRecorder()
		h.Create(rec, req)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("code=%d want 400", rec.Code)
		}
	})

	t.Run("Create with ws but no caller → 401", func(t *testing.T) {
		h := &CredentialInternalAdapter{creds: &CredentialHandler{}}
		req := httptest.NewRequest(http.MethodPost, "/x?workspace_id=w", nil)
		rec := httptest.NewRecorder()
		h.Create(rec, req)
		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("code=%d want 401", rec.Code)
		}
	})

	t.Run("Rotate with ws but no caller → 401", func(t *testing.T) {
		h := &CredentialInternalAdapter{creds: &CredentialHandler{}}
		req := httptest.NewRequest(http.MethodPost, "/x?workspace_id=w", nil)
		req.SetPathValue("credentialId", "c1")
		rec := httptest.NewRecorder()
		h.Rotate(rec, req)
		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("code=%d want 401", rec.Code)
		}
	})
}
