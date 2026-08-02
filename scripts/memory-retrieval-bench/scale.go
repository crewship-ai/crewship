//go:build memorybench

// scale.go answers the two questions the small-corpus measurements leave
// open: does the episodic lane's query builder survive Czech, and does an
// OR rewrite still rank the right chunk first once the corpus is realistic
// (OR without ranking would just return everything).
package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/crewship-ai/crewship/internal/episodic"
	"github.com/crewship-ai/crewship/internal/memory"
)

func init() {
	register(120, sectionEpisodicQueryBuilder)
	register(130, sectionORAtScale)
}

// ------------------------------ 12. the episodic lane's query builder

func sectionEpisodicQueryBuilder() {
	fmt.Println("## 12. What the episodic lane sends to FTS5")
	fmt.Println()
	fmt.Println("`escapeFTSQuery` (episodic/hybrid.go:158) accepts only `a-z0-9`.")
	fmt.Println("Every other rune — including every Czech diacritic — is a token")
	fmt.Println("SEPARATOR, and runs shorter than 2 chars are dropped entirely.")
	fmt.Println()
	fmt.Println("| query | expression built |")
	fmt.Println("|---|---|")
	for _, q := range []string{
		"aux slots",
		"keeper aux sloty",
		"rozhodnutí o retenci žurnálu",
		"jak dlouho se drží žurnál",
		"Které sloty se projevily až po restartu?",
		"deploy 502",
		"co-author trailer",
	} {
		fmt.Printf("| `%s` | `%s` |\n", q, episodic.EscapeFTSQueryForBench(q))
	}
	fmt.Println()
	fmt.Println("Note the fragments a Czech query decomposes into: a diacritic in")
	fmt.Println("the middle of a word truncates the term at that point and emits")
	fmt.Println("the head as a PREFIX, so short heads become very wide matchers.")
	fmt.Println()

	// Show how wide, against a realistic corpus.
	dir, _ := os.MkdirTemp("", "epiq")
	defer os.RemoveAll(dir)
	db := mustOpen(filepath.Join(dir, "e.sqlite"))
	defer db.Close()
	mustExec(db, `CREATE VIRTUAL TABLE j USING fts5(summary, payload, tokenize='porter ascii')`)
	const n = 2000
	for i := 0; i < n; i++ {
		mustExec(db, "INSERT INTO j(summary,payload) VALUES (?,?)",
			fmt.Sprintf("run %d finished", i), corpusDoc(i))
	}
	fmt.Printf("Against %d journal rows (`porter ascii`, as shipped):\n\n", n)
	fmt.Println("| query | expression | rows matched | % of corpus |")
	fmt.Println("|---|---|---|---|")
	for _, q := range []string{
		"jak dlouho se drží žurnál",
		"rozhodnutí o retenci žurnálu",
		"deploy 502",
		"aux slots",
	} {
		expr := episodic.EscapeFTSQueryForBench(q)
		c, e := matchCount(db, "j", expr)
		pct := "—"
		if c >= 0 {
			pct = fmt.Sprintf("%.0f%%", 100*float64(c)/float64(n))
		}
		fmt.Printf("| `%s` | `%s` | %s | %s |\n", q, expr, fmtCount(c, e), pct)
	}
	fmt.Println()
}

// ------------------------------------- 13. does OR still rank correctly

// distractor bodies: realistic memory chunks that share vocabulary with the
// queries without being the right answer. This is what makes an OR rewrite
// risky, so it is what the measurement has to include.
func distractor(i int) string {
	topics := []string{
		"The deploy step retried twice and then gave up; see run log.",
		"Slot allocation for the dev environment changed this week.",
		"Journal rows older than the cutoff are archived, not deleted.",
		"Trust zones are evaluated before any credential is mounted.",
		"Rozhodli jsme se odložit rebuild kontejneru na příští týden.",
		"Retence backupů je jiná než retence žurnálu, nepleť si to.",
		"The backend returned 500 during the migration window.",
		"Aux model bindings need an explicit key reference to resolve.",
	}
	return fmt.Sprintf("## Note %d\n\n%s\n\nFollow-up %d recorded.", i, topics[i%len(topics)], i)
}

func sectionORAtScale() {
	fmt.Println("## 13. Does the OR rewrite still rank the right chunk first?")
	fmt.Println()
	fmt.Println("The 8/10 → OR result in §7 was on a 6-chunk corpus, where OR")
	fmt.Println("cannot hurt. Here the same two real files sit inside 400")
	fmt.Println("vocabulary-overlapping distractor notes and we score rank-of-")
	fmt.Println("correct-file, not just presence.")
	fmt.Println()

	dir, _ := os.MkdirTemp("", "orscale")
	defer os.RemoveAll(dir)
	os.MkdirAll(filepath.Join(dir, "daily"), 0o755)
	os.WriteFile(filepath.Join(dir, "daily", "2026-07-26.md"), []byte(dailyNote), 0o644)
	os.WriteFile(filepath.Join(dir, "AGENT.md"), []byte(agentMD), 0o644)
	for i := 0; i < 400; i++ {
		os.WriteFile(filepath.Join(dir, fmt.Sprintf("note-%04d.md", i)),
			[]byte(distractor(i)), 0o644)
	}
	e, err := memory.New(dir, memory.DefaultConfig())
	if err != nil {
		panic(err)
	}
	defer e.Close()
	if err := e.Reindex(); err != nil {
		panic(err)
	}
	st, _ := e.Status(context.Background())
	fmt.Printf("Corpus: %d files, %d chunks.\n\n", st.TotalFiles, st.TotalChunks)

	fmt.Println("| query | AND rank | OR rank | OR hits |")
	fmt.Println("|---|---|---|---|")
	var andTop3, orTop3 int
	for _, q := range realQueries {
		a, _ := e.Search(context.Background(), q.q, 10)
		orExpr := strings.Join(quoteEach(strings.Fields(q.q)), " OR ")
		o, _ := e.Search(context.Background(), orExpr, 10)
		ra := rankOf(a, q.want)
		ro := rankOf(o, q.want)
		if ra > 0 && ra <= 3 {
			andTop3++
		}
		if ro > 0 && ro <= 3 {
			orTop3++
		}
		fmt.Printf("| `%s` | %s | %s | %d |\n", q.q, rankStr(ra), rankStr(ro), len(o))
	}
	fmt.Printf("\n**Correct file in top-3: AND %d/%d, OR %d/%d.**\n\n",
		andTop3, len(realQueries), orTop3, len(realQueries))

	fmt.Println("The same, with the `file` column excluded from matching — the")
	fmt.Println("interaction §8 predicts, measured: under OR, a path word like")
	fmt.Println("`daily` or `md` otherwise matches every chunk of a whole tier.")
	fmt.Println()
	fmt.Println("| query | OR rank (file indexed) | OR rank (content only) |")
	fmt.Println("|---|---|---|")
	var orContentTop3 int
	for _, q := range realQueries {
		orExpr := strings.Join(quoteEach(strings.Fields(q.q)), " OR ")
		o, _ := e.Search(context.Background(), orExpr, 10)
		// `content: <expr>` restricts matching to the content column without
		// changing the tokenizer or the index — FTS5 column-filter syntax.
		oc, _ := e.Search(context.Background(), "content: "+orExpr, 10)
		ro, roc := rankOf(o, q.want), rankOf(oc, q.want)
		if roc > 0 && roc <= 3 {
			orContentTop3++
		}
		fmt.Printf("| `%s` | %s | %s |\n", q.q, rankStr(ro), rankStr(roc))
	}
	fmt.Printf("\n**Correct file in top-3, content-only OR: %d/%d.**\n\n",
		orContentTop3, len(realQueries))
	fmt.Println("(Note: `content:` is reachable here only because the bench calls")
	fmt.Println("Engine.Search directly. sanitizeFTSQuery strips `:` from any query")
	fmt.Println("a model or user supplies, so this shape is not reachable in prod.)")
	fmt.Println()
}

func rankOf(rs []memory.SearchResult, file string) int {
	for i, r := range rs {
		if r.File == file {
			return i + 1
		}
	}
	return 0
}

func rankStr(r int) string {
	if r == 0 {
		return "—"
	}
	return fmt.Sprintf("#%d", r)
}
