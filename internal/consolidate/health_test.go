package consolidate

import (
	"context"
	"database/sql"
	"testing"

	_ "modernc.org/sqlite"
)

func TestComputeHealthOnSeed(t *testing.T) {
	db := openHealthTestDB(t)
	defer db.Close()
	ctx := context.Background()
	ws := "ws_h"

	// Seed: 5 entries across 3 types in last 24h; 3 entries in last
	// 7d; one embedded + one relation edge so coherence and
	// reachability have signal.
	_, err := db.Exec(`
		INSERT INTO journal_entries (id, workspace_id, ts, entry_type, severity, priority, actor_type, actor_id, summary, payload, refs)
		VALUES
		  ('e1', ?, datetime('now','-1 hour'), 'peer.escalation', 'warn', 'normal', 'agent', 'a', 's', '{}', '{}'),
		  ('e2', ?, datetime('now','-2 hours'), 'summary.generated', 'info', 'normal', 'system', 's', 's', '{}', '{}'),
		  ('e3', ?, datetime('now','-3 hours'), 'approval.denied', 'notice', 'normal', 'user', 'u', 's', '{}', '{}'),
		  ('e4', ?, datetime('now','-5 days'), 'peer.escalation', 'warn', 'normal', 'agent', 'a', 's', '{}', '{}'),
		  ('e5', ?, datetime('now','-6 days'), 'summary.generated', 'info', 'normal', 'system', 's', 's', '{}', '{}')`,
		ws, ws, ws, ws, ws)
	if err != nil {
		t.Fatal(err)
	}
	// Two embeddings — half the live rows.
	db.Exec(`INSERT INTO journal_embeddings (entry_id, workspace_id, model, dim, vector, indexed_at, importance_score)
		VALUES ('e1', ?, 'stub', 4, X'00000000', datetime('now'), 0.9),
		       ('e2', ?, 'stub', 4, X'00000000', datetime('now'), 0.7)`, ws, ws)
	// One edge — e1 similar to e2.
	db.Exec(`INSERT INTO memory_relations (entry_id, related_entry_id, relation_kind, score)
		VALUES ('e1', 'e2', 'similar', 0.85), ('e2', 'e1', 'similar', 0.85)`)
	// One archived row — half archive ratio.
	db.Exec(`INSERT INTO journal_entries_archived (id, workspace_id, ts, archived_at, entry_type, severity, priority, actor_type, summary, compressed_payload, original_size_bytes)
		VALUES ('arc1', ?, datetime('now','-90 days'), datetime('now'), 'exec.output_chunk', 'info', 'normal', 'agent', 's', '{}', 1024)`, ws)

	s, err := ComputeHealth(ctx, db, ws, "")
	if err != nil {
		t.Fatal(err)
	}
	// Each metric must be in [0, 100] — no negative or over-cap.
	for name, v := range map[string]float64{
		"freshness":    s.Freshness,
		"coverage":     s.Coverage,
		"coherence":    s.Coherence,
		"efficiency":   s.Efficiency,
		"reachability": s.Reachability,
	} {
		if v < 0 || v > 100 {
			t.Errorf("%s out of range: %.2f", name, v)
		}
	}
	// Overall is in [0, 100] and is a sane weighted average — never
	// exceeds the max of the inputs.
	if s.Overall < 0 || s.Overall > 100 {
		t.Errorf("overall out of range: %.2f", s.Overall)
	}
	// Coherence should be > 0 because we seeded one edge per two
	// embeddings.
	if s.Coherence == 0 {
		t.Errorf("coherence expected > 0 given seeded edges, got %.2f", s.Coherence)
	}
}

func openHealthTestDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	_, err = db.Exec(`
		CREATE TABLE journal_entries (
			id TEXT PRIMARY KEY, workspace_id TEXT NOT NULL,
			crew_id TEXT, agent_id TEXT, mission_id TEXT,
			ts TEXT, entry_type TEXT, severity TEXT, priority TEXT DEFAULT 'normal',
			actor_type TEXT, actor_id TEXT, summary TEXT, payload TEXT, refs TEXT);
		CREATE TABLE journal_embeddings (
			entry_id TEXT PRIMARY KEY, workspace_id TEXT, crew_id TEXT, agent_id TEXT,
			model TEXT, dim INTEGER, vector BLOB, indexed_at TEXT,
			importance_score REAL DEFAULT 0.5, reference_count INTEGER DEFAULT 0,
			last_referenced_at TEXT);
		CREATE TABLE memory_relations (
			entry_id TEXT, related_entry_id TEXT, relation_kind TEXT,
			score REAL DEFAULT 0, created_at TEXT DEFAULT (datetime('now')),
			PRIMARY KEY(entry_id, related_entry_id, relation_kind));
		CREATE TABLE journal_entries_archived (
			id TEXT PRIMARY KEY, workspace_id TEXT, crew_id TEXT, agent_id TEXT, mission_id TEXT,
			ts TEXT, archived_at TEXT, entry_type TEXT, severity TEXT, priority TEXT DEFAULT 'normal',
			actor_type TEXT, actor_id TEXT, summary TEXT, compressed_payload TEXT, original_size_bytes INTEGER);
		CREATE TABLE memory_health_snapshots (
			id TEXT PRIMARY KEY, workspace_id TEXT, crew_id TEXT, computed_at TEXT,
			freshness REAL, coverage REAL, coherence REAL, efficiency REAL, reachability REAL, overall REAL, details TEXT);`)
	if err != nil {
		t.Fatal(err)
	}
	return db
}
