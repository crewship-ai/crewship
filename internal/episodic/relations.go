package episodic

import (
	"context"
	"database/sql"
	"fmt"
	"sort"
)

// RelationKind enumerates the edges we record in memory_relations.
// The enum is tiny by design: union-find over four relation types is
// tractable, more types (contradicts, elaborates, is-a, ...) would
// push us into real graph DB territory and the LLM to classify them
// would need its own judge.
type RelationKind string

const (
	RelationSimilar    RelationKind = "similar"
	RelationSupports   RelationKind = "supports"
	RelationRefutes    RelationKind = "refutes"
	RelationDuplicates RelationKind = "duplicates"
)

// Relation is one row in memory_relations.
type Relation struct {
	EntryID        string
	RelatedEntryID string
	Kind           RelationKind
	Score          float64
}

// LinkSimilarOnIndex is called by the indexer right after a new
// embedding lands. It finds the top-3 most cosine-similar existing
// entries above threshold (default 0.8), inserts symmetric
// (entry_id → related_entry_id, kind='similar') rows into
// memory_relations. Deduplication is handled by the table's
// composite primary key.
//
// Scope is always (workspace, crew) — cross-crew edges would let
// one crew's consolidation surface another's memory, which violates
// tenant boundaries.
//
// Cost: 1 query against journal_embeddings + 1 cosine pass in Go +
// up to 6 INSERT statements (3 symmetric edges). Runs on every
// embed, so keep it cheap; a future optimisation could batch by
// deferring link creation to a nightly job.
func LinkSimilarOnIndex(ctx context.Context, db *sql.DB, newEntryID, workspaceID, crewID string, newVec []float32, threshold float64) error {
	if threshold <= 0 || threshold > 1 {
		threshold = 0.8
	}
	// Gather candidate embeddings in the same (workspace, crew). Same-
	// agent edges are allowed — a single agent's memories can still be
	// similar to each other, and surfacing those links is useful for
	// the reflection prompt.
	rows, err := db.QueryContext(ctx, `
		SELECT em.entry_id, em.dim, em.vector
		  FROM journal_embeddings em
		  JOIN journal_entries e ON e.id = em.entry_id
		 WHERE em.workspace_id = ? AND em.dim > 0
		   AND em.entry_id != ?
		   AND (e.crew_id = ? OR (e.crew_id IS NULL AND ? = ''))`,
		workspaceID, newEntryID, crewID, crewID)
	if err != nil {
		return fmt.Errorf("relations: candidate query: %w", err)
	}
	defer rows.Close()

	type cand struct {
		id  string
		sim float64
	}
	var cands []cand
	for rows.Next() {
		var (
			id   string
			dim  int
			blob []byte
		)
		if err := rows.Scan(&id, &dim, &blob); err != nil {
			continue
		}
		if dim != len(newVec) {
			continue
		}
		vec, err := DecodeVector(blob, dim)
		if err != nil {
			continue
		}
		sim := cosine(newVec, vec)
		if sim >= threshold {
			cands = append(cands, cand{id: id, sim: sim})
		}
	}
	if err := rows.Err(); err != nil {
		return err
	}
	sort.Slice(cands, func(i, j int) bool { return cands[i].sim > cands[j].sim })
	if len(cands) > 3 {
		cands = cands[:3]
	}

	if len(cands) == 0 {
		return nil
	}

	// Symmetric edges — "a similar to b" implies "b similar to a" for
	// cosine. Two INSERT OR IGNOREs per pair so concurrent link runs
	// don't collide on the primary key.
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("relations: tx: %w", err)
	}
	stmt, err := tx.PrepareContext(ctx,
		`INSERT OR IGNORE INTO memory_relations (entry_id, related_entry_id, relation_kind, score)
		 VALUES (?, ?, ?, ?)`)
	if err != nil {
		_ = tx.Rollback()
		return fmt.Errorf("relations: prepare: %w", err)
	}
	defer stmt.Close()
	for _, c := range cands {
		if _, err := stmt.ExecContext(ctx, newEntryID, c.id, string(RelationSimilar), c.sim); err != nil {
			_ = tx.Rollback()
			return fmt.Errorf("relations: insert forward: %w", err)
		}
		if _, err := stmt.ExecContext(ctx, c.id, newEntryID, string(RelationSimilar), c.sim); err != nil {
			_ = tx.Rollback()
			return fmt.Errorf("relations: insert reverse: %w", err)
		}
	}
	return tx.Commit()
}

// LinkSupports records that a rule (consolidator output) is supported
// by a set of evidence entries. Called by the consolidator per-rule
// with the refs it cites. Edges are directional: rule → evidence.
// Score is 1.0 because supports is a curated relation, not a
// similarity score.
func LinkSupports(ctx context.Context, db *sql.DB, ruleEntryID string, evidenceIDs []string) error {
	if len(evidenceIDs) == 0 {
		return nil
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("relations supports: tx: %w", err)
	}
	stmt, err := tx.PrepareContext(ctx,
		`INSERT OR IGNORE INTO memory_relations (entry_id, related_entry_id, relation_kind, score)
		 VALUES (?, ?, ?, 1.0)`)
	if err != nil {
		_ = tx.Rollback()
		return fmt.Errorf("relations supports: prepare: %w", err)
	}
	defer stmt.Close()
	for _, eid := range evidenceIDs {
		if _, err := stmt.ExecContext(ctx, ruleEntryID, eid, string(RelationSupports)); err != nil {
			_ = tx.Rollback()
			return fmt.Errorf("relations supports: insert %s → %s: %w", ruleEntryID, eid, err)
		}
	}
	return tx.Commit()
}

// RelationsFor returns all outbound edges from entry id. Used by the
// health computer and by debug tooling.
func RelationsFor(ctx context.Context, db *sql.DB, entryID string) ([]Relation, error) {
	rows, err := db.QueryContext(ctx,
		`SELECT entry_id, related_entry_id, relation_kind, score
		   FROM memory_relations WHERE entry_id = ?`, entryID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Relation
	for rows.Next() {
		var r Relation
		var kind string
		if err := rows.Scan(&r.EntryID, &r.RelatedEntryID, &kind, &r.Score); err != nil {
			return nil, err
		}
		r.Kind = RelationKind(kind)
		out = append(out, r)
	}
	return out, rows.Err()
}
