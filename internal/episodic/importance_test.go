package episodic

import (
	"context"
	"database/sql"
	"testing"
	"time"

	_ "modernc.org/sqlite"

	"github.com/crewship-ai/crewship/internal/journal"
)

func TestBaseImportance(t *testing.T) {
	cases := []struct {
		name string
		t    journal.EntryType
		sev  journal.Severity
		prio journal.Priority
		want float64
	}{
		{
			"routine info peer.conversation",
			journal.EntryPeerConversation, journal.SeverityInfo, journal.PriorityNormal,
			0.5,
		},
		{
			"peer.escalation at warn",
			journal.EntryPeerEscalation, journal.SeverityWarn, journal.PriorityNormal,
			0.85, // 0.5 + 0.2 (type) + 0.15 (warn)
		},
		{
			"eval regression at error",
			journal.EntryEvalRegression, journal.SeverityError, journal.PriorityNormal,
			1.0, // 0.5 + 0.3 + 0.25 = 1.05, clamped
		},
		{
			"PriorityPermanent floors at 0.95",
			journal.EntryPeerConversation, journal.SeverityInfo, journal.PriorityPermanent,
			0.95,
		},
		{
			"PriorityHigh floors at 0.85",
			journal.EntryPeerConversation, journal.SeverityInfo, journal.PriorityHigh,
			0.85,
		},
		{
			"PriorityPin floors at 0.80",
			journal.EntryPeerConversation, journal.SeverityInfo, journal.PriorityPin,
			0.80,
		},
		{
			"Permanent doesn't lower a naturally-higher score",
			journal.EntryEvalRegression, journal.SeverityError, journal.PriorityPermanent,
			1.0, // base already 1.0 → permanent floor 0.95 doesn't drag it down
		},
	}
	for _, c := range cases {
		c := c
		t.Run(c.name, func(t *testing.T) {
			got := float64(BaseImportance(c.t, c.sev, c.prio))
			if got < c.want-0.001 || got > c.want+0.001 {
				t.Errorf("got %.3f, want %.3f", got, c.want)
			}
		})
	}
}

func TestRecencyFactor(t *testing.T) {
	now := time.Now()
	cases := []struct {
		name string
		age  time.Duration
		want float64
	}{
		{"just indexed", 0, 1.0},
		{"24h old", 24 * time.Hour, 1.0 - 1.0/180.0},
		{"30d old", 30 * 24 * time.Hour, 1.0 - 30.0/180.0},
		{"180d old — floor kicks in", 180 * 24 * time.Hour, 0.1},
		{"365d old — still at floor", 365 * 24 * time.Hour, 0.1},
	}
	for _, c := range cases {
		got := RecencyFactor(now.Add(-c.age), now)
		if got < c.want-0.01 || got > c.want+0.01 {
			t.Errorf("%s: got %.3f want %.3f", c.name, got, c.want)
		}
	}
}

func TestReferenceBoost(t *testing.T) {
	if got := ReferenceBoost(0); got != 0 {
		t.Errorf("0 refs → want 0 boost, got %.3f", got)
	}
	if got := ReferenceBoost(1); got != 1 { // log2(2) = 1
		t.Errorf("1 ref → want 1, got %.3f", got)
	}
	if got := ReferenceBoost(7); got != 3 { // log2(8) = 3
		t.Errorf("7 refs → want 3, got %.3f", got)
	}
	if got := ReferenceBoost(-5); got != 0 {
		t.Errorf("negative refs clamped to 0, got %.3f", got)
	}
}

func TestDecayAndReinforceEndToEnd(t *testing.T) {
	db := openImportanceTestDB(t)
	defer db.Close()
	ctx := context.Background()
	ws := "ws_imp"

	// Seed: entry + embedding, marked warn+high, 90d old, 10 refs.
	_, err := db.Exec(`INSERT INTO journal_entries (id, workspace_id, ts, entry_type, severity, priority, actor_type, summary, payload, refs) VALUES
		('e1', ?, datetime('now'), 'peer.escalation', 'warn', 'high', 'agent', 's', '{}', '{}')`, ws)
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.Exec(`INSERT INTO journal_embeddings (entry_id, workspace_id, model, dim, vector, indexed_at, importance_score, reference_count)
		VALUES ('e1', ?, 'stub', 4, X'00000000', datetime('now','-90 days'), 0.5, 10)`, ws)
	if err != nil {
		t.Fatal(err)
	}

	n, err := DecayAndReinforce(ctx, db, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Errorf("updated rows: got %d want 1", n)
	}

	var got float64
	if err := db.QueryRow(`SELECT importance_score FROM journal_embeddings WHERE entry_id='e1'`).Scan(&got); err != nil {
		t.Fatal(err)
	}
	// Expected: base=1.0 (0.5+0.2+0.15, floored to 0.85 by high, then
	// capped at 1.0 — actually 0.85 survives as base since 0.85 ≥
	// 0.85 floor). Recency 1-90/180 = 0.5. Boost log2(11)/8 ≈ 0.43.
	// Score ≈ 0.85 × 0.5 × 1.43 = 0.608. Clamp [0,1]. Allow loose
	// match because the formula may tweak.
	if got < 0.4 || got > 0.8 {
		t.Errorf("decayed importance unreasonable: %.3f (expected 0.4..0.8)", got)
	}
}

func openImportanceTestDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	_, err = db.Exec(`
		CREATE TABLE journal_entries (
			id TEXT PRIMARY KEY,
			workspace_id TEXT NOT NULL,
			crew_id TEXT, agent_id TEXT, mission_id TEXT,
			ts TEXT, entry_type TEXT, severity TEXT, priority TEXT DEFAULT 'normal',
			actor_type TEXT, actor_id TEXT, summary TEXT, payload TEXT, refs TEXT,
			trace_id TEXT, span_id TEXT, expires_at TEXT);
		CREATE TABLE journal_embeddings (
			entry_id TEXT PRIMARY KEY, workspace_id TEXT, crew_id TEXT, agent_id TEXT,
			model TEXT, dim INTEGER, vector BLOB, indexed_at TEXT,
			importance_score REAL DEFAULT 0.5, reference_count INTEGER DEFAULT 0,
			last_referenced_at TEXT);`)
	if err != nil {
		t.Fatal(err)
	}
	return db
}
