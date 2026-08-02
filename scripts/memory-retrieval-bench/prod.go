//go:build memorybench

// prod.go measures the PRODUCTION code path — internal/memory's real
// Engine, chunker and query sanitiser — rather than a replica of it.
// Anything measured here is what `memory.search` actually does.
package main

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/crewship-ai/crewship/internal/memory"
)

func init() {
	register(70, sectionProdPath)
	register(80, sectionColumnWeights)
	register(90, sectionChunking)
	register(100, sectionPrefixTiming)
	register(110, sectionWriteCost)
}

// ------------------------------------------- 7. the real memory.search path

// dailyNote is a realistic daily memory file: a Czech/English mix, the
// heading style the product's own writers use, and no `## ` at all for the
// first one — which is what most agent-written notes look like.
const dailyNote = `# 2026-07-26

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

const agentMD = `# Agent notes

## Deployment

The dev slot deploy is durable only if the branch is pinned in the
infra-crewship slots.yaml and the reconcile trigger is fired afterwards.
Rebase onto main FIRST or the backend 502s against a newer-schema DB.

## Credentials

The keeper gatekeeper denies a credential when its trust zone is unset.
A credential named github-token used to kill every run of its crew.
`

// realQueries are phrased the way an agent or a person actually asks —
// full questions, not single keywords. This is the case the [MEMORY GAP]
// recovery path depends on.
var realQueries = []struct {
	q    string
	want string
}{
	{"aux slots", "daily/2026-07-26.md"},
	{"keeper aux sloty", "daily/2026-07-26.md"},
	{"co-author trailer", "daily/2026-07-26.md"},
	{"what did we decide about journal retention", "daily/2026-07-26.md"},
	{"journal retention", "daily/2026-07-26.md"},
	{"jak dlouho se drží žurnál", "daily/2026-07-26.md"},
	{"why does the backend return 502 after deploy", "AGENT.md"},
	{"deploy 502", "AGENT.md"},
	{"trust zone", "AGENT.md"},
	{"how do I deploy to a dev slot", "AGENT.md"},
}

func newProdEngine() (string, *memory.Engine) {
	dir, _ := os.MkdirTemp("", "prodmem")
	os.MkdirAll(filepath.Join(dir, "daily"), 0o755)
	os.WriteFile(filepath.Join(dir, "daily", "2026-07-26.md"), []byte(dailyNote), 0o644)
	os.WriteFile(filepath.Join(dir, "AGENT.md"), []byte(agentMD), 0o644)
	e, err := memory.New(dir, memory.DefaultConfig())
	if err != nil {
		panic(err)
	}
	if err := e.Reindex(); err != nil {
		panic(err)
	}
	return dir, e
}

func sectionProdPath() {
	fmt.Println("## 7. The real `memory.search` path, end to end")
	fmt.Println()
	fmt.Println("Two files indexed by the production `memory.Engine`: a Czech daily")
	fmt.Println("note and an English AGENT.md. Queries are phrased the way a woken")
	fmt.Println("agent or a person actually asks.")
	fmt.Println()
	dir, e := newProdEngine()
	defer os.RemoveAll(dir)
	defer e.Close()

	st, _ := e.Status(context.Background())
	fmt.Printf("Index: %d files, %d chunks.\n\n", st.TotalFiles, st.TotalChunks)

	fmt.Println("| query | words | hits | top hit | correct file? |")
	fmt.Println("|---|---|---|---|---|")
	var found, zero int
	for _, q := range realQueries {
		res, err := e.Search(context.Background(), q.q, 5)
		if err != nil {
			fmt.Printf("| `%s` | | ERR | %v | |\n", q.q, err)
			continue
		}
		top := "—"
		ok := false
		if len(res) > 0 {
			top = res[0].File
			for _, r := range res {
				if r.File == q.want {
					ok = true
				}
			}
		} else {
			zero++
		}
		if ok {
			found++
		}
		fmt.Printf("| `%s` | %d | %d | `%s` | %s |\n",
			q.q, len(strings.Fields(q.q)), len(res), top, tick(ok))
	}
	fmt.Printf("\n**%d/%d queries found the right file. %d returned ZERO hits.**\n\n",
		found, len(realQueries), zero)

	fmt.Println("The MATCH expression the shipping sanitiser builds:")
	fmt.Println()
	fmt.Println("| query | MATCH expression sent to FTS5 |")
	fmt.Println("|---|---|")
	for _, q := range realQueries {
		fmt.Printf("| `%s` | `%s` |\n", q.q, memory.SanitizeFTSQueryForBench(q.q))
	}
	fmt.Println()
	fmt.Println("FTS5's implicit operator between two bare terms is **AND**, so a")
	fmt.Println("space-joined N-word question requires all N words in ONE ~500-char")
	fmt.Println("chunk — the #1678 failure. Since #1678 the builder emits")
	fmt.Println("phrase-OR-terms with stopwords removed instead.")
	fmt.Println()

	// The bare term-OR-term arm, kept as the comparison the fix was
	// chosen against. The first column is whatever the shipping builder
	// does today, so this table is the before/after when run either side
	// of a change to sanitizeFTSQuery.
	fmt.Println("Same corpus, same engine, queries rewritten term-OR-term:")
	fmt.Println()
	fmt.Println("| query | production builder | bare OR |")
	fmt.Println("|---|---|---|")
	var andF, orF int
	for _, q := range realQueries {
		a, _ := e.Search(context.Background(), q.q, 5)
		orExpr := strings.Join(quoteEach(strings.Fields(q.q)), " OR ")
		o, _ := e.Search(context.Background(), orExpr, 5)
		af := containsFile(a, q.want)
		of := containsFile(o, q.want)
		if af {
			andF++
		}
		if of {
			orF++
		}
		fmt.Printf("| `%s` | %s (%d hits) | %s (%d hits) |\n", q.q, tick(af), len(a), tick(of), len(o))
	}
	fmt.Printf("\n**production %d/%d → bare OR %d/%d.**\n\n", andF, len(realQueries), orF, len(realQueries))
}

func quoteEach(ws []string) []string {
	out := make([]string, 0, len(ws))
	for _, w := range ws {
		w = strings.Trim(w, `"?.,!:;`)
		if w != "" {
			out = append(out, `"`+w+`"`)
		}
	}
	return out
}

func containsFile(rs []memory.SearchResult, f string) bool {
	for _, r := range rs {
		if r.File == f {
			return true
		}
	}
	return false
}

// -------------------------------------- 8. the `file` column and BM25 weights

func sectionColumnWeights() {
	fmt.Println("## 8. The `file` column, and why bm25 weights could not fix it")
	fmt.Println()
	fmt.Println("`memory_chunks` used to be `fts5(file, content)` with `file`")
	fmt.Println("SEARCHABLE, so a path fragment was a full-strength search term")
	fmt.Println("competing with the note's prose — `md` matched 100% of a markdown")
	fmt.Println("corpus. Since #1678 the column is `file UNINDEXED`. The list below")
	fmt.Println("is the shipping engine; the table after it is the same corpus")
	fmt.Println("measured both ways, which is the comparison that decided it.")
	fmt.Println()
	dir, e := newProdEngine()
	defer os.RemoveAll(dir)
	defer e.Close()

	for _, q := range []string{"daily", "AGENT", "2026", "md"} {
		res, _ := e.Search(context.Background(), q, 10)
		fmt.Printf("- query `%s` → %d hits", q, len(res))
		if len(res) > 0 {
			fmt.Printf(" (top: `%s`)", res[0].File)
		}
		fmt.Println()
	}
	fmt.Println()
	fmt.Println("Now the same corpus with `file UNINDEXED` and with bm25 weights,")
	fmt.Println("measured directly on the FTS table:")
	fmt.Println()

	tmp, _ := os.MkdirTemp("", "cw")
	defer os.RemoveAll(tmp)
	db := mustOpen(filepath.Join(tmp, "cw.sqlite"))
	defer db.Close()
	mustExec(db, `CREATE VIRTUAL TABLE a USING fts5(file, content, tokenize='unicode61')`)
	mustExec(db, `CREATE VIRTUAL TABLE b USING fts5(file UNINDEXED, content, tokenize='unicode61')`)
	for _, tbl := range []string{"a", "b"} {
		for _, c := range memory.ChunkMarkdown("daily/2026-07-26.md", dailyNote) {
			mustExec(db, fmt.Sprintf("INSERT INTO %s(file, content) VALUES (?,?)", tbl), c.File, c.Content)
		}
		for _, c := range memory.ChunkMarkdown("AGENT.md", agentMD) {
			mustExec(db, fmt.Sprintf("INSERT INTO %s(file, content) VALUES (?,?)", tbl), c.File, c.Content)
		}
	}
	fmt.Println("| query | `file` indexed (today) | `file UNINDEXED` |")
	fmt.Println("|---|---|---|")
	for _, q := range []string{`"daily"`, `"agent"`, `"2026"`, `"md"`, `"keeper"`} {
		na, _ := matchCount(db, "a", q)
		nb, _ := matchCount(db, "b", q)
		fmt.Printf("| `%s` | %d | %d |\n", q, na, nb)
	}
	fmt.Println()

	// Show the effect of bm25 column weights on ordering.
	fmt.Println("bm25 column weighting, query `keeper` (weights file:content):")
	fmt.Println()
	for _, w := range []string{"1.0, 1.0", "0.0, 1.0", "10.0, 1.0"} {
		rows, err := db.Query(fmt.Sprintf(
			`SELECT file, bm25(a, %s) AS s FROM a WHERE a MATCH '"keeper"' ORDER BY s LIMIT 3`, w))
		if err != nil {
			fmt.Printf("- weights (%s): %v\n", w, err)
			continue
		}
		var parts []string
		for rows.Next() {
			var f string
			var s float64
			rows.Scan(&f, &s)
			parts = append(parts, fmt.Sprintf("%s=%.3f", f, s))
		}
		rows.Close()
		fmt.Printf("- weights (%s): %s\n", w, strings.Join(parts, ", "))
	}
	fmt.Println()
}

// ------------------------------------------------------- 9. chunking shape

func sectionChunking() {
	fmt.Println("## 9. Chunking — what ChunkMarkdown actually emits")
	fmt.Println()
	fmt.Println("Boundaries are `## ` headings only (chunk.go:34), then a")
	fmt.Println("paragraph-boundary split at ~500 chars (chunk.go:7,79).")
	fmt.Println()

	cases := []struct{ name, body string }{
		{"daily note (Czech, mixed headings)", dailyNote},
		{"AGENT.md", agentMD},
		{"h1+h3 only, no h2", "# Title\n\n### Sub A\n\n" + strings.Repeat("Alpha beta gamma. ", 40) + "\n\n### Sub B\n\n" + strings.Repeat("Delta epsilon. ", 40)},
		{"one 4 KB paragraph, no blank lines", "# T\n\n" + strings.Repeat("word ", 800)},
		{"bullet list, no blank lines", "# T\n\n" + strings.Repeat("- a bullet line about deployments\n", 60)},
	}
	fmt.Println("| input | chunks | min | max | mean | oversize (>500) |")
	fmt.Println("|---|---|---|---|---|---|")
	for _, c := range cases {
		chunks := memory.ChunkMarkdown("x.md", c.body)
		if len(chunks) == 0 {
			fmt.Printf("| %s | 0 | | | | |\n", c.name)
			continue
		}
		mn, mx, sum, over := 1<<30, 0, 0, 0
		for _, ch := range chunks {
			l := len(ch.Content)
			if l < mn {
				mn = l
			}
			if l > mx {
				mx = l
			}
			sum += l
			if l > 500 {
				over++
			}
		}
		fmt.Printf("| %s | %d | %d | %d | %d | %d |\n",
			c.name, len(chunks), mn, mx, sum/len(chunks), over)
	}
	fmt.Println()

	// Heading retention: does a non-first chunk of a section keep its heading?
	fmt.Println("Heading retention — a section long enough to be split:")
	long := "## Retence žurnálu\n\n" + strings.Repeat("Paragraph about retention policy and archival.\n\n", 20)
	chunks := memory.ChunkMarkdown("x.md", long)
	for i, ch := range chunks {
		first := strings.SplitN(ch.Content, "\n", 2)[0]
		if len(first) > 60 {
			first = first[:60]
		}
		fmt.Printf("- chunk %d (%d B) starts: %q\n", i, len(ch.Content), first)
	}
	fmt.Println()
	fmt.Println("Only chunk 0 carries the heading. Every later chunk of the same")
	fmt.Println("section is indexed without the words that say what it is about.")
	fmt.Println()

	// LineStart survives chunking but is dropped by Search().
	fmt.Println("Line numbers: ChunkMarkdown produces them, Search does not return them.")
	dir, e := newProdEngine()
	defer os.RemoveAll(dir)
	defer e.Close()
	res, _ := e.Search(context.Background(), "keeper OR retence OR deployment", 10)
	fmt.Println()
	fmt.Println("| hit | file | LineStart | LineEnd |")
	fmt.Println("|---|---|---|---|")
	for i, r := range res {
		fmt.Printf("| %d | `%s` | %d | %d |\n", i, r.File, r.LineStart, r.LineEnd)
	}
	fmt.Println()
	fmt.Println("`SearchResult.LineStart` is never assigned in search.go, so")
	fmt.Println("`ftsKey` (hybrid.go:186-190) yields `<file>:0` for EVERY chunk of")
	fmt.Println("a file — a collision that reassigns one rank to all of them.")
	fmt.Println()
	dedup := map[string]int{}
	for _, r := range res {
		dedup[fmt.Sprintf("%s:%d", r.File, r.LineStart)]++
	}
	for k, v := range dedup {
		if v > 1 {
			fmt.Printf("- key `%s` is shared by %d distinct chunks\n", k, v)
		}
	}
	fmt.Println()
}

// -------------------------------------------- 10. prefix index, timed right

func sectionPrefixTiming() {
	fmt.Println("## 10. Prefix index — timed, at a prefix length it actually covers")
	fmt.Println()
	const nDocs = 20000
	for _, c := range []struct{ name, extra string }{
		{"no prefix index", ""},
		{"prefix='2 3'", ", prefix='2 3'"},
	} {
		dir, _ := os.MkdirTemp("", "pt")
		p := filepath.Join(dir, "p.sqlite")
		db := mustOpen(p)
		mustExec(db, fmt.Sprintf(
			"CREATE VIRTUAL TABLE docs USING fts5(body, tokenize='unicode61'%s)", c.extra))
		tx, _ := db.Begin()
		st, _ := tx.Prepare("INSERT INTO docs(body) VALUES (?)")
		for i := 0; i < nDocs; i++ {
			st.Exec(corpusDoc(i))
		}
		st.Close()
		tx.Commit()
		mustExec(db, "INSERT INTO docs(docs) VALUES('optimize')")
		mustExec(db, "PRAGMA wal_checkpoint(TRUNCATE)")
		fi, _ := os.Stat(p)

		// 3-char prefix: inside the prefix index's coverage.
		for _, expr := range []string{`"dep"*`, `"rozh"*`, `"keep"*`} {
			start := time.Now()
			var runs int
			for time.Since(start) < 300*time.Millisecond {
				matchCount(db, "docs", expr)
				runs++
			}
			per := time.Since(start) / time.Duration(runs)
			fmt.Printf("- %-16s %-9s %8s/query  (index %s)\n", c.name, expr, per.Round(time.Microsecond), human(fi.Size()))
		}
		db.Close()
		os.RemoveAll(dir)
	}
	fmt.Println()
	fmt.Println("Corpus here is 20 000 chunks — far larger than a real agent's")
	fmt.Println("`.memory/`, which is capped at 10 MB total (engine.go:31).")
	fmt.Println()
}

// ------------------------------------------------------ 11. write-path cost

func sectionWriteCost() {
	fmt.Println("## 11. Cost of index maintenance on every write")
	fmt.Println()
	fmt.Println("`memory.write` calls `ReindexPath` (index.go:166), which re-chunks")
	fmt.Println("one file. Measured against corpus size to confirm it is O(file).")
	fmt.Println()
	fmt.Println("| corpus files | ReindexPath p50 | full Reindex |")
	fmt.Println("|---|---|---|")
	for _, n := range []int{10, 100, 1000, 5000} {
		dir, _ := os.MkdirTemp("", "wc")
		for i := 0; i < n; i++ {
			os.WriteFile(filepath.Join(dir, fmt.Sprintf("n%06d.md", i)),
				[]byte(corpusDoc(i)), 0o644)
		}
		e, err := memory.New(dir, memory.DefaultConfig())
		if err != nil {
			panic(err)
		}
		fullStart := time.Now()
		e.Reindex()
		full := time.Since(fullStart)

		// Rewrite one file repeatedly with changing content so the hash
		// short-circuit never fires.
		var samples []time.Duration
		for i := 0; i < 40; i++ {
			body := corpusDoc(0) + fmt.Sprintf("\n\nrevision %d\n", i)
			os.WriteFile(filepath.Join(dir, "n000000.md"), []byte(body), 0o644)
			s := time.Now()
			if _, err := e.ReindexPath(context.Background(), "n000000.md"); err != nil {
				panic(err)
			}
			samples = append(samples, time.Since(s))
		}
		med := median(samples)
		fmt.Printf("| %d | %s | %s |\n", n, med.Round(time.Microsecond), full.Round(time.Millisecond))
		e.Close()
		os.RemoveAll(dir)
	}
	fmt.Println()

	// Pragma comparison on the index DB.
	fmt.Println("Effect of the index DB's pragmas on that per-write cost:")
	fmt.Println()
	pragmaSets := []struct{ name, dsn string }{
		{"TODAY (wal, sync NORMAL)", "?_pragma=journal_mode(wal)&_pragma=busy_timeout(5000)&_pragma=synchronous(NORMAL)&_pragma=temp_store(MEMORY)"},
		{"delete journal, sync FULL", "?_pragma=journal_mode(delete)&_pragma=synchronous(FULL)"},
		{"wal, sync OFF", "?_pragma=journal_mode(wal)&_pragma=synchronous(OFF)"},
		{"wal, sync NORMAL, mmap 256MB", "?_pragma=journal_mode(wal)&_pragma=synchronous(NORMAL)&_pragma=mmap_size(268435456)"},
	}
	for _, ps := range pragmaSets {
		dir, _ := os.MkdirTemp("", "pg")
		db, _ := sql.Open("sqlite", filepath.Join(dir, "x.sqlite")+ps.dsn)
		mustExec(db, `CREATE VIRTUAL TABLE memory_chunks USING fts5(file, content, tokenize='unicode61')`)
		var samples []time.Duration
		for i := 0; i < 200; i++ {
			s := time.Now()
			tx, _ := db.Begin()
			tx.Exec("DELETE FROM memory_chunks WHERE file = ?", "n.md")
			for j := 0; j < 3; j++ {
				tx.Exec("INSERT INTO memory_chunks(file, content) VALUES (?,?)", "n.md", corpusDoc(i*3+j))
			}
			tx.Commit()
			samples = append(samples, time.Since(s))
		}
		fmt.Printf("- %-32s %s per write-tx\n", ps.name, median(samples).Round(time.Microsecond))
		db.Close()
		os.RemoveAll(dir)
	}
	fmt.Println()
}

func median(d []time.Duration) time.Duration {
	c := append([]time.Duration(nil), d...)
	for i := 1; i < len(c); i++ {
		for j := i; j > 0 && c[j] < c[j-1]; j-- {
			c[j], c[j-1] = c[j-1], c[j]
		}
	}
	return c[len(c)/2]
}
