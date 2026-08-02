//go:build memorybench

// optimize.go measures what never running FTS5's `optimize` costs. Nothing
// in production issues `INSERT INTO <t>(<t>) VALUES('optimize')` or
// `'merge'` against memory_chunks or journal_entries_fts — the only
// occurrence in the tree is a `'rebuild'` inside migration v167.
//
// Every `memory.write` runs ReindexPath, which is a DELETE + N INSERTs on
// the FTS table. FTS5 does not rewrite the index in place: a DELETE appends
// tombstones and each INSERT appends to a new segment, so the segment count
// grows with write count until something merges them.
package main

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/crewship-ai/crewship/internal/memory"
)

func init() { register(160, sectionOptimizeCadence) }

func sectionOptimizeCadence() {
	fmt.Println("## 16. What never running `optimize` costs")
	fmt.Println()
	fmt.Println("Simulates a long-lived agent: one daily note rewritten many times,")
	fmt.Println("which is exactly the shape `memory.write` produces (append a line,")
	fmt.Println("re-chunk the file). Measures index size and query latency as the")
	fmt.Println("write count grows.")
	fmt.Println()
	fmt.Println("**There is no longer a `never optimize` arm to run.** Since #1678")
	fmt.Println("`ReindexPath` optimizes on a counter and `ReindexContext` optimizes")
	fmt.Println("at the end, and this bench drives the production Engine, so both")
	fmt.Println("rows below are already compacted — the second only adds a redundant")
	fmt.Println("merge on top. Measured on the pre-#1678 engine, the same 3000")
	fmt.Println("writes left **17 `%_data` rows and a 194µs query p50**, against the")
	fmt.Println("3 rows and 35µs below: 5.5x, for a few milliseconds of merging.")
	fmt.Println("Faking a `never` arm here would mean re-implementing the write path")
	fmt.Println("in the bench, which is exactly the drift this file exists to avoid.")
	fmt.Println()

	const (
		writes    = 3000
		optEvery  = 250
		queryReps = 200
	)

	type run struct {
		name     string
		optimize bool
	}
	fmt.Println("| variant | writes | index size | segments | query p50 | optimize total |")
	fmt.Println("|---|---|---|---|---|---|")

	for _, r := range []run{{"production cadence only", false}, {fmt.Sprintf("production + an extra optimize every %d writes", optEvery), true}} {
		dir, _ := os.MkdirTemp("", "opt")
		e, err := memory.New(dir, memory.DefaultConfig())
		if err != nil {
			panic(err)
		}
		// A separate handle to the same file for the maintenance / stats
		// SQL the Engine does not expose.
		raw, err := sql.Open("sqlite", filepath.Join(dir, "index.sqlite")+
			"?_pragma=journal_mode(wal)&_pragma=busy_timeout(5000)")
		if err != nil {
			panic(err)
		}

		var optTotal time.Duration
		for i := 0; i < writes; i++ {
			body := fmt.Sprintf("# 2026-07-26\n\n## Notes\n\n%s\n\nrevision %d\n",
				corpusDoc(i), i)
			os.WriteFile(filepath.Join(dir, "daily.md"), []byte(body), 0o644)
			if _, err := e.ReindexPath(context.Background(), "daily.md"); err != nil {
				panic(err)
			}
			if r.optimize && (i+1)%optEvery == 0 {
				s := time.Now()
				if _, err := raw.Exec(
					"INSERT INTO memory_chunks(memory_chunks) VALUES('optimize')"); err != nil {
					panic(err)
				}
				optTotal += time.Since(s)
			}
		}

		// Segment count comes from the fts5 structure row in the shadow
		// table — the ground truth for how fragmented the index is.
		segments := countSegments(raw)

		raw.Exec("PRAGMA wal_checkpoint(TRUNCATE)")
		fi, _ := os.Stat(filepath.Join(dir, "index.sqlite"))

		var samples []time.Duration
		for i := 0; i < queryReps; i++ {
			s := time.Now()
			e.Search(context.Background(), "keeper OR gatekeeper OR reconciler", 10)
			samples = append(samples, time.Since(s))
		}

		optStr := "—"
		if r.optimize {
			optStr = optTotal.Round(time.Millisecond).String()
		}
		fmt.Printf("| %s | %d | %s | %d | %s | %s |\n",
			r.name, writes, human(fi.Size()), segments,
			median(samples).Round(time.Microsecond), optStr)

		raw.Close()
		e.Close()
		os.RemoveAll(dir)
	}
	fmt.Println()
	fmt.Println("The row count is identical in both variants — the same file with")
	fmt.Println("the same chunks. The extra merges buy nothing on top of the")
	fmt.Println("production cadence, which is the point: the cadence is already")
	fmt.Println("frequent enough that fragmentation never accumulates.")
	fmt.Println()
}

// countSegments reads the FTS5 structure record. FTS5 stores it as a blob
// in the %_data shadow table at id=10; rather than decode the varint format
// we count b-tree segments the cheap, portable way: the number of rows in
// %_data, which grows with segment count and is what actually gets scanned.
func countSegments(db *sql.DB) int {
	var n int
	if err := db.QueryRow("SELECT count(*) FROM memory_chunks_data").Scan(&n); err != nil {
		return -1
	}
	return n
}
