package server

// W2 (Release 1.0 hardening): episodic indexer boot wiring + degraded-mode
// signal. Three contracts pinned here:
//
//  1. BOOT WIRING: Server.Start launches the episodic indexer sweeper when
//     an embedder is configured, so journal entries written in production
//     actually land in journal_embeddings. Before this fix the indexer was
//     never constructed outside tests and HybridRecall queried an empty
//     index in every real deployment.
//  2. DEGRADED SIGNAL (healthz): /healthz reports `episodic: vector` vs
//     `episodic: sparse-only` so operators (and `crewship doctor`) can see
//     when recall is running without an embedder.
//  3. DEGRADED SIGNAL (boot log): when no embedder is configured, boot logs
//     one clear WARN instead of silently returning "" from every recall.
//
// The embedder is injected via Deps.EpisodicEmbedder (a fake here) — per
// the workstream ground rules no Ollama/Docker is started in tests.

import (
	"bytes"
	"context"
	"database/sql"
	"log/slog"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/logging"
)

// bootFakeEmbedder is a deterministic Embedder stand-in; tracks calls so
// the boot test can assert the sweeper actually embedded the seeded entry.
type bootFakeEmbedder struct {
	mu    sync.Mutex
	calls int
}

func (f *bootFakeEmbedder) Embed(_ context.Context, _ string) ([]float32, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	return []float32{0.25, 0.5, 0.75, 1.0}, nil
}
func (f *bootFakeEmbedder) Dim() int      { return 4 }
func (f *bootFakeEmbedder) Model() string { return "boot-fake-embedder" }

// seedEmbeddableEntry inserts a workspace plus one always-embeddable
// journal entry (peer.escalation) so the indexer's first sweep has work.
func seedEmbeddableEntry(t *testing.T, db *sql.DB, entryID string) {
	t.Helper()
	now := time.Now().UTC().Format(time.RFC3339)
	if _, err := db.Exec(`INSERT INTO workspaces (id, name, slug, created_at, updated_at)
		VALUES ('w-epi', 'Epi', 'epi', ?, ?)`, now, now); err != nil {
		t.Fatalf("insert workspace: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO journal_entries
		(id, workspace_id, ts, entry_type, severity, actor_type, summary, payload, refs)
		VALUES (?, 'w-epi', ?, 'peer.escalation', 'warn', 'agent', 'escalated: prod deploy blocked', '{}', '{}')`,
		entryID, now); err != nil {
		t.Fatalf("insert journal entry: %v", err)
	}
}

func TestHealthz_EpisodicSparseOnlyWithoutEmbedder(t *testing.T) {
	t.Parallel()
	s := newTestServerForT(t) // no embedder injected, Keeper disabled by default

	req := httptest.NewRequest("GET", "/healthz", nil)
	w := httptest.NewRecorder()
	s.mux.ServeHTTP(w, req)

	body := parseJSON(t, w.Body.Bytes())
	if got := body["episodic"]; got != "sparse-only" {
		t.Fatalf("healthz episodic = %v, want %q", got, "sparse-only")
	}
}

func TestHealthz_EpisodicVectorWithEmbedder(t *testing.T) {
	t.Parallel()
	cfg := silentCfg()
	logger := logging.New("error", "json", nil)
	s := New(cfg, logger, &Deps{DB: openTestDB(t), EpisodicEmbedder: &bootFakeEmbedder{}})
	t.Cleanup(s.StopBackground)
	s.startedAt = time.Now()

	req := httptest.NewRequest("GET", "/healthz", nil)
	w := httptest.NewRecorder()
	s.mux.ServeHTTP(w, req)

	body := parseJSON(t, w.Body.Bytes())
	if got := body["episodic"]; got != "vector" {
		t.Fatalf("healthz episodic = %v, want %q", got, "vector")
	}
}

func TestStartEpisodicIndexer_WarnsOnceWhenNoEmbedder(t *testing.T) {
	t.Parallel()
	var buf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelWarn}))

	cfg := silentCfg()
	s := New(cfg, logger, &Deps{DB: openTestDB(t)})
	t.Cleanup(s.StopBackground)

	s.startEpisodicIndexer(context.Background())

	out := buf.String()
	if !strings.Contains(out, "sparse-only") {
		t.Fatalf("expected sparse-only WARN at boot, log output:\n%s", out)
	}
	if !strings.Contains(out, "KEEPER_OLLAMA_URL") {
		t.Fatalf("WARN should tell the operator how to enable vector recall, got:\n%s", out)
	}
}

// TestStart_EpisodicIndexerSweepsAtBoot drives the real Server.Start
// lifecycle (random HTTP port, throwaway IPC socket) with a fake embedder
// injected and a pre-seeded embeddable journal entry, then asserts the
// indexer's initial sweep writes the journal_embeddings row. This is the
// "server startup starts the sweeper" contract — testing the private
// helper alone would not catch Start() dropping the call.
func TestStart_EpisodicIndexerSweepsAtBoot(t *testing.T) {
	db := openTestDB(t)
	const entryID = "je-epi-boot-1"
	seedEmbeddableEntry(t, db, entryID)

	// Unix sockets have a ~104-char path limit on macOS — shorter than
	// t.TempDir() can produce. Same workaround as the startIPC test.
	sockPath := filepath.Join("/tmp", "cs-epi-"+randomShort()+".sock")
	t.Cleanup(func() { _ = os.Remove(sockPath) })

	cfg := silentCfg()
	cfg.Server.Host = "127.0.0.1"
	cfg.Server.Port = 0 // ephemeral port, no collision across parallel runs
	cfg.IPC.SocketPath = sockPath
	cfg.Storage.BasePath = t.TempDir()
	cfg.Storage.LogPath = t.TempDir()

	// Shrink the sweep interval: the initial sweep can lose a SQLite
	// busy race against the other boot goroutines, and the production
	// 30s retry interval would make this test slow or flaky. NOT
	// t.Parallel for this reason — the var is package-global.
	origPoll := episodicIndexerPoll
	episodicIndexerPoll = 100 * time.Millisecond
	t.Cleanup(func() { episodicIndexerPoll = origPoll })

	logger := logging.New("error", "json", nil)
	fake := &bootFakeEmbedder{}
	s := New(cfg, logger, &Deps{DB: db, EpisodicEmbedder: fake})

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- s.Start(ctx) }()

	// The indexer kicks off an immediate sweep at Start, so the seeded
	// entry should be embedded well within the deadline.
	deadline := time.Now().Add(10 * time.Second)
	indexed := false
	for time.Now().Before(deadline) {
		var n int
		if err := db.QueryRow(`SELECT COUNT(*) FROM journal_embeddings WHERE entry_id = ?`, entryID).Scan(&n); err == nil && n == 1 {
			indexed = true
			break
		}
		select {
		case err := <-done:
			t.Fatalf("server exited early: %v", err)
		case <-time.After(50 * time.Millisecond):
		}
	}

	cancel()
	select {
	case <-done:
	case <-time.After(15 * time.Second):
		t.Fatal("server did not shut down after cancel")
	}

	if !indexed {
		t.Fatalf("journal_embeddings row for %s never appeared — indexer not started at boot", entryID)
	}
	if fake.callsCount() == 0 {
		t.Fatal("embedder was never invoked — sweep did not run through the injected embedder")
	}
}

func (f *bootFakeEmbedder) callsCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls
}
