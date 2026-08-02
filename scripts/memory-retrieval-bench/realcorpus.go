//go:build memorybench

// realcorpus.go runs the same questions against a REAL corpus: this
// repository's own `docs/` tree, chunked by the production chunker. It is
// the closest thing available to a real agent memory directory — technical
// prose, in this product's own vocabulary, written by humans — without
// needing access to a live instance.
package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/crewship-ai/crewship/internal/memory"
)

func init() { register(150, sectionRealCorpus) }

// docsQueries are questions whose answers genuinely live in docs/. The
// answer key is a substring of the expected file path, checked by hand.
var docsQueries = []struct {
	q    string
	want string
}{
	{"how do I rotate a credential", "credential"},
	{"what is a routine", "routine"},
	{"crew memory tiers", "memory"},
	{"how does the keeper decide", "keeper"},
	{"backup restore", "backup"},
	{"agent container resource limits", "container"},
	{"notification channels", "notif"},
	{"what does crewship seed do", "seed"},
	{"delegation and hiring", "hire"},
	{"webhook signature verification", "webhook"},
}

func sectionRealCorpus() {
	fmt.Println("## 15. The same questions against a REAL corpus")
	fmt.Println()
	fmt.Println("Corpus: this repository's `docs/` tree — every `.md` and `.mdx` —")
	fmt.Println("chunked by the production `ChunkMarkdown` and indexed by the")
	fmt.Println("production `memory.Engine`. Real technical prose in the product's")
	fmt.Println("own vocabulary, which the synthetic corpora above are not.")
	fmt.Println()

	src := "docs"
	if _, err := os.Stat(src); err != nil {
		fmt.Println("_docs/ not found; skipping._")
		fmt.Println()
		return
	}

	dir, _ := os.MkdirTemp("", "realcorp")
	defer os.RemoveAll(dir)

	var files, bytesIn int
	err := filepath.Walk(src, func(p string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return nil
		}
		ext := strings.ToLower(filepath.Ext(p))
		if ext != ".md" && ext != ".mdx" {
			return nil
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return nil
		}
		// Flatten into the temp memory dir; the engine only walks .md.
		flat := strings.ReplaceAll(strings.TrimPrefix(p, src+"/"), "/", "__")
		flat = strings.TrimSuffix(flat, filepath.Ext(flat)) + ".md"
		if err := os.WriteFile(filepath.Join(dir, flat), b, 0o644); err != nil {
			return nil
		}
		files++
		bytesIn += len(b)
		return nil
	})
	if err != nil || files == 0 {
		fmt.Printf("_could not build corpus: %v (files=%d)_\n\n", err, files)
		return
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
	idx, _ := os.Stat(filepath.Join(dir, "index.sqlite"))
	fmt.Printf("Corpus: %d files, %s of markdown → %d chunks, index %s (%.2fx text).\n\n",
		files, human(int64(bytesIn)), st.TotalChunks, human(idx.Size()),
		float64(idx.Size())/float64(bytesIn))

	// Chunk size distribution on real markdown.
	var sizes []int
	for _, f := range mustList(dir) {
		b, err := os.ReadFile(filepath.Join(dir, f))
		if err != nil {
			continue
		}
		for _, c := range memory.ChunkMarkdown(f, string(b)) {
			sizes = append(sizes, len(c.Content))
		}
	}
	sort.Ints(sizes)
	if len(sizes) > 0 {
		fmt.Println("Chunk size distribution on real markdown (target is 500):")
		fmt.Println()
		fmt.Printf("| p10 | p50 | p90 | p99 | max | >500 | >2000 |\n|---|---|---|---|---|---|---|\n")
		over500, over2000 := 0, 0
		for _, s := range sizes {
			if s > 500 {
				over500++
			}
			if s > 2000 {
				over2000++
			}
		}
		fmt.Printf("| %d | %d | %d | %d | %d | %d (%.0f%%) | %d (%.0f%%) |\n\n",
			sizes[len(sizes)/10], sizes[len(sizes)/2], sizes[len(sizes)*9/10],
			sizes[len(sizes)*99/100], sizes[len(sizes)-1],
			over500, 100*float64(over500)/float64(len(sizes)),
			over2000, 100*float64(over2000)/float64(len(sizes)))
		fmt.Println("The 500-char target is a floor, not a cap: `splitLargeChunk`")
		fmt.Println("only breaks at blank lines, so any run of prose or bullets")
		fmt.Println("without one stays whole however long it gets (chunk.go:79).")
		fmt.Println()
	}

	// The first column is whatever the shipping sanitiser does; the other
	// two are the local comparison arms the #1678 fix was chosen against.
	// Run this either side of a change to sanitizeFTSQuery and column one
	// is the before/after.
	fmt.Println("| question | production builder | bare OR | OR + stopwords |")
	fmt.Println("|---|---|---|---|")
	var a3, o3, s3 int
	for _, q := range docsQueries {
		andHits, _ := e.Search(context.Background(), q.q, 10)
		orHits, _ := e.Search(context.Background(), strings.Join(quoteAll(tokenizeWords(q.q)), " OR "), 10)
		stHits, _ := e.Search(context.Background(), strings.Join(quoteAll(stripStop(tokenizeWords(q.q))), " OR "), 10)
		ra := rankOfSubstr(andHits, q.want)
		ro := rankOfSubstr(orHits, q.want)
		rs := rankOfSubstr(stHits, q.want)
		if ra > 0 && ra <= 3 {
			a3++
		}
		if ro > 0 && ro <= 3 {
			o3++
		}
		if rs > 0 && rs <= 3 {
			s3++
		}
		fmt.Printf("| `%s` | %s (%d hits) | %s (%d) | %s (%d) |\n",
			q.q, rankStr(ra), len(andHits), rankStr(ro), len(orHits), rankStr(rs), len(stHits))
	}
	fmt.Printf("\n**Right file in top-3: production %d/%d · bare OR %d/%d · OR+stopwords %d/%d.**\n\n",
		a3, len(docsQueries), o3, len(docsQueries), s3, len(docsQueries))

	// Vocabulary and prefix expansion — the honest version of §12b.
	fmt.Println("Vocabulary, and how far a short prefix reaches in it:")
	fmt.Println()
	tmp, _ := os.MkdirTemp("", "vocab")
	defer os.RemoveAll(tmp)
	db := mustOpen(filepath.Join(tmp, "v.sqlite"))
	defer db.Close()
	mustExec(db, `CREATE VIRTUAL TABLE d USING fts5(body, tokenize='unicode61')`)
	tx, _ := db.Begin()
	ins, _ := tx.Prepare("INSERT INTO d(body) VALUES (?)")
	nChunks := 0
	for _, f := range mustList(dir) {
		b, err := os.ReadFile(filepath.Join(dir, f))
		if err != nil {
			continue
		}
		for _, c := range memory.ChunkMarkdown(f, string(b)) {
			ins.Exec(c.Content)
			nChunks++
		}
	}
	ins.Close()
	tx.Commit()
	mustExec(db, `CREATE VIRTUAL TABLE dv USING fts5vocab(d, row)`)
	var vocab int
	db.QueryRow("SELECT count(*) FROM dv").Scan(&vocab)
	fmt.Printf("Distinct index terms across %d chunks: **%d**.\n\n", nChunks, vocab)
	fmt.Println("| prefix | distinct terms it expands to | % of vocabulary | chunks matched | % of corpus |")
	fmt.Println("|---|---|---|---|---|")
	for _, p := range []string{"se", "dr", "lu", "po", "ur", "kter", "depl", "keep"} {
		var terms int
		db.QueryRow("SELECT count(*) FROM dv WHERE term >= ? AND term < ?",
			p, p+"￿").Scan(&terms)
		c, _ := matchCount(db, "d", `"`+p+`"*`)
		fmt.Printf("| `%s*` | %d | %.1f%% | %d | %.1f%% |\n", p, terms,
			100*float64(terms)/float64(vocab), c, 100*float64(c)/float64(nChunks))
	}
	fmt.Println()
	fmt.Println("These are the fragments `escapeFTSQuery` USED to produce from")
	fmt.Println("Czech input (§12), and the reason #1678 raised its prefix floor")
	fmt.Println("to three runes: a two-character prefix is not a search term, it")
	fmt.Println("is a request for a double-digit percentage of the corpus, OR-ed")
	fmt.Println("into a query alongside the terms that actually mattered. The")
	fmt.Println("three-rune fragments are kept in the table to show the floor is")
	fmt.Println("a floor and not a fix — `keep*` still reaches 7.6%.")
	fmt.Println()
}

func mustList(dir string) []string {
	ents, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var out []string
	for _, e := range ents {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".md") {
			out = append(out, e.Name())
		}
	}
	return out
}

func rankOfSubstr(rs []memory.SearchResult, want string) int {
	for i, r := range rs {
		if strings.Contains(strings.ToLower(r.File), strings.ToLower(want)) {
			return i + 1
		}
	}
	return 0
}
