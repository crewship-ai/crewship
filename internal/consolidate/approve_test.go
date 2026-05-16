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

	appr, err := ApproveProposal(context.Background(), db, w, quietLogger(), proposalID, "user_42")
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
	if _, err := ApproveProposal(context.Background(), db, w, quietLogger(), proposalID, "u1"); err != nil {
		t.Fatalf("first approve: %v", err)
	}
	_, err := ApproveProposal(context.Background(), db, w, quietLogger(), proposalID, "u2")
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

	_, err := ApproveProposal(context.Background(), db, w, quietLogger(), "mp_doesnotexist", "u1")
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

	if _, err := ApproveProposal(context.Background(), db, w, quietLogger(), proposalID, "u1"); err == nil {
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

func TestRejectProposal_NonPending_Returns409(t *testing.T) {
	db := openDB(t)
	defer db.Close()
	w := journal.NewWriter(db, quietLogger(), journal.WriterOptions{FlushSize: 1})
	defer w.Close()
	applyV89Schema(t, db)

	proposalID, _ := seedPendingProposal(t, db, w)
	if _, err := ApproveProposal(context.Background(), db, w, quietLogger(), proposalID, "u1"); err != nil {
		t.Fatalf("seed approve: %v", err)
	}
	err := RejectProposal(context.Background(), db, w, quietLogger(), proposalID, "u2", "")
	if !errors.Is(err, ErrProposalNotPending) {
		t.Errorf("err = %v, want ErrProposalNotPending", err)
	}
}
