package api

import (
	"bytes"
	"database/sql"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/consolidate"
	"github.com/crewship-ai/crewship/internal/memory"
)

// TestConsolidateRun_WritesIntoTheCrewBindSource covers the third
// consolidator entry point of #1663 — the manual POST
// /api/v1/consolidate/run — which had its own copy of the
// container-absolute root ("/crew/shared/.memory") and so wrote pins.md
// at the host filesystem root exactly like the cron runner and the
// post-run trigger did.
//
// The assertion is the same one the other two are held to: the file
// lands at the host path that the container sees at
// /crew/shared/.memory/{crew_slug}/topics/pins.md, which is what
// orchestrator.buildPinsBlock reads.
func TestConsolidateRun_WritesIntoTheCrewBindSource(t *testing.T) {
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)

	const (
		crewID   = "crew_pins_host"
		crewSlug = "pins-host-crew"
		needle   = "manual-run-pins-canary"
	)
	seedCrewRow(t, db, crewID, wsID, "Pins Host Crew", crewSlug)

	ts := time.Now().UTC().Add(-time.Minute).Format(time.RFC3339Nano)
	if _, err := db.Exec(
		`INSERT INTO journal_entries
		 (id, workspace_id, crew_id, ts, entry_type, severity, priority, actor_type, actor_id, summary, payload, refs)
		 VALUES ('j_pin_api', ?, ?, ?, 'peer.escalation', 'info', 'pin', 'agent', 'a', ?, '{}', '{}')`,
		wsID, crewID, ts, needle); err != nil {
		t.Fatalf("seed pinned entry: %v", err)
	}

	basePath := t.TempDir()
	h := NewConsolidateHandler(db, newTestLogger())
	h.SetConsolidator(&consolidate.Consolidator{
		DB:         db,
		Journal:    noopEmitter{},
		Summarizer: &stubSummarizer{},
		Logger:     slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError})),
	})
	h.SetStorageBasePath(basePath)

	// Run is async; wait for the goroutine before asserting on disk (and
	// before t.TempDir() cleanup races its writes).
	done := make(chan struct{}, 1)
	h.testRunDone = done

	req := httptest.NewRequest("POST", "/api/v1/consolidate/run", bytes.NewBufferString(`{"since":"6h"}`))
	req = withWorkspaceUser(req, userID, wsID, "OWNER")
	rr := httptest.NewRecorder()
	h.Run(rr, req)
	if rr.Code != http.StatusAccepted {
		t.Fatalf("status = %d, body=%s", rr.Code, rr.Body.String())
	}
	<-done

	// The host path behind the container path the prompt builder reads.
	// {basePath}/crews/{crewID} is what the docker provider binds at
	// /crew, so /crew/shared/.memory/... resolves under it.
	containerTopics := memory.ContainerCrewTopicsDir(crewSlug)
	rel := strings.TrimPrefix(containerTopics, "/crew/")
	wantPins := filepath.Join(basePath, "crews", crewID, filepath.FromSlash(rel), "pins.md")

	body, err := os.ReadFile(wantPins)
	if err != nil {
		t.Fatalf(`manual consolidation did not write pins.md where the prompt builder reads it.
  container path: %s/pins.md
  host path:      %s
  err: %v
This is #1663 on the manual-run endpoint.`, containerTopics, wantPins, err)
	}
	if !strings.Contains(string(body), needle) {
		t.Errorf("pins.md at %s missing the pinned entry:\n%s", wantPins, body)
	}
}

// TestConsolidateRun_SkipsCrewsWithUnresolvablePaths: a crew whose host
// output directory cannot be resolved must be SKIPPED, not written to a
// guessed root. The guess is #1663 — the handler used to fall back to the
// container literal memory.ContainerCrewMemoryRoot ("/crew/shared/.memory"),
// which from a host process silently creates a tree at the host filesystem
// root, outside every bind source, where no container will ever read it.
// The request still succeeds (nothing to do); nothing is written.
//
// HOST INDEPENDENCE — please do not reintroduce the assumption this
// replaced. The original assertion was `os.Stat("/crew/shared/.memory")`
// must return "not exist". That is a claim about the machine, not about the
// handler: /crew/shared/.memory is a real directory on the crewship-dev
// workstations, so the test was red for everyone there (#1894) and red in
// CI on any host that happens to have it. Both halves below establish the
// precondition themselves instead:
//
//   - the crew slug is generated per run, so every path the handler could
//     derive from it — host-side or container-side — is one this process
//     names for the first time. "Does it exist afterwards" is then a fact
//     about what this run created, whatever else the host keeps under /crew.
//   - the second half keeps the whole resolvable region inside t.TempDir(),
//     so the unresolvable input is manufactured by the test and a stray
//     write has nowhere to hide.
//
// Each half opens with a positive control: the same crew and the same
// pinned entry, with the path resolvable, must produce pins.md. Without it
// "nothing was written" would also hold for a handler that resolved the
// path perfectly and merely had nothing to say — a tautology that passes
// forever.
func TestConsolidateRun_SkipsCrewsWithUnresolvablePaths(t *testing.T) {
	// Unique per test binary run: nothing on this host can already own a
	// path derived from it, and a failing run cannot poison the next one.
	unique := fmt.Sprintf("%d-%d", os.Getpid(), time.Now().UnixNano())

	t.Run("storage base not configured", func(t *testing.T) {
		db := setupTestDB(t)
		userID := seedTestUser(t, db)
		wsID := seedTestWorkspace(t, db, userID)

		const crewID = "crew_nobase"
		const needle = "no-base-pins-canary"
		crewSlug := "no-base-crew-" + unique
		seedCrewRow(t, db, crewID, wsID, "No Base Crew", crewSlug)
		seedPinnedJournalEntry(t, db, "j_pin_nobase", wsID, crewID, needle)

		// The only directory an unconfigured handler could still write is
		// the container literal for THIS crew. The slug is fresh, so its
		// absence now is guaranteed by construction rather than assumed —
		// assert it anyway, so a surprise here reads as a broken premise
		// instead of a passing test.
		containerCrewDir := filepath.Dir(filepath.FromSlash(memory.ContainerCrewTopicsDir(crewSlug)))
		if _, err := os.Stat(containerCrewDir); !errors.Is(err, fs.ErrNotExist) {
			t.Fatalf("premise broken: %s already exists before the run (stat err = %v)", containerCrewDir, err)
		}

		// SetStorageBasePath deliberately NOT called.
		runConsolidateForTest(t, db, userID, wsID, "")

		if _, err := os.Stat(containerCrewDir); !errors.Is(err, fs.ErrNotExist) {
			t.Errorf("handler fell back to the container literal: %s exists after a run with no storage base (stat err = %v)\nThis is #1663: a host process writing the in-container path.", containerCrewDir, err)
		}

		// Positive control: identical crew and entry, base path configured,
		// pins.md must land under it. Proves the seeded data is
		// write-producing, so the assertion above has teeth.
		base := t.TempDir()
		runConsolidateForTest(t, db, userID, wsID, base)
		topics, err := memory.HostCrewTopicsDir(base, crewID, crewSlug)
		if err != nil {
			t.Fatalf("resolve host topics dir: %v", err)
		}
		body, err := os.ReadFile(filepath.Join(topics, "pins.md"))
		if err != nil {
			t.Fatalf("positive control: a resolvable base wrote no pins.md at %s: %v", topics, err)
		}
		if !strings.Contains(string(body), needle) {
			t.Fatalf("positive control: pins.md missing the pinned entry:\n%s", body)
		}
	})

	t.Run("crew slug is not a safe path component", func(t *testing.T) {
		db := setupTestDB(t)
		userID := seedTestUser(t, db)
		wsID := seedTestWorkspace(t, db, userID)

		const crewID = "crew_badslug"
		const needle = "bad-slug-pins-canary"
		// safepath.ValidateComponent refuses a slug carrying a separator,
		// so HostCrewTopicsDir cannot resolve it. This is the other way a
		// crew path becomes unresolvable, and unlike an empty base it is
		// fully expressible inside a directory the test owns.
		crewSlug := "../escaped-" + unique
		seedCrewRow(t, db, crewID, wsID, "Bad Slug Crew", crewSlug)
		seedPinnedJournalEntry(t, db, "j_pin_badslug", wsID, crewID, needle)

		// root is the entire filesystem region reachable from the
		// configured base, including one level of "..". After the run it
		// must still hold nothing but the storage dir we created: any other
		// entry is the handler having guessed somewhere.
		root := t.TempDir()
		base := filepath.Join(root, "storage")
		if err := os.MkdirAll(base, 0o755); err != nil {
			t.Fatalf("mkdir storage base: %v", err)
		}

		runConsolidateForTest(t, db, userID, wsID, base)

		var stray []string
		if err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if p == root || p == base {
				return nil
			}
			stray = append(stray, p)
			return nil
		}); err != nil {
			t.Fatalf("walk %s: %v", root, err)
		}
		if len(stray) > 0 {
			t.Errorf("unresolvable crew slug %q still produced writes under %s:\n  %s\nThe crew must be skipped, not written to a sanitised guess.",
				crewSlug, root, strings.Join(stray, "\n  "))
		}

		// Positive control: same crew ID, same entries, a slug that IS a
		// safe component — the run must write. Otherwise "no stray files"
		// above would pass for a handler that never writes at all.
		okSlug := "safe-slug-" + unique
		if _, err := db.Exec(`UPDATE crews SET slug = ? WHERE id = ?`, okSlug, crewID); err != nil {
			t.Fatalf("rename crew slug: %v", err)
		}
		runConsolidateForTest(t, db, userID, wsID, base)
		topics, err := memory.HostCrewTopicsDir(base, crewID, okSlug)
		if err != nil {
			t.Fatalf("resolve host topics dir: %v", err)
		}
		body, err := os.ReadFile(filepath.Join(topics, "pins.md"))
		if err != nil {
			t.Fatalf("positive control: a safe slug wrote no pins.md at %s: %v", topics, err)
		}
		if !strings.Contains(string(body), needle) {
			t.Fatalf("positive control: pins.md missing the pinned entry:\n%s", body)
		}
	})
}

// seedPinnedJournalEntry inserts one priority=pin entry, the minimum the
// consolidator needs to write pins.md into whatever OutputDir it resolves
// (it snapshots pins regardless of the MinEntries threshold).
func seedPinnedJournalEntry(t *testing.T, db *sql.DB, entryID, wsID, crewID, summary string) {
	t.Helper()
	ts := time.Now().UTC().Add(-time.Minute).Format(time.RFC3339Nano)
	if _, err := db.Exec(
		`INSERT INTO journal_entries
		 (id, workspace_id, crew_id, ts, entry_type, severity, priority, actor_type, actor_id, summary, payload, refs)
		 VALUES (?, ?, ?, ?, 'peer.escalation', 'info', 'pin', 'agent', 'a', ?, '{}', '{}')`,
		entryID, wsID, crewID, ts, summary); err != nil {
		t.Fatalf("seed pinned entry: %v", err)
	}
}

// runConsolidateForTest drives the real async POST /api/v1/consolidate/run
// path and waits for the background goroutine. An empty basePath means
// SetStorageBasePath is never called — the unconfigured-server case.
func runConsolidateForTest(t *testing.T, db *sql.DB, userID, wsID, basePath string) {
	t.Helper()
	h := NewConsolidateHandler(db, newTestLogger())
	h.SetConsolidator(&consolidate.Consolidator{
		DB:         db,
		Journal:    noopEmitter{},
		Summarizer: &stubSummarizer{},
		Logger:     slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError})),
	})
	if basePath != "" {
		h.SetStorageBasePath(basePath)
	}

	done := make(chan struct{}, 1)
	h.testRunDone = done

	req := httptest.NewRequest("POST", "/api/v1/consolidate/run", bytes.NewBufferString(`{"since":"6h"}`))
	req = withWorkspaceUser(req, userID, wsID, "OWNER")
	rr := httptest.NewRecorder()
	h.Run(rr, req)
	if rr.Code != http.StatusAccepted {
		t.Fatalf("status = %d, body=%s", rr.Code, rr.Body.String())
	}
	<-done
}
