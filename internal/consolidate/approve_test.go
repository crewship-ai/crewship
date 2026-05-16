package consolidate

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/journal"
)

// seedPendingProposal runs the consolidator in ProposalMode once and
// returns the resulting proposal id + output dir. Every approve / reject
// test starts from this same fixture so the assertions stay scoped to
// the state transition under test.
func seedPendingProposal(t *testing.T, db *sql.DB, w *journal.Writer) (proposalID, outputDir string) {
	t.Helper()
	ids := seedEntries(t, db, w, "ws_test", "crew_test", 12, journal.EntryPeerEscalation)
	reply := `[{"pattern":"approve me","action":"do it","evidence":["` + ids[0] + `","` + ids[1] + `"],"confidence":0.9}]`
	c := &Consolidator{DB: db, Journal: w, Summarizer: &stubSummarizer{Reply: reply}, Logger: quietLogger()}
	outputDir = t.TempDir()
	cfg := Config{
		WorkspaceID:  "ws_test",
		CrewID:       "crew_test",
		Since:        time.Hour,
		MinEntries:   10,
		OutputDir:    outputDir,
		ProposalMode: true,
	}
	if _, err := c.Run(context.Background(), cfg); err != nil {
		t.Fatalf("seed Run: %v", err)
	}
	if err := db.QueryRow(`SELECT id FROM memory_proposals WHERE workspace_id = 'ws_test' LIMIT 1`).Scan(&proposalID); err != nil {
		t.Fatalf("read proposal id: %v", err)
	}
	return proposalID, outputDir
}

func TestApproveProposal_HappyPath(t *testing.T) {
	db := openDB(t)
	defer db.Close()
	w := journal.NewWriter(db, quietLogger(), journal.WriterOptions{FlushSize: 1})
	defer w.Close()
	applyV89Schema(t, db)

	proposalID, outputDir := seedPendingProposal(t, db, w)

	appr, err := ApproveProposal(context.Background(), db, w, quietLogger(), proposalID, "user_42", ApprovalOptions{})
	if err != nil {
		t.Fatalf("ApproveProposal: %v", err)
	}
	if appr.RulesMerged != 1 {
		t.Errorf("RulesMerged = %d, want 1", appr.RulesMerged)
	}

	// Canonical learned-*.md now exists and contains the rule body.
	canonical := filepath.Join(outputDir, "learned-"+time.Now().UTC().Format("2006-01-02")+".md")
	if appr.CanonicalPath != canonical {
		t.Errorf("CanonicalPath = %q, want %q", appr.CanonicalPath, canonical)
	}
	body, err := os.ReadFile(canonical)
	if err != nil {
		t.Fatalf("read canonical: %v", err)
	}
	if !strings.Contains(string(body), "approve me") {
		t.Errorf("canonical content missing rule pattern: %q", body)
	}

	// memory_proposals row → approved + decided_at + decided_by.
	var status, decidedBy string
	var decidedAt sql.NullString
	if err := db.QueryRow(
		`SELECT status, decided_at, decided_by_user_id FROM memory_proposals WHERE id = ?`,
		proposalID).Scan(&status, &decidedAt, &decidedBy); err != nil {
		t.Fatalf("read proposal: %v", err)
	}
	if status != "approved" {
		t.Errorf("status = %q, want approved", status)
	}
	if !decidedAt.Valid || decidedAt.String == "" {
		t.Errorf("decided_at not populated: %v", decidedAt)
	}
	if decidedBy != "user_42" {
		t.Errorf("decided_by_user_id = %q, want user_42", decidedBy)
	}

	// Inbox row resolved with action='approved'.
	var inboxState, action string
	if err := db.QueryRow(
		`SELECT state, COALESCE(resolved_action,'') FROM inbox_items WHERE source_id = ? AND kind = 'memory_consolidation'`,
		proposalID).Scan(&inboxState, &action); err != nil {
		t.Fatalf("read inbox: %v", err)
	}
	if inboxState != "resolved" {
		t.Errorf("inbox state = %q, want resolved", inboxState)
	}
	if action != "approved" {
		t.Errorf("inbox action = %q, want approved", action)
	}

	// Canonical EntryMemoryConsolidated emitted (distinct from the
	// EntryMemoryConsolidationProposed the seed Run fired).
	if err := w.Flush(context.Background()); err != nil {
		t.Fatalf("flush journal: %v", err)
	}
	var canonicalEmits int
	if err := db.QueryRow(
		`SELECT COUNT(*) FROM journal_entries WHERE workspace_id = 'ws_test' AND entry_type = 'memory.consolidated'`,
	).Scan(&canonicalEmits); err != nil {
		t.Fatalf("count emits: %v", err)
	}
	if canonicalEmits != 1 {
		t.Errorf("canonical memory.consolidated emit count = %d, want 1", canonicalEmits)
	}
}

func TestApproveProposal_NonPending_Returns409(t *testing.T) {
	db := openDB(t)
	defer db.Close()
	w := journal.NewWriter(db, quietLogger(), journal.WriterOptions{FlushSize: 1})
	defer w.Close()
	applyV89Schema(t, db)

	proposalID, _ := seedPendingProposal(t, db, w)
	if _, err := ApproveProposal(context.Background(), db, w, quietLogger(), proposalID, "u1", ApprovalOptions{}); err != nil {
		t.Fatalf("first approve: %v", err)
	}
	_, err := ApproveProposal(context.Background(), db, w, quietLogger(), proposalID, "u2", ApprovalOptions{})
	if !errors.Is(err, ErrProposalNotPending) {
		t.Errorf("second approve err = %v, want ErrProposalNotPending", err)
	}
}

func TestApproveProposal_MissingProposal_Returns404(t *testing.T) {
	db := openDB(t)
	defer db.Close()
	w := journal.NewWriter(db, quietLogger(), journal.WriterOptions{FlushSize: 1})
	defer w.Close()
	applyV89Schema(t, db)

	_, err := ApproveProposal(context.Background(), db, w, quietLogger(), "mp_doesnotexist", "u1", ApprovalOptions{})
	if !errors.Is(err, ErrProposalNotFound) {
		t.Errorf("err = %v, want ErrProposalNotFound", err)
	}
}

func TestApproveProposal_FileMissing_RowUntouched(t *testing.T) {
	db := openDB(t)
	defer db.Close()
	w := journal.NewWriter(db, quietLogger(), journal.WriterOptions{FlushSize: 1})
	defer w.Close()
	applyV89Schema(t, db)

	proposalID, _ := seedPendingProposal(t, db, w)

	var path string
	if err := db.QueryRow(`SELECT proposal_path FROM memory_proposals WHERE id = ?`, proposalID).Scan(&path); err != nil {
		t.Fatalf("read path: %v", err)
	}
	if err := os.Remove(path); err != nil {
		t.Fatalf("remove file: %v", err)
	}

	if _, err := ApproveProposal(context.Background(), db, w, quietLogger(), proposalID, "u1", ApprovalOptions{}); err == nil {
		t.Fatalf("expected error when proposal file is missing, got nil")
	}

	var status string
	if err := db.QueryRow(`SELECT status FROM memory_proposals WHERE id = ?`, proposalID).Scan(&status); err != nil {
		t.Fatalf("re-read status: %v", err)
	}
	if status != "pending" {
		t.Errorf("status = %q, want pending (approve should have rolled back)", status)
	}
}

func TestRejectProposal_HappyPath(t *testing.T) {
	db := openDB(t)
	defer db.Close()
	w := journal.NewWriter(db, quietLogger(), journal.WriterOptions{FlushSize: 1})
	defer w.Close()
	applyV89Schema(t, db)

	proposalID, outputDir := seedPendingProposal(t, db, w)

	if err := RejectProposal(context.Background(), db, w, quietLogger(), proposalID, "u1", "rule wrong"); err != nil {
		t.Fatalf("RejectProposal: %v", err)
	}

	var status, decidedBy string
	if err := db.QueryRow(`SELECT status, decided_by_user_id FROM memory_proposals WHERE id = ?`, proposalID).Scan(&status, &decidedBy); err != nil {
		t.Fatalf("read proposal: %v", err)
	}
	if status != "rejected" || decidedBy != "u1" {
		t.Errorf("after reject: status=%q decided_by=%q want rejected/u1", status, decidedBy)
	}

	canonical := filepath.Join(outputDir, "learned-"+time.Now().UTC().Format("2006-01-02")+".md")
	if _, err := os.Stat(canonical); !os.IsNotExist(err) {
		t.Errorf("reject path should not create canonical file: %v", err)
	}

	var action string
	if err := db.QueryRow(`SELECT COALESCE(resolved_action,'') FROM inbox_items WHERE source_id = ?`, proposalID).Scan(&action); err != nil {
		t.Fatalf("read inbox: %v", err)
	}
	if action != "rejected" {
		t.Errorf("inbox action = %q, want rejected", action)
	}
}

// TestApproveProposal_RecordsVersion asserts the post-merge audit row.
// With ApprovalOptions.BlobRoot set, the approve writes a content-
// addressed blob + memory_versions row whose path matches the canonical
// audit-path convention 'crew:{crewID}/{filename}' and whose
// written_by is the approving user (NOT 'consolidator').
func TestApproveProposal_RecordsVersion(t *testing.T) {
	db := openDB(t)
	defer db.Close()
	w := journal.NewWriter(db, quietLogger(), journal.WriterOptions{FlushSize: 1})
	defer w.Close()
	applyV89Schema(t, db)
	// memory_versions table — minimal v90 schema, same shape the
	// hook test uses.
	if _, err := db.Exec(`
CREATE TABLE IF NOT EXISTS memory_versions (
    id           TEXT PRIMARY KEY,
    workspace_id TEXT NOT NULL,
    path         TEXT NOT NULL,
    tier         TEXT NOT NULL CHECK (tier IN ('agent','crew','workspace','pins','learned')),
    sha256       TEXT NOT NULL,
    bytes        INTEGER NOT NULL,
    written_at   TEXT NOT NULL DEFAULT (datetime('now','subsec')),
    written_by   TEXT,
    parent_sha   TEXT,
    payload_ref  TEXT NOT NULL
);`); err != nil {
		t.Fatalf("v90 schema: %v", err)
	}

	proposalID, outputDir := seedPendingProposal(t, db, w)
	blobRoot := filepath.Join(outputDir, "versions")

	appr, err := ApproveProposal(context.Background(), db, w, quietLogger(), proposalID, "operator_99",
		ApprovalOptions{BlobRoot: blobRoot})
	if err != nil {
		t.Fatalf("ApproveProposal: %v", err)
	}
	if appr.VersionSha == "" {
		t.Fatalf("expected VersionSha populated when BlobRoot is set, got empty")
	}

	// Row landed with tier=learned, written_by=operator, and the
	// path follows the canonical audit convention.
	var tier, path, writtenBy string
	if err := db.QueryRow(
		`SELECT tier, path, COALESCE(written_by,'') FROM memory_versions WHERE workspace_id = 'ws_test' ORDER BY written_at DESC LIMIT 1`,
	).Scan(&tier, &path, &writtenBy); err != nil {
		t.Fatalf("read memory_versions: %v", err)
	}
	if tier != "learned" {
		t.Errorf("tier = %q, want learned", tier)
	}
	if !strings.Contains(path, "crew:crew_test/learned-") {
		t.Errorf("path = %q, want prefix crew:crew_test/learned-", path)
	}
	if writtenBy != "operator_99" {
		t.Errorf("written_by = %q, want operator_99 (the approving user, not 'consolidator')", writtenBy)
	}

	// Blob exists at sha-addressed path.
	blobPath := filepath.Join(blobRoot, appr.VersionSha[:2], appr.VersionSha)
	if _, err := os.Stat(blobPath); err != nil {
		t.Errorf("blob missing at %s: %v", blobPath, err)
	}
}

// TestApproveProposal_NoBlobRoot_SkipsVersioning asserts the legacy
// approve contract survives: when ApprovalOptions{} (no BlobRoot)
// is passed, the approve succeeds AND no memory_versions row lands.
func TestApproveProposal_NoBlobRoot_SkipsVersioning(t *testing.T) {
	db := openDB(t)
	defer db.Close()
	w := journal.NewWriter(db, quietLogger(), journal.WriterOptions{FlushSize: 1})
	defer w.Close()
	applyV89Schema(t, db)
	if _, err := db.Exec(`CREATE TABLE IF NOT EXISTS memory_versions (id TEXT PRIMARY KEY, workspace_id TEXT, path TEXT, tier TEXT, sha256 TEXT, bytes INTEGER, written_at TEXT, written_by TEXT, parent_sha TEXT, payload_ref TEXT NOT NULL)`); err != nil {
		t.Fatalf("schema: %v", err)
	}

	proposalID, _ := seedPendingProposal(t, db, w)
	appr, err := ApproveProposal(context.Background(), db, w, quietLogger(), proposalID, "u1", ApprovalOptions{})
	if err != nil {
		t.Fatalf("ApproveProposal: %v", err)
	}
	if appr.VersionSha != "" {
		t.Errorf("VersionSha = %q, want empty when BlobRoot disabled", appr.VersionSha)
	}
	var count int
	if err := db.QueryRow(`SELECT COUNT(*) FROM memory_versions`).Scan(&count); err != nil {
		t.Fatalf("count: %v", err)
	}
	if count != 0 {
		t.Errorf("no versioning rows expected when BlobRoot disabled, got %d", count)
	}
}

func TestRejectProposal_NonPending_Returns409(t *testing.T) {
	db := openDB(t)
	defer db.Close()
	w := journal.NewWriter(db, quietLogger(), journal.WriterOptions{FlushSize: 1})
	defer w.Close()
	applyV89Schema(t, db)

	proposalID, _ := seedPendingProposal(t, db, w)
	if _, err := ApproveProposal(context.Background(), db, w, quietLogger(), proposalID, "u1", ApprovalOptions{}); err != nil {
		t.Fatalf("seed approve: %v", err)
	}
	err := RejectProposal(context.Background(), db, w, quietLogger(), proposalID, "u2", "")
	if !errors.Is(err, ErrProposalNotPending) {
		t.Errorf("err = %v, want ErrProposalNotPending", err)
	}
}
