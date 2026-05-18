package api

// End-to-end pipeline test pinning the memory-hardening series
// (Iter 1–4) as a single integrated flow rather than a constellation
// of unit-level contracts. Each prior PR covers its own surface;
// this test asserts the surfaces compose correctly.
//
// Pipeline under test:
//
//   1. Consolidator runs in ProposalMode (the production setting
//      when CREWSHIP_CONSOLIDATE_HITL=1) over a seeded journal
//      window. Produces a memory_proposals row + an inbox item +
//      the .proposed/proposal-*.md artefact on disk.
//
//   2. GET /api/v1/consolidate/proposed/{id}/diff (Iter 3) returns
//      a non-empty preview of what an approve would land in the
//      canonical learned-*.md file.
//
//   3. ApproveProposal commits the merge: writes the canonical
//      file, emits memory.consolidated, resolves the inbox item,
//      flips memory_proposals.status to "approved".
//
//   4. Re-running the diff endpoint on the same id now reflects
//      the approved status (status field carries through;
//      mapDecisionError doesn't 404 a decided proposal). Verifies
//      the diff endpoint is robust to repeated reads — an
//      operator double-clicking the preview should not be a
//      special case.
//
//   5. GET /api/v1/admin/memory/stats (Iter 2) shows the audit
//      trail rows the approve recorded (when BlobRoot is wired
//      to ApprovalOptions). Verifies the stats endpoint sees the
//      same memory_versions writes the rest of the pipeline did.
//
//   6. memory.SweepAllWorkspaces (Iter 4) with retention_days=0
//      (clamped to default 30 inside the helper) leaves the
//      just-written rows alone; with retention_days=1 set on the
//      workspace + back-dated rows it would trim them. We exercise
//      the no-op case here (positive assertion: rows survive) so
//      the unit-test suite (retention_test.go,
//      retention_coordination_test.go) keeps the trim assertions
//      and this test stays focused on integration.
//
// Notes:
//   - The audit watcher (Iter 1) is NOT exercised here because it
//     watches the filesystem; spinning it up + waiting for an
//     fsnotify debounce round-trip in this test would add seconds
//     of latency for a contract that the watcher's own test
//     pinned. The pipeline writes memory rows via the same
//     RecordVersion call the watcher uses on its happy path, so
//     downstream state is identical.
//   - Stubbed summarizer returns a hardcoded learned-rules JSON
//     payload — keeping the test deterministic and CI-runnable
//     without an LLM round-trip.

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/consolidate"
	"github.com/crewship-ai/crewship/internal/journal"
	"github.com/crewship-ai/crewship/internal/memory"
)

// e2eStubSummarizer returns one learned rule referencing the two
// supplied evidence ids. Replicates the shape the LLM emits via
// stub.go's stubSummarizer; inlined here so this test file
// stays self-contained (consolidate's test helpers live in a
// different package).
type e2eStubSummarizer struct {
	EvidenceIDs []string
}

func (s *e2eStubSummarizer) Summarize(ctx context.Context, prompt string) (string, error) {
	// The consolidator's filterMultipleEvidence drops rules with
	// fewer than 2 evidence ids — supply both seeded ids.
	evid1, evid2 := "ev_a", "ev_b"
	if len(s.EvidenceIDs) >= 2 {
		evid1, evid2 = s.EvidenceIDs[0], s.EvidenceIDs[1]
	}
	return `[{"pattern":"escalation requires authoring user lookup",` +
		`"action":"resolve user_id from session and attach to escalation payload",` +
		`"evidence":["` + evid1 + `","` + evid2 + `"],"confidence":0.9}]`, nil
}

func TestMemoryPipeline_E2E_ConsolidateDiffApproveStatsCompose(t *testing.T) {
	if testing.Short() {
		t.Skip("E2E pipeline test exercises consolidator + handlers; skip in -short")
	}

	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	crewID := seedTestCrew(t, db, wsID)

	jw := journal.NewWriter(db, newTestLogger(), journal.WriterOptions{FlushSize: 1})
	t.Cleanup(func() { _ = jw.Close() })

	// Seed >= MinEntries (10) journal entries the consolidator
	// will pick up. EntryPeerEscalation is in candidateTypes
	// (consolidator.go:42), so these are eligible.
	entryIDs := seedE2EJournalEntries(t, jw, wsID, crewID, 12)
	if err := jw.Flush(context.Background()); err != nil {
		t.Fatalf("flush journal: %v", err)
	}

	// Step 1: consolidator in ProposalMode.
	outputDir := t.TempDir()
	blobRoot := t.TempDir()
	conso := &consolidate.Consolidator{
		DB:         db,
		Journal:    jw,
		Summarizer: &e2eStubSummarizer{EvidenceIDs: entryIDs[:2]},
		Logger:     newTestLogger(),
	}
	if _, err := conso.Run(context.Background(), consolidate.Config{
		WorkspaceID:  wsID,
		CrewID:       crewID,
		Since:        time.Hour,
		MinEntries:   10,
		OutputDir:    outputDir,
		ProposalMode: true,
		BlobRoot:     blobRoot,
	}); err != nil {
		t.Fatalf("consolidator Run: %v", err)
	}

	// Verify the proposal row + inbox item + .proposed/ markdown
	// all materialised.
	var proposalID, proposalPath, status string
	if err := db.QueryRow(
		`SELECT id, proposal_path, status FROM memory_proposals WHERE workspace_id = ? LIMIT 1`, wsID,
	).Scan(&proposalID, &proposalPath, &status); err != nil {
		t.Fatalf("read proposal: %v", err)
	}
	if status != "pending" {
		t.Errorf("proposal status = %q after consolidator; want pending", status)
	}
	if _, err := os.Stat(proposalPath); err != nil {
		t.Errorf("proposal markdown missing on disk: %v", err)
	}
	var inboxKind string
	if err := db.QueryRow(
		`SELECT kind FROM inbox_items WHERE source_id = ?`, proposalID,
	).Scan(&inboxKind); err != nil {
		t.Errorf("inbox item not created for proposal: %v", err)
	}

	// Step 2: GET /diff returns a meaningful preview.
	diffH := NewProposedHandler(db, newTestLogger())
	diffH.SetJournal(jw)
	diffReq := httptest.NewRequest("GET", "/api/v1/consolidate/proposed/"+proposalID+"/diff", nil)
	diffReq.SetPathValue("id", proposalID)
	diffReq = withWorkspaceUser(diffReq, userID, wsID, "MEMBER")
	diffRR := httptest.NewRecorder()
	diffH.Diff(diffRR, diffReq)
	if diffRR.Code != http.StatusOK {
		t.Fatalf("/diff before approve: status %d, body=%s", diffRR.Code, diffRR.Body.String())
	}
	var preview proposalDiffResponse
	if err := json.Unmarshal(diffRR.Body.Bytes(), &preview); err != nil {
		t.Fatalf("decode preview: %v", err)
	}
	if preview.Status != "pending" {
		t.Errorf("preview.status = %q before approve; want pending", preview.Status)
	}
	if preview.Stats.Additions <= 0 {
		t.Errorf("preview.stats.additions = %d; want >0", preview.Stats.Additions)
	}
	if !strings.Contains(preview.Diff, "escalation requires authoring user lookup") {
		t.Errorf("preview diff missing the seeded rule pattern; got:\n%s", preview.Diff)
	}

	// Step 3: Approve commits the merge with BlobRoot wired so
	// the audit row lands in memory_versions.
	apprRes, err := consolidate.ApproveProposal(context.Background(), db, jw, newTestLogger(),
		proposalID, userID, consolidate.ApprovalOptions{BlobRoot: blobRoot})
	if err != nil {
		t.Fatalf("ApproveProposal: %v", err)
	}
	if apprRes.RulesMerged != 1 {
		t.Errorf("RulesMerged = %d; want 1", apprRes.RulesMerged)
	}
	if apprRes.CanonicalPath == "" {
		t.Errorf("CanonicalPath empty after approve")
	}
	canonical, err := os.ReadFile(apprRes.CanonicalPath)
	if err != nil {
		t.Fatalf("read canonical post-approve: %v", err)
	}
	if !strings.Contains(string(canonical), "escalation requires authoring user lookup") {
		t.Errorf("canonical missing seeded rule pattern; got:\n%s", canonical)
	}

	// Verify inbox item resolved. inbox_items uses `state`
	// (unread/read/resolved), not `status` — different vocabulary
	// from memory_proposals.
	var inboxState string
	if err := db.QueryRow(
		`SELECT state FROM inbox_items WHERE source_id = ?`, proposalID,
	).Scan(&inboxState); err != nil {
		t.Fatalf("read inbox state: %v", err)
	}
	if inboxState != "resolved" {
		t.Errorf("inbox state = %q after approve; want resolved", inboxState)
	}

	// Step 4: re-running /diff on the now-approved proposal still
	// works (no 404 or 500) and reflects the new status.
	diffReq2 := httptest.NewRequest("GET", "/api/v1/consolidate/proposed/"+proposalID+"/diff", nil)
	diffReq2.SetPathValue("id", proposalID)
	diffReq2 = withWorkspaceUser(diffReq2, userID, wsID, "MEMBER")
	diffRR2 := httptest.NewRecorder()
	diffH.Diff(diffRR2, diffReq2)
	if diffRR2.Code != http.StatusOK {
		t.Errorf("/diff after approve: status %d, want 200; body=%s",
			diffRR2.Code, diffRR2.Body.String())
	}
	var preview2 proposalDiffResponse
	if err := json.Unmarshal(diffRR2.Body.Bytes(), &preview2); err != nil {
		t.Fatalf("decode preview after approve: %v", err)
	}
	if preview2.Status != "approved" {
		t.Errorf("preview.status post-approve = %q; want approved", preview2.Status)
	}

	// Step 5: stats endpoint sees the audit row the approve wrote.
	statsH := NewMemoryStatsHandler(db, newTestLogger())
	statsReq := httptest.NewRequest("GET", "/api/v1/admin/memory/stats", nil)
	statsReq = withWorkspaceUser(statsReq, userID, wsID, "OWNER")
	statsRR := httptest.NewRecorder()
	statsH.Stats(statsRR, statsReq)
	if statsRR.Code != http.StatusOK {
		t.Fatalf("/stats: status %d, body=%s", statsRR.Code, statsRR.Body.String())
	}
	var stats memoryStatsResponse
	if err := json.Unmarshal(statsRR.Body.Bytes(), &stats); err != nil {
		t.Fatalf("decode stats: %v", err)
	}
	if stats.Totals.Versions < 1 {
		t.Errorf("stats.totals.versions = %d; want >=1 (approve recorded one audit row)",
			stats.Totals.Versions)
	}
	// The recorded row should be tier=learned (canonical_audit_path
	// in approve.go writes to TierLearned).
	foundLearned := false
	for _, byT := range stats.ByTier {
		if byT.Tier == string(memory.TierLearned) && byT.Versions >= 1 {
			foundLearned = true
			break
		}
	}
	if !foundLearned {
		t.Errorf("stats.by_tier missing learned tier with >=1 version; got %+v", stats.ByTier)
	}

	// Step 6: Per-workspace retention sweep (Iter 4). No
	// memory_config row → defaults to 30 days. The just-written
	// learned row is far younger than 30 d, so the sweep must be
	// a no-op (positive assertion).
	//
	// Scope the before/after count to wsID specifically. The
	// stats endpoint's Totals.Versions also only counts the
	// caller's workspace, so today both numbers agree by
	// design — but pinning the per-workspace count BEFORE
	// the sweep removes any cross-tenant-row drift risk if
	// a future helper seeds multiple workspaces in the same
	// fixture.
	var versionsBefore int
	if err := db.QueryRow(
		`SELECT COUNT(*) FROM memory_versions WHERE workspace_id = ?`, wsID,
	).Scan(&versionsBefore); err != nil {
		t.Fatalf("count versions before sweep: %v", err)
	}
	if err := memory.SweepAllWorkspaces(context.Background(), db, jw); err != nil {
		t.Fatalf("SweepAllWorkspaces: %v", err)
	}
	var versionsAfter int
	if err := db.QueryRow(
		`SELECT COUNT(*) FROM memory_versions WHERE workspace_id = ?`, wsID,
	).Scan(&versionsAfter); err != nil {
		t.Fatalf("count versions after sweep: %v", err)
	}
	if versionsAfter != versionsBefore {
		t.Errorf("retention sweep deleted rows under default 30d window: before=%d after=%d",
			versionsBefore, versionsAfter)
	}

	// Final sanity: the proposal row went all the way to
	// approved, the canonical file holds the rule, the audit
	// trail shows the row, and the inbox is resolved. The
	// pipeline composes — Iter 1–4 are coherent.
}

// seedE2EJournalEntries inserts `n` EntryPeerEscalation rows
// into the journal via the writer (so they land with the
// schema's CHECK constraints satisfied) and returns the
// generated entry ids in seed order. The seeded entries are
// the source the consolidator scans.
//
// FlushSize=1 on the writer commits synchronously per entry,
// and the trailing w.Flush() is the explicit barrier — by the
// time this function returns, the consolidator can SELECT
// against journal_entries and see all n rows. Earlier
// iterations of this helper had a getRowCount probe AFTER
// the Flush call, but it always returned a hardcoded (1,
// true) — pure cargo-cult; deleted.
func seedE2EJournalEntries(t *testing.T, w *journal.Writer, wsID, crewID string, n int) []string {
	t.Helper()
	ids := make([]string, 0, n)
	for i := 0; i < n; i++ {
		id, err := w.Emit(context.Background(), journal.Entry{
			WorkspaceID: wsID,
			CrewID:      crewID,
			Type:        journal.EntryPeerEscalation,
			ActorType:   journal.ActorAgent,
			ActorID:     "agent_e2e",
			Severity:    journal.SeverityInfo,
			// Earlier iteration used `filepath.Base(t.TempDir())`
			// here, which CREATED a temp dir per loop iteration
			// just to extract its basename for the summary
			// string — 12 leaked TempDirs per test run × any
			// -count=N multiplier. Simple counter is plenty.
			Summary: fmt.Sprintf("escalation #%d", i),
			Payload: map[string]any{
				"i":       i,
				"context": "e2e pipeline test seed",
			},
		})
		if err != nil {
			t.Fatalf("emit entry %d: %v", i, err)
		}
		ids = append(ids, id)
	}
	if err := w.Flush(context.Background()); err != nil {
		t.Fatalf("flush journal: %v", err)
	}
	return ids
}

// Compile-time guard: signature drift on Consolidator.Run or
// ApproveProposal would break this test in surprising ways
// (silent assertion mismatch). The literal references below
// fail to compile if either signature changes, forcing the
// test author to re-confirm the integration contract before
// the change can land.
var _ = func(c *consolidate.Consolidator, cfg consolidate.Config) {
	var ctx context.Context
	_, _ = c.Run(ctx, cfg)
}
var _ = func(ctx context.Context, db *sql.DB, j journal.Emitter, logger *slog.Logger,
	proposalID, userID string, opts consolidate.ApprovalOptions) {
	_, _ = consolidate.ApproveProposal(ctx, db, j, logger, proposalID, userID, opts)
}
