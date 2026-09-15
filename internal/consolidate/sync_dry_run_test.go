package consolidate

import (
	"context"
	"database/sql"
	"io"
	"log/slog"
	"path/filepath"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/memory"
)

// #1702 — the dry-run seam on the two memory sweeps.
//
// A dry run does everything a real sweep does up to the point where a byte
// would land — the candidate query, the consent probe, the threshold, the
// extractor (which is the expensive, model-backed step and the one an
// operator wants to observe) — and then reports what WOULD have happened
// instead of doing it. Nothing on disk, nothing in the index tables, nothing
// in peer_card_audit. That last one matters: an audit row that says "write"
// for a write that never happened is worse than no row.

func dryRunCount(t *testing.T, db *sql.DB, query string, args ...any) int {
	t.Helper()
	var n int
	if err := db.QueryRow(query, args...).Scan(&n); err != nil {
		t.Fatalf("count %q: %v", query, err)
	}
	return n
}

func TestRunUserModelSync_DryRunReportsTheWriteAndWritesNothing(t *testing.T) {
	db := userModelWorkerDB(t)
	seedUserModelFixture(t, db)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	base := t.TempDir()

	sum, err := RunUserModelSync(context.Background(), db, logger, "ws1", UserModelSyncOptions{
		OutputBasePath: base,
		Extractor:      fixedExtractor{body: "- tone: terse"},
		DryRun:         true,
	})
	if err != nil {
		t.Fatalf("RunUserModelSync: %v", err)
	}
	if !sum.DryRun {
		t.Errorf("summary does not say it was a dry run: %+v", sum)
	}
	// The report is the real sweep's report: the candidate crossed the
	// threshold and the extractor produced a body, so this counts as a
	// write — the one that would have happened.
	if sum.Candidates != 1 || sum.Writes != 1 || sum.Errors != 0 {
		t.Errorf("expected 1 candidate / 1 write / 0 errors; got %+v", sum)
	}

	paths := memory.UserModelPaths{SharedDir: filepath.Join(base, "crews", "cr1", "shared", ".memory")}
	if body, _ := memory.LoadUserModel(paths, "u1", "ws1"); body != "" {
		t.Errorf("dry run wrote a user model to disk: %q", body)
	}
	if n := dryRunCount(t, db, `SELECT COUNT(*) FROM user_models WHERE workspace_id = 'ws1'`); n != 0 {
		t.Errorf("dry run inserted %d user_models row(s)", n)
	}
	if n := dryRunCount(t, db, `SELECT COUNT(*) FROM peer_card_audit WHERE workspace_id = 'ws1'`); n != 0 {
		t.Errorf("dry run emitted %d audit row(s)", n)
	}
}

func TestRunUserModelSync_DryRunReportsThePurgeAndPurgesNothing(t *testing.T) {
	db := userModelWorkerDB(t)
	seedUserModelFixture(t, db)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	base := t.TempDir()
	paths := memory.UserModelPaths{SharedDir: filepath.Join(base, "crews", "cr1", "shared", ".memory")}
	if err := memory.WriteUserModel(paths, "u1", "ws1", "- tone: terse"); err != nil {
		t.Fatalf("seed model: %v", err)
	}
	slug := memory.UserSlug("u1", "ws1")
	if _, err := db.Exec(`INSERT INTO user_models (id, workspace_id, crew_id, user_id, user_slug, path, bytes, created_at, updated_at)
		VALUES ('um1', 'ws1', 'cr1', 'u1', ?, ?, 13, ?, ?)`,
		slug, paths.ModelPath(slug), time.Now().UTC().Format(time.RFC3339), time.Now().UTC().Format(time.RFC3339)); err != nil {
		t.Fatalf("seed index: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO user_peer_consent (user_id, workspace_id, opted_out) VALUES ('u1','ws1',1)`); err != nil {
		t.Fatalf("opt out: %v", err)
	}

	sum, err := RunUserModelSync(context.Background(), db, logger, "ws1", UserModelSyncOptions{
		OutputBasePath: base,
		Extractor:      fixedExtractor{body: "- tone: warm"},
		DryRun:         true,
	})
	if err != nil {
		t.Fatalf("RunUserModelSync: %v", err)
	}
	if sum.PurgedOptOut != 1 {
		t.Errorf("expected the purge to be reported; got %+v", sum)
	}
	if body, _ := memory.LoadUserModel(paths, "u1", "ws1"); body == "" {
		t.Errorf("dry run purged the user model from disk")
	}
	if n := dryRunCount(t, db, `SELECT COUNT(*) FROM user_models WHERE workspace_id = 'ws1'`); n != 1 {
		t.Errorf("dry run removed the user_models index row (have %d)", n)
	}
	if n := dryRunCount(t, db, `SELECT COUNT(*) FROM peer_card_audit WHERE workspace_id = 'ws1'`); n != 0 {
		t.Errorf("dry run emitted %d audit row(s)", n)
	}
}

func TestRunPeerCardSync_DryRunReportsTheWriteAndWritesNothing(t *testing.T) {
	db, dir := peerRoutineDB(t)
	seedChat(t, db, "c1", "u1", 12, time.Minute)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))

	sum, err := RunPeerCardSync(context.Background(), db, logger, "ws1", PeerCardSyncOptions{
		OutputBasePath: dir,
		Extractor:      staticExtractor{body: "Pavel notes"},
		DryRun:         true,
	})
	if err != nil {
		t.Fatalf("RunPeerCardSync: %v", err)
	}
	if !sum.DryRun {
		t.Errorf("summary does not say it was a dry run: %+v", sum)
	}
	if sum.Candidates != 1 || sum.Writes != 1 || sum.Errors != 0 {
		t.Errorf("expected 1 candidate / 1 write / 0 errors; got %+v", sum)
	}
	paths := memory.PeerPaths{AgentDir: filepath.Join(dir, "crews", "crew1", "agents", "alice", ".memory")}
	if body, _ := memory.LoadPeerCard(paths, "u1", "ws1"); body != "" {
		t.Errorf("dry run wrote a peer card to disk: %q", body)
	}
	if n := dryRunCount(t, db, `SELECT COUNT(*) FROM peer_cards WHERE workspace_id = 'ws1'`); n != 0 {
		t.Errorf("dry run inserted %d peer_cards row(s)", n)
	}
	if n := dryRunCount(t, db, `SELECT COUNT(*) FROM peer_card_audit WHERE workspace_id = 'ws1'`); n != 0 {
		t.Errorf("dry run emitted %d audit row(s)", n)
	}
}

func TestRunPeerCardSync_DryRunReportsThePurgeAndPurgesNothing(t *testing.T) {
	db, dir := peerRoutineDB(t)
	seedChat(t, db, "c1", "u1", 12, time.Minute)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	paths := memory.PeerPaths{AgentDir: filepath.Join(dir, "crews", "crew1", "agents", "alice", ".memory")}
	if err := memory.WritePeerCard(paths, "u1", "ws1", "Pavel notes"); err != nil {
		t.Fatalf("seed card: %v", err)
	}
	slug := memory.UserSlug("u1", "ws1")
	if _, err := db.Exec(`INSERT INTO peer_cards (id, workspace_id, agent_id, user_id, user_slug, path, bytes, created_at, updated_at)
		VALUES ('pc1', 'ws1', 'a1', 'u1', ?, ?, 11, ?, ?)`,
		slug, paths.CardPath(slug), time.Now().UTC().Format(time.RFC3339), time.Now().UTC().Format(time.RFC3339)); err != nil {
		t.Fatalf("seed index: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO user_peer_consent (user_id, workspace_id, opted_out) VALUES ('u1','ws1',1)`); err != nil {
		t.Fatalf("opt out: %v", err)
	}

	sum, err := RunPeerCardSync(context.Background(), db, logger, "ws1", PeerCardSyncOptions{
		OutputBasePath: dir,
		Extractor:      staticExtractor{body: "newer notes"},
		DryRun:         true,
	})
	if err != nil {
		t.Fatalf("RunPeerCardSync: %v", err)
	}
	if sum.PurgedOptOut != 1 {
		t.Errorf("expected the purge to be reported; got %+v", sum)
	}
	if body, _ := memory.LoadPeerCard(paths, "u1", "ws1"); body == "" {
		t.Errorf("dry run purged the peer card from disk")
	}
	if n := dryRunCount(t, db, `SELECT COUNT(*) FROM peer_cards WHERE workspace_id = 'ws1'`); n != 1 {
		t.Errorf("dry run removed the peer_cards index row (have %d)", n)
	}
	if n := dryRunCount(t, db, `SELECT COUNT(*) FROM peer_card_audit WHERE workspace_id = 'ws1'`); n != 0 {
		t.Errorf("dry run emitted %d audit row(s)", n)
	}
}
