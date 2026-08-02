package api

import (
	"context"
	"database/sql"
	"testing"
)

// The stored-sentinel half of #1643: a crews row holding container_memory_mb
// = 0 must resolve to the DOCUMENTED default, not to whatever fallback the
// consumer happens to carry.
//
// The backfill migration fixes the rows that exist today. It cannot fix the
// rows that arrive tomorrow, and there are two ways they still do:
//
//   - a restore. `crewship admin backup restore` re-inserts the source
//     bundle's rows verbatim, and the per-migration restore-backfill hooks are
//     only wired for the legacy Go migrations — a file migration has no hook.
//     Restoring a bundle taken before #1641 puts the 0s straight back.
//   - anything that writes the column without going through the two API
//     handlers: a hand-written UPDATE, a future importer, a seed.
//
// So the resolution belongs at the read boundary as well, which is also where
// its sibling already lives: resolveCrewContainerTTLHours sits three lines
// away in buildCrewRuntimeConfig, put there by #1662 for exactly this reason.
//
// 4096 and 2.0 are asserted as numbers. The bug was that a `<= 0` fallback
// produced a plausible-looking 8192 — twice the documented default — so
// "positive" and "non-zero" are both predicates the defect satisfies.
func TestBuildCrewRuntimeConfig_ResolvesStoredSizeSentinel(t *testing.T) {
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	crewID := seedCrewRow(t, db, "crew-sentinel", wsID, "Sentinel", "sentinel")

	// Exactly what the pre-#1641 update path wrote, and what a restore of a
	// bundle from that era puts back.
	if _, err := db.Exec(`UPDATE crews SET container_memory_mb = 0, container_cpus = 0 WHERE id = ?`, crewID); err != nil {
		t.Fatalf("write sentinel row: %v", err)
	}

	cfg, err := buildCrewRuntimeConfig(context.Background(), db, crewID, wsID)
	if err != nil {
		t.Fatalf("buildCrewRuntimeConfig: %v", err)
	}
	if cfg.MemoryMB != 4096 {
		t.Errorf("MemoryMB = %d, want 4096 — a 0 reaches the docker provider's own `<= 0` fallback of 8192, twice what the create path and the docs promise", cfg.MemoryMB)
	}
	if cfg.CPUs != 2.0 {
		t.Errorf("CPUs = %g, want 2", cfg.CPUs)
	}
}

// The negative half: a configured size must survive the resolution untouched,
// or the fix would quietly resize every crew on the instance.
func TestBuildCrewRuntimeConfig_KeepsConfiguredSize(t *testing.T) {
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	crewID := seedCrewRow(t, db, "crew-sized", wsID, "Sized", "sized")

	if _, err := db.Exec(`UPDATE crews SET container_memory_mb = 2048, container_cpus = 1.5 WHERE id = ?`, crewID); err != nil {
		t.Fatalf("size crew: %v", err)
	}

	cfg, err := buildCrewRuntimeConfig(context.Background(), db, crewID, wsID)
	if err != nil {
		t.Fatalf("buildCrewRuntimeConfig: %v", err)
	}
	if cfg.MemoryMB != 2048 || cfg.CPUs != 1.5 {
		t.Errorf("MemoryMB/CPUs = %d/%g, want 2048/1.5", cfg.MemoryMB, cfg.CPUs)
	}
}

// The same sentinel reaches the agent-config path, which is what every
// assignment-driven run resolves its container size through. It had the
// defaults right for a NULL column (4096 / 2.0) and wrong for a stored 0 —
// `Valid` is true for a 0, so the value passed straight through.
func TestResolveContainerResources_ResolvesStoredSizeSentinel(t *testing.T) {
	h := &InternalHandler{}

	t.Run("stored sentinel", func(t *testing.T) {
		mem, cpus, _ := h.resolveContainerResources(&agentConfigData{
			crewMemoryMB: sql.NullInt64{Int64: 0, Valid: true},
			crewCPUs:     sql.NullFloat64{Float64: 0, Valid: true},
		})
		if mem != 4096 {
			t.Errorf("memoryMB = %d, want 4096", mem)
		}
		if cpus != 2.0 {
			t.Errorf("cpus = %g, want 2", cpus)
		}
	})

	t.Run("configured size survives", func(t *testing.T) {
		mem, cpus, _ := h.resolveContainerResources(&agentConfigData{
			crewMemoryMB: sql.NullInt64{Int64: 2048, Valid: true},
			crewCPUs:     sql.NullFloat64{Float64: 1.5, Valid: true},
		})
		if mem != 2048 || cpus != 1.5 {
			t.Errorf("memoryMB/cpus = %d/%g, want 2048/1.5", mem, cpus)
		}
	})

	t.Run("null column keeps the default", func(t *testing.T) {
		mem, cpus, _ := h.resolveContainerResources(&agentConfigData{})
		if mem != 4096 || cpus != 2.0 {
			t.Errorf("memoryMB/cpus = %d/%g, want 4096/2", mem, cpus)
		}
	})
}
