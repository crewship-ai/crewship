package server

// Boot recovery for chat runs orphaned by a hard server crash
// (SIGKILL/OOM mid-reply). recoverOrphanedRuns historically only
// cleaned the journal and agent statuses — the conversation itself got
// NOTHING, so after the restart the user saw their message with no
// assistant turn and no explanation. These tests pin the fix: each
// orphaned CHAT run (run.started payload carries a chat_id) appends a
// system/error turn to its conversation and bumps the chat's message
// count; non-chat orphans stay conversation-silent.

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/config"
	"github.com/crewship-ai/crewship/internal/database"
	"github.com/crewship-ai/crewship/internal/journal"
	"github.com/crewship-ai/crewship/internal/logging"
)

func TestRecoverOrphanedRuns_SurfacesInterruptedChatReply(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	db, err := database.Open("file:" + filepath.Join(dir, "rec-chat.db"))
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
	mustExec(t, db.DB, `INSERT INTO chats (id, workspace_id, agent_id, message_count, created_at, updated_at) VALUES ('c1','w1','a1',1,?,?)`, now, now)

	// Orphan 1: a CHAT run (payload carries chat_id) that never reached a
	// terminal entry — the server died mid-reply.
	mustExec(t, db.DB, `INSERT INTO journal_entries
		(id, workspace_id, agent_id, ts, entry_type, severity, actor_type, summary, payload, refs, trace_id, priority)
		VALUES ('je1','w1','a1', strftime('%Y-%m-%dT%H:%M:%fZ','now'),
		        'run.started','info','sidecar','run r1 started','{"trigger_type":"USER","chat_id":"c1"}','{}','r1','normal')`)
	// Orphan 2: a non-chat run (routine dispatch) — recovery must NOT
	// invent a conversation for it.
	mustExec(t, db.DB, `INSERT INTO journal_entries
		(id, workspace_id, agent_id, ts, entry_type, severity, actor_type, summary, payload, refs, trace_id, priority)
		VALUES ('je2','w1','a1', strftime('%Y-%m-%dT%H:%M:%fZ','now'),
		        'run.started','info','sidecar','run r2 started','{"trigger_type":"ROUTINE"}','{}','r2','normal')`)

	cfg := config.Default()
	cfg.Auth.JWTSecret = "test-secret-for-recovery-chat-test-32c"
	// Point the conversation store at the test dir so the appended turn
	// is observable (and never lands in the default storage path).
	cfg.Storage.BasePath = dir
	s := New(cfg, logger, &Deps{DB: db.DB})
	t.Cleanup(s.StopBackground)
	s.startedAt = time.Now()
	s.journalWriter = journal.NewWriter(db.DB, logger, journal.WriterOptions{FlushSize: 1})
	defer s.journalWriter.Close()

	s.recoverOrphanedRuns(context.Background())
	_ = s.journalWriter.Flush(context.Background())

	// The interrupted chat gained an explicit system/error turn so the
	// user sees why there is no reply — on reload, not just live.
	msgs, err := s.convStore.Read(context.Background(), "c1", 0, 0)
	if err != nil {
		t.Fatalf("read conversation: %v", err)
	}
	if len(msgs) != 1 {
		t.Fatalf("conversation messages = %d (%+v), want exactly one recovery turn", len(msgs), msgs)
	}
	turn := msgs[0]
	if turn.Role != "system" {
		t.Errorf("recovery turn role = %q, want system", turn.Role)
	}
	if want := "interrupted by a server restart"; !strings.Contains(turn.Content, want) {
		t.Errorf("recovery turn content = %q, want it to mention %q", turn.Content, want)
	}
	if len(turn.Parts) != 1 || turn.Parts[0].Type != "error" {
		t.Errorf("recovery turn parts = %+v, want a single error part", turn.Parts)
	}

	// The chat's message_count includes the recovery turn.
	var count int
	if err := db.QueryRow(`SELECT message_count FROM chats WHERE id = 'c1'`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 2 {
		t.Errorf("message_count = %d, want 2 (user turn + recovery turn)", count)
	}

	// The non-chat orphan must not have grown a conversation.
	if msgs, err := s.convStore.Read(context.Background(), "r2", 0, 0); err == nil && len(msgs) != 0 {
		t.Errorf("non-chat orphan grew conversation messages: %+v", msgs)
	}

	// Journal cleanup still happened for BOTH orphans (no regression of
	// the pre-existing recovery contract).
	for _, trace := range []string{"r1", "r2"} {
		var terminal string
		if err := db.QueryRow(`SELECT entry_type FROM journal_entries
			WHERE trace_id = ? AND entry_type IN ('run.completed','run.failed','run.cancelled','run.timeout')
			LIMIT 1`, trace).Scan(&terminal); err != nil {
			t.Errorf("trace %s: expected terminal run entry: %v", trace, err)
		}
	}
}

// Running recovery twice must not stack duplicate "interrupted" turns:
// the first pass terminalizes the trace, so the second pass sees no
// orphan and appends nothing.
func TestRecoverOrphanedRuns_InterruptedChatTurnIsIdempotent(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	db, err := database.Open("file:" + filepath.Join(dir, "rec-chat2.db"))
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
	mustExec(t, db.DB, `INSERT INTO chats (id, workspace_id, agent_id, message_count, created_at, updated_at) VALUES ('c9','w1','a1',1,?,?)`, now, now)
	mustExec(t, db.DB, `INSERT INTO journal_entries
		(id, workspace_id, agent_id, ts, entry_type, severity, actor_type, summary, payload, refs, trace_id, priority)
		VALUES ('je1','w1','a1', strftime('%Y-%m-%dT%H:%M:%fZ','now'),
		        'run.started','info','sidecar','run r9 started','{"trigger_type":"USER","chat_id":"c9"}','{}','r9','normal')`)

	cfg := config.Default()
	cfg.Auth.JWTSecret = "test-secret-for-recovery-chat-test-32c"
	cfg.Storage.BasePath = dir
	s := New(cfg, logger, &Deps{DB: db.DB})
	t.Cleanup(s.StopBackground)
	s.startedAt = time.Now()
	s.journalWriter = journal.NewWriter(db.DB, logger, journal.WriterOptions{FlushSize: 1})
	defer s.journalWriter.Close()

	s.recoverOrphanedRuns(context.Background())
	_ = s.journalWriter.Flush(context.Background())
	s.recoverOrphanedRuns(context.Background())
	_ = s.journalWriter.Flush(context.Background())

	msgs, err := s.convStore.Read(context.Background(), "c9", 0, 0)
	if err != nil {
		t.Fatalf("read conversation: %v", err)
	}
	if len(msgs) != 1 {
		t.Fatalf("conversation messages after two recovery passes = %d, want 1", len(msgs))
	}
}
