package episodic

// Coverage tests for relations.go — LinkSimilarOnIndex happy path
// (symmetric edge insertion, top-3 cap, dim mismatch / corrupt blob
// skipping, crew scoping) and RelationsFor round-trips.

import (
	"context"
	"database/sql"
	"math"
	"testing"

	"github.com/crewship-ai/crewship/internal/journal"
)

// seedEmbedding inserts a journal_entries row + a journal_embeddings row
// so LinkSimilarOnIndex's JOIN finds the candidate.
func seedEmbedding(t *testing.T, db *sql.DB, id, ws, crew, agent string, vec []float32, dim int, blob []byte) {
	t.Helper()
	insertEntry(t, db, journal.Entry{
		ID: id, WorkspaceID: ws, CrewID: crew, AgentID: agent,
		Type: journal.EntryPeerEscalation, Severity: journal.SeverityWarn,
		ActorType: journal.ActorAgent, Summary: "seed " + id,
	})
	if blob == nil {
		blob = EncodeVector(vec)
	}
	_, err := db.Exec(`INSERT INTO journal_embeddings
		(entry_id, workspace_id, crew_id, agent_id, model, dim, vector, indexed_at)
		VALUES (?, ?, ?, ?, 't', ?, ?, '2026-01-01T00:00:00Z')`,
		id, ws, nullableStr(crew), nullableStr(agent), dim, blob)
	if err != nil {
		t.Fatalf("seed embedding %s: %v", id, err)
	}
}

// unitVec returns a 4-dim unit vector at angle theta from the x axis so
// cosine similarity against [1,0,0,0] is exactly cos(theta).
func unitVec(theta float64) []float32 {
	return []float32{float32(math.Cos(theta)), float32(math.Sin(theta)), 0, 0}
}

func TestLinkSimilarOnIndex_TopThreeSymmetricEdges(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	ctx := context.Background()

	// Five candidates above the 0.8 threshold with distinct similarities;
	// only the top three may be linked.
	sims := []float64{0.99, 0.97, 0.95, 0.90, 0.85}
	ids := []string{"c1", "c2", "c3", "c4", "c5"}
	for i, s := range sims {
		seedEmbedding(t, db, ids[i], "ws_test", "", "a1", unitVec(math.Acos(s)), 4, nil)
	}
	// One candidate with a mismatched dim — must be skipped.
	seedEmbedding(t, db, "wrongdim", "ws_test", "", "a1", []float32{1, 0, 0}, 3, nil)
	// One candidate with a corrupt blob (length != dim*4) — must be skipped.
	seedEmbedding(t, db, "corrupt", "ws_test", "", "a1", nil, 4, []byte{0x01, 0x02})

	newVec := []float32{1, 0, 0, 0}
	if err := LinkSimilarOnIndex(ctx, db, "newE", "ws_test", "", newVec, 0.8); err != nil {
		t.Fatalf("LinkSimilarOnIndex: %v", err)
	}

	rels, err := RelationsFor(ctx, db, "newE")
	if err != nil {
		t.Fatalf("RelationsFor: %v", err)
	}
	if len(rels) != 3 {
		t.Fatalf("expected 3 outbound edges (top-3 cap), got %d: %+v", len(rels), rels)
	}
	gotTargets := map[string]float64{}
	for _, r := range rels {
		if r.Kind != RelationSimilar {
			t.Errorf("edge kind = %q, want similar", r.Kind)
		}
		gotTargets[r.RelatedEntryID] = r.Score
	}
	for _, want := range []string{"c1", "c2", "c3"} {
		if _, ok := gotTargets[want]; !ok {
			t.Errorf("expected %s in top-3 targets, got %v", want, gotTargets)
		}
	}
	if _, ok := gotTargets["c4"]; ok {
		t.Error("c4 should be cut by top-3 cap")
	}
	// Symmetric reverse edges must exist with the same score.
	for target, score := range gotTargets {
		rev, err := RelationsFor(ctx, db, target)
		if err != nil {
			t.Fatalf("RelationsFor(%s): %v", target, err)
		}
		found := false
		for _, r := range rev {
			if r.RelatedEntryID == "newE" && r.Kind == RelationSimilar {
				found = true
				if math.Abs(r.Score-score) > 1e-9 {
					t.Errorf("reverse edge score %v != forward %v", r.Score, score)
				}
			}
		}
		if !found {
			t.Errorf("missing reverse edge %s → newE", target)
		}
	}
	// Score sanity: best edge ≈ 0.99.
	if s := gotTargets["c1"]; math.Abs(s-0.99) > 0.01 {
		t.Errorf("c1 score = %v, want ≈0.99", s)
	}
}

func TestLinkSimilarOnIndex_CrewScoping(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	ctx := context.Background()

	// Identical vectors so similarity is 1.0 for every candidate — only
	// scope decides inclusion.
	v := []float32{1, 0, 0, 0}
	seedEmbedding(t, db, "same_crew", "ws_test", "crew_a", "a1", v, 4, nil)
	seedEmbedding(t, db, "other_crew", "ws_test", "crew_b", "a2", v, 4, nil)
	seedEmbedding(t, db, "no_crew", "ws_test", "", "a3", v, 4, nil)

	if err := LinkSimilarOnIndex(ctx, db, "newE", "ws_test", "crew_a", v, 0.8); err != nil {
		t.Fatalf("LinkSimilarOnIndex: %v", err)
	}
	rels, err := RelationsFor(ctx, db, "newE")
	if err != nil {
		t.Fatalf("RelationsFor: %v", err)
	}
	if len(rels) != 1 || rels[0].RelatedEntryID != "same_crew" {
		t.Fatalf("crew_a scope should link only same_crew, got %+v", rels)
	}

	// Empty crew scope matches only NULL-crew entries.
	if err := LinkSimilarOnIndex(ctx, db, "newE2", "ws_test", "", v, 0.8); err != nil {
		t.Fatalf("LinkSimilarOnIndex(empty crew): %v", err)
	}
	rels2, err := RelationsFor(ctx, db, "newE2")
	if err != nil {
		t.Fatalf("RelationsFor: %v", err)
	}
	if len(rels2) != 1 || rels2[0].RelatedEntryID != "no_crew" {
		t.Fatalf("empty crew scope should link only no_crew, got %+v", rels2)
	}
}

func TestLinkSimilarOnIndex_IdempotentOnRerun(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	ctx := context.Background()

	v := []float32{1, 0, 0, 0}
	seedEmbedding(t, db, "cand", "ws_test", "", "a1", v, 4, nil)

	for i := 0; i < 2; i++ {
		if err := LinkSimilarOnIndex(ctx, db, "newE", "ws_test", "", v, 0.8); err != nil {
			t.Fatalf("run %d: %v", i, err)
		}
	}
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM memory_relations`).Scan(&n); err != nil {
		t.Fatalf("count: %v", err)
	}
	// One forward + one reverse, no duplicates thanks to INSERT OR IGNORE.
	if n != 2 {
		t.Errorf("expected 2 rows after rerun (symmetric pair, deduped), got %d", n)
	}
}

// Closed-DB error propagation: every relations helper must surface the
// driver error rather than swallow it.
func TestRelations_ClosedDBErrors(t *testing.T) {
	db := openTestDB(t)
	db.Close()
	ctx := context.Background()

	if err := LinkSimilarOnIndex(ctx, db, "x", "ws_test", "", []float32{1, 0, 0, 0}, 0.8); err == nil {
		t.Error("LinkSimilarOnIndex on closed DB should error")
	}
	if err := LinkSupports(ctx, db, "rule", []string{"ev"}); err == nil {
		t.Error("LinkSupports on closed DB should error")
	}
	if _, err := RelationsFor(ctx, db, "x"); err == nil {
		t.Error("RelationsFor on closed DB should error")
	}
}

// A RAISE(ABORT) trigger on memory_relations forces the insert inside the
// transaction to fail, exercising the rollback paths and verifying that
// no partial edge set survives.
func TestRelations_InsertFailureRollsBack(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	ctx := context.Background()

	seedEmbedding(t, db, "cand", "ws_test", "", "a1", []float32{1, 0, 0, 0}, 4, nil)
	if _, err := db.Exec(`CREATE TRIGGER fail_rel BEFORE INSERT ON memory_relations
		BEGIN SELECT RAISE(ABORT, 'injected failure'); END`); err != nil {
		t.Fatalf("create trigger: %v", err)
	}

	if err := LinkSimilarOnIndex(ctx, db, "newE", "ws_test", "", []float32{1, 0, 0, 0}, 0.8); err == nil {
		t.Error("LinkSimilarOnIndex should surface the insert failure")
	}
	if err := LinkSupports(ctx, db, "rule", []string{"ev1"}); err == nil {
		t.Error("LinkSupports should surface the insert failure")
	}
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM memory_relations`).Scan(&n); err != nil {
		t.Fatalf("count: %v", err)
	}
	if n != 0 {
		t.Errorf("rolled-back transaction left %d rows", n)
	}
}

// Dropping the table makes PrepareContext fail after BeginTx succeeded —
// the prepare-error branch distinct from the exec-error branch above.
func TestLinkSupports_PrepareError(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	if _, err := db.Exec(`DROP TABLE memory_relations`); err != nil {
		t.Fatalf("drop: %v", err)
	}
	if err := LinkSupports(context.Background(), db, "rule", []string{"ev1"}); err == nil {
		t.Error("LinkSupports without memory_relations table should error")
	}
}
