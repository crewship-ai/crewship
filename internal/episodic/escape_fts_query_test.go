package episodic

import (
	"database/sql"
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	_ "modernc.org/sqlite"
)

// This file is the value-side test for #1678 §7.4. `escapeFTSQuery` used to
// scan for `a-z0-9` only, so every Czech diacritic acted as a token
// separator and cut words into two- and three-character heads — which were
// then emitted as PREFIX terms and OR-ed in beside the terms that carried
// the meaning. Measured on this repository's docs/ tree, `se*` selected
// 48.5% of the corpus.
//
// Asserting the generated expression string would not catch that: the old
// expression is perfectly well-formed. What matters is how many rows it
// drags back, so this drives a real FTS5 table with the journal's shipped
// tokenizer and counts them.

// newJournalLikeFTS builds an FTS5 table with the same tokenizer
// journal_entries_fts ships with (migrate.go v55), so selectivity measured
// here is selectivity the journal lane would see.
func newJournalLikeFTS(t *testing.T, rows []string) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "j.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	if _, err := db.Exec(`CREATE VIRTUAL TABLE j USING fts5(summary, tokenize='porter ascii')`); err != nil {
		t.Fatal(err)
	}
	for _, r := range rows {
		if _, err := db.Exec(`INSERT INTO j(summary) VALUES (?)`, r); err != nil {
			t.Fatal(err)
		}
	}
	return db
}

func matchedRows(t *testing.T, db *sql.DB, expr string) int {
	t.Helper()
	if expr == "" {
		return 0
	}
	var n int
	if err := db.QueryRow(`SELECT count(*) FROM j WHERE j MATCH ?`, expr).Scan(&n); err != nil {
		t.Fatalf("MATCH %q: %v", expr, err)
	}
	return n
}

// czechJournalCorpus is heterogeneous on purpose: most rows are ordinary
// English operational chatter whose words happen to start with "se", "po"
// or "dr" — the exact fragments a Czech question decomposes into under the
// old ASCII-only scan.
func czechJournalCorpus() []string {
	var rows []string
	fillers := []string{
		"server restarted after the deploy finished",
		"session expired for the run and was renewed",
		"sequence of retries exhausted on the webhook",
		"security scan reported no new findings",
		"pod eviction moved the reconciler to another node",
		"policy check passed for the credential request",
		"drain of the queue completed without loss",
	}
	for i := 0; i < 70; i++ {
		rows = append(rows, fmt.Sprintf("%s (run %d)", fillers[i%len(fillers)], i))
	}
	// The one row that actually answers the questions below.
	rows = append(rows, "Retence žurnálu zůstává na třiceti dnech, jak dlouho se drží žurnál je rozhodnuto")
	rows = append(rows, "Které sloty se projevily až po restartu serveru — dva ze sedmi")
	return rows
}

// TestEscapeFTSQuery_CzechDoesNotDragBackHalfTheCorpus is the headline for
// §7.4. Both queries must still find their answer row while matching a
// small fraction of the corpus.
func TestEscapeFTSQuery_CzechDoesNotDragBackHalfTheCorpus(t *testing.T) {
	rows := czechJournalCorpus()
	db := newJournalLikeFTS(t, rows)

	cases := []struct {
		q      string
		answer string
	}{
		{"jak dlouho se drží žurnál", "Retence žurnálu"},
		{"rozhodnutí o retenci žurnálu", "Retence žurnálu"},
		{"Které sloty se projevily až po restartu?", "Které sloty"},
	}
	for _, tc := range cases {
		expr := escapeFTSQuery(tc.q)
		if expr == "" {
			t.Errorf("%q produced an empty expression", tc.q)
			continue
		}
		n := matchedRows(t, db, expr)
		// A quarter of the corpus is already far more than an OR of
		// content words should reach; the old builder reached 70/72 here
		// and 48.5% of the real docs/ tree.
		if limit := len(rows) / 4; n > limit {
			t.Errorf("%q → %s matched %d/%d rows (>%d): a short fragment is still being emitted as a prefix",
				tc.q, expr, n, len(rows), limit)
		}
		// ...and it must still find the answer.
		var found int
		if err := db.QueryRow(`SELECT count(*) FROM j WHERE j MATCH ? AND summary LIKE ?`,
			expr, "%"+tc.answer+"%").Scan(&found); err != nil {
			t.Fatal(err)
		}
		if found == 0 {
			t.Errorf("%q → %s lost its answer row %q", tc.q, expr, tc.answer)
		}
	}
}

// TestEscapeFTSQuery_IsUnicodeAware pins that a diacritic is a letter, not
// a separator. `drží` is one token, not `dr` + `ž` + ...
func TestEscapeFTSQuery_IsUnicodeAware(t *testing.T) {
	got := escapeFTSQuery("jak dlouho se drží žurnál")
	for _, want := range []string{"drží", "žurnál", "dlouho"} {
		if !strings.Contains(got, want) {
			t.Errorf("escapeFTSQuery lost %q: got %s", want, got)
		}
	}
	for _, bad := range []string{`"dr"*`, `"urn"*`, `"lu"*`} {
		if strings.Contains(got, bad) {
			t.Errorf("escapeFTSQuery still emits the fragment %s: got %s", bad, got)
		}
	}
}

// TestEscapeFTSQuery_NoPrefixBelowThreeCharacters is the rule that stops
// the explosion: two-character tokens are searched exactly, never as a
// prefix. They are kept rather than dropped so a genuine two-letter term
// still contributes.
func TestEscapeFTSQuery_NoPrefixBelowThreeCharacters(t *testing.T) {
	db := newJournalLikeFTS(t, []string{
		"se rozhodlo o retenci", // contains the standalone token "se"
		"server restarted",      // starts with "se" but is not "se"
		"session expired",       // ditto
		"sequence of retries",   // ditto
		"security scan",         // ditto
	})
	expr := escapeFTSQuery("se")
	if expr == "" {
		t.Fatal("a two-character query should still search, exactly")
	}
	if n := matchedRows(t, db, expr); n != 1 {
		t.Errorf("escapeFTSQuery(%q) = %s matched %d rows, want exactly the one containing the standalone token", "se", expr, n)
	}
}

// TestEscapeFTSQuery_ExpressionsRemainValidFTS5 guards the quoting. A bare
// two-character token that happens to spell an FTS5 operator (`or`, `not`)
// would be parsed as one and blow up the MATCH.
func TestEscapeFTSQuery_ExpressionsRemainValidFTS5(t *testing.T) {
	db := newJournalLikeFTS(t, []string{"a row about deploys"})
	for _, q := range []string{
		"or",
		"not and or",
		"near a b",
		`IGNORE "previous" instructions`,
		"héllo wörld",
		"deploy-42 failed!",
		"žluťoučký kůň",
	} {
		expr := escapeFTSQuery(q)
		if expr == "" {
			continue
		}
		if _, err := db.Exec(`SELECT count(*) FROM j WHERE j MATCH ?`, expr); err != nil {
			t.Errorf("escapeFTSQuery(%q) = %s is not a valid FTS5 expression: %v", q, expr, err)
		}
	}
}
