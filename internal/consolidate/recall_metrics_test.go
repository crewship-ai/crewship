package consolidate

import (
	"context"
	"database/sql"
	"encoding/json"
	"testing"
	"time"
)

// seedSearchEvent inserts a memory.searched journal row with the
// supplied query + hit_chunk_ids payload. Returns the synthetic entry
// id so caller can assert against it if needed.
func seedSearchEvent(t *testing.T, db *sql.DB, id, workspaceID, query string, hitChunkIDs []string, ts time.Time) {
	t.Helper()
	payload, err := json.Marshal(map[string]any{
		"query":         query,
		"hit_chunk_ids": hitChunkIDs,
		"hit_count":     len(hitChunkIDs),
		"scope":         "own",
	})
	if err != nil {
		t.Fatalf("marshal payload: %v", err)
	}
	if _, err := db.Exec(`
		INSERT INTO journal_entries (id, workspace_id, ts, entry_type, severity, actor_type, summary, payload)
		VALUES (?, ?, ?, 'memory.searched', 'info', 'system', 'search', ?)`,
		id, workspaceID, ts.UTC().Format(time.RFC3339Nano), string(payload)); err != nil {
		t.Fatalf("seed search event: %v", err)
	}
}

// TestLoadRecallMetrics_NoSearches asserts zero counts when no
// memory.searched events exist — the steady-state for a freshly
// proposed rule that hasn't been recalled yet.
func TestLoadRecallMetrics_NoSearches(t *testing.T) {
	db := openDB(t)
	defer db.Close()
	recall, unique := loadRecallMetrics(context.Background(), db, "ws_x",
		[]string{"j_001", "j_002"}, time.Now().Add(-7*24*time.Hour))
	if recall != 0 || unique != 0 {
		t.Errorf("expected (0,0), got (%d,%d)", recall, unique)
	}
}

// TestLoadRecallMetrics_NoEvidence asserts zero counts when the rule
// has no evidence ids — no JOIN keys means no possible matches.
func TestLoadRecallMetrics_NoEvidence(t *testing.T) {
	db := openDB(t)
	defer db.Close()
	recall, unique := loadRecallMetrics(context.Background(), db, "ws_x",
		nil, time.Now().Add(-7*24*time.Hour))
	if recall != 0 || unique != 0 {
		t.Errorf("expected (0,0) for empty evidence, got (%d,%d)", recall, unique)
	}
}

// TestLoadRecallMetrics_OneSearchOneEvidenceMatches asserts the simple
// happy path: one memory.searched event whose hit_chunk_ids contains
// one of the rule's evidence ids → recall=1, unique=1.
func TestLoadRecallMetrics_OneSearchOneEvidenceMatches(t *testing.T) {
	db := openDB(t)
	defer db.Close()
	now := time.Now()
	seedSearchEvent(t, db, "j_search_1", "ws_x", "how to deploy", []string{"j_001"}, now.Add(-1*time.Hour))

	recall, unique := loadRecallMetrics(context.Background(), db, "ws_x",
		[]string{"j_001", "j_002"}, now.Add(-7*24*time.Hour))
	if recall != 1 || unique != 1 {
		t.Errorf("expected (1,1), got (%d,%d)", recall, unique)
	}
}

// TestLoadRecallMetrics_DistinctQueryCount asserts that two searches
// with the same query string count as ONE unique query, but TWO
// distinct events for the recall counter. Mirrors the OpenClaw
// definition: recall = how often the rule surfaced; unique = how
// diverse the surfacing contexts were.
func TestLoadRecallMetrics_DistinctQueryCount(t *testing.T) {
	db := openDB(t)
	defer db.Close()
	now := time.Now()
	// Same query, different events → 2 recalls, 1 unique
	seedSearchEvent(t, db, "j_search_1", "ws_x", "deploy production", []string{"j_001"}, now.Add(-2*time.Hour))
	seedSearchEvent(t, db, "j_search_2", "ws_x", "deploy production", []string{"j_001"}, now.Add(-1*time.Hour))
	// Different query hitting same evidence → +1 recall, +1 unique
	seedSearchEvent(t, db, "j_search_3", "ws_x", "shipping checklist", []string{"j_001"}, now.Add(-30*time.Minute))

	recall, unique := loadRecallMetrics(context.Background(), db, "ws_x",
		[]string{"j_001"}, now.Add(-7*24*time.Hour))
	if recall != 3 {
		t.Errorf("recall = %d, want 3", recall)
	}
	if unique != 2 {
		t.Errorf("unique = %d, want 2 (deploy production + shipping checklist)", unique)
	}
}

// TestLoadRecallMetrics_QueryCaseFolded asserts that query strings are
// case-folded for uniqueness — "Deploy" and "deploy" should count
// as the same query (operators don't expect capitalisation to
// fragment the metric).
func TestLoadRecallMetrics_QueryCaseFolded(t *testing.T) {
	db := openDB(t)
	defer db.Close()
	now := time.Now()
	seedSearchEvent(t, db, "j_search_1", "ws_x", "Deploy", []string{"j_001"}, now.Add(-1*time.Hour))
	seedSearchEvent(t, db, "j_search_2", "ws_x", "deploy", []string{"j_001"}, now.Add(-30*time.Minute))
	seedSearchEvent(t, db, "j_search_3", "ws_x", "DEPLOY", []string{"j_001"}, now.Add(-10*time.Minute))

	_, unique := loadRecallMetrics(context.Background(), db, "ws_x",
		[]string{"j_001"}, now.Add(-7*24*time.Hour))
	if unique != 1 {
		t.Errorf("case-folded unique = %d, want 1 (Deploy/deploy/DEPLOY all collapse)", unique)
	}
}

// TestLoadRecallMetrics_OldSearchesIgnored asserts the lookback cutoff
// is honoured — searches older than `since` don't contribute.
func TestLoadRecallMetrics_OldSearchesIgnored(t *testing.T) {
	db := openDB(t)
	defer db.Close()
	now := time.Now()
	// Inside the window
	seedSearchEvent(t, db, "j_recent", "ws_x", "recent", []string{"j_001"}, now.Add(-1*time.Hour))
	// Outside the window (8 days old vs 7-day cutoff)
	seedSearchEvent(t, db, "j_old", "ws_x", "stale", []string{"j_001"}, now.Add(-8*24*time.Hour))

	recall, unique := loadRecallMetrics(context.Background(), db, "ws_x",
		[]string{"j_001"}, now.Add(-7*24*time.Hour))
	if recall != 1 || unique != 1 {
		t.Errorf("expected only recent counted: (1,1), got (%d,%d)", recall, unique)
	}
}

// TestLoadRecallMetrics_WorkspaceIsolation asserts cross-workspace
// recall events don't leak between scoring passes — a search in
// ws_other never contributes to ws_x's metrics.
func TestLoadRecallMetrics_WorkspaceIsolation(t *testing.T) {
	db := openDB(t)
	defer db.Close()
	now := time.Now()
	seedSearchEvent(t, db, "j_ours", "ws_x", "deploy", []string{"j_001"}, now.Add(-1*time.Hour))
	seedSearchEvent(t, db, "j_theirs", "ws_other", "deploy", []string{"j_001"}, now.Add(-1*time.Hour))

	recall, unique := loadRecallMetrics(context.Background(), db, "ws_x",
		[]string{"j_001"}, now.Add(-7*24*time.Hour))
	if recall != 1 || unique != 1 {
		t.Errorf("expected only ws_x events counted: (1,1), got (%d,%d)", recall, unique)
	}
}

// TestComputeProposalScoresWithRecall_PopulatesScoreFields runs the
// full pipeline and asserts the populated CandidateMetrics flow
// through to the ScoreResult.RecallCount / UniqueQueries fields the
// Skill-promotion gate consults.
func TestComputeProposalScoresWithRecall_PopulatesScoreFields(t *testing.T) {
	db := openDB(t)
	defer db.Close()
	now := time.Now()
	// 4 searches hitting the rule's evidence, 3 distinct query strings.
	for i, q := range []string{"deploy", "deploy", "ship", "release"} {
		seedSearchEvent(t, db, "j_s_"+string(rune('a'+i)), "ws_x", q, []string{"j_001"}, now.Add(-time.Duration(i+1)*time.Hour))
	}
	rules := []LearnedRule{
		{Pattern: "deploy on friday", Action: "warn", Evidence: []string{"j_001"}, Confidence: 0.9},
	}
	scores := computeProposalScoresWithRecall(context.Background(), db, "ws_x", rules, 0, now)
	got := scores["deploy on friday"]
	if got.RecallCount != 4 {
		t.Errorf("RecallCount = %d, want 4", got.RecallCount)
	}
	if got.UniqueQueries != 3 {
		t.Errorf("UniqueQueries = %d, want 3 (deploy + ship + release)", got.UniqueQueries)
	}
}

// TestComputeProposalScoresWithRecall_NilDB_LegacyZeroBehaviour
// guards the back-compat contract: passing db=nil short-circuits to
// the zero-counter behaviour so tests + offline callers don't break
// after the signature widened.
func TestComputeProposalScoresWithRecall_NilDB_LegacyZeroBehaviour(t *testing.T) {
	rules := []LearnedRule{
		{Pattern: "x", Action: "y", Evidence: []string{"j_001"}, Confidence: 0.9},
	}
	scores := computeProposalScoresWithRecall(context.Background(), nil, "", rules, 0, time.Now())
	got := scores["x"]
	if got.RecallCount != 0 || got.UniqueQueries != 0 {
		t.Errorf("nil db should yield zero counters; got recall=%d unique=%d", got.RecallCount, got.UniqueQueries)
	}
}
