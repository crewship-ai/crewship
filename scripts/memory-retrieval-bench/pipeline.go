//go:build memorybench

// pipeline.go compares candidate (tokenizer x query-builder) configurations
// on one fixed task, so the recommendation rests on a measured comparison
// rather than on which option sounds better.
//
// Honest limits, stated because they bound every number below:
//   - 10 queries is not an evaluation set. It is a smoke test with a known
//     answer key. Differences smaller than ~2 queries are noise.
//   - The corpus is synthetic. The two "real" files are real in shape;
//     the 400 distractors are generated from 8 templates.
//   - Only recall/rank is scored. Nothing here measures whether the model
//     then USES the retrieved chunk correctly.
package main

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/crewship-ai/crewship/internal/episodic"
	"github.com/crewship-ai/crewship/internal/memory"
)

func init() {
	register(125, sectionTermSelectivity)
	register(140, sectionPipelines)
}

// -------------------------- 12b. per-term selectivity of the episodic lane

func sectionTermSelectivity() {
	fmt.Println("## 12b. Per-term selectivity of the episodic fragments")
	fmt.Println()
	fmt.Println("§12's whole-query percentages are inflated by a uniform synthetic")
	fmt.Println("corpus. This measures each FRAGMENT on its own against a")
	fmt.Println("heterogeneous corpus, which is the property that actually matters:")
	fmt.Println("an OR-of-prefixes is only as precise as its least selective term.")
	fmt.Println()

	dir, _ := os.MkdirTemp("", "sel")
	defer os.RemoveAll(dir)
	db := mustOpen(filepath.Join(dir, "s.sqlite"))
	defer db.Close()
	mustExec(db, `CREATE VIRTUAL TABLE j USING fts5(summary, payload, tokenize='porter ascii')`)
	const n = 2000
	for i := 0; i < n; i++ {
		mustExec(db, "INSERT INTO j(summary,payload) VALUES (?,?)",
			fmt.Sprintf("run %d finished", i), distractor(i))
	}

	fmt.Printf("Corpus: %d heterogeneous journal rows (8 templates).\n\n", n)
	fmt.Println("| source query | fragment | rows matched | % |")
	fmt.Println("|---|---|---|---|")
	for _, q := range []string{
		"jak dlouho se drží žurnál",
		"rozhodnutí o retenci žurnálu",
		"Které sloty se projevily až po restartu?",
	} {
		expr := episodic.EscapeFTSQueryForBench(q)
		first := true
		for _, frag := range strings.Split(expr, " OR ") {
			c, e := matchCount(db, "j", frag)
			label := ""
			if first {
				label = "`" + q + "`"
				first = false
			}
			pct := "—"
			if c >= 0 {
				pct = fmt.Sprintf("%.0f%%", 100*float64(c)/float64(n))
			}
			fmt.Printf("| %s | `%s` | %s | %s |\n", label, frag, fmtCount(c, e), pct)
		}
	}
	fmt.Println()
	fmt.Println("Fragments of two or three characters are the problem: they are")
	fmt.Println("produced only because a Czech diacritic cut the word short, and")
	fmt.Println("as prefixes they select a large fraction of any corpus.")
	fmt.Println()
}

// ---------------------------------------- 14. candidate pipeline comparison

// stopwords are the highest-frequency function words in the two languages
// this product is used in. Deliberately short: a long list starts deleting
// content words, and the failure mode of a too-short list (a little noise)
// is much cheaper than the failure mode of a too-long one (a lost answer).
var stopwords = map[string]bool{
	// English
	"a": true, "about": true, "after": true, "an": true, "and": true,
	"any": true, "are": true, "as": true, "at": true, "be": true, "but": true,
	"by": true, "did": true, "do": true, "does": true, "for": true, "from": true,
	"how": true, "i": true, "if": true, "in": true, "is": true, "it": true,
	"of": true, "on": true, "or": true, "should": true, "that": true,
	"the": true, "then": true, "there": true, "this": true, "to": true,
	"was": true, "we": true, "were": true, "what": true, "when": true,
	"where": true, "which": true, "who": true, "why": true, "with": true,
	// Czech ("do" and "to" already listed above — the two languages share
	// several function-word spellings, which is itself a reason to keep the
	// list one flat set rather than two per-language sets.)
	"aby": true, "ale": true, "ani": true, "až": true,
	"co": true, "další": true, "je": true, "jak": true,
	"jako": true, "jsem": true, "jsme": true, "jsou": true, "již": true,
	"k": true, "kde": true, "která": true, "které": true, "který": true,
	"na": true, "nebo": true, "než": true, "o": true, "po": true, "pro": true,
	"při": true, "s": true, "se": true, "si": true, "tak": true, "také": true,
	"u": true, "už": true, "v": true, "ve": true, "z": true,
	"za": true, "že": true,
}

type queryBuilder struct {
	name string
	fn   func(string) string
}

func tokenizeWords(q string) []string {
	f := strings.FieldsFunc(strings.ToLower(q), func(r rune) bool {
		return r == ' ' || r == '\t' || r == '\n' || r == '?' || r == '!' ||
			r == ',' || r == '.' || r == ';' || r == '"' || r == '\''
	})
	return f
}

func stripStop(ws []string) []string {
	out := make([]string, 0, len(ws))
	for _, w := range ws {
		if stopwords[w] || len([]rune(w)) < 2 {
			continue
		}
		out = append(out, w)
	}
	if len(out) == 0 { // never return an empty query
		return ws
	}
	return out
}

func quoteAll(ws []string) []string {
	out := make([]string, 0, len(ws))
	for _, w := range ws {
		out = append(out, `"`+strings.ReplaceAll(w, `"`, ``)+`"`)
	}
	return out
}

var builders = []queryBuilder{
	{"AND (today)", func(q string) string {
		return strings.Join(quoteAll(tokenizeWords(q)), " ")
	}},
	{"OR", func(q string) string {
		return strings.Join(quoteAll(tokenizeWords(q)), " OR ")
	}},
	{"OR + stopwords", func(q string) string {
		return strings.Join(quoteAll(stripStop(tokenizeWords(q))), " OR ")
	}},
	{"OR + stopwords + prefix", func(q string) string {
		ws := quoteAll(stripStop(tokenizeWords(q)))
		for i := range ws {
			ws[i] += "*"
		}
		return strings.Join(ws, " OR ")
	}},
	{"phrase OR (OR+stop)", func(q string) string {
		ws := stripStop(tokenizeWords(q))
		phrase := `"` + strings.Join(ws, " ") + `"`
		return phrase + " OR " + strings.Join(quoteAll(ws), " OR ")
	}},
}

var pipelineTokenizers = []tokCfg{
	{name: "unicode61 (today)", spec: "unicode61"},
	{name: "porter unicode61", spec: "porter unicode61"},
	{name: "trigram ci+rd", spec: "trigram case_sensitive 0 remove_diacritics 1"},
}

type chunkRow struct{ file, content string }

func sectionPipelines() {
	fmt.Println("## 14. Candidate pipelines, same task, same answer key")
	fmt.Println()
	fmt.Println("Corpus: the two real files from §7 plus 400 vocabulary-overlapping")
	fmt.Println("distractors, chunked by the production `ChunkMarkdown`. Score is")
	fmt.Println("`recall@10` (correct file anywhere in top 10) and `top-3`.")
	fmt.Println()
	fmt.Println("**10 queries is a smoke test, not an eval. A 1-query difference")
	fmt.Println("is noise. Read the shape, not the decimals.**")
	fmt.Println()

	// Build the chunk set once; every table gets identical rows.
	var rows []chunkRow
	add := func(name, body string) {
		for _, c := range memory.ChunkMarkdown(name, body) {
			rows = append(rows, chunkRow{c.File, c.Content})
		}
	}
	add("daily/2026-07-26.md", dailyNote)
	add("AGENT.md", agentMD)
	for i := 0; i < 400; i++ {
		add(fmt.Sprintf("note-%04d.md", i), distractor(i))
	}

	fmt.Printf("Corpus: %d chunks.\n\n", len(rows))

	type cell struct{ recall, top3 int }
	results := map[string]map[string]cell{}

	for _, tk := range pipelineTokenizers {
		dir, _ := os.MkdirTemp("", "pipe")
		db := mustOpen(filepath.Join(dir, "p.sqlite"))
		// `file UNINDEXED` is held constant across every configuration so
		// the comparison isolates tokenizer x query-builder. §8 already
		// measured what indexing `file` does on its own.
		if _, err := db.ExecContext(context.Background(), fmt.Sprintf(
			"CREATE VIRTUAL TABLE c USING fts5(file UNINDEXED, content, tokenize=%q)", tk.spec)); err != nil {
			fmt.Printf("- %s unavailable: %v\n", tk.name, err)
			db.Close()
			os.RemoveAll(dir)
			continue
		}
		tx, _ := db.Begin()
		st, _ := tx.Prepare("INSERT INTO c(file, content) VALUES (?,?)")
		for _, r := range rows {
			st.Exec(r.file, r.content)
		}
		st.Close()
		tx.Commit()
		mustExec(db, "INSERT INTO c(c) VALUES('optimize')")

		results[tk.name] = map[string]cell{}
		for _, b := range builders {
			var cl cell
			for _, q := range realQueries {
				expr := b.fn(q.q)
				got, err := topFiles(db, expr, 10)
				if err != nil {
					continue
				}
				for i, f := range got {
					if f == q.want {
						cl.recall++
						if i < 3 {
							cl.top3++
						}
						break
					}
				}
			}
			results[tk.name][b.name] = cl
		}
		db.Close()
		os.RemoveAll(dir)
	}

	fmt.Printf("| query builder |")
	for _, tk := range pipelineTokenizers {
		fmt.Printf(" %s |", tk.name)
	}
	fmt.Println()
	fmt.Printf("|---|")
	for range pipelineTokenizers {
		fmt.Printf("---|")
	}
	fmt.Println()
	for _, b := range builders {
		fmt.Printf("| %s |", b.name)
		for _, tk := range pipelineTokenizers {
			c := results[tk.name][b.name]
			fmt.Printf(" %d/%d recall, %d/%d top-3 |",
				c.recall, len(realQueries), c.top3, len(realQueries))
		}
		fmt.Println()
	}
	fmt.Println()
	fmt.Println("Per-query detail for the best-scoring configuration:")
	fmt.Println()
	printBestDetail(rows)
}

func printBestDetail(rows []chunkRow) {
	dir, _ := os.MkdirTemp("", "best")
	defer os.RemoveAll(dir)
	db := mustOpen(filepath.Join(dir, "b.sqlite"))
	defer db.Close()
	mustExec(db, `CREATE VIRTUAL TABLE c USING fts5(file UNINDEXED, content, tokenize='porter unicode61')`)
	tx, _ := db.Begin()
	st, _ := tx.Prepare("INSERT INTO c(file, content) VALUES (?,?)")
	for _, r := range rows {
		st.Exec(r.file, r.content)
	}
	st.Close()
	tx.Commit()

	b := builders[2] // OR + stopwords
	fmt.Println("| query | expression | rank of correct file |")
	fmt.Println("|---|---|---|")
	for _, q := range realQueries {
		expr := b.fn(q.q)
		got, _ := topFiles(db, expr, 10)
		r := 0
		for i, f := range got {
			if f == q.want {
				r = i + 1
				break
			}
		}
		fmt.Printf("| `%s` | `%s` | %s |\n", q.q, expr, rankStr(r))
	}
	fmt.Println()
}

// topFiles runs a MATCH and returns the ranked, de-duplicated file list.
// De-duplication matters: several chunks of one file can occupy the whole
// top-N, which would make a per-chunk rank flatter the score than it is.
func topFiles(db *sql.DB, expr string, limit int) ([]string, error) {
	rows, err := db.Query(
		`SELECT file FROM c WHERE c MATCH ? ORDER BY rank LIMIT ?`, expr, limit*4)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	seen := map[string]bool{}
	var out []string
	for rows.Next() {
		var f string
		if err := rows.Scan(&f); err != nil {
			return nil, err
		}
		if seen[f] {
			continue
		}
		seen[f] = true
		out = append(out, f)
		if len(out) == limit {
			break
		}
	}
	return out, rows.Err()
}
