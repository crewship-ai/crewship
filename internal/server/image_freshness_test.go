package server

// #1845 — the sweep that turns a per-crew freshness reading into a durable,
// notifiable event, and the de-duplication that keeps it from becoming noise.
//
// The de-duplication is the load-bearing half. The condition this sweep detects
// persists until an operator acts on it, so a daily cron with no memory writes
// the same row every day for as long as a crew is behind — on a fleet of
// long-lived containers that is exactly the "category people mute" outcome the
// issue warns about, and a muted category is worse than none.

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"testing"

	"github.com/crewship-ai/crewship/internal/journal"
	"github.com/crewship-ai/crewship/internal/provider"
)

// fakeFreshness is a provider.CrewImageFreshness whose answer the test picks.
type fakeFreshness struct {
	state *provider.CrewImageState
	err   error
	calls int
}

func (f *fakeFreshness) CrewImageState(context.Context, provider.CrewConfig) (*provider.CrewImageState, error) {
	f.calls++
	if f.err != nil {
		return nil, f.err
	}
	return f.state, nil
}

func (f *fakeFreshness) RefreshCrewImage(context.Context, provider.CrewConfig) (*provider.CrewImageRefresh, error) {
	return nil, errors.New("not used by the sweep")
}

// recordingEmitter (listening_port_scanner_test.go) captures every Emit call;
// reused here rather than duplicated.

const (
	sweepRunningDigest  = "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	sweepResolvedDigest = "sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
)

func behindState() *provider.CrewImageState {
	return &provider.CrewImageState{
		Image:          "ghcr.io/crewship-ai/agent-runtime:latest",
		ContainerID:    "ctr_deadbeef0000",
		Running:        true,
		RunningDigest:  sweepRunningDigest,
		ResolvedDigest: sweepResolvedDigest,
		Behind:         true,
	}
}

func quietLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, &slog.HandlerOptions{Level: slog.LevelError}))
}

// sweepDB gives the sweep the two tables it reads: the crew roster, and the
// journal it checks its own history against.
func sweepDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	for _, stmt := range []string{
		`CREATE TABLE crews (
		   id TEXT PRIMARY KEY, workspace_id TEXT, slug TEXT, name TEXT,
		   runtime_image TEXT, cached_image TEXT, deleted_at TEXT)`,
		`CREATE TABLE journal_entries (
		   id TEXT PRIMARY KEY, workspace_id TEXT, crew_id TEXT,
		   entry_type TEXT, payload TEXT, ts TEXT DEFAULT '')`,
		`INSERT INTO crews (id, workspace_id, slug, name) VALUES ('crew_1','ws_1','alpha','Alpha')`,
	} {
		if _, err := db.Exec(stmt); err != nil {
			t.Fatalf("seed: %v (%s)", err, stmt)
		}
	}
	return db
}

// TestImageFreshnessSweep_EmitsForABehindCrew is the headline: a crew whose
// container is behind produces a journal row that the notify bridge can route.
func TestImageFreshnessSweep_EmitsForABehindCrew(t *testing.T) {
	db := sweepDB(t)
	em := &recordingEmitter{}

	runImageFreshnessSweep(context.Background(), db, &fakeFreshness{state: behindState()}, em, quietLogger())

	if len(em.entries) != 1 {
		t.Fatalf("emitted %d entries, want 1: %+v", len(em.entries), em.entries)
	}
	e := em.entries[0]
	if e.Type != journal.EntryImageStale {
		t.Errorf("Type = %q, want %q", e.Type, journal.EntryImageStale)
	}
	if e.Severity != journal.SeverityWarn {
		t.Errorf("Severity = %q, want warn — an out-of-date image is not a malfunction, and "+
			"error severity would push it past channel priority floors set for real failures", e.Severity)
	}
	if e.WorkspaceID != "ws_1" || e.CrewID != "crew_1" {
		t.Errorf("scope = (%q, %q), want (ws_1, crew_1)", e.WorkspaceID, e.CrewID)
	}
	// Without a workspace the notify bridge drops the entry on the floor
	// (ObserveJournal skips entries with no audience), so an unscoped emit
	// would be journalled and never delivered — the exact gap #1845 is about.
	for _, key := range []string{"running_digest", "resolved_digest", "image", "remediation"} {
		if _, ok := e.Payload[key]; !ok {
			t.Errorf("payload is missing %q — %+v", key, e.Payload)
		}
	}
}

// TestImageFreshnessSweep_QuietWhenCurrent — the negative that stops the test
// above passing for a sweep that emits unconditionally.
func TestImageFreshnessSweep_QuietWhenCurrent(t *testing.T) {
	db := sweepDB(t)
	em := &recordingEmitter{}
	state := behindState()
	state.Behind = false
	state.RunningDigest = sweepResolvedDigest

	runImageFreshnessSweep(context.Background(), db, &fakeFreshness{state: state}, em, quietLogger())

	if len(em.entries) != 0 {
		t.Fatalf("emitted %d entries for a current crew, want 0: %+v", len(em.entries), em.entries)
	}
}

// TestImageFreshnessSweep_DedupesAcrossRuns is the noise contract. The
// condition persists until an operator acts, so the second and hundredth sweep
// must stay silent.
//
// De-duplication is checked against the JOURNAL rather than an in-memory set
// (which is how the sidecar signal does it, correctly for its per-run cadence).
// A daily cron outlives nothing: an in-memory set is empty again after every
// restart and every deploy, so a weekly redeploy would re-alert weekly on a
// condition nobody had touched.
func TestImageFreshnessSweep_DedupesAcrossRuns(t *testing.T) {
	db := sweepDB(t)
	em := &recordingEmitter{}
	fr := &fakeFreshness{state: behindState()}

	runImageFreshnessSweep(context.Background(), db, fr, em, quietLogger())
	if len(em.entries) != 1 {
		t.Fatalf("first sweep emitted %d, want 1", len(em.entries))
	}
	// The production journal is what the sweep reads back; persist what the
	// emitter recorded so the second sweep sees the same history a live one
	// would.
	persistSweepEntry(t, db, em.entries[0])

	runImageFreshnessSweep(context.Background(), db, fr, em, quietLogger())
	if len(em.entries) != 1 {
		t.Fatalf("second sweep emitted again (total %d) — a crew nobody has recycled would "+
			"produce one identical row per day until the category is muted", len(em.entries))
	}
}

// TestImageFreshnessSweep_ReAlertsWhenTheDigestsMove: dedup must key on the
// OBSERVED PAIR, not on the crew. A crew that was recycled onto a newer image
// and has since fallen behind AGAIN is a new fact and has to be reported;
// keying on crew alone would silence it forever after the first alert.
func TestImageFreshnessSweep_ReAlertsWhenTheDigestsMove(t *testing.T) {
	db := sweepDB(t)
	em := &recordingEmitter{}
	fr := &fakeFreshness{state: behindState()}

	runImageFreshnessSweep(context.Background(), db, fr, em, quietLogger())
	persistSweepEntry(t, db, em.entries[0])

	moved := behindState()
	moved.ResolvedDigest = "sha256:cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc"
	fr.state = moved

	runImageFreshnessSweep(context.Background(), db, fr, em, quietLogger())
	if len(em.entries) != 2 {
		t.Fatalf("emitted %d entries, want 2 — the tag moved again, which is a new fact", len(em.entries))
	}
}

// TestImageFreshnessSweep_SkipsSoftDeletedCrews. Nothing runs for a deleted
// crew, and alerting about one is an alert with no possible action.
func TestImageFreshnessSweep_SkipsSoftDeletedCrews(t *testing.T) {
	db := sweepDB(t)
	if _, err := db.Exec(`UPDATE crews SET deleted_at = '2026-01-01T00:00:00Z' WHERE id = 'crew_1'`); err != nil {
		t.Fatalf("soft delete: %v", err)
	}
	em := &recordingEmitter{}
	fr := &fakeFreshness{state: behindState()}

	runImageFreshnessSweep(context.Background(), db, fr, em, quietLogger())

	if fr.calls != 0 {
		t.Errorf("queried the provider %d times for a deleted crew, want 0", fr.calls)
	}
	if len(em.entries) != 0 {
		t.Errorf("emitted %d entries for a deleted crew, want 0", len(em.entries))
	}
}

// TestImageFreshnessSweep_ProviderErrorIsSurvivable — one crew's daemon error
// must not abort the sweep for the rest of the fleet.
func TestImageFreshnessSweep_ProviderErrorIsSurvivable(t *testing.T) {
	db := sweepDB(t)
	if _, err := db.Exec(`INSERT INTO crews (id, workspace_id, slug, name) VALUES ('crew_2','ws_1','beta','Beta')`); err != nil {
		t.Fatalf("seed crew_2: %v", err)
	}
	em := &recordingEmitter{}
	fr := &fakeFreshness{err: errors.New("daemon unreachable")}

	runImageFreshnessSweep(context.Background(), db, fr, em, quietLogger())

	if fr.calls != 2 {
		t.Errorf("provider called %d times, want 2 — the first crew's error stopped the sweep", fr.calls)
	}
}

func persistSweepEntry(t *testing.T, db *sql.DB, e journal.Entry) {
	t.Helper()
	b, err := json.Marshal(e.Payload)
	if err != nil {
		t.Fatalf("marshal payload: %v", err)
	}
	resolved, _ := e.Payload["resolved_digest"].(string)
	if _, err := db.Exec(
		`INSERT INTO journal_entries (id, workspace_id, crew_id, entry_type, payload)
		 VALUES (?, ?, ?, ?, ?)`,
		"je_"+e.CrewID+"_"+resolved, e.WorkspaceID, e.CrewID, string(e.Type), string(b)); err != nil {
		t.Fatalf("persist journal entry: %v", err)
	}
}
