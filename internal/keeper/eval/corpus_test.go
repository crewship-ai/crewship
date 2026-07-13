package eval

import (
	"context"
	"database/sql"
	"testing"

	_ "modernc.org/sqlite"
)

// newCorpusDB stands up a minimal keeper_requests table with just the columns
// LoadCorpus reads. It deliberately does NOT run the full migration set — the
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
			request_type TEXT NOT NULL DEFAULT 'access',
			ollama_prompt TEXT,
			decision TEXT,
			risk_score INTEGER,
			created_at TEXT NOT NULL
		)`)
	if err != nil {
		t.Fatalf("create table: %v", err)
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

	// Included: the three live-activity request types with a settled decision.
	insertRow(t, db, "a1", "access", "prompt-access", "allow", nullInt(2), "2026-01-01T00:00:03Z")
	insertRow(t, db, "e1", "execute", "prompt-execute", "DENY", nullInt(9), "2026-01-01T00:00:02Z")
	insertRow(t, db, "b1", "behavior", "prompt-behavior", "escalate", sql.NullInt64{}, "2026-01-01T00:00:01Z")

	// Excluded, each for one reason:
	insertRow(t, db, "sk", "skill_review", "prompt-skill", "allow", nullInt(1), "2026-01-01T00:00:09Z") // wrong type
	insertRow(t, db, "mp", "access", "", "allow", nullInt(1), "2026-01-01T00:00:09Z")                   // empty prompt
	insertRow(t, db, "pd", "access", "prompt-pending", "PENDING", nullInt(1), "2026-01-01T00:00:09Z")   // unsettled

	got, err := LoadCorpus(context.Background(), db, 0)
	if err != nil {
		t.Fatalf("LoadCorpus: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("got %d rows, want 3: %+v", len(got), got)
	}

	// Ordered newest-first by created_at: a1 (…03) > e1 (…02) > b1 (…01).
	if got[0].ID != "a1" || got[1].ID != "e1" || got[2].ID != "b1" {
		t.Fatalf("order = %s,%s,%s; want a1,e1,b1", got[0].ID, got[1].ID, got[2].ID)
	}

	// Decision normalized to uppercase.
	if got[0].Recorded != Allow || got[1].Recorded != Deny || got[2].Recorded != Escalate {
		t.Errorf("decisions = %v,%v,%v", got[0].Recorded, got[1].Recorded, got[2].Recorded)
	}

	// NULL risk on the behavior row clamps to 1; others pass through.
	if got[0].RecordedRisk != 2 || got[1].RecordedRisk != 9 || got[2].RecordedRisk != 1 {
		t.Errorf("risks = %d,%d,%d; want 2,9,1", got[0].RecordedRisk, got[1].RecordedRisk, got[2].RecordedRisk)
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
		byID[r.ID] = r.RecordedRisk
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
