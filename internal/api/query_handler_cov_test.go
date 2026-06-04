package api

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/encryption"
	"github.com/crewship-ai/crewship/internal/journal"
)

// covQRecordingEmitter is a journal.Emitter that records every emitted entry
// so tests can assert the dual-write / terminal-run journal paths in the
// query handler actually fire. Prefixed covQ per the harness rules.
type covQRecordingEmitter struct {
	mu      sync.Mutex
	entries []journal.Entry
}

func (e *covQRecordingEmitter) Emit(_ context.Context, entry journal.Entry) (string, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.entries = append(e.entries, entry)
	return "rec-" + string(entry.Type), nil
}

func (e *covQRecordingEmitter) Flush(_ context.Context) error { return nil }

func (e *covQRecordingEmitter) count() int {
	e.mu.Lock()
	defer e.mu.Unlock()
	return len(e.entries)
}

// TestCovQTruncate exercises both the no-cut and the cut+ellipsis branches
// of truncate, plus the n<=0 guard.
func TestCovQTruncate(t *testing.T) {
	cases := []struct {
		name string
		in   string
		n    int
		want string
	}{
		{"no cut needed", "hello", 10, "hello"},
		{"exact length", "hello", 5, "hello"},
		{"cut with ellipsis", "hello world", 5, "hello…"},
		{"n is zero returns input", "anything", 0, "anything"},
		{"n negative returns input", "anything", -3, "anything"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := truncate(tc.in, tc.n); got != tc.want {
				t.Errorf("truncate(%q,%d)=%q want %q", tc.in, tc.n, got, tc.want)
			}
		})
	}
}

// TestCovQSetJournal covers the nil->noop fallback branch and the
// real-emitter branch of SetJournal.
func TestCovQSetJournal(t *testing.T) {
	db := setupTestDB(t)
	h := NewQueryHandler(db, nil, nil, "token", newTestLogger())

	// nil maps to the no-op emitter (must not panic on later Emit).
	h.SetJournal(nil)
	if _, ok := h.journal.(noopEmitter); !ok {
		t.Fatalf("expected noopEmitter after SetJournal(nil), got %T", h.journal)
	}

	rec := &covQRecordingEmitter{}
	h.SetJournal(rec)
	if h.journal != rec {
		t.Fatalf("expected recording emitter to be wired, got %T", h.journal)
	}
}

// TestCovQCreateInvalidJSON covers the readJSON-failure 400 branch.
func TestCovQCreateInvalidJSON(t *testing.T) {
	db := setupTestDB(t)
	h := NewQueryHandler(db, nil, nil, "token", newTestLogger())

	req := httptest.NewRequest(http.MethodPost, "/api/v1/internal/queries",
		bytes.NewBufferString(`{not json`))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	h.Create(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for invalid JSON, got %d", w.Code)
	}
}

// TestCovQCreateNilOrchEmitsJournal drives Create with a nil orchestrator so
// the synchronous prelude runs end-to-end: from-agent lookup, target lookup,
// credential load (populated), peer_conversations insert, the running/started
// journal emits, then finishQuery's FAILED terminal emits. Asserting on the
// recording emitter covers the journal branches in both Create and finishQuery.
func TestCovQCreateNilOrchEmitsJournal(t *testing.T) {
	setTestEncryptionKey(t)
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)

	execOrFatal(t, db,
		`INSERT INTO crews (id, workspace_id, name, slug) VALUES ('crewX', ?, 'Eng', 'eng')`, wsID)
	execOrFatal(t, db,
		`INSERT INTO agents (id, crew_id, workspace_id, name, slug) VALUES ('from1', 'crewX', ?, 'Viktor', 'viktor')`, wsID)
	execOrFatal(t, db,
		`INSERT INTO agents (id, crew_id, workspace_id, name, slug) VALUES ('to1', 'crewX', ?, 'Nela', 'nela')`, wsID)
	execOrFatal(t, db,
		`INSERT INTO chats (id, agent_id, workspace_id, mode, status) VALUES ('chatX', 'from1', ?, 'CHAT', 'ACTIVE')`, wsID)

	// Wire a credential to the target so loadAgentCredentials walks its
	// populated path (scan + decrypt), not just the empty-rows path.
	enc, err := encryption.Encrypt("sk-secret-value")
	if err != nil {
		t.Fatalf("encrypt: %v", err)
	}
	execOrFatal(t, db,
		`INSERT INTO credentials (id, workspace_id, name, encrypted_value, type, provider, created_by)
		 VALUES ('credX', ?, 'Key', ?, 'API_KEY', 'ANTHROPIC', ?)`, wsID, enc, userID)
	execOrFatal(t, db,
		`INSERT INTO agent_credentials (id, agent_id, credential_id, env_var_name, priority)
		 VALUES ('acX', 'to1', 'credX', 'ANTHROPIC_API_KEY', 1)`)

	rec := &covQRecordingEmitter{}
	h := NewQueryHandler(db, nil, nil, "token", newTestLogger())
	h.SetJournal(rec)

	body := bytes.NewBufferString(`{"target_slug":"nela","question":"What CSS framework should we use here?","from_slug":"viktor","crew_id":"crewX","workspace_id":"` + wsID + `","chat_id":"chatX","depth":1}`)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/internal/queries", body)
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	h.Create(w, req)

	// nil orchestrator -> 503, and the conversation marked FAILED.
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected 503, got %d; body: %s", w.Code, w.Body.String())
	}

	var status string
	if err := db.QueryRowContext(context.Background(),
		`SELECT status FROM peer_conversations WHERE to_agent_id = 'to1'`).Scan(&status); err != nil {
		t.Fatalf("expected peer_conversations row: %v", err)
	}
	if status != "FAILED" {
		t.Errorf("expected status=FAILED, got %s", status)
	}

	// Journal must have received: question(running), run.started,
	// answer(failed), run.failed. So at least 4 entries.
	if got := rec.count(); got < 4 {
		t.Errorf("expected >=4 journal entries (running+started+answer+terminal), got %d", got)
	}
}

// TestCovQCreateNilFromSlug covers Create's branch where from_slug is empty,
// so the from-agent lookup is skipped entirely.
func TestCovQCreateNilFromSlug(t *testing.T) {
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)

	execOrFatal(t, db,
		`INSERT INTO crews (id, workspace_id, name, slug) VALUES ('crewY', ?, 'Eng', 'eng')`, wsID)
	execOrFatal(t, db,
		`INSERT INTO agents (id, crew_id, workspace_id, name, slug) VALUES ('toY', 'crewY', ?, 'Nela', 'nela')`, wsID)
	execOrFatal(t, db,
		`INSERT INTO chats (id, agent_id, workspace_id, mode, status) VALUES ('chatY', 'toY', ?, 'CHAT', 'ACTIVE')`, wsID)

	h := NewQueryHandler(db, nil, nil, "token", newTestLogger())

	// from_slug omitted entirely.
	body := bytes.NewBufferString(`{"target_slug":"nela","question":"ping?","crew_id":"crewY","workspace_id":"` + wsID + `","chat_id":"chatY"}`)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/internal/queries", body)
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	h.Create(w, req)

	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected 503 (nil orch), got %d; body: %s", w.Code, w.Body.String())
	}

	var fromAgent *string
	if err := db.QueryRowContext(context.Background(),
		`SELECT from_agent_id FROM peer_conversations WHERE to_agent_id = 'toY'`).Scan(&fromAgent); err != nil {
		t.Fatalf("expected peer_conversations row: %v", err)
	}
	// from_agent_id should be empty string (no from lookup performed).
	if fromAgent != nil && *fromAgent != "" {
		t.Errorf("expected empty from_agent_id, got %q", *fromAgent)
	}
}

// TestCovQLoadAgentCredentialsEmpty covers loadAgentCredentials when the agent
// has no credentials wired (rows iterate zero times, nil slice returned).
func TestCovQLoadAgentCredentialsEmpty(t *testing.T) {
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)

	execOrFatal(t, db,
		`INSERT INTO crews (id, workspace_id, name, slug) VALUES ('crewZ', ?, 'Eng', 'eng')`, wsID)
	execOrFatal(t, db,
		`INSERT INTO agents (id, crew_id, workspace_id, name, slug) VALUES ('agZ', 'crewZ', ?, 'Solo', 'solo')`, wsID)

	h := NewQueryHandler(db, nil, nil, "token", newTestLogger())

	creds, err := h.loadAgentCredentials(context.Background(), "agZ")
	if err != nil {
		t.Fatalf("loadAgentCredentials: %v", err)
	}
	if len(creds) != 0 {
		t.Errorf("expected 0 creds, got %d", len(creds))
	}
}

// TestCovQLoadAgentCredentialsPopulated covers the scan+decrypt happy path of
// loadAgentCredentials directly, asserting the plaintext round-trips.
func TestCovQLoadAgentCredentialsPopulated(t *testing.T) {
	setTestEncryptionKey(t)
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)

	execOrFatal(t, db,
		`INSERT INTO crews (id, workspace_id, name, slug) VALUES ('crewC', ?, 'Eng', 'eng')`, wsID)
	execOrFatal(t, db,
		`INSERT INTO agents (id, crew_id, workspace_id, name, slug) VALUES ('agC', 'crewC', ?, 'Solo', 'solo')`, wsID)

	enc, err := encryption.Encrypt("plaintext-token")
	if err != nil {
		t.Fatalf("encrypt: %v", err)
	}
	execOrFatal(t, db,
		`INSERT INTO credentials (id, workspace_id, name, encrypted_value, type, provider, created_by)
		 VALUES ('credC', ?, 'Key', ?, 'API_KEY', 'ANTHROPIC', ?)`, wsID, enc, userID)
	execOrFatal(t, db,
		`INSERT INTO agent_credentials (id, agent_id, credential_id, env_var_name, priority)
		 VALUES ('acC', 'agC', 'credC', 'ANTHROPIC_API_KEY', 1)`)

	h := NewQueryHandler(db, nil, nil, "token", newTestLogger())

	creds, err := h.loadAgentCredentials(context.Background(), "agC")
	if err != nil {
		t.Fatalf("loadAgentCredentials: %v", err)
	}
	if len(creds) != 1 {
		t.Fatalf("expected 1 cred, got %d", len(creds))
	}
	if creds[0].PlainValue != "plaintext-token" {
		t.Errorf("expected decrypted plaintext-token, got %q", creds[0].PlainValue)
	}
	if creds[0].EnvVarName != "ANTHROPIC_API_KEY" {
		t.Errorf("expected env var ANTHROPIC_API_KEY, got %q", creds[0].EnvVarName)
	}
}

// TestCovQListPeerConversationsPagination covers ListPeerConversations
// happy path with response/duration columns populated and a custom limit
// query param, exercising the scan + escalated-int conversion branches.
func TestCovQListPeerConversationsPagination(t *testing.T) {
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)

	execOrFatal(t, db,
		`INSERT INTO crews (id, workspace_id, name, slug) VALUES ('crewP', ?, 'Eng', 'eng')`, wsID)
	execOrFatal(t, db,
		`INSERT INTO agents (id, crew_id, workspace_id, name, slug) VALUES ('pf', 'crewP', ?, 'Viktor', 'viktor')`, wsID)
	execOrFatal(t, db,
		`INSERT INTO agents (id, crew_id, workspace_id, name, slug) VALUES ('pt', 'crewP', ?, 'Nela', 'nela')`, wsID)
	execOrFatal(t, db,
		`INSERT INTO peer_conversations (id, workspace_id, crew_id, chat_id, from_agent_id, to_agent_id, question, response, status, duration_ms, escalated, created_at, finished_at)
		 VALUES ('pcP', ?, 'crewP', 'chatP', 'pf', 'pt', 'Q?', 'A!', 'COMPLETED', 1234, 1, '2025-01-01T12:00:00Z', '2025-01-01T12:00:05Z')`, wsID)

	h := NewQueryHandler(db, nil, nil, "token", newTestLogger())

	req := httptest.NewRequest(http.MethodGet, "/api/v1/crews/crewP/peer-conversations?limit=10&offset=0", nil)
	req.SetPathValue("crewId", "crewP")
	req = req.WithContext(context.WithValue(req.Context(), ctxWorkspaceID, wsID))
	w := httptest.NewRecorder()

	h.ListPeerConversations(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d; body: %s", w.Code, w.Body.String())
	}
	var result []map[string]interface{}
	if err := json.NewDecoder(w.Body).Decode(&result); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(result) != 1 {
		t.Fatalf("expected 1 conversation, got %d", len(result))
	}
	if result[0]["escalated"] != true {
		t.Errorf("expected escalated=true, got %v", result[0]["escalated"])
	}
	if result[0]["response"] != "A!" {
		t.Errorf("expected response='A!', got %v", result[0]["response"])
	}
	if df, _ := result[0]["duration_ms"].(float64); df != 1234 {
		t.Errorf("expected duration_ms=1234, got %v", result[0]["duration_ms"])
	}
}

// TestCovQFinishQueryCompletedJournal covers finishQuery's COMPLETED path
// (no errMsg): peer_conversations update, info-severity answer + run.completed
// terminal entry with exit_code, exercising the success branches not hit by
// the nil-orchestrator FAILED tests.
func TestCovQFinishQueryCompletedJournal(t *testing.T) {
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)

	execOrFatal(t, db,
		`INSERT INTO crews (id, workspace_id, name, slug) VALUES ('crewF', ?, 'Eng', 'eng')`, wsID)
	execOrFatal(t, db,
		`INSERT INTO peer_conversations (id, workspace_id, crew_id, chat_id, from_agent_id, to_agent_id, question, status, created_at)
		 VALUES ('pcF', ?, 'crewF', 'chatF', 'f1', 't1', 'Q?', 'RUNNING', '2025-01-01T12:00:00Z')`, wsID)

	rec := &covQRecordingEmitter{}
	h := NewQueryHandler(db, nil, nil, "token", newTestLogger())
	h.SetJournal(rec)

	startCtx := context.Background()
	h.finishQuery(startCtx, "pcF", "run-abc", "chatF", "viktor", "nela", wsID, "crewF", "t1",
		strings.Repeat("ok ", 3), "", time.Now())

	var status, resp string
	if err := db.QueryRowContext(context.Background(),
		`SELECT status, COALESCE(response,'') FROM peer_conversations WHERE id = 'pcF'`).Scan(&status, &resp); err != nil {
		t.Fatalf("query: %v", err)
	}
	if status != "COMPLETED" {
		t.Errorf("expected COMPLETED, got %s", status)
	}
	if resp == "" {
		t.Error("expected response persisted")
	}
	// answer entry + terminal run.completed entry.
	if got := rec.count(); got < 2 {
		t.Errorf("expected >=2 journal entries, got %d", got)
	}
}
