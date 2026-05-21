package consolidate

import (
	"context"
	"database/sql"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/database"
	"github.com/crewship-ai/crewship/internal/memory"
)

// peerCardTestDB spins up a fully-migrated SQLite DB and seeds the
// minimum FK targets (workspace, user, agent) the SyncPeerCard
// path needs. Returns the DB + the seed identifiers.
func peerCardTestDB(t *testing.T) (*sql.DB, string, string, string) {
	t.Helper()
	dir := t.TempDir()
	dbh, err := database.Open("file:" + filepath.Join(dir, "peer.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	silent := slog.New(slog.NewTextHandler(io.Discard, nil))
	if err := database.Migrate(context.Background(), dbh.DB, silent); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	t.Cleanup(func() { _ = dbh.Close() })

	if _, err := dbh.Exec(`INSERT INTO workspaces (id, name, slug) VALUES ('ws1','W','w')`); err != nil {
		t.Fatalf("seed ws: %v", err)
	}
	if _, err := dbh.Exec(`INSERT INTO users (id, email) VALUES ('u1','u1@x')`); err != nil {
		t.Fatalf("seed user: %v", err)
	}
	if _, err := dbh.Exec(`INSERT INTO agents (id, workspace_id, slug, name, agent_role)
		VALUES ('a1','ws1','dev','Dev','AGENT')`); err != nil {
		t.Fatalf("seed agent: %v", err)
	}
	return dbh.DB, "ws1", "u1", "a1"
}

func TestMeetsThreshold(t *testing.T) {
	th := DefaultPeerCardThreshold
	cases := []struct {
		name string
		msgs int
		dur  time.Duration
		want bool
	}{
		{"below both", 3, time.Minute, false},
		{"messages over", 12, 30 * time.Second, true},
		{"duration over", 4, 6 * time.Minute, true},
		{"exact bar", 10, 5 * time.Minute, true},
		{"zero", 0, 0, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := th.MeetsThreshold(tc.msgs, tc.dur); got != tc.want {
				t.Errorf("got %v, want %v", got, tc.want)
			}
		})
	}
}

func TestSyncPeerCard_Write(t *testing.T) {
	db, wsID, userID, agentID := peerCardTestDB(t)
	dir := t.TempDir()
	paths := memory.PeerPaths{AgentDir: dir}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))

	out := SyncPeerCard(
		context.Background(), db, logger,
		DefaultPeerCardThreshold,
		PeerCandidate{
			WorkspaceID: wsID, AgentID: agentID, AgentSlug: "dev",
			UserID: userID, MessageCount: 12, SessionDuration: time.Minute,
		},
		"Pavel: technical, terse.",
		paths, time.Now(),
	)
	if out.Err != nil {
		t.Fatalf("write outcome err: %v", out.Err)
	}
	if out.Action != "write" || out.Bytes == 0 {
		t.Errorf("expected write action with bytes; got %+v", out)
	}
	// Disk file landed.
	body, _ := memory.LoadPeerCard(paths, userID, wsID)
	if !strings.Contains(body, "Pavel") {
		t.Errorf("disk peer card missing expected content: %q", body)
	}
	// DB row landed.
	var cnt int
	if err := db.QueryRow(`SELECT COUNT(*) FROM peer_cards WHERE agent_id=? AND user_slug=?`,
		agentID, memory.UserSlug(userID, wsID)).Scan(&cnt); err != nil {
		t.Fatalf("count peer_cards: %v", err)
	}
	if cnt != 1 {
		t.Errorf("expected 1 peer_cards row; got %d", cnt)
	}
	// Audit row landed with action=write.
	var auditCnt int
	if err := db.QueryRow(`SELECT COUNT(*) FROM peer_card_audit
		WHERE target_user_id=? AND action='write'`, userID).Scan(&auditCnt); err != nil {
		t.Fatalf("count audit: %v", err)
	}
	if auditCnt != 1 {
		t.Errorf("expected 1 audit write; got %d", auditCnt)
	}
}

// Second write for the same pair must upsert in place, not create a
// duplicate row (UNIQUE constraint would otherwise blow up). Also
// asserts updated_at moves and bytes reflect the new content.
func TestSyncPeerCard_WriteIsUpsert(t *testing.T) {
	db, wsID, userID, agentID := peerCardTestDB(t)
	dir := t.TempDir()
	paths := memory.PeerPaths{AgentDir: dir}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	cand := PeerCandidate{
		WorkspaceID: wsID, AgentID: agentID, UserID: userID,
		MessageCount: 12, SessionDuration: time.Minute,
	}
	SyncPeerCard(context.Background(), db, logger, DefaultPeerCardThreshold,
		cand, "v1 content", paths, time.Now())
	SyncPeerCard(context.Background(), db, logger, DefaultPeerCardThreshold,
		cand, "v2 longer content here", paths, time.Now())

	var cnt int
	if err := db.QueryRow(`SELECT COUNT(*) FROM peer_cards`).Scan(&cnt); err != nil {
		t.Fatalf("count: %v", err)
	}
	if cnt != 1 {
		t.Errorf("expected upsert (1 row); got %d", cnt)
	}
	body, _ := memory.LoadPeerCard(paths, userID, wsID)
	if !strings.Contains(body, "v2") {
		t.Errorf("expected v2 content on disk; got %q", body)
	}
}

func TestSyncPeerCard_SkipThreshold(t *testing.T) {
	db, wsID, userID, agentID := peerCardTestDB(t)
	dir := t.TempDir()
	paths := memory.PeerPaths{AgentDir: dir}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	out := SyncPeerCard(
		context.Background(), db, logger,
		DefaultPeerCardThreshold,
		PeerCandidate{
			WorkspaceID: wsID, AgentID: agentID, UserID: userID,
			MessageCount: 2, SessionDuration: 30 * time.Second,
		},
		"would be content", paths, time.Now(),
	)
	if out.Action != "skip_threshold" {
		t.Errorf("expected skip_threshold; got %q (%+v)", out.Action, out)
	}
	// No file on disk.
	if _, err := os.Stat(paths.CardPath(memory.UserSlug(userID, wsID))); err == nil {
		t.Errorf("expected no card file on threshold skip")
	}
}

// Opt-out propagation: setting consent.opted_out → next SyncPeerCard
// call deletes any existing card AND removes the index row AND emits
// a delete-audit entry, even when threshold + content are fine.
func TestSyncPeerCard_OptOutPurges(t *testing.T) {
	db, wsID, userID, agentID := peerCardTestDB(t)
	dir := t.TempDir()
	paths := memory.PeerPaths{AgentDir: dir}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	cand := PeerCandidate{
		WorkspaceID: wsID, AgentID: agentID, UserID: userID,
		MessageCount: 30, SessionDuration: 10 * time.Minute,
	}
	// First write a card.
	SyncPeerCard(context.Background(), db, logger, DefaultPeerCardThreshold,
		cand, "Pavel notes", paths, time.Now())

	// Now flip opt-out.
	if _, err := db.Exec(`INSERT INTO user_peer_consent (user_id, workspace_id, opted_out, opted_out_at)
		VALUES (?, ?, 1, ?)`, userID, wsID, time.Now().UTC().Format(time.RFC3339)); err != nil {
		t.Fatalf("set opt out: %v", err)
	}
	out := SyncPeerCard(context.Background(), db, logger, DefaultPeerCardThreshold,
		cand, "fresh would-be content", paths, time.Now())
	if out.Action != "delete_opt_out" {
		t.Errorf("expected delete_opt_out; got %q", out.Action)
	}
	// File gone.
	if _, err := os.Stat(paths.CardPath(memory.UserSlug(userID, wsID))); err == nil {
		t.Errorf("expected disk card to be purged on opt-out")
	}
	// Index row gone.
	var cnt int
	if err := db.QueryRow(`SELECT COUNT(*) FROM peer_cards`).Scan(&cnt); err != nil {
		t.Fatalf("count: %v", err)
	}
	if cnt != 0 {
		t.Errorf("expected 0 peer_cards after opt-out purge; got %d", cnt)
	}
	// Audit row with action=delete.
	var auditCnt int
	if err := db.QueryRow(`SELECT COUNT(*) FROM peer_card_audit
		WHERE target_user_id=? AND action='delete'`, userID).Scan(&auditCnt); err != nil {
		t.Fatalf("count audit: %v", err)
	}
	if auditCnt != 1 {
		t.Errorf("expected 1 delete audit row; got %d", auditCnt)
	}
}

func TestSyncPeerCard_SkipEmptyContent(t *testing.T) {
	db, wsID, userID, agentID := peerCardTestDB(t)
	dir := t.TempDir()
	paths := memory.PeerPaths{AgentDir: dir}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	out := SyncPeerCard(
		context.Background(), db, logger,
		DefaultPeerCardThreshold,
		PeerCandidate{
			WorkspaceID: wsID, AgentID: agentID, UserID: userID,
			MessageCount: 100, SessionDuration: time.Hour,
		},
		"   \n\t", paths, time.Now(),
	)
	if out.Action != "skip_empty_content" {
		t.Errorf("expected skip_empty_content; got %q", out.Action)
	}
}
