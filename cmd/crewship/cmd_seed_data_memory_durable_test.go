package main

import (
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"testing"
)

// TestWriteFileIfAbsent_NeverObservableEmpty is the #2124 regression test
// for the seed's memory-file writer, in the shape #1999 used for
// progress.jsonl.
//
// writeFileIfAbsent lays down PERSONA.md, pins.md, learned.md and the
// daily journal — the very files this repo's durable-write guard exists
// for — under the storage base path that the running server reads through
// its memory subsystem. It used a plain os.WriteFile, whose O_CREATE and
// first write are two syscalls: between them the file EXISTS at zero
// bytes, and a reader in that window is told the agent has no persona and
// no pins. What makes this site worse than an ordinary one-shot write is
// its own guard: the os.Stat that keeps a re-seed from clobbering operator
// edits also means a torn first write is never repaired — the next seed
// sees the file, skips it, and the empty PERSONA.md stays.
//
// The invariant asserted: if the path exists it already holds the whole
// content. Verified to FAIL against the pre-fix os.WriteFile
// implementation.
func TestWriteFileIfAbsent_NeverObservableEmpty(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	const content = "# PERSONA.md — casey voice\n\nTerse, direct, cites line numbers.\n"

	// A tiny file means a window of microseconds; the pre-fix code lost the
	// race in as few as 1 of 300 rounds on a loaded box, so 600 rounds to
	// keep detection above 95% even at that rate. The whole loop runs in
	// tens of milliseconds either way.
	const iterations = 600
	emptyObservations := 0
	for i := range iterations {
		// A fresh path per round so every round exercises the
		// create-then-write window.
		path := filepath.Join(dir, "PERSONA-"+strconv.Itoa(i)+".md")

		var wg sync.WaitGroup
		stop := make(chan struct{})
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-stop:
					return
				default:
				}
				// Stat-then-read is what an out-of-process reader does. If
				// the file is visible at all it must carry the whole body.
				if _, err := os.Stat(path); err != nil {
					continue
				}
				data, err := os.ReadFile(path)
				if err != nil {
					continue
				}
				if string(data) != content {
					emptyObservations++
					return
				}
			}
		}()

		err := writeFileIfAbsent(path, content)
		close(stop)
		wg.Wait()
		if err != nil {
			t.Fatalf("writeFileIfAbsent: %v", err)
		}
	}

	if emptyObservations != 0 {
		t.Errorf("a seeded memory file was observed existing-but-incomplete in %d/%d writes — "+
			"the create-then-write window is back; writeFileIfAbsent must publish via "+
			"memory.WriteFileDurable's atomic rename",
			emptyObservations, iterations)
	}

	// No tempfile may be left behind: the memory subsystem lists these
	// directories, and a PERSONA.md.tmp.* would be an unexplained extra.
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if filepath.Ext(e.Name()) != ".md" {
			t.Errorf("leftover file %q beside the seeded memory files", e.Name())
		}
	}
}
