package main

import (
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"testing"
)

// TestPromptSave_NeverObservableTorn is the #2124 regression test for
// `prompt save`, in the shape #1999 used for progress.jsonl.
//
// The prompt library is not a `-o` path the operator named; it is CLI
// state under ~/.crewship/prompts that a LATER command reads back —
// `prompt use <name>` prints it straight into `ask`/`run`, and
// `prompt path` hands its location to `--prompt @<path>`. `prompt save`
// used to overwrite that file with os.WriteFile, which truncates before it
// writes, so an interrupted re-save of an existing prompt left a torn
// file that the next `prompt use` piped into a run without a word.
//
// The invariant asserted: the file at promptPath is only ever the whole
// previous prompt or the whole new one. Verified to FAIL against the
// pre-fix os.WriteFile implementation.
func TestPromptSave_NeverObservableTorn(t *testing.T) {
	dir := covPromptHome(t)
	saveCLIState(t)
	t.Cleanup(func() {
		_ = promptSaveCmd.Flags().Set("content", "")
		_ = promptSaveCmd.Flags().Set("file", "")
	})

	// The pre-fix implementation lost this race in only 1-3 of 60 rounds
	// on a loaded box — the file is tiny, so the O_TRUNC-to-write window
	// is a few microseconds — hence 300 rounds, which still detect a 2%
	// per-round rate with probability 1-0.98^300 (> 99.7%) and run in
	// well under a second either way.
	const (
		iterations = 300
		oldBody    = "Review this diff for correctness."
		newBody    = "Review this diff for correctness, security and style. Lead with the worst finding."
	)

	tornObservations := 0
	for i := range iterations {
		// A fresh name per iteration, seeded with the previous version, so
		// each round exercises the overwrite window on a file a reader is
		// already watching.
		name := "review-" + strconv.Itoa(i)
		path := filepath.Join(dir, name+".md")
		covWritePrompt(t, dir, name, oldBody)

		if err := promptSaveCmd.Flags().Set("content", newBody); err != nil {
			t.Fatal(err)
		}

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
				// os.ReadFile is exactly what promptUseCmd does.
				data, err := os.ReadFile(path)
				if err != nil {
					continue
				}
				if s := string(data); s != oldBody && s != newBody {
					tornObservations++
					return
				}
			}
		}()

		err := promptSaveCmd.RunE(promptSaveCmd, []string{name})
		close(stop)
		wg.Wait()
		if err != nil {
			t.Fatalf("prompt save: %v", err)
		}
	}

	if tornObservations != 0 {
		t.Errorf("a saved prompt was observed torn (empty or partial) in %d/%d re-saves — "+
			"the O_TRUNC window is back; prompt save must publish via "+
			"memory.WriteFileDurable's atomic rename",
			tornObservations, iterations)
	}

	// The durable write must not leave its tempfile behind: `prompt list`
	// globs this directory, and a stray review.md.tmp.* would show up as a
	// prompt nobody saved.
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if filepath.Ext(e.Name()) != ".md" {
			t.Errorf("leftover file %q in the prompt library", e.Name())
		}
	}
}
