package eval

import (
	"context"
	"database/sql"
	"testing"

	_ "modernc.org/sqlite"
)

// newCorpusDB stands up the three tables LoadCorpus joins, with just the
// columns it reads. It deliberately does NOT run the full migration set — the
// loader's contract is the query shape, and a focused schema keeps the test
// fast and independent of unrelated migrations.
func newCorpusDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	_, err = db.Exec(`
		CREATE TABLE keeper_requests (
			id TEXT PRIMARY KEY,
			requesting_agent_id TEXT NOT NULL DEFAULT '',
			credential_id TEXT NOT NULL DEFAULT '',
			request_type TEXT NOT NULL DEFAULT 'access',
			ollama_prompt TEXT,
			decision TEXT,
			risk_score INTEGER,
			created_at TEXT NOT NULL
		);
		CREATE TABLE escalations (
			id TEXT PRIMARY KEY,
			from_agent_id TEXT NOT NULL,
			credential_id TEXT,
			status TEXT NOT NULL DEFAULT 'PENDING',
			action TEXT DEFAULT 'approve',
			resolved_by TEXT,
			resolved_at TEXT
		);
		CREATE TABLE inbox_items (
			id TEXT PRIMARY KEY,
			kind TEXT NOT NULL,
			source_id TEXT NOT NULL,
			state TEXT NOT NULL DEFAULT 'unread',
			resolved_action TEXT,
			resolved_by_user_id TEXT
		)`)
	if err != nil {
		t.Fatalf("create tables: %v", err)
	}
	return db
}

func insertRow(t *testing.T, db *sql.DB, id, reqType, prompt, decision string, risk sql.NullInt64, createdAt string) {
	t.Helper()
	_, err := db.Exec(
		`INSERT INTO keeper_requests (id, request_type, ollama_prompt, decision, risk_score, created_at)
		 VALUES (?, ?, ?, ?, ?, ?)`,
		id, reqType, prompt, decision, risk, createdAt)
	if err != nil {
		t.Fatalf("insert %s: %v", id, err)
	}
}

func nullInt(n int64) sql.NullInt64 { return sql.NullInt64{Int64: n, Valid: true} }

func TestLoadCorpus_FiltersAndNormalizes(t *testing.T) {
	db := newCorpusDB(t)

	// Included: the live-activity request types (access/execute) with a settled
	// decision. NULL risk on e1 clamps to 1.
	insertRow(t, db, "a1", "access", "prompt-access", "allow", nullInt(2), "2026-01-01T00:00:03Z")
	insertRow(t, db, "e1", "execute", "prompt-execute", "DENY", sql.NullInt64{}, "2026-01-01T00:00:02Z")

	// Excluded, each for one reason:
	insertRow(t, db, "b1", "behavior", "prompt-behavior", "escalate", nullInt(5), "2026-01-01T00:00:08Z") // behavior excluded (WARN-space drift)
	insertRow(t, db, "bw", "behavior", "prompt-warn", "warn", nullInt(4), "2026-01-01T00:00:07Z")         // behavior WARN — would be silently dropped anyway
	insertRow(t, db, "sk", "skill_review", "prompt-skill", "allow", nullInt(1), "2026-01-01T00:00:09Z")   // wrong type
	insertRow(t, db, "mp", "access", "", "allow", nullInt(1), "2026-01-01T00:00:09Z")                     // empty prompt
	insertRow(t, db, "pd", "access", "prompt-pending", "PENDING", nullInt(1), "2026-01-01T00:00:09Z")     // unsettled

	got, err := LoadCorpus(context.Background(), db, 0)
	if err != nil {
		t.Fatalf("LoadCorpus: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d rows, want 2: %+v", len(got), got)
	}

	// Ordered newest-first by created_at: a1 (…03) > e1 (…02).
	if got[0].ID != "a1" || got[1].ID != "e1" {
		t.Fatalf("order = %s,%s; want a1,e1", got[0].ID, got[1].ID)
	}

	// behavior (incl. WARN) must not leak into the corpus while it's excluded.
	for _, r := range got {
		if r.RequestType == "behavior" {
			t.Errorf("behavior row %s must be excluded from the corpus, got %+v", r.ID, r)
		}
	}

	// Decision normalized to uppercase.
	if got[0].Label != Allow || got[1].Label != Deny {
		t.Errorf("decisions = %v,%v", got[0].Label, got[1].Label)
	}

	// risk passes through for a1; NULL risk on e1 clamps to 1.
	if got[0].IncumbentRisk != 2 || got[1].IncumbentRisk != 1 {
		t.Errorf("risks = %d,%d; want 2,1", got[0].IncumbentRisk, got[1].IncumbentRisk)
	}

	// Nobody ruled on either row, so both fall back to the incumbent's own
	// decision — and must say so rather than passing as ground truth.
	for _, r := range got {
		if r.LabelSource != LabelIncumbent || r.LabelOrigin != OriginIncumbentDecision {
			t.Errorf("row %s: source/origin = %q/%q, want incumbent fallback", r.ID, r.LabelSource, r.LabelOrigin)
		}
	}
}

func TestLoadCorpus_ClampsOutOfRangeRisk(t *testing.T) {
	db := newCorpusDB(t)
	insertRow(t, db, "hi", "access", "p", "allow", nullInt(99), "2026-01-01T00:00:01Z")
	insertRow(t, db, "lo", "access", "p", "allow", nullInt(-4), "2026-01-01T00:00:02Z")

	got, err := LoadCorpus(context.Background(), db, 0)
	if err != nil {
		t.Fatalf("LoadCorpus: %v", err)
	}
	byID := map[string]int{}
	for _, r := range got {
		byID[r.ID] = r.IncumbentRisk
	}
	if byID["hi"] != 10 {
		t.Errorf("hi risk = %d, want 10", byID["hi"])
	}
	if byID["lo"] != 1 {
		t.Errorf("lo risk = %d, want 1", byID["lo"])
	}
}

func TestLoadCorpus_Limit(t *testing.T) {
	db := newCorpusDB(t)
	insertRow(t, db, "old", "access", "p", "allow", nullInt(1), "2026-01-01T00:00:01Z")
	insertRow(t, db, "new", "access", "p", "allow", nullInt(1), "2026-01-01T00:00:05Z")

	got, err := LoadCorpus(context.Background(), db, 1)
	if err != nil {
		t.Fatalf("LoadCorpus: %v", err)
	}
	if len(got) != 1 || got[0].ID != "new" {
		t.Fatalf("limit=1 should return newest only; got %+v", got)
	}
}

func TestLoadCorpus_Empty(t *testing.T) {
	db := newCorpusDB(t)
	got, err := LoadCorpus(context.Background(), db, 0)
	if err != nil {
		t.Fatalf("LoadCorpus: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("want 0 rows, got %d", len(got))
	}
}
