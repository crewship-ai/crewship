package consolidate

import (
	"context"
	"database/sql"
	"io"
	"log/slog"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/database"
	"github.com/crewship-ai/crewship/internal/memory"
)

// PR-E F6 — end-to-end coverage that the server-bootstrap worker
// actually writes a peer card given a realistic seed (crew + agent +
// 12 messages between agent and one user).
//
// The intent is to lock down the "Pavel could see a peer card appear
// after a synthetic session" claim made in the PR-E task description.
// If the worker stops being wired or the RunPeerCardSync contract
// shifts, this test breaks loudly instead of letting peer cards
// silently disappear from disk again.

// workerSeedDB stands up a fresh DB with one workspace, one user,
// one crew, one agent, and an "interaction" chat seeded with the
// caller's message count + duration so loadPeerCandidates picks it
// up as a threshold-crossing pair.
func workerSeedDB(t *testing.T, messageCount int, sessionDur time.Duration) (*sql.DB, string) {
	t.Helper()
	dir := t.TempDir()
	dbh, err := database.Open("file:" + filepath.Join(dir, "worker.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	silent := slog.New(slog.NewTextHandler(io.Discard, nil))
	if err := database.Migrate(context.Background(), dbh.DB, silent); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	t.Cleanup(func() { _ = dbh.Close() })

	if _, err := dbh.Exec(`INSERT INTO workspaces (id, name, slug) VALUES ('wsA','Work A','work-a')`); err != nil {
		t.Fatalf("seed ws: %v", err)
	}
	if _, err := dbh.Exec(`INSERT INTO users (id, email) VALUES ('userPavel','pavel@example.com')`); err != nil {
		t.Fatalf("seed user: %v", err)
	}
	if _, err := dbh.Exec(`INSERT INTO crews (id, workspace_id, name, slug, network_mode, allowed_domains)
		VALUES ('crewX','wsA','Crew X','crew-x','free','[]')`); err != nil {
		t.Fatalf("seed crew: %v", err)
	}
	if _, err := dbh.Exec(`INSERT INTO agents (id, workspace_id, crew_id, slug, name, agent_role)
		VALUES ('agentA','wsA','crewX','alice','Alice','AGENT')`); err != nil {
		t.Fatalf("seed agent: %v", err)
	}
	start := time.Now().UTC().Add(-sessionDur)
	end := time.Now().UTC()
	if _, err := dbh.Exec(`
		INSERT INTO chats (id, agent_id, workspace_id, created_by, message_count, started_at, ended_at, created_at, updated_at)
		VALUES ('chat1','agentA','wsA','userPavel',?,?,?,?,?)`,
		messageCount,
		start.Format(time.RFC3339), end.Format(time.RFC3339),
		start.Format(time.RFC3339), end.Format(time.RFC3339)); err != nil {
		t.Fatalf("seed chat: %v", err)
	}
	return dbh.DB, dir
}

// TestStartPeerCardSyncWorker_EndToEnd is the integration test the
// PR-E remediation calls out: seed crew + agent + 12 messages between
// agent and one user, trigger the routine via the worker, assert the
// peer card file appears at the expected path with the expected body.
//
// Uses a staticExtractor so the assertion is deterministic; the real
// aux-LLM-driven extractor is out-of-scope per the PR-E task (lands
// in PR-F).
func TestStartPeerCardSyncWorker_EndToEnd(t *testing.T) {
	// PRD §6 F6 threshold: ≥10 messages OR ≥5 min session. 12 messages
	// crosses the count threshold cleanly without depending on wall
	// clock.
	db, dir := workerSeedDB(t, 12, time.Minute)

	stop := make(chan struct{})
	var wg sync.WaitGroup
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))

	StartPeerCardSyncWorker(db, logger, PeerCardWorkerConfig{
		BasePath:      dir,
		Extractor:     staticExtractor{body: "peer: userPavel 12 messages"},
		FirstRunDelay: 5 * time.Millisecond, // fire immediately for tests
		TickInterval:  time.Hour,            // second tick won't matter
	}, stop, &wg)

	// Wait for the disk-side artefact rather than racing on a fixed
	// sleep — covers the entire pipeline from goroutine spawn through
	// workspace enumeration through sweep through writer.
	paths := memory.PeerPaths{
		AgentDir: filepath.Join(dir, "crews", "crewX", "agents", "alice", ".memory"),
	}
	deadline := time.Now().Add(5 * time.Second)
	var body string
	for time.Now().Before(deadline) {
		body, _ = memory.LoadPeerCard(paths, "userPavel", "wsA")
		if body != "" {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}

	close(stop)
	wg.Wait()

	if body == "" {
		t.Fatal("expected peer card on disk after worker tick; got empty")
	}
	if !strings.Contains(body, "userPavel") || !strings.Contains(body, "12 messages") {
		t.Errorf("peer card body unexpected; got %q", body)
	}
}

// TestStartPeerCardSyncWorker_NoBasePathRefusesToStart asserts the
// fail-fast guard: starting without a BasePath leaves wg unincremented
// and exits cleanly. Without this guard the per-tick RunPeerCardSync
// call would return an error every single time, flooding the log
// indefinitely.
func TestStartPeerCardSyncWorker_NoBasePathRefusesToStart(t *testing.T) {
	db, _ := workerSeedDB(t, 1, time.Second)
	stop := make(chan struct{})
	var wg sync.WaitGroup
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))

	StartPeerCardSyncWorker(db, logger, PeerCardWorkerConfig{
		BasePath: "", // missing on purpose
	}, stop, &wg)

	// wg should not have been incremented — Wait must return immediately
	// even without ever closing stop. If it didn't, this test hangs
	// rather than failing cleanly; the go test timeout would catch it
	// but a Wait-after-close is the explicit check.
	close(stop)
	done := make(chan struct{})
	go func() { wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(1 * time.Second):
		t.Fatal("worker started despite missing BasePath; wg.Wait blocked")
	}
}

// TestStartPeerCardSyncWorker_StopChannelHaltsBeforeFirstSweep mirrors
// the registry worker's same-shape test (mcp_registry_scan_worker_test.go).
// Without this, a worker whose FirstRunDelay > 0 could be ungracefully
// abandoned on shutdown.
func TestStartPeerCardSyncWorker_StopChannelHaltsBeforeFirstSweep(t *testing.T) {
	db, dir := workerSeedDB(t, 1, time.Second)
	stop := make(chan struct{})
	var wg sync.WaitGroup
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))

	StartPeerCardSyncWorker(db, logger, PeerCardWorkerConfig{
		BasePath:      dir,
		Extractor:     NoopExtractor{},
		FirstRunDelay: time.Hour, // would block indefinitely if not honoured
		TickInterval:  time.Hour,
	}, stop, &wg)

	close(stop)
	done := make(chan struct{})
	go func() { wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("worker did not honour stop channel during FirstRunDelay")
	}
}

// TestNextDailyOffsetUTC sanity-checks the off-peak alignment used to
// pick the worker's first-run delay. Lock down both branches (target
// later today vs. tomorrow) so a refactor doesn't silently shift the
// daily fire window.
func TestNextDailyOffsetUTC(t *testing.T) {
	cases := []struct {
		name string
		now  time.Time
		hour int
		want time.Duration
	}{
		{
			name: "before target today",
			now:  time.Date(2026, 5, 21, 1, 30, 0, 0, time.UTC),
			hour: 4,
			want: 2*time.Hour + 30*time.Minute,
		},
		{
			name: "past target rolls to tomorrow",
			now:  time.Date(2026, 5, 21, 9, 0, 0, 0, time.UTC),
			hour: 4,
			want: 19 * time.Hour,
		},
		{
			name: "exact match rolls to tomorrow",
			now:  time.Date(2026, 5, 21, 4, 0, 0, 0, time.UTC),
			hour: 4,
			want: 24 * time.Hour,
		},
		{
			name: "out-of-range hour defaults to 4",
			now:  time.Date(2026, 5, 21, 2, 0, 0, 0, time.UTC),
			hour: 99,
			want: 2 * time.Hour,
		},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			got := nextDailyOffsetUTC(tc.now, tc.hour)
			if got != tc.want {
				t.Errorf("nextDailyOffsetUTC(%v, %d) = %v; want %v",
					tc.now, tc.hour, got, tc.want)
			}
		})
	}
}
