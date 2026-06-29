package memory

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

// These tests cover finding P2 (HIGH) from the 2026-06 security audit
// (.claude/context/SECURITY-AUDIT-2026-06.md): the sidecar memory-write handler
// used to call engine.ReindexContext after EVERY write (internal/sidecar/memory_write.go),
// and ReindexContext (internal/memory/index.go) is a FULL-corpus rebuild — it
// DELETEs every chunk, re-walks the whole .memory tree, re-reads every .md file,
// and re-chunks all of them inside one transaction. So N agent writes against a
// corpus that grows by one file per write cost O(N x corpus) ~= O(N^2) total
// reindex work (write amplification).
//
// The fix is engine.ReindexPath: the per-write path now re-chunks ONLY the file
// that changed (O(changed file), not O(corpus)). ReindexContext stays available
// for the explicit /memory/reindex full rebuild. These tests now assert the
// SECURE invariant — per-write reindex work is constant regardless of corpus
// size — and would FAIL if the per-write path regressed to a full rebuild.

// corpusFileContent returns a uniform markdown body so every corpus file yields the
// SAME number of chunks regardless of its index — only then is "chunks re-processed"
// a clean proxy for reindex work. The index keeps the content distinct so files are
// not deduplicated, without changing the section/line structure the chunker sees.
func corpusFileContent(i int) string {
	return fmt.Sprintf(`# Memory File %06d

## Notes
Entry %06d body line one about deployments and pagination.
Entry %06d body line two about authentication and migrations.

## Decisions
Decided thing %06d for the record.
`, i, i, i, i)
}

// corpusFileName is the relative path of corpus file i, both on disk and as the
// `file` key the engine stores for its chunks.
func corpusFileName(i int) string {
	return fmt.Sprintf("note-%06d.md", i)
}

// writeCorpusFile drops one .md file into the memory base dir, mimicking a single
// agent /memory/write (which appends a file then triggers a reindex).
func writeCorpusFile(tb testing.TB, dir string, i int) {
	tb.Helper()
	if err := os.WriteFile(filepath.Join(dir, corpusFileName(i)), []byte(corpusFileContent(i)), 0o644); err != nil {
		tb.Fatal(err)
	}
}

func newCorpusEngine(tb testing.TB) (string, *Engine) {
	tb.Helper()
	dir := tb.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "daily"), 0o755); err != nil {
		tb.Fatal(err)
	}
	engine, err := New(dir, DefaultConfig())
	if err != nil {
		tb.Fatal(err)
	}
	tb.Cleanup(func() { engine.Close() })
	return dir, engine
}

// TestReindexWriteAmplification_SecureTarget performs N sequential writes — each
// adds one file and triggers the per-write reindex, exactly like the sidecar
// write path (internal/sidecar/memory_write.go -> engine.ReindexPath) — and
// proves the total reindex work grows LINEARLY with the number of writes, not
// quadratically. (Previously skipped; activated by the P2 incremental-reindex fix.)
//
// With the incremental fix, write i re-chunks only the single new file, so the
// total work over the run is ~= finalChunks (each file chunked exactly once;
// amplification ~= 1x). A regression to the old full-corpus rebuild would make
// write i re-process all i files, blowing the amplification up to ~(N+1)/2.
func TestReindexWriteAmplification_SecureTarget(t *testing.T) {
	ctx := context.Background()
	dir, engine := newCorpusEngine(t)

	const N = 60

	totalReindexWork := 0 // chunks actually re-processed summed across every reindex
	for i := 0; i < N; i++ {
		// One agent write: append a new memory file, then incrementally reindex
		// just that file (mirrors memory_write.go -> ReindexPath).
		writeCorpusFile(t, dir, i)
		processed, err := engine.ReindexPath(ctx, corpusFileName(i))
		if err != nil {
			t.Fatalf("reindex after write %d: %v", i, err)
		}
		if processed == 0 {
			t.Fatalf("write %d: incremental reindex processed 0 chunks (file should have indexed)", i)
		}
		totalReindexWork += processed
	}

	finalSt, err := engine.Status(ctx)
	if err != nil {
		t.Fatal(err)
	}
	finalChunks := finalSt.TotalChunks
	if finalChunks == 0 {
		t.Fatal("expected a non-empty corpus after writes")
	}
	if finalSt.TotalFiles != N {
		t.Fatalf("expected %d indexed files, got %d", N, finalSt.TotalFiles)
	}

	// Incremental: each file chunked roughly once over the whole run, so the
	// total work re-processed equals the final corpus size (amplification ~1x).
	amplification := float64(totalReindexWork) / float64(finalChunks)

	// The OLD full-corpus rebuild gave ~(N+1)/2 = 30.5x for N=60. Require the
	// amplification stay near 1x — a small constant slack covers chunker tweaks
	// but anything super-linear (a full-rebuild regression) trips this.
	const wantMax = 3.0
	if amplification > wantMax {
		t.Fatalf("P2 regression: per-write reindex re-processed the whole corpus "+
			"(amplification %.1fx > %.1fx for N=%d) — the write path must call the "+
			"incremental ReindexPath, not a full-corpus rebuild", amplification, wantMax, N)
	}
	t.Logf("P2 fixed: %d writes re-processed %d chunks total vs %d minimal "+
		"= %.1fx write amplification (incremental per-file reindex)",
		N, totalReindexWork, finalChunks, amplification)
}

// TestReindexCostIsIndependentOfCorpus proves the per-write cost itself is
// O(changed file), NOT O(corpus): a SINGLE incremental reindex re-processes the
// same number of chunks whether the surrounding corpus is small or large. We
// seed corpora of 10 and 1000 files (one full index each), then add ONE more
// file and measure how many chunks ReindexPath processes — it must be flat.
func TestReindexCostIsIndependentOfCorpus(t *testing.T) {
	ctx := context.Background()

	incrementalCostAt := func(n int) int {
		dir, engine := newCorpusEngine(t)
		for i := 0; i < n; i++ {
			writeCorpusFile(t, dir, i)
		}
		// One full reindex to establish the baseline index + hash map.
		if err := engine.Reindex(); err != nil {
			t.Fatalf("seed reindex %d files: %v", n, err)
		}
		// Now a single new write + incremental reindex: this is the cost the
		// agent pays per write, and it must not depend on n.
		writeCorpusFile(t, dir, n)
		processed, err := engine.ReindexPath(ctx, corpusFileName(n))
		if err != nil {
			t.Fatalf("incremental reindex over %d-file corpus: %v", n, err)
		}
		return processed
	}

	small := incrementalCostAt(10)
	large := incrementalCostAt(1000)
	if small == 0 {
		t.Fatal("expected the incremental reindex to process a non-empty file")
	}

	// 100x more files must NOT mean more per-write work. The old full rebuild
	// scaled ~100x here; incremental is flat. Allow a tiny constant slack.
	ratio := float64(large) / float64(small)
	const wantMax = 2.0
	if ratio > wantMax {
		t.Fatalf("P2 regression: single-write reindex cost scales with corpus size "+
			"(1000-file/10-file work ratio %.1fx > %.1fx) — per-write reindex is not "+
			"incremental", ratio, wantMax)
	}
	t.Logf("P2 fixed: one incremental reindex processes %d chunks at 1000 files vs %d at 10 files "+
		"= %.1fx — per-write cost is O(changed file), not O(corpus)", large, small, ratio)
}

// TestReindexPathPreservesSearchAndOtherFiles is the correctness regression
// guard: an incremental reindex must make the new file searchable WITHOUT
// disturbing previously-indexed files, and re-indexing unchanged content must be
// a no-op.
func TestReindexPathPreservesSearchAndOtherFiles(t *testing.T) {
	ctx := context.Background()
	dir, engine := newCorpusEngine(t)

	// Seed two files via the incremental path.
	writeCorpusFile(t, dir, 0)
	if _, err := engine.ReindexPath(ctx, corpusFileName(0)); err != nil {
		t.Fatal(err)
	}
	writeCorpusFile(t, dir, 1)
	if _, err := engine.ReindexPath(ctx, corpusFileName(1)); err != nil {
		t.Fatal(err)
	}

	// Both files are searchable (FTS correctness across incremental writes).
	for _, i := range []int{0, 1} {
		res, err := engine.Search(ctx, fmt.Sprintf("%06d", i), 10)
		if err != nil {
			t.Fatalf("search for file %d: %v", i, err)
		}
		if len(res) == 0 {
			t.Fatalf("file %d not searchable after incremental reindex", i)
		}
	}

	// Re-indexing unchanged content is a no-op (hash-skip).
	processed, err := engine.ReindexPath(ctx, corpusFileName(0))
	if err != nil {
		t.Fatal(err)
	}
	if processed != 0 {
		t.Fatalf("re-indexing unchanged file processed %d chunks, want 0 (hash-skip)", processed)
	}

	// Updating one file replaces only its chunks; the other file's chunks remain.
	updated := corpusFileContent(0) + "\n\n## Extra\nbrandnewtoken about kubernetes.\n"
	if err := os.WriteFile(filepath.Join(dir, corpusFileName(0)), []byte(updated), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := engine.ReindexPath(ctx, corpusFileName(0)); err != nil {
		t.Fatal(err)
	}
	res, err := engine.Search(ctx, "brandnewtoken", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(res) == 0 {
		t.Fatal("updated content not searchable after incremental reindex")
	}
	// The untouched file is still present.
	st, err := engine.Status(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if st.TotalFiles != 2 {
		t.Fatalf("expected 2 files still indexed after update, got %d", st.TotalFiles)
	}

	// A deleted file drops out of the index incrementally.
	if err := os.Remove(filepath.Join(dir, corpusFileName(1))); err != nil {
		t.Fatal(err)
	}
	if _, err := engine.ReindexPath(ctx, corpusFileName(1)); err != nil {
		t.Fatal(err)
	}
	st, err = engine.Status(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if st.TotalFiles != 1 {
		t.Fatalf("expected 1 file after deleting one, got %d", st.TotalFiles)
	}
}

// BenchmarkReindexCostVsCorpus reports the wall-clock cost of a single full
// reindex at corpora of 10/100/1000 files (the explicit /memory/reindex path).
// The ns/op climbs roughly linearly with the file count — which is exactly why
// the per-write path must NOT use it. Run:
//
//	go test ./internal/memory/ -run x -bench BenchmarkReindexCostVsCorpus -benchmem
func BenchmarkReindexCostVsCorpus(b *testing.B) {
	for _, n := range []int{10, 100, 1000} {
		b.Run(fmt.Sprintf("files=%d", n), func(b *testing.B) {
			dir := b.TempDir()
			if err := os.MkdirAll(filepath.Join(dir, "daily"), 0o755); err != nil {
				b.Fatal(err)
			}
			engine, err := New(dir, DefaultConfig())
			if err != nil {
				b.Fatal(err)
			}
			defer engine.Close()

			for i := 0; i < n; i++ {
				writeCorpusFile(b, dir, i)
			}

			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				if err := engine.Reindex(); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

// BenchmarkIncrementalReindexVsCorpus reports the wall-clock cost of a single
// INCREMENTAL reindex (one new file) at corpora of 10/100/1000 files. Unlike the
// full reindex above, ns/op should stay roughly flat as the corpus grows — the
// P2 fix in action. Run:
//
//	go test ./internal/memory/ -run x -bench BenchmarkIncrementalReindexVsCorpus -benchmem
func BenchmarkIncrementalReindexVsCorpus(b *testing.B) {
	ctx := context.Background()
	for _, n := range []int{10, 100, 1000} {
		b.Run(fmt.Sprintf("files=%d", n), func(b *testing.B) {
			dir := b.TempDir()
			if err := os.MkdirAll(filepath.Join(dir, "daily"), 0o755); err != nil {
				b.Fatal(err)
			}
			engine, err := New(dir, DefaultConfig())
			if err != nil {
				b.Fatal(err)
			}
			defer engine.Close()

			for i := 0; i < n; i++ {
				writeCorpusFile(b, dir, i)
			}
			if err := engine.Reindex(); err != nil {
				b.Fatal(err)
			}

			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				// Rewrite one file with fresh content each iteration so the
				// hash-skip never short-circuits the measured work.
				body := corpusFileContent(n) + fmt.Sprintf("\n\niter %d\n", i)
				if err := os.WriteFile(filepath.Join(dir, corpusFileName(n)), []byte(body), 0o644); err != nil {
					b.Fatal(err)
				}
				if _, err := engine.ReindexPath(ctx, corpusFileName(n)); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}
