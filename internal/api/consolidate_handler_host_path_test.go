package api

import (
	"bytes"
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

// TestConsolidateRun_SkipsCrewsWithUnresolvablePaths: an unconfigured
// storage base must not fall back to a guessed root. The request still
// succeeds (nothing to do), but nothing is written.
func TestConsolidateRun_SkipsCrewsWithUnresolvablePaths(t *testing.T) {
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	seedCrewRow(t, db, "crew_nobase", wsID, "No Base Crew", "no-base-crew")

	h := NewConsolidateHandler(db, newTestLogger())
	h.SetConsolidator(&consolidate.Consolidator{
		DB:         db,
		Journal:    noopEmitter{},
		Summarizer: &stubSummarizer{},
		Logger:     slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError})),
	})
	// SetStorageBasePath deliberately NOT called.

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

	if _, err := os.Stat("/crew/shared/.memory"); err == nil {
		t.Errorf("/crew/shared/.memory exists on this host — the handler fell back to the container literal")
	}
}
