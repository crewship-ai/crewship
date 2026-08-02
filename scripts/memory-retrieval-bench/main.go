//go:build memorybench

// Command memory-retrieval-bench measures the retrieval and storage layer
// behind `memory.search`. It is excluded from normal builds by the
// `memorybench` build tag so it never enters the shipped binary or CI.
//
// Run:
//
//	go run -tags memorybench ./scripts/memory-retrieval-bench
//
// Every number this prints is a measurement taken on the machine that runs
// it against the real SQLite build the product ships (modernc.org/sqlite,
// pure Go, the same driver internal/memory/engine.go opens). Nothing here
// is inferred; where a property is analytic rather than measured the
// section says so in its own output.
package main

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	_ "modernc.org/sqlite"
)

func main() {
	fmt.Println("# memory retrieval layer — measurements")
	fmt.Println()
	sectionTokenizerCzech()
	sectionAsciiHighBytes()
	sectionDiacritics()
	sectionIndexSize()
	sectionPrefixIndex()
	sectionRRF()
	runRegistered()
}

// register / runRegistered keep the printed report in section order no
// matter which file's init() ran first — Go gives no ordering guarantee
// across files in a package, and a report whose sections arrive shuffled is
// much harder to read against the document that cites it. Keys are the
// section number times ten so a "12b" can sit between 12 and 13.
var registry = map[int]func(){}

func register(n int, f func()) {
	if _, dup := registry[n]; dup {
		panic(fmt.Sprintf("duplicate bench section key %d", n))
	}
	registry[n] = f
}

func runRegistered() {
	keys := make([]int, 0, len(registry))
	for k := range registry {
		keys = append(keys, k)
	}
	sort.Ints(keys)
	for _, k := range keys {
		registry[k]()
	}
}

// ---------------------------------------------------------------- helpers

func mustOpen(path string) *sql.DB {
	db, err := sql.Open("sqlite", path+"?_pragma=journal_mode(wal)&_pragma=busy_timeout(5000)&_pragma=synchronous(NORMAL)&_pragma=temp_store(MEMORY)")
	if err != nil {
		panic(err)
	}
	return db
}

func mustExec(db *sql.DB, q string, args ...any) {
	if _, err := db.ExecContext(context.Background(), q, args...); err != nil {
		panic(fmt.Sprintf("exec %q: %v", q, err))
	}
}

// matchCount runs a MATCH and returns the number of rows, or -1 plus the
// error string when FTS5 rejects the expression.
func matchCount(db *sql.DB, table, expr string) (int, string) {
	var n int
	err := db.QueryRow(
		fmt.Sprintf("SELECT count(*) FROM %s WHERE %s MATCH ?", table, table), expr).Scan(&n)
	if err != nil {
		return -1, err.Error()
	}
	return n, ""
}

func tick(ok bool) string {
	if ok {
		return "HIT "
	}
	return "MISS"
}

// ------------------------------------------------- 1. tokenizers vs Czech

// czechDocs are short, realistic Czech memory lines. Each is the kind of
// sentence this product's owner actually writes into a daily note.
var czechDocs = []string{
	"Rozhodli jsme se, že commity nesmí obsahovat co-author trailer.",
	"Keeper má sedm aux slotů; dva se projevily až po restartu serveru.",
	"Uživatel požaduje, aby se nasazení dělalo přes reconciler, ne ručně.",
	"Diskutovali jsme rozhodnutí o retenci žurnálu — třicet dní je strop.",
	"Agent se probudil po týdnu a neměl žádný kontext o předchozí práci.",
	"We decided the deployment pipeline should retry twice before failing.",
	"The keeper gatekeeper denies credentials when the trust zone is unset.",
}

// czechQueries pair a natural query with the index of the doc a human would
// call the right answer. The queries are inflected differently from the
// document text on purpose — that is the entire point of the Czech problem.
var czechQueries = []struct {
	q    string
	want int
	note string
}{
	{"commit", 0, "English stem inside Czech sentence"},
	{"commity", 0, "exact surface form"},
	{"rozhodnutí", 3, "exact surface form, diacritics"},
	{"rozhodli", 0, "exact surface form, no diacritics"},
	{"rozhodnuti", 3, "same word typed WITHOUT diacritics"},
	{"rozhodnutích", 3, "different case ending than the document"},
	{"slotů", 1, "genitive plural, document has 'slotů'"},
	{"slot", 1, "bare stem, document has 'slotů'"},
	{"nasazení", 2, "exact surface form"},
	{"nasazeni", 2, "same word typed WITHOUT diacritics"},
	{"žurnál", 3, "bare stem, document has 'žurnálu'"},
	{"týden", 4, "nominative; document has 'týdnu'"},
	{"kontext", 4, "exact surface form"},
	{"deployment", 5, "English control"},
	{"deployments", 5, "English plural — porter should stem this"},
	{"credentials", 6, "English control"},
}

type tokCfg struct {
	name  string
	spec  string
	extra string // extra FTS5 options, e.g. prefix
}

var tokenizers = []tokCfg{
	{name: "unicode61 (TODAY)", spec: "unicode61"},
	{name: "unicode61 rd=0", spec: "unicode61 remove_diacritics 0"},
	{name: "unicode61 rd=2", spec: "unicode61 remove_diacritics 2"},
	{name: "porter ascii (journal TODAY)", spec: "porter ascii"},
	{name: "porter unicode61", spec: "porter unicode61"},
	{name: "trigram", spec: "trigram"},
	{name: "trigram ci+rd", spec: "trigram case_sensitive 0 remove_diacritics 1"},
}

func buildCzechTable(dir string, t tokCfg) (*sql.DB, error) {
	db := mustOpen(filepath.Join(dir, "czech.sqlite"))
	stmt := fmt.Sprintf(
		"CREATE VIRTUAL TABLE docs USING fts5(body, tokenize=%q%s);",
		t.spec, t.extra)
	if _, err := db.ExecContext(context.Background(), stmt); err != nil {
		db.Close()
		return nil, err
	}
	for _, d := range czechDocs {
		mustExec(db, "INSERT INTO docs(body) VALUES (?)", d)
	}
	return db, nil
}

// candidateExprs are the MATCH expressions the product actually produces for
// a one-word query, plus the two alternatives worth comparing.
func candidateExprs(word string) map[string]string {
	return map[string]string{
		// internal/memory/search.go sanitizeFTSQuery: quote every word.
		"exact (memory.search today)": `"` + word + `"`,
		// internal/episodic/hybrid.go escapeFTSQuery: ASCII-only, prefix, OR.
		"prefix": `"` + word + `"*`,
	}
}

func sectionTokenizerCzech() {
	fmt.Println("## 1. Tokenizer recall on Czech")
	fmt.Println()
	fmt.Println("Corpus: 7 short memory lines (5 Czech, 2 English).")
	fmt.Println("Each query is a word a human would type looking for one specific line.")
	fmt.Println("`exact` is the expression internal/memory/search.go builds today.")
	fmt.Println("`prefix` is what internal/episodic/hybrid.go builds today.")
	fmt.Println()

	type row struct {
		tok           string
		exactHits     int
		prefixHits    int
		exactFalsePos int
	}
	var summary []row

	for _, t := range tokenizers {
		dir, _ := os.MkdirTemp("", "tok")
		db, err := buildCzechTable(dir, t)
		if err != nil {
			fmt.Printf("### %s — UNAVAILABLE: %v\n\n", t.name, err)
			os.RemoveAll(dir)
			continue
		}
		fmt.Printf("### %s\n\n", t.name)
		fmt.Printf("| query | expects | exact | prefix | note |\n")
		fmt.Printf("|---|---|---|---|---|\n")
		r := row{tok: t.name}
		for _, q := range czechQueries {
			ex := candidateExprs(q.q)
			eHit := docMatches(db, ex["exact (memory.search today)"], q.want)
			pHit := docMatches(db, ex["prefix"], q.want)
			if eHit {
				r.exactHits++
			}
			if pHit {
				r.prefixHits++
			}
			fmt.Printf("| `%s` | doc %d | %s | %s | %s |\n",
				q.q, q.want, tick(eHit), tick(pHit), q.note)
		}
		fmt.Printf("\n**%s: exact %d/%d, prefix %d/%d**\n\n",
			t.name, r.exactHits, len(czechQueries), r.prefixHits, len(czechQueries))
		summary = append(summary, r)
		db.Close()
		os.RemoveAll(dir)
	}

	fmt.Println("### Summary")
	fmt.Println()
	fmt.Println("| tokenizer | exact-expr recall | prefix-expr recall |")
	fmt.Println("|---|---|---|")
	for _, r := range summary {
		fmt.Printf("| %s | %d/%d | %d/%d |\n", r.tok,
			r.exactHits, len(czechQueries), r.prefixHits, len(czechQueries))
	}
	fmt.Println()
}

// docMatches reports whether the wanted document is in the MATCH result set.
func docMatches(db *sql.DB, expr string, wantRowid int) bool {
	rows, err := db.Query("SELECT rowid FROM docs WHERE docs MATCH ?", expr)
	if err != nil {
		return false
	}
	defer rows.Close()
	for rows.Next() {
		var id int
		if err := rows.Scan(&id); err != nil {
			return false
		}
		if id-1 == wantRowid {
			return true
		}
	}
	return false
}

// ------------------------------ 2. what the ascii tokenizer does with >0x7F

func sectionAsciiHighBytes() {
	fmt.Println("## 2. What `ascii` does with bytes above 0x7F")
	fmt.Println()
	fmt.Println("journal_entries_fts and conversation_messages_fts both use")
	fmt.Println("`tokenize='porter ascii'`. This probes the tokenizer directly")
	fmt.Println("rather than trusting the documentation.")
	fmt.Println()

	dir, _ := os.MkdirTemp("", "ascii")
	defer os.RemoveAll(dir)
	db := mustOpen(filepath.Join(dir, "a.sqlite"))
	defer db.Close()
	mustExec(db, `CREATE VIRTUAL TABLE t USING fts5(body, tokenize='ascii')`)
	mustExec(db, `CREATE VIRTUAL TABLE u USING fts5(body, tokenize='unicode61')`)
	for _, tbl := range []string{"t", "u"} {
		mustExec(db, fmt.Sprintf("INSERT INTO %s(body) VALUES (?)", tbl), "rozhodnutí o keeperu")
		mustExec(db, fmt.Sprintf("INSERT INTO %s(body) VALUES (?)", tbl), "žádný kontext")
	}

	fmt.Println("Indexed: `rozhodnutí o keeperu` and `žádný kontext`.")
	fmt.Println()
	fmt.Println("| MATCH expression | ascii | unicode61 |")
	fmt.Println("|---|---|---|")
	for _, expr := range []string{
		`"rozhodnutí"`, `"rozhodnut"`, `"rozhodnut"*`, `"rozhodnuti"`,
		`"žádný"`, `"adny"`, `"zadny"`, `"kontext"`,
	} {
		a, ae := matchCount(db, "t", expr)
		u, ue := matchCount(db, "u", expr)
		fmt.Printf("| `%s` | %s | %s |\n", expr, fmtCount(a, ae), fmtCount(u, ue))
	}
	fmt.Println()

	// Dump the actual token stream each tokenizer produced, using the
	// fts5vocab shadow table — this is the ground truth, not inference.
	for _, tbl := range []string{"t", "u"} {
		mustExec(db, fmt.Sprintf(
			"CREATE VIRTUAL TABLE v_%s USING fts5vocab(%s, row)", tbl, tbl))
		rows, err := db.Query(fmt.Sprintf("SELECT term FROM v_%s ORDER BY term", tbl))
		if err != nil {
			fmt.Printf("vocab %s: %v\n", tbl, err)
			continue
		}
		var terms []string
		for rows.Next() {
			var s string
			rows.Scan(&s)
			terms = append(terms, s)
		}
		rows.Close()
		name := map[string]string{"t": "ascii", "u": "unicode61"}[tbl]
		fmt.Printf("Tokens produced by **%s**: `%s`\n\n", name, strings.Join(terms, "` `"))
	}
}

func fmtCount(n int, e string) string {
	if n < 0 {
		return "ERR: " + e
	}
	return fmt.Sprintf("%d", n)
}

// ------------------------------------------- 3. diacritic folding behaviour

func sectionDiacritics() {
	fmt.Println("## 3. Diacritic folding — which Czech letters actually fold")
	fmt.Println()
	fmt.Println("`remove_diacritics 1` is the unicode61 DEFAULT, i.e. what")
	fmt.Println("internal/memory/engine.go:103 gets today. `2` is the corrected")
	fmt.Println("version. This table shows, per Czech letter, whether a query")
	fmt.Println("typed WITHOUT the diacritic finds a document written WITH it.")
	fmt.Println()

	letters := []struct{ with, without string }{
		{"á", "a"}, {"é", "e"}, {"í", "i"}, {"ó", "o"}, {"ú", "u"}, {"ý", "y"},
		{"č", "c"}, {"ď", "d"}, {"ě", "e"}, {"ň", "n"}, {"ř", "r"},
		{"š", "s"}, {"ť", "t"}, {"ž", "z"}, {"ů", "u"},
	}

	dir, _ := os.MkdirTemp("", "dia")
	defer os.RemoveAll(dir)
	db := mustOpen(filepath.Join(dir, "d.sqlite"))
	defer db.Close()

	cfgs := []struct{ name, spec string }{
		{"rd=1 (default, TODAY)", "unicode61 remove_diacritics 1"},
		{"rd=0", "unicode61 remove_diacritics 0"},
		{"rd=2", "unicode61 remove_diacritics 2"},
	}
	for i, c := range cfgs {
		mustExec(db, fmt.Sprintf(
			"CREATE VIRTUAL TABLE d%d USING fts5(body, tokenize=%q)", i, c.spec))
		for _, l := range letters {
			// A distinct word per letter so a hit is unambiguous.
			mustExec(db, fmt.Sprintf("INSERT INTO d%d(body) VALUES (?)", i),
				"x"+l.with+"x")
		}
	}

	fmt.Printf("| letter | query |")
	for _, c := range cfgs {
		fmt.Printf(" %s |", c.name)
	}
	fmt.Println()
	fmt.Printf("|---|---|")
	for range cfgs {
		fmt.Printf("---|")
	}
	fmt.Println()

	folded := make([]int, len(cfgs))
	for _, l := range letters {
		fmt.Printf("| %s | `x%sx` |", l.with, l.without)
		for i := range cfgs {
			n, _ := matchCount(db, fmt.Sprintf("d%d", i), `"x`+l.without+`x"`)
			ok := n > 0
			if ok {
				folded[i]++
			}
			fmt.Printf(" %s |", tick(ok))
		}
		fmt.Println()
	}
	fmt.Printf("| **folded** | |")
	for i := range cfgs {
		fmt.Printf(" **%d/%d** |", folded[i], len(letters))
	}
	fmt.Println()
	fmt.Println()
}

// --------------------------------------------------- 4. index size on disk

// corpusDoc generates a realistic memory chunk of roughly the size the
// chunker emits (defaultChunkSize = 500 chars).
func corpusDoc(i int) string {
	return fmt.Sprintf(`## Decision %d

Rozhodli jsme se, že deployment %d poběží přes reconciler a ne ručně.
The keeper gatekeeper for slot %d denies credentials when the trust zone
is unset, and the aux model binding needs an explicit key reference.
Poznámka: retence žurnálu je třicet dní, potom se řádky archivují.
Follow-up %d: confirm the mutation actually applied before trusting green.`,
		i, i, i, i)
}

func sectionIndexSize() {
	fmt.Println("## 4. Index size and build cost per tokenizer")
	fmt.Println()
	const nDocs = 4000
	fmt.Printf("Corpus: %d chunks of ~%d bytes each (%.1f MB of text),\n",
		nDocs, len(corpusDoc(0)), float64(nDocs*len(corpusDoc(0)))/(1024*1024))
	fmt.Println("mixed Czech/English, shaped like what ChunkMarkdown emits.")
	fmt.Println()
	fmt.Println("| tokenizer | index bytes | x text | distinct terms |")
	fmt.Println("|---|---|---|---|")

	textBytes := 0
	for i := 0; i < nDocs; i++ {
		textBytes += len(corpusDoc(i))
	}

	cfgs := []tokCfg{
		{name: "unicode61 (TODAY)", spec: "unicode61"},
		{name: "unicode61 + prefix='2 3'", spec: "unicode61", extra: ", prefix='2 3'"},
		{name: "unicode61 + prefix='2 3 4'", spec: "unicode61", extra: ", prefix='2 3 4'"},
		{name: "porter unicode61", spec: "porter unicode61"},
		{name: "trigram", spec: "trigram"},
	}
	for _, c := range cfgs {
		dir, _ := os.MkdirTemp("", "size")
		p := filepath.Join(dir, "s.sqlite")
		db := mustOpen(p)
		stmt := fmt.Sprintf("CREATE VIRTUAL TABLE docs USING fts5(body, tokenize=%q%s)", c.spec, c.extra)
		if _, err := db.ExecContext(context.Background(), stmt); err != nil {
			fmt.Printf("| %s | UNAVAILABLE: %v | | |\n", c.name, err)
			db.Close()
			os.RemoveAll(dir)
			continue
		}
		tx, _ := db.Begin()
		st, _ := tx.Prepare("INSERT INTO docs(body) VALUES (?)")
		for i := 0; i < nDocs; i++ {
			st.Exec(corpusDoc(i))
		}
		st.Close()
		tx.Commit()
		mustExec(db, "INSERT INTO docs(docs) VALUES('optimize')")
		mustExec(db, "PRAGMA wal_checkpoint(TRUNCATE)")

		var terms int
		mustExec(db, "CREATE VIRTUAL TABLE vv USING fts5vocab(docs, row)")
		db.QueryRow("SELECT count(*) FROM vv").Scan(&terms)
		db.Close()

		fi, _ := os.Stat(p)
		fmt.Printf("| %s | %s | %.2fx | %d |\n", c.name,
			human(fi.Size()), float64(fi.Size())/float64(textBytes), terms)
		os.RemoveAll(dir)
	}
	fmt.Printf("\nRaw text indexed: %s\n\n", human(int64(textBytes)))
}

func human(n int64) string {
	switch {
	case n > 1<<20:
		return fmt.Sprintf("%.1f MB", float64(n)/(1<<20))
	case n > 1<<10:
		return fmt.Sprintf("%.1f KB", float64(n)/(1<<10))
	}
	return fmt.Sprintf("%d B", n)
}

// ----------------------------------------------- 5. does a prefix index pay

func sectionPrefixIndex() {
	fmt.Println("## 5. Prefix index — does it pay at our corpus size")
	fmt.Println()
	fmt.Println("A `prefix='2 3'` index only speeds up `term*` queries.")
	fmt.Println("internal/memory/search.go never emits one for a plain query;")
	fmt.Println("internal/episodic/hybrid.go emits one for EVERY term.")
	fmt.Println()
	const nDocs = 4000
	for _, c := range []tokCfg{
		{name: "no prefix index", spec: "unicode61"},
		{name: "prefix='2 3'", spec: "unicode61", extra: ", prefix='2 3'"},
	} {
		dir, _ := os.MkdirTemp("", "pfx")
		db := mustOpen(filepath.Join(dir, "p.sqlite"))
		mustExec(db, fmt.Sprintf(
			"CREATE VIRTUAL TABLE docs USING fts5(body, tokenize=%q%s)", c.spec, c.extra))
		tx, _ := db.Begin()
		st, _ := tx.Prepare("INSERT INTO docs(body) VALUES (?)")
		for i := 0; i < nDocs; i++ {
			st.Exec(corpusDoc(i))
		}
		st.Close()
		tx.Commit()
		mustExec(db, "INSERT INTO docs(docs) VALUES('optimize')")

		// Ask SQLite what it will actually do, rather than timing noise.
		rows, _ := db.Query(`EXPLAIN QUERY PLAN SELECT rowid FROM docs WHERE docs MATCH '"depl"*'`)
		var plan []string
		for rows.Next() {
			var a, b, c2 int
			var d string
			rows.Scan(&a, &b, &c2, &d)
			plan = append(plan, d)
		}
		rows.Close()
		n, _ := matchCount(db, "docs", `"depl"*`)
		fmt.Printf("- **%s**: `\"depl\"*` matches %d rows; plan: %s\n",
			c.name, n, strings.Join(plan, " / "))
		db.Close()
		os.RemoveAll(dir)
	}
	fmt.Println()
}

// ------------------------------------------------- 6. RRF over disjoint sets

func sectionRRF() {
	fmt.Println("## 6. RRF with disjoint corpora — is k=60 doing anything?")
	fmt.Println()
	fmt.Println("internal/memory/hybrid.go fuses two lists that can never share")
	fmt.Println("an identifier (markdown chunks vs journal rows). This computes")
	fmt.Println("the resulting order for several k to show what k changes.")
	fmt.Println()

	fts := []string{"fts#1", "fts#2", "fts#3", "fts#4"}
	epi := []string{"epi#1", "epi#2", "epi#3", "epi#4"}

	fmt.Println("| k | resulting order |")
	fmt.Println("|---|---|")
	for _, k := range []float64{0, 1, 10, 60, 600, 60000} {
		type it struct {
			id string
			s  float64
			i  int
		}
		var all []it
		for i, f := range fts {
			all = append(all, it{f, 1 / (k + float64(i+1)), len(all)})
		}
		for i, e := range epi {
			all = append(all, it{e, 1 / (k + float64(i+1)), len(all)})
		}
		sort.SliceStable(all, func(a, b int) bool { return all[a].s > all[b].s })
		var ids []string
		for _, a := range all {
			ids = append(ids, a.id)
		}
		fmt.Printf("| %g | %s |\n", k, strings.Join(ids, " → "))
	}
	fmt.Println()
	fmt.Println("This is analytic, not statistical: with disjoint lists every")
	fmt.Println("item's score is 1/(k+rank) from exactly ONE list, so the order")
	fmt.Println("is a pure function of rank. k cannot change a pure function of")
	fmt.Println("rank into a different ordering.")
	fmt.Println()
}
