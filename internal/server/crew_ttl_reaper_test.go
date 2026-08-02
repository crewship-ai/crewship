package server

// The reaper's two DB-facing halves (#1662):
//
//   loadCrewTTLHours     — the per-sweep read that makes the crews table, not
//                          process memory, the authority on every TTL.
//   seedCrewReaperClock  — the boot pass that dates a rehydrated container's
//                          idle clock from the container's own start rather
//                          than from now.

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/logging"
	"github.com/crewship-ai/crewship/internal/provider"
)

func TestLoadCrewTTLHours_ResolvesNullToTheServerDefault(t *testing.T) {
	// The whole bug: container_ttl_hours is nullable with no DEFAULT, so an
	// unconfigured crew resolved to 0 and 0 means "never stop".
	s := newTestServerWithDeps(t)
	logger := logging.New("error", "json", nil)
	mustExec(t, s.db, `INSERT INTO workspaces (id, name, slug) VALUES ('ws_ttl','TTL','ws-ttl')`)
	mustExec(t, s.db, `INSERT INTO crews (id, workspace_id, name, slug) VALUES ('cr_null','ws_ttl','N','n')`)

	got := loadCrewTTLHours(context.Background(), s.db, logger)
	if got["cr_null"] <= 0 {
		t.Errorf("effective TTL for an unconfigured crew = %d, want a positive default", got["cr_null"])
	}
}

func TestLoadCrewTTLHours_KeepsExplicitValuesIncludingNeverStop(t *testing.T) {
	s := newTestServerWithDeps(t)
	logger := logging.New("error", "json", nil)
	mustExec(t, s.db, `INSERT INTO workspaces (id, name, slug) VALUES ('ws_ttl','TTL','ws-ttl')`)
	mustExec(t, s.db, `INSERT INTO crews (id, workspace_id, name, slug, container_ttl_hours)
		VALUES ('cr_never','ws_ttl','Never','never', 0)`)
	mustExec(t, s.db, `INSERT INTO crews (id, workspace_id, name, slug, container_ttl_hours)
		VALUES ('cr_12','ws_ttl','Twelve','twelve', 12)`)

	got := loadCrewTTLHours(context.Background(), s.db, logger)
	if got["cr_never"] != 0 {
		t.Errorf("TTL for an explicit 0 = %d, want 0 (never stop)", got["cr_never"])
	}
	if got["cr_12"] != 12 {
		t.Errorf("TTL for an explicit 12 = %d, want 12", got["cr_12"])
	}
}

func TestLoadCrewTTLHours_ExcludesDeletedCrews(t *testing.T) {
	// A crew present in the map is reapable; one absent from it is never
	// touched. Deleted crews have no business in either category, and
	// including them would have the reaper stop containers on behalf of rows
	// the rest of the product treats as gone.
	s := newTestServerWithDeps(t)
	logger := logging.New("error", "json", nil)
	mustExec(t, s.db, `INSERT INTO workspaces (id, name, slug) VALUES ('ws_ttl','TTL','ws-ttl')`)
	mustExec(t, s.db, `INSERT INTO crews (id, workspace_id, name, slug, deleted_at)
		VALUES ('cr_gone','ws_ttl','Gone','gone', '2026-01-01T00:00:00Z')`)

	got := loadCrewTTLHours(context.Background(), s.db, logger)
	if _, present := got["cr_gone"]; present {
		t.Error("a soft-deleted crew appeared in the reaper's TTL map")
	}
}

func TestLoadCrewTTLHours_QueryFailureReturnsNilNotAnEmptyMap(t *testing.T) {
	// nil and an empty map are opposites here. An empty map is authoritative
	// — "no crew has a TTL" — and would exempt the whole fleet from reaping
	// with no signal. checkTTLs treats a nil resolver result as "resolver
	// said nothing" and falls back, so a DB blip must produce nil.
	s := newTestServerWithDeps(t)
	logger := logging.New("error", "json", nil)
	db := s.db
	if err := db.Close(); err != nil {
		t.Fatalf("close db: %v", err)
	}

	if got := loadCrewTTLHours(context.Background(), db, logger); got != nil {
		t.Errorf("loadCrewTTLHours on a closed DB = %v, want nil", got)
	}
}

// ---------------------------------------------------------------------------
// seedCrewReaperClock
// ---------------------------------------------------------------------------

// startedAtContainer answers ContainerStatus with a canned StartedAt, which is
// exactly what the docker provider puts in ContainerStatus.Uptime.
type startedAtContainer struct {
	mockContainer
	uptime string
	err    error
}

func (c *startedAtContainer) ContainerStatus(_ context.Context, id string) (*provider.ContainerStatus, error) {
	if c.err != nil {
		return nil, c.err
	}
	return &provider.ContainerStatus{ID: id, State: "running", Uptime: c.uptime}, nil
}

func TestSeedCrewReaperClock_DatesTheClockFromTheContainerStart(t *testing.T) {
	// dev1 had a container that had been running five days with zero agent
	// runs and survived several crewshipd restarts. Seeding from now would
	// have handed it a fresh TTL window on each one.
	s := newTestServerWithDeps(t)
	started := time.Now().UTC().Add(-5 * 24 * time.Hour)
	s.container = &startedAtContainer{uptime: started.Format(time.RFC3339Nano)}
	mustExec(t, s.db, `INSERT INTO workspaces (id, name, slug) VALUES ('ws_ttl','TTL','ws-ttl')`)
	mustExec(t, s.db, `INSERT INTO crews (id, workspace_id, name, slug, container_ttl_hours)
		VALUES ('cr_old','ws_ttl','Old','old', 4)`)

	s.seedCrewReaperClock(context.Background(), "cr_old", "ws_ttl", "ctr-old")

	act, ok := s.orchestrator.CrewActivity("cr_old")
	if !ok {
		t.Fatal("rehydrated container was not handed to the reaper")
	}
	if act.LastActivity.Sub(started).Abs() > time.Second {
		t.Errorf("seeded clock = %v, want the container start %v", act.LastActivity, started)
	}
	if act.TTLHours != 4 {
		t.Errorf("seeded TTL = %d, want 4", act.TTLHours)
	}
	if act.ContainerID != "ctr-old" {
		t.Errorf("seeded containerID = %q, want ctr-old", act.ContainerID)
	}
}

func TestSeedCrewReaperClock_NullColumnSeedsTheServerDefault(t *testing.T) {
	s := newTestServerWithDeps(t)
	s.container = &startedAtContainer{uptime: time.Now().UTC().Format(time.RFC3339Nano)}
	mustExec(t, s.db, `INSERT INTO workspaces (id, name, slug) VALUES ('ws_ttl','TTL','ws-ttl')`)
	mustExec(t, s.db, `INSERT INTO crews (id, workspace_id, name, slug) VALUES ('cr_null','ws_ttl','N','n')`)

	s.seedCrewReaperClock(context.Background(), "cr_null", "ws_ttl", "ctr-null")

	act, ok := s.orchestrator.CrewActivity("cr_null")
	if !ok {
		t.Fatal("rehydrated container was not handed to the reaper")
	}
	if act.TTLHours <= 0 {
		t.Errorf("seeded TTL for a NULL column = %d, want the positive server default", act.TTLHours)
	}
}

func TestSeedCrewReaperClock_UnreadableStartTimeFallsBackToNow(t *testing.T) {
	// A provider that cannot answer costs us one TTL window, not a container
	// that is never reaped.
	s := newTestServerWithDeps(t)
	s.container = &startedAtContainer{err: errors.New("docker down")}
	mustExec(t, s.db, `INSERT INTO workspaces (id, name, slug) VALUES ('ws_ttl','TTL','ws-ttl')`)
	mustExec(t, s.db, `INSERT INTO crews (id, workspace_id, name, slug, container_ttl_hours)
		VALUES ('cr_x','ws_ttl','X','x', 4)`)

	s.seedCrewReaperClock(context.Background(), "cr_x", "ws_ttl", "ctr-x")

	act, ok := s.orchestrator.CrewActivity("cr_x")
	if !ok {
		t.Fatal("rehydrated container was not handed to the reaper")
	}
	if time.Since(act.LastActivity) > time.Minute {
		t.Errorf("fallback clock = %v, want ~now", act.LastActivity)
	}
}

func TestSeedCrewReaperClock_UnknownCrewSeedsNothing(t *testing.T) {
	s := newTestServerWithDeps(t)
	s.container = &startedAtContainer{uptime: time.Now().UTC().Format(time.RFC3339Nano)}

	s.seedCrewReaperClock(context.Background(), "cr_missing", "ws_missing", "ctr-missing")

	if _, ok := s.orchestrator.CrewActivity("cr_missing"); ok {
		t.Error("a crew with no row was registered with the reaper")
	}
}

func TestRehydrateContainers_SeedsTheReaperNotJustStats(t *testing.T) {
	// The gap that made a surviving container immortal: rehydration
	// registered the stats collector and nothing else.
	s := newTestServerWithDeps(t)
	lookup := &covLookupContainer{}
	s.container = lookup
	s.statsCollector = NewStatsCollector(lookup, nil, nil, time.Hour)

	mustExec(t, s.db, `INSERT INTO workspaces (id, name, slug) VALUES ('ws_rh2','RH','ws-rh2')`)
	mustExec(t, s.db, `INSERT INTO crews (id, workspace_id, name, slug) VALUES ('cr_run2','ws_rh2','R','run-slug')`)

	s.rehydrateContainers(context.Background())

	if _, ok := s.orchestrator.CrewActivity("cr_run2"); !ok {
		t.Fatal("rehydration registered stats but left the container invisible to the reaper")
	}
}
