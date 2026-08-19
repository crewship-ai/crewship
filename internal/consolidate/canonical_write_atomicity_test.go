package consolidate

import (
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/journal"
)

// canonical_write_atomicity_test.go — #1807.
//
// snapshotPins and appendRules used to create their canonical file and
// fill it in two separate syscalls:
//
//	f, err := root.OpenFile(name, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
//	...
//	f.WriteString(block.String())
//
// Between those two lines the file exists on disk with ZERO BYTES. The
// flock taken above them guards <name>.lock, so it serialises two
// consolidator runs against each other but does nothing for an outside
// reader that is not taking the lock — and pins.md / learned-*.md are
// exactly that: watched, agent-readable memory files
// (internal/memory/audit_watcher.go maps pins.md to TierPins). A reader
// landing in that window gets an empty file rather than the previous
// good contents.
//
// That is also what made TestPostRunTrigger_WritesIntoTheCrewBindSource
// flake on the loaded CI box: it polled for the file by existence, and
// os.ReadFile on an empty file returns a non-nil zero-length slice, so
// it stopped polling and asserted against "".
//
// This test drives the real writers with a concurrent reader that does
// nothing but os.ReadFile the target path in a tight loop, and fails if
// the reader ever observes the file existing at zero bytes. It is a
// probabilistic observer of a real window, not a timing assumption: it
// can only fail if the zero-byte state is genuinely reachable, so it
// never fails against an atomic (temp + rename) writer, however the
// scheduler behaves. On the pre-fix code it reproduces in roughly 10%
// of iterations, which over iterations below is a certainty in practice.
func TestCanonicalWrites_AreNeverObservableAsZeroBytes(t *testing.T) {
	t.Parallel()

	// Enough iterations that a ~10%-per-iteration window is caught with
	// overwhelming probability, while the whole test still runs in well
	// under a second: each iteration is one small file write.
	const iterations = 300

	cases := []struct {
		name string
		// file is the basename the writer is expected to create in dir.
		file string
		// write performs one real canonical write into dir. iter is the
		// iteration index, so cases can keep IDs unique.
		write func(t *testing.T, dir string, iter int)
	}{
		{
			name: "pins.md via snapshotPins",
			file: "pins.md",
			write: func(t *testing.T, dir string, iter int) {
				t.Helper()
				entries := []journal.Entry{{
					ID:       fmt.Sprintf("j_pin_%d", iter),
					Priority: journal.PriorityPin,
					Type:     journal.EntryPeerEscalation,
					TS:       time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC),
					Summary:  "never restart prod on friday",
				}}
				wrote, err := snapshotPins(Config{OutputDir: dir}, entries)
				if err != nil {
					t.Errorf("snapshotPins: %v", err)
					return
				}
				if !wrote {
					t.Errorf("snapshotPins reported wrote=false on a fresh dir")
				}
			},
		},
		{
			name: "learned-*.md via appendRules",
			file: fmt.Sprintf("learned-%s.md", time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC).Format("2006-01-02")),
			write: func(t *testing.T, dir string, iter int) {
				t.Helper()
				now := time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC)
				rules := []LearnedRule{{
					Pattern:    fmt.Sprintf("pattern-%d", iter),
					Action:     "escalate to the on-call human",
					Confidence: 0.9,
					Evidence:   []string{fmt.Sprintf("j_%d", iter)},
				}}
				if _, _, err := (&Consolidator{}).appendRules(dir, now, rules); err != nil {
					t.Errorf("appendRules: %v", err)
				}
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			zeroByteObservations := 0
			for i := range iterations {
				dir := t.TempDir()
				path := filepath.Join(dir, tc.file)

				var sawZeroBytes atomic.Bool
				stop := make(chan struct{})
				var wg sync.WaitGroup
				wg.Add(1)
				go func() {
					defer wg.Done()
					for {
						select {
						case <-stop:
							return
						default:
						}
						// The only assertion this reader makes is the
						// one the issue documents: the file must never
						// be readable while empty. A short read of a
						// partially-written file would also be a bug,
						// but zero bytes is the state the two-syscall
						// shape provably produces.
						b, err := os.ReadFile(path)
						if err == nil && len(b) == 0 {
							sawZeroBytes.Store(true)
							return
						}
					}
				}()

				tc.write(t, dir, i)

				close(stop)
				wg.Wait()
				if sawZeroBytes.Load() {
					zeroByteObservations++
				}
				if t.Failed() {
					return
				}
			}
			if zeroByteObservations > 0 {
				t.Errorf("a concurrent reader observed %s existing with zero bytes in %d/%d runs — "+
					"the writer creates the file and fills it in two syscalls, so any reader "+
					"(the audit watcher, an agent, TestPostRunTrigger_WritesIntoTheCrewBindSource) "+
					"can see an empty file instead of the previous contents; write to a "+
					"tempfile and rename it into place instead",
					tc.file, zeroByteObservations, iterations)
			}
		})
	}
}
