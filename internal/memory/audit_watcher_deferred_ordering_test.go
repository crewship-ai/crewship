package memory

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/fsnotify/fsnotify"
)

// Both writes are legitimate first-provision writes; only forced event ordering differs.
func TestAuditWatcherDeferredBoot_FirstWriteOrdering(t *testing.T) {
	for _, beforeBaseline := range []bool{true, false} {
		name := "after_initial_snapshot"
		if beforeBaseline {
			name = "during_deferred_boot"
		}
		t.Run(name, func(t *testing.T) {
			base, db, journal, scrubber := auditTestRig(t)
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			bootAttempt := make(chan struct{})
			releaseBoot := make(chan struct{})
			snapshots := make(chan struct{}, 8)
			StartAuditWatcher(ctx, db, journal, AuditWatcherConfig{
				BasePath: base, BlobRoot: filepath.Join(base, "versions"), Scrubber: scrubber,
				DeferredBootInterval: time.Millisecond,
				PollFallbackInterval: 10 * time.Millisecond,
				DebounceInterval:     time.Millisecond,
				watchHooks: fsnotifyHooks{
					newWatcher: func() (*fsnotify.Watcher, error) {
						close(bootAttempt)
						select {
						case <-releaseBoot:
						case <-ctx.Done():
						}
						return nil, errors.New("owned fixture forces poll-only fallback")
					},
					afterPollSnapshot: func() {
						select {
						case snapshots <- struct{}{}:
						default:
						}
					},
				},
			}, quietLogger())
			if err := os.MkdirAll(filepath.Join(base, "crews"), 0o755); err != nil {
				t.Fatal(err)
			}
			await := func(ch <-chan struct{}, what string) {
				t.Helper()
				select {
				case <-ch:
				case <-time.After(3 * time.Second):
					t.Fatalf("did not reach %s", what)
				}
			}
			await(bootAttempt, "deferred watcher startup")
			content := []byte("# Benign fixture\nSingle first-provision write.\n")
			sum := sha256.Sum256(content)
			wantSHA := hex.EncodeToString(sum[:])
			write := func() {
				t.Helper()
				writeMemoryFile(t, base, "crew_audit", "martin", "AGENT.md", content)
			}
			if beforeBaseline {
				write()
			}
			close(releaseBoot)
			await(snapshots, "actual initial poll snapshot")
			if !beforeBaseline {
				write()
			}
			await(snapshots, "actual subsequent poll snapshot")
			// Deadline is only a deadlock/failure bound, not a readiness sleep.
			// Native scan barriers establish the causal ordering before assertions.
			deadline := time.NewTimer(3 * time.Second)
			defer deadline.Stop()
			tick := time.NewTicker(10 * time.Millisecond)
			defer tick.Stop()
			for {
				var rows int
				if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM memory_versions WHERE workspace_id = ? AND path = ? AND sha256 = ? AND written_by = 'audit-watcher'`, "ws_audit", "agent:martin/AGENT.md", wantSHA).Scan(&rows); err != nil {
					t.Fatal(err)
				}
				var events int
				if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM journal_entries WHERE workspace_id = ? AND entry_type = 'memory.updated' AND actor_id = 'audit-watcher'`, "ws_audit").Scan(&events); err != nil {
					t.Fatal(err)
				}
				if rows == 1 && events == 1 {
					return
				}
				select {
				case <-deadline.C:
					t.Fatalf("single first-provision write was not audited after observed scans (rows=%d)", rows)
				case <-tick.C:
				}
			}
		})
	}
}

// Ordinary existing-root boot must keep historical files silent. The native
// poller is allowed to complete scans before explicitly flushing any pending
// paths; the long debounce is a controlled timer, not a readiness sleep.
func TestAuditWatchConfig_ExistingRootKeepsHistoricalBaseline(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "AGENT.md")
	if err := os.WriteFile(path, []byte("historical fixture"), 0o644); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	snapshots := make(chan struct{}, 8)
	cfg := auditWatchConfig(AuditWatcherConfig{
		PollFallbackInterval: 10 * time.Millisecond,
		DebounceInterval:     time.Hour,
		watchHooks: fsnotifyHooks{afterPollSnapshot: func() {
			select {
			case snapshots <- struct{}{}:
			default:
			}
		}},
	}, quietLogger())
	cfg.UseFsnotify = false
	w, err := StartWatcher(t.Context(), root, cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer w.Stop()
	awaitScan := func() {
		t.Helper()
		select {
		case <-snapshots:
		case <-time.After(3 * time.Second):
			t.Fatal("native poll scan did not finish")
		}
	}
	awaitScan()
	awaitScan()
	w.flush()
	select {
	case event := <-w.Events():
		t.Fatalf("ordinary baseline replayed historical content: %+v", event)
	default:
	}
	// A changed file must still be detected after that baseline. Set an explicit
	// distinct mtime so filesystem timestamp granularity cannot hide this write.
	if err := os.WriteFile(path, []byte("new ordinary write"), 0o644); err != nil {
		t.Fatal(err)
	}
	changed := info.ModTime().Add(time.Second)
	if err := os.Chtimes(path, changed, changed); err != nil {
		t.Fatal(err)
	}
	deadline := time.NewTimer(3 * time.Second)
	defer deadline.Stop()
	for {
		w.debounceMu.Lock()
		_, pending := w.pendingPaths[path]
		w.debounceMu.Unlock()
		if pending {
			break
		}
		select {
		case <-snapshots:
		case <-deadline.C:
			t.Fatal("post-baseline write never reached debounce")
		}
	}
	w.flush()
	select {
	case event := <-w.Events():
		if len(event.Paths) != 1 || event.Paths[0] != path {
			t.Fatalf("post-baseline paths = %v", event.Paths)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("post-baseline write was not emitted")
	}
}
