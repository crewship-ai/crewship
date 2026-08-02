package memory

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// This file is the read-side regression suite for #1678. It asserts WHICH
// ROWS COME BACK for real questions against a realistic corpus — not the
// shape of the generated MATCH expression. A test on the expression string
// would pass while `file` stayed indexed and every query returned the whole
// tier, which is exactly the failure mode the issue warns about.
//
// The corpus and the question set are lifted from
// scripts/memory-retrieval-bench (build tag `memorybench`), §7, so the
// numbers in the PR and the numbers this test pins are the same numbers.

// retrievalDaily is a realistic agent daily note: a Czech/English mix, the
// heading style this product's writers actually use, and prose long enough
// that ChunkMarkdown splits it — which is what makes the implicit-AND bug
// bite.
const retrievalDaily = `# 2026-07-26

Dnes jsme dokončili refaktoring keeper aux slotů. Dva ze sedmi slotů se
projevily až po restartu serveru, protože konfigurace se četla jenom při
bootu. Oprava je v PR #1606.

Uživatel potvrdil, že commity nesmí obsahovat co-author trailer — ani
"generated with" patičku. Tohle platí globálně pro všechny projekty.

## Rozhodnutí

Retence žurnálu zůstává na třiceti dnech. Archivace je ztrátová: summary
se řeže na 200 znaků a payload na 400, a mazání proběhne i když archivace
selže. Je to známé riziko, zatím ho neřešíme.

## Follow-ups

- Ověřit, že mutace opravdu aplikovala, než uvěříme zelenému testu.
- Reconciler pro dev sloty potřebuje rebase na main, jinak backend hodí 502.
`

const retrievalAgentMD = `# Agent notes

## Deployment

The dev slot deploy is durable only if the branch is pinned in the
infra-crewship slots.yaml and the reconcile trigger is fired afterwards.
Rebase onto main FIRST or the backend 502s against a newer-schema DB.

## Credentials

The keeper gatekeeper denies a credential when its trust zone is unset.
A credential named github-token used to kill every run of its crew.
`

// retrievalDistractor is vocabulary-overlapping noise. Without it an OR
// query cannot be wrong — every hit is the right file by default — so the
// ranking assertions below would be vacuous.
const retrievalDistractor = `# Standup

## Notes

The crew reviewed the deploy checklist and the run log. Nothing about
slots, nothing about the keeper, nothing decided.

## Other

Paperwork, an invoice, and a reminder that the run finished.
`

// retrievalNoStopwords is a file whose prose contains none of the English
// function words in the stopword list. It is the control for the
// all-stopwords query case: a query made only of stopwords must not drag
// this file back.
//
// Its filename shares no stem with its content on purpose. unicode61 folds
// diacritics, so a file called `inventar.md` holding the word "Inventář"
// would match the query "inventar" through its CONTENT — and the
// path-column assertion below would pass or fail for the wrong reason.
const retrievalNoStopwords = `# Inventář

## Součástky

Šroubovák, kladivo, pilník, kleště, metr.
`

func setupRetrievalCorpus(t *testing.T) *Engine {
	t.Helper()
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "daily"), 0o755); err != nil {
		t.Fatal(err)
	}
	files := map[string]string{
		"daily/2026-07-26.md": retrievalDaily,
		"AGENT.md":            retrievalAgentMD,
		"daily/2026-07-20.md": retrievalDistractor,
		"nastroje.md":         retrievalNoStopwords,
	}
	for name, body := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	eng, err := New(dir, DefaultConfig())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { eng.Close() })
	if err := eng.Reindex(); err != nil {
		t.Fatal(err)
	}
	return eng
}

func topFiles(t *testing.T, eng *Engine, q string, n int) []string {
	t.Helper()
	res, err := eng.Search(context.Background(), q, 10)
	if err != nil {
		t.Fatalf("Search(%q): %v", q, err)
	}
	var out []string
	for i, r := range res {
		if i >= n {
			break
		}
		out = append(out, r.File)
	}
	return out
}

func containsFile(hay []string, needle string) bool {
	for _, h := range hay {
		if h == needle {
			return true
		}
	}
	return false
}

// TestSearch_NaturalQuestionsReachTheRightFile is the headline regression.
// Every question below returned ZERO rows under the space-joined (implicit
// AND) builder because it demanded all its words inside one ~500-char
// chunk. The [MEMORY GAP] block tells a woken agent to run exactly this
// kind of search, so a zero-row answer is a broken recovery path, not a
// ranking nit.
//
// What this change does NOT fix, and is not claimed to: a question whose
// content words are absent from the note in the FORM the note used them.
// "jak dlouho se drží žurnál" against a note saying "Retence žurnálu" needs
// stemming, and "journal retention" against the same Czech note needs
// translation. Those are §7.7 of docs/prd/memory-retrieval-layer.md, parked
// pending an eval, and no OR rewrite reaches them — the bench measures both
// as misses under every builder it tried.
func TestSearch_NaturalQuestionsReachTheRightFile(t *testing.T) {
	eng := setupRetrievalCorpus(t)

	cases := []struct {
		q    string
		want string
	}{
		{"aux slots", "daily/2026-07-26.md"},
		{"keeper aux sloty", "daily/2026-07-26.md"},
		{"co-author trailer", "daily/2026-07-26.md"},
		{"jaká je retence žurnálu", "daily/2026-07-26.md"},
		{"why does the backend return 502 after deploy", "AGENT.md"},
		{"how do I deploy to a dev slot", "AGENT.md"},
		{"what did we decide about the co-author trailer", "daily/2026-07-26.md"},
		{"trust zone", "AGENT.md"},
	}
	for _, tc := range cases {
		got := topFiles(t, eng, tc.q, 3)
		if len(got) == 0 {
			t.Errorf("%q returned ZERO rows — the failure #1678 exists to fix", tc.q)
			continue
		}
		if !containsFile(got, tc.want) {
			t.Errorf("%q: want %s in the top 3, got %v", tc.q, tc.want, got)
		}
	}
}

// TestSearch_PathTokensAreNotSearchable is the precision half of the same
// change, and the reason 7.1 and 7.2 could not ship apart. `memory_chunks`
// used to index `file` as a searchable column, so the token "md" matched
// every chunk of every markdown file. Under the old implicit-AND builder
// that was invisible; under OR it would hand back the entire tier for any
// query containing a path word.
func TestSearch_PathTokensAreNotSearchable(t *testing.T) {
	eng := setupRetrievalCorpus(t)

	for _, q := range []string{"md", "daily", "nastroje"} {
		res, err := eng.Search(context.Background(), q, 50)
		if err != nil {
			t.Fatalf("Search(%q): %v", q, err)
		}
		if len(res) != 0 {
			t.Errorf("%q matched %d chunk(s) through the file column; want 0 (file must be UNINDEXED). Files: %v",
				q, len(res), func() []string {
					var f []string
					for _, r := range res {
						f = append(f, r.File)
					}
					return f
				}())
		}
	}

	// The control: a word that IS in the prose still matches, so the
	// assertion above is about the path column and not about search being
	// broken outright.
	if got := topFiles(t, eng, "gatekeeper", 3); !containsFile(got, "AGENT.md") {
		t.Errorf("control failed: 'gatekeeper' should hit AGENT.md, got %v", got)
	}
}

// TestSearch_OrDoesNotBecomeMatchEverything guards the other direction. An
// OR builder is only safe if a question whose words are absent from the
// corpus still returns nothing.
func TestSearch_OrDoesNotBecomeMatchEverything(t *testing.T) {
	eng := setupRetrievalCorpus(t)

	for _, q := range []string{
		"kubernetes ingress controller",
		"what is a kubernetes ingress",
	} {
		res, err := eng.Search(context.Background(), q, 50)
		if err != nil {
			t.Fatalf("Search(%q): %v", q, err)
		}
		if len(res) != 0 {
			t.Errorf("%q matched %d chunk(s); no word of it is in the corpus, want 0", q, len(res))
		}
	}

	// Partial match is the point of OR: one real word among junk still
	// finds the file that has it.
	if got := topFiles(t, eng, "zzzz gatekeeper qqqq", 3); !containsFile(got, "AGENT.md") {
		t.Errorf("partial match failed: want AGENT.md, got %v", got)
	}
}

// TestSearch_AllStopwordQueryStaysBounded pins the decided behaviour for
// the degenerate case: a query made entirely of stopwords must not become
// an empty MATCH expression (an FTS5 error) and must not match everything.
// It falls back to the unfiltered words, so it answers with the files that
// actually contain those words — and only those.
func TestSearch_AllStopwordQueryStaysBounded(t *testing.T) {
	eng := setupRetrievalCorpus(t)

	// Every word of these is a stopword AND appears somewhere in the
	// corpus, so "returns nothing" can only mean the fallback did not fire.
	for _, q := range []string{"what is the", "the and a", "is it that"} {
		res, err := eng.Search(context.Background(), q, 50)
		if err != nil {
			t.Fatalf("Search(%q) errored — an all-stopword query must degrade, not fail: %v", q, err)
		}
		// It must still search. Dropping every word and returning nothing
		// would be a silent empty answer to a real question — the same
		// class of failure as the implicit-AND bug, arrived at differently.
		if len(res) == 0 {
			t.Errorf("%q returned nothing; the corpus contains those words, so the fallback did not fire", q)
		}
		for _, r := range res {
			if r.File == "nastroje.md" {
				t.Errorf("%q returned nastroje.md, which contains none of its words — the query matched everything", q)
			}
		}
	}
}

// TestSearch_PunctuationOnlyQueriesDoNotError guards the new construct. The
// phrase leg quotes whatever the caller typed, and a "word" made only of
// punctuation quotes to a phrase with no tokens in it — the shape most
// likely to trip the FTS5 parser and turn a nonsense query into a 500.
func TestSearch_PunctuationOnlyQueriesDoNotError(t *testing.T) {
	eng := setupRetrievalCorpus(t)

	for _, q := range []string{
		"—", "— —", "-", "--", "…", "🙂", "!!!", "?", ". .", "«»", "'", "``",
		"™ ®", "e.g.", "n/a", "50%", "#1606", "keeper —", "— keeper —",
	} {
		if _, err := eng.Search(context.Background(), q, 10); err != nil {
			t.Errorf("Search(%q) errored: %v (expression: %q)", q, err, sanitizeFTSQuery(q))
		}
	}

	// A punctuation-wrapped real word must still find its file, so the loop
	// above is not passing merely because everything returns nothing.
	if got := topFiles(t, eng, "— gatekeeper —", 3); !containsFile(got, "AGENT.md") {
		t.Errorf("punctuation around a real word lost the hit: %v", got)
	}
}

// TestSearch_ExactPhraseLeadsTheExpression is why the built expression
// starts with the whole question as a phrase rather than only its terms.
// An OR of single terms has no way to prefer adjacency: BM25 rewards term
// frequency, so a chunk that mentions both words separately, more often,
// outranks the chunk that actually says the thing.
//
// The corpus below is tuned to isolate exactly that — same padding, same
// length, and `scattered.md` carries one extra occurrence of "zone", which
// is enough to win a terms-only ranking (measured: it does) and not enough
// to survive the phrase leg.
func TestSearch_ExactPhraseLeadsTheExpression(t *testing.T) {
	dir := t.TempDir()
	pad := "The reconciler ran and the deploy queue drained without incident. "
	write := func(name, body string) {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("says-it.md", "# A\n\n"+pad+"trust zone "+pad+"\n")
	write("scattered.md", "# B\n\n"+pad+"trust "+pad+"zone zone "+pad+"\n")
	// Filler so "trust" and "zone" are rare enough to carry positive IDF.
	// With a two-document corpus every term is in every document and BM25
	// scores everything at zero, which makes any ranking assertion vacuous.
	for i := 0; i < 30; i++ {
		write(fmt.Sprintf("filler-%02d.md", i), fmt.Sprintf("# Filler %d\n\n%s\n", i, pad))
	}
	eng, err := New(dir, DefaultConfig())
	if err != nil {
		t.Fatal(err)
	}
	defer eng.Close()
	if err := eng.Reindex(); err != nil {
		t.Fatal(err)
	}

	// Control: without the phrase leg, the scattered file wins. If this
	// ever stops holding the test below has stopped proving anything.
	if got := topFiles(t, eng, `"trust" OR "zone"`, 2); len(got) == 0 || got[0] != "scattered.md" {
		t.Fatalf("control failed: terms-only ranking should favour scattered.md, got %v", got)
	}

	got := topFiles(t, eng, "trust zone", 2)
	if len(got) == 0 {
		t.Fatal("no hits at all")
	}
	if got[0] != "says-it.md" {
		t.Errorf("the chunk containing the exact phrase should rank first, got %v", got)
	}
}

// TestSearch_ExplicitFTS5SyntaxStillPassesThrough keeps the escape hatch a
// caller who writes real FTS5 gets today. The OR rewrite is for plain
// questions; it must not rewrite a deliberate expression.
func TestSearch_ExplicitFTS5SyntaxStillPassesThrough(t *testing.T) {
	eng := setupRetrievalCorpus(t)

	// An explicit AND still means AND: both words are in AGENT.md's
	// credentials section, neither is in the daily note's prose.
	got := topFiles(t, eng, "gatekeeper AND credential", 3)
	if !containsFile(got, "AGENT.md") {
		t.Fatalf("explicit AND lost its hit: %v", got)
	}
	// And an explicit AND across two files that share no chunk returns
	// nothing — proving it was not silently rewritten to OR.
	res, err := eng.Search(context.Background(), "gatekeeper AND šroubovák", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(res) != 0 {
		t.Errorf("explicit AND was rewritten: got %d rows, want 0", len(res))
	}
}

// ---------------------------------------------------------------- schema

// TestInitSchema_RebuildsAnIndexCreatedWithTheOldColumnDefinition is the
// half of #1678 that a fresh-database test cannot reach.
// `CREATE VIRTUAL TABLE IF NOT EXISTS` does not alter an existing table, so
// every index.sqlite written before this change keeps `file` as a
// SEARCHABLE column forever — the OR builder ships, the prerequisite does
// not, and every query with a path word returns the tier. The two other
// ways to get this wrong are dropping the table and never rebuilding it
// (search silently empty until the next full Reindex) and re-migrating on
// every open.
func TestInitSchema_RebuildsAnIndexCreatedWithTheOldColumnDefinition(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "index.sqlite")

	// Write an index with the pre-#1678 schema and some rows in it.
	old, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := old.Exec(`
		CREATE VIRTUAL TABLE IF NOT EXISTS memory_chunks USING fts5(
			file,
			content,
			tokenize='unicode61'
		);
		CREATE TABLE IF NOT EXISTS memory_meta (key TEXT PRIMARY KEY, value TEXT);
	`); err != nil {
		t.Fatal(err)
	}
	rows := [][2]string{
		{"daily/2026-07-26.md", "the keeper gatekeeper denies a credential when its trust zone is unset"},
		{"AGENT.md", "the dev slot deploy is durable only if the branch is pinned"},
		{"notes/legacy.md", "nothing here about slots at all"},
	}
	for _, r := range rows {
		if _, err := old.Exec(`INSERT INTO memory_chunks (file, content) VALUES (?, ?)`, r[0], r[1]); err != nil {
			t.Fatal(err)
		}
	}
	// Sanity: on the OLD schema the path token matches every row.
	var n int
	if err := old.QueryRow(`SELECT count(*) FROM memory_chunks WHERE memory_chunks MATCH '"md"'`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != len(rows) {
		t.Fatalf("precondition failed: old schema should match all %d rows for 'md', got %d", len(rows), n)
	}
	if err := old.Close(); err != nil {
		t.Fatal(err)
	}

	// Open it with the production engine. No Reindex — the point is that
	// the migration itself must neither leave the old shape nor lose data,
	// because not every caller of New reindexes.
	eng, err := New(dir, DefaultConfig())
	if err != nil {
		t.Fatalf("New on a pre-#1678 index: %v", err)
	}
	defer eng.Close()

	st, err := eng.Status(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if st.TotalChunks != len(rows) {
		t.Errorf("migration lost rows: %d chunks after, %d before", st.TotalChunks, len(rows))
	}

	// The old shape is gone: a path-only token matches nothing.
	res, err := eng.Search(context.Background(), "md", 50)
	if err != nil {
		t.Fatal(err)
	}
	if len(res) != 0 {
		t.Errorf("existing index kept the old shape: 'md' matched %d chunk(s), want 0", len(res))
	}

	// ...and the content survived and is still searchable.
	got := topFiles(t, eng, "gatekeeper", 3)
	if !containsFile(got, "daily/2026-07-26.md") {
		t.Errorf("content lost or unsearchable after migration: %v", got)
	}
}

// TestInitSchema_IsStableAcrossReopens proves the migration is not
// re-run on every open. A check that never recognises its own output
// would rebuild the index at every boot — data-preserving, so no other
// assertion here would notice, and quietly O(corpus) forever.
func TestInitSchema_IsStableAcrossReopens(t *testing.T) {
	dir := t.TempDir()
	eng, err := New(dir, DefaultConfig())
	if err != nil {
		t.Fatal(err)
	}
	defer eng.Close()

	var stored string
	if err := eng.db.QueryRow(
		`SELECT sql FROM sqlite_master WHERE type='table' AND name='memory_chunks'`,
	).Scan(&stored); err != nil {
		t.Fatal(err)
	}
	if !chunksSchemaIsCurrent(stored) {
		t.Fatalf("the schema this code writes is not recognised as current — every open would re-migrate.\nstored: %q", stored)
	}
	if !strings.Contains(strings.ToUpper(stored), "UNINDEXED") {
		t.Errorf("memory_chunks was created without an UNINDEXED file column: %q", stored)
	}
}

// TestReindexPath_OptimizesTheIndexPeriodically covers §7.3. FTS5 never
// rewrites in place — every ReindexPath appends a delete tombstone and a
// new segment — so a long-lived agent's index fragments monotonically and
// queries slow down against unchanged data. Measured in the bench at
// 197µs → 36µs for ~3ms of work across 3000 writes.
func TestReindexPath_OptimizesTheIndexPeriodically(t *testing.T) {
	dir := t.TempDir()
	eng, err := New(dir, DefaultConfig())
	if err != nil {
		t.Fatal(err)
	}
	defer eng.Close()

	segments := func() int {
		var n int
		if err := eng.db.QueryRow(`SELECT count(*) FROM memory_chunks_data`).Scan(&n); err != nil {
			t.Fatalf("count segments: %v", err)
		}
		return n
	}

	writes := optimizeEveryNWrites * 2
	for i := 0; i < writes; i++ {
		body := fmt.Sprintf("# Daily\n\n## Notes\n\nrevision %d of the keeper reconciler note\n", i)
		if err := os.WriteFile(filepath.Join(dir, "daily.md"), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
		if _, err := eng.ReindexPath(context.Background(), "daily.md"); err != nil {
			t.Fatal(err)
		}
	}

	// Without a periodic optimize this grows with the write count. The
	// bound is deliberately loose — the property is "bounded", not an
	// exact segment count, which is an FTS5 internal.
	if got := segments(); got > 6 {
		t.Errorf("index fragmented to %d %%_data rows after %d writes; a periodic optimize should keep it small", got, writes)
	}

	// Optimize must not eat the data.
	got := topFiles(t, eng, "reconciler", 3)
	if !containsFile(got, "daily.md") {
		t.Errorf("content unsearchable after optimize: %v", got)
	}
}
