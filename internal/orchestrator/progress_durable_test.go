package orchestrator

import (
	"os"
	"path/filepath"
	"sync"
	"testing"
)

// progressPath mirrors WriteEvent's layout so the tests can watch the
// exact file it writes.
func progressPath(crewSlug, traceID string) string {
	return filepath.Join("data", "crews", crewSlug, "missions", traceID, "progress.jsonl")
}

// TestWriteEvent_NeverObservableEmpty is the #1999 regression test, in
// the shape #1807 used for pins.md.
//
// The old WriteEvent opened progress.jsonl with O_APPEND|O_CREATE and
// then issued a separate f.Write. O_CREATE and the first write are two
// syscalls, so between them the file EXISTS at zero bytes. Any reader
// that lands in that window — and ReadProgress, in this very package,
// is that reader — sees a mission with no history at all rather than
// a mission with one event.
//
// The invariant asserted here is the one the durable helper buys and
// the append form cannot: progress.jsonl becomes visible only via an
// atomic rename, so if the path exists it already holds the complete
// first event. "Exists" therefore implies "non-empty and parseable",
// always.
//
// Verified to FAIL against the pre-fix O_APPEND implementation.
func TestWriteEvent_NeverObservableEmpty(t *testing.T) {
	t.Chdir(t.TempDir())

	// The pre-fix implementation lost this race in 163 of 300 runs (a
	// ~54% hit rate, in line with the 101/300 #1807 measured on pins.md).
	// 60 iterations therefore still detect it with probability
	// 1-0.46^60 — effectively certain — while keeping the fsync-bound
	// runtime of the FIXED path down to a couple of seconds.
	const iterations = 60
	pw := NewProgressWriter()

	emptyObservations := 0
	for i := range iterations {
		// A fresh trace per iteration so every round exercises the
		// create-then-write window rather than appending to a file that
		// is already non-empty.
		traceID := "trace-" + itoa(i)
		path := progressPath("crew", traceID)

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
				// Stat-then-read is exactly what an out-of-process
				// reader does. If the file is visible at all it must
				// already carry the whole event.
				if _, err := os.Stat(path); err != nil {
					continue
				}
				data, err := os.ReadFile(path)
				if err != nil {
					continue
				}
				if len(data) == 0 {
					emptyObservations++
					return
				}
			}
		}()

		pw.WriteEvent(traceID, "crew", ProgressEvent{Type: "task_started", Title: "t"})
		close(stop)
		wg.Wait()
	}

	if emptyObservations != 0 {
		t.Errorf("progress.jsonl was observed existing-but-empty in %d/%d runs — "+
			"the create-then-write window is back; WriteEvent must publish via "+
			"memory.WriteFileDurable's atomic rename",
			emptyObservations, iterations)
	}
}

// TestWriteEvent_AppendsPreservePriorEvents pins the behaviour the
// read-modify-write conversion has to keep: WriteEvent is still an
// append in effect. Swapping O_APPEND for a whole-file write is only
// correct if earlier events survive it.
func TestWriteEvent_AppendsPreservePriorEvents(t *testing.T) {
	t.Chdir(t.TempDir())

	pw := NewProgressWriter()
	titles := []string{"first", "second", "third"}
	for _, title := range titles {
		pw.WriteEvent("trace-1", "crew", ProgressEvent{Type: "task_completed", Title: title})
	}

	events, err := pw.ReadProgress("trace-1", "crew")
	if err != nil {
		t.Fatalf("ReadProgress: %v", err)
	}
	if len(events) != len(titles) {
		t.Fatalf("got %d events, want %d — a whole-file write that drops "+
			"prior events is not an append", len(events), len(titles))
	}
	for i, want := range titles {
		if events[i].Title != want {
			t.Errorf("event %d title = %q, want %q (order must be chronological)",
				i, events[i].Title, want)
		}
	}
}

// TestWriteEvent_LeavesNoTempFiles guards the durable helper's cleanup
// through this call site: a stray progress.jsonl.tmp.* would be read by
// nothing but would accumulate one file per event.
func TestWriteEvent_LeavesNoTempFiles(t *testing.T) {
	t.Chdir(t.TempDir())

	pw := NewProgressWriter()
	for range 5 {
		pw.WriteEvent("trace-1", "crew", ProgressEvent{Type: "task_started"})
	}

	dir := filepath.Dir(progressPath("crew", "trace-1"))
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read mission dir: %v", err)
	}
	for _, e := range entries {
		if e.Name() != "progress.jsonl" {
			t.Errorf("unexpected leftover file %q in the mission dir", e.Name())
		}
	}
}
