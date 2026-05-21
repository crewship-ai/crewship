package consolidate

import (
	"context"
	"database/sql"
	"io"
	"log/slog"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/database"
	"github.com/crewship-ai/crewship/internal/memory"
)

// staticExtractor lets tests pin the extractor output to a known
// value without wiring an LLM dependency.
type staticExtractor struct{ body string }

func (s staticExtractor) Extract(_ context.Context, _ PeerCandidate) (string, error) {
	return s.body, nil
}

func peerRoutineDB(t *testing.T) (*sql.DB, string) {
	t.Helper()
	dir := t.TempDir()
	dbh, err := database.Open("file:" + filepath.Join(dir, "r.db"))
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
	if _, err := dbh.Exec(`INSERT INTO users (id, email) VALUES ('u1','u1@x'),('u2','u2@x')`); err != nil {
		t.Fatalf("seed users: %v", err)
	}
	if _, err := dbh.Exec(`INSERT INTO crews (id, workspace_id, name, slug, network_mode, allowed_domains)
		VALUES ('crew1','ws1','C','c','free','[]')`); err != nil {
		t.Fatalf("seed crew: %v", err)
	}
	if _, err := dbh.Exec(`INSERT INTO agents (id, workspace_id, crew_id, slug, name, agent_role)
		VALUES ('a1','ws1','crew1','alice','Alice','AGENT')`); err != nil {
		t.Fatalf("seed agent: %v", err)
	}
	return dbh.DB, dir
}

// seedChat creates a chats row with a synthetic message count + start/
// end window so loadPeerCandidates picks it up at the right signal
// level.
func seedChat(t *testing.T, db *sql.DB, chatID, userID string, msgCount int, dur time.Duration) {
	t.Helper()
	start := time.Now().UTC().Add(-dur)
	end := time.Now().UTC()
	_, err := db.Exec(`
		INSERT INTO chats (id, agent_id, workspace_id, created_by, message_count, started_at, ended_at, created_at, updated_at)
		VALUES (?, 'a1', 'ws1', ?, ?, ?, ?, ?, ?)
	`, chatID, userID, msgCount,
		start.Format(time.RFC3339), end.Format(time.RFC3339),
		start.Format(time.RFC3339), end.Format(time.RFC3339))
	if err != nil {
		t.Fatalf("seed chat: %v", err)
	}
}

func TestRunPeerCardSync_WritesAboveThreshold(t *testing.T) {
	db, dir := peerRoutineDB(t)
	seedChat(t, db, "c1", "u1", 12, time.Minute)

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	sum, err := RunPeerCardSync(context.Background(), db, logger, "ws1",
		PeerCardSyncOptions{
			OutputBasePath: dir,
			Extractor:      staticExtractor{body: "Pavel notes"},
		})
	if err != nil {
		t.Fatalf("RunPeerCardSync: %v", err)
	}
	if sum.Writes != 1 || sum.Candidates != 1 {
		t.Errorf("expected 1 write + 1 candidate; got %+v", sum)
	}
	// Disk file present.
	paths := memory.PeerPaths{AgentDir: filepath.Join(dir, "crews", "crew1", "agents", "alice", ".memory")}
	body, _ := memory.LoadPeerCard(paths, "u1", "ws1")
	if !strings.Contains(body, "Pavel") {
		t.Errorf("expected peer card on disk; got %q", body)
	}
}

func TestRunPeerCardSync_SkipsBelowThreshold(t *testing.T) {
	db, dir := peerRoutineDB(t)
	seedChat(t, db, "c1", "u1", 3, 30*time.Second) // both below
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	sum, err := RunPeerCardSync(context.Background(), db, logger, "ws1",
		PeerCardSyncOptions{OutputBasePath: dir, Extractor: staticExtractor{body: "x"}})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if sum.SkippedThresh != 1 || sum.Writes != 0 {
		t.Errorf("expected skip_threshold; got %+v", sum)
	}
}

func TestRunPeerCardSync_PurgesOnOptOut(t *testing.T) {
	db, dir := peerRoutineDB(t)
	seedChat(t, db, "c1", "u1", 30, 10*time.Minute)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))

	// First pass: writes a card.
	if _, err := RunPeerCardSync(context.Background(), db, logger, "ws1",
		PeerCardSyncOptions{OutputBasePath: dir, Extractor: staticExtractor{body: "card content"}}); err != nil {
		t.Fatalf("first run: %v", err)
	}

	// User opts out.
	if _, err := db.Exec(`INSERT INTO user_peer_consent (user_id, workspace_id, opted_out, opted_out_at)
		VALUES ('u1', 'ws1', 1, ?)`, time.Now().UTC().Format(time.RFC3339)); err != nil {
		t.Fatalf("opt out: %v", err)
	}

	// Second pass: card MUST be purged, no write.
	sum, err := RunPeerCardSync(context.Background(), db, logger, "ws1",
		PeerCardSyncOptions{OutputBasePath: dir, Extractor: staticExtractor{body: "should not land"}})
	if err != nil {
		t.Fatalf("second run: %v", err)
	}
	if sum.PurgedOptOut != 1 || sum.Writes != 0 {
		t.Errorf("expected purged_opt_out=1 writes=0; got %+v", sum)
	}
	// Disk file gone.
	paths := memory.PeerPaths{AgentDir: filepath.Join(dir, "crews", "crew1", "agents", "alice", ".memory")}
	body, _ := memory.LoadPeerCard(paths, "u1", "ws1")
	if body != "" {
		t.Errorf("expected card purged after opt-out routine; got %q", body)
	}
}

func TestRunPeerCardSync_HonoursLookbackWindow(t *testing.T) {
	db, dir := peerRoutineDB(t)
	// Chat well outside the 14-day default lookback.
	old := time.Now().UTC().AddDate(0, -2, 0)
	if _, err := db.Exec(`INSERT INTO chats
		(id, agent_id, workspace_id, created_by, message_count, started_at, ended_at, created_at, updated_at)
		VALUES ('cold','a1','ws1','u1', 100, ?, ?, ?, ?)`,
		old.Format(time.RFC3339), old.Format(time.RFC3339),
		old.Format(time.RFC3339), old.Format(time.RFC3339)); err != nil {
		t.Fatalf("seed old chat: %v", err)
	}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	sum, err := RunPeerCardSync(context.Background(), db, logger, "ws1",
		PeerCardSyncOptions{OutputBasePath: dir, Extractor: staticExtractor{body: "x"}})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if sum.Candidates != 0 {
		t.Errorf("expected stale chat to be excluded; got %+v", sum)
	}
}
