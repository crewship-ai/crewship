package consolidate

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/journal"
)

// TestProposalMode_LandsInProposedDir asserts that with ProposalMode
// set, the consolidator writes proposal-{id}.md under .proposed/ and
// does NOT touch the canonical learned-YYYY-MM-DD.md.
func TestProposalMode_LandsInProposedDir(t *testing.T) {
	db := openDB(t)
	defer db.Close()
	w := journal.NewWriter(db, quietLogger(), journal.WriterOptions{FlushSize: 1})
	defer w.Close()

	// Apply v89 schema bits (memory_proposals + widened inbox CHECK)
	// directly so this test does not depend on the full migrate chain.
	applyV89Schema(t, db)

	ids := seedEntries(t, db, w, "ws_test", "crew_test", 12, journal.EntryPeerEscalation)
	reply := `[{"pattern":"x","action":"y","evidence":["` + ids[0] + `","` + ids[1] + `"],"confidence":0.7}]`
	// Pin the consolidator clock to a single instant so the
	// canonical-path assertion below derives its date from that same
	// instant — defends against the (rare but real) flake where Run()
	// and the time.Now() in the assertion straddle a UTC midnight.
	// We anchor on the current real time rather than a fixed past date
	// so the consolidator's "Since: time.Hour" window still covers the
	// freshly seeded journal entries.
	fixedNow := time.Now().UTC()
	c := &Consolidator{
		DB: db, Journal: w, Summarizer: &stubSummarizer{Reply: reply}, Logger: quietLogger(),
		Now: func() time.Time { return fixedNow },
	}

	tmp := t.TempDir()
	cfg := Config{
		WorkspaceID:  "ws_test",
		CrewID:       "crew_test",
		Since:        time.Hour,
		MinEntries:   10,
		OutputDir:    tmp,
		ProposalMode: true,
	}
	res, err := c.Run(context.Background(), cfg)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	if !strings.Contains(res.OutputPath, ".proposed/proposal-") {
		t.Errorf("OutputPath should be inside .proposed/, got %q", res.OutputPath)
	}
	if _, err := os.Stat(res.OutputPath); err != nil {
		t.Errorf("proposal file missing: %v", err)
	}

	// Canonical learned-*.md MUST NOT exist when ProposalMode is on.
	canonical := filepath.Join(tmp, "learned-"+fixedNow.UTC().Format("2006-01-02")+".md")
	if _, err := os.Stat(canonical); !os.IsNotExist(err) {
		t.Errorf("canonical learned-*.md should not exist in proposal mode, stat err=%v", err)
	}

	// memory_proposals row landed with status='pending'.
	var status string
	var rulesCount int
	if err := db.QueryRow(
		`SELECT status, rules_count FROM memory_proposals WHERE workspace_id = ? AND crew_id = ? LIMIT 1`,
		"ws_test", "crew_test",
	).Scan(&status, &rulesCount); err != nil {
		t.Fatalf("memory_proposals query: %v", err)
	}
	if status != "pending" {
		t.Errorf("status = %q, want pending", status)
	}
	if rulesCount != 1 {
		t.Errorf("rules_count = %d, want 1", rulesCount)
	}

	// inbox_items row landed with kind=memory_consolidation.
	var inboxCount int
	if err := db.QueryRow(
		`SELECT COUNT(*) FROM inbox_items WHERE workspace_id = ? AND kind = 'memory_consolidation'`,
		"ws_test",
	).Scan(&inboxCount); err != nil {
		t.Fatalf("inbox_items query: %v", err)
	}
	if inboxCount != 1 {
		t.Errorf("inbox count = %d, want 1", inboxCount)
	}
}

// TestProposalMode_DefaultOff: without explicit ProposalMode, the
// canonical write path still fires (no regression on existing flow).
func TestProposalMode_DefaultOff(t *testing.T) {
	db := openDB(t)
	defer db.Close()
	w := journal.NewWriter(db, quietLogger(), journal.WriterOptions{FlushSize: 1})
	defer w.Close()
	applyV89Schema(t, db)

	ids := seedEntries(t, db, w, "ws_test", "crew_test", 12, journal.EntryPeerEscalation)
	reply := `[{"pattern":"x","action":"y","evidence":["` + ids[0] + `","` + ids[1] + `"],"confidence":0.7}]`
	c := &Consolidator{
		DB: db, Journal: w, Summarizer: &stubSummarizer{Reply: reply}, Logger: quietLogger(),
	}
	tmp := t.TempDir()
	cfg := Config{WorkspaceID: "ws_test", CrewID: "crew_test", Since: time.Hour, MinEntries: 10, OutputDir: tmp}
	res, err := c.Run(context.Background(), cfg)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if strings.Contains(res.OutputPath, ".proposed") {
		t.Errorf("default mode wrote into .proposed/: %q", res.OutputPath)
	}
	if !strings.Contains(res.OutputPath, "learned-") {
		t.Errorf("expected canonical learned-*.md output, got %q", res.OutputPath)
	}
}

// applyV89Schema adds the memory_proposals table + widens the
// inbox_items.kind CHECK constraint in the test database. The full
// Migrate runs all 89 migrations against a real SQLite handle; this
// helper duplicates only the v89 surface the proposal tests need so
// they stay focused on the consolidate package contract.
func applyV89Schema(t *testing.T, db *sql.DB) {
	t.Helper()
	if _, err := db.Exec(`
CREATE TABLE IF NOT EXISTS memory_proposals (
    id                  TEXT PRIMARY KEY,
    workspace_id        TEXT NOT NULL,
    crew_id             TEXT NOT NULL,
    proposal_path       TEXT NOT NULL,
    status              TEXT NOT NULL DEFAULT 'pending',
    inbox_item_id       TEXT,
    evidence_json       TEXT NOT NULL DEFAULT '{}',
    rules_count         INTEGER NOT NULL DEFAULT 0,
    entries_scanned     INTEGER NOT NULL DEFAULT 0,
    created_at          TEXT NOT NULL DEFAULT (datetime('now','subsec')),
    decided_at          TEXT,
    decided_by_user_id  TEXT
);`); err != nil {
		t.Fatalf("create memory_proposals: %v", err)
	}
	if _, err := db.Exec(`
CREATE TABLE IF NOT EXISTS inbox_items (
    id                  TEXT PRIMARY KEY,
    workspace_id        TEXT NOT NULL,
    kind                TEXT NOT NULL,
    source_id           TEXT NOT NULL,
    target_user_id      TEXT,
    target_role         TEXT,
    title               TEXT NOT NULL,
    body_md             TEXT,
    sender_type         TEXT,
    sender_id           TEXT,
    sender_name         TEXT,
    state               TEXT NOT NULL DEFAULT 'unread',
    priority            TEXT NOT NULL DEFAULT 'medium',
    blocking            INTEGER NOT NULL DEFAULT 1,
    payload_json        TEXT NOT NULL DEFAULT '{}',
    read_at             TEXT,
    read_by_user_id     TEXT,
    resolved_at         TEXT,
    resolved_by_user_id TEXT,
    resolved_action     TEXT,
    created_at          TEXT NOT NULL DEFAULT (datetime('now','subsec')),
    updated_at          TEXT NOT NULL DEFAULT (datetime('now','subsec'))
);
CREATE UNIQUE INDEX IF NOT EXISTS idx_inbox_items_kind_source
    ON inbox_items (kind, source_id);`); err != nil {
		t.Fatalf("create inbox_items: %v", err)
	}
}
