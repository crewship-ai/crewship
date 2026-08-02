package api

import (
	"context"
	"database/sql"
	"testing"
	"time"
)

func setSetting(t *testing.T, db *sql.DB, key, value string) {
	t.Helper()
	_, err := db.ExecContext(context.Background(),
		`INSERT INTO app_settings (key, value, updated_at) VALUES (?, ?, datetime('now'))
		 ON CONFLICT(key) DO UPDATE SET value = excluded.value`, key, value)
	if err != nil {
		t.Fatalf("set %s: %v", key, err)
	}
}

// Out of the box the gate must be ON with numbers that mean something: room
// for one whole agent (runtime.agent_min_memory_mb) PLUS a reserve for the
// kernel, dockerd and page cache.
func TestAdmissionLimits_DefaultsAreAnAgentPlusAReserve(t *testing.T) {
	db := setupTestDB(t)
	lim := AdmissionLimits(context.Background(), db)

	if want := int64(defaultAgentMinMemoryMB + defaultHostMemoryReserveMB); lim.RequiredFreeMB != want {
		t.Errorf("RequiredFreeMB = %d, want %d (one agent + the host reserve)", lim.RequiredFreeMB, want)
	}
	if lim.MaxConcurrentStarts != defaultMaxConcurrentContainerStarts {
		t.Errorf("MaxConcurrentStarts = %d, want %d", lim.MaxConcurrentStarts, defaultMaxConcurrentContainerStarts)
	}
	if lim.MinStartInterval != defaultContainerStartStagger {
		t.Errorf("MinStartInterval = %v, want %v", lim.MinStartInterval, defaultContainerStartStagger)
	}
	if lim.MaxPressurePct != defaultHostMemoryPressurePct {
		t.Errorf("MaxPressurePct = %v, want %v", lim.MaxPressurePct, defaultHostMemoryPressurePct)
	}
}

// The threshold is deliberately NOT a fourth independent number. It composes
// with runtime.agent_min_memory_mb — the value that already answers "how much
// memory does one agent need" for the sizing advisory and for the per-crew
// concurrency budget. Raising that one value must move the host gate too, or
// the instance ends up admitting runs the crews it admits them into cannot
// hold.
func TestAdmissionLimits_MemoryFloorComposesWithAgentMinMemory(t *testing.T) {
	db := setupTestDB(t)
	setSetting(t, db, SettingAgentMinMemoryMB, "4096")
	setSetting(t, db, SettingHostMemoryReserveMB, "2048")

	lim := AdmissionLimits(context.Background(), db)
	if lim.RequiredFreeMB != 6144 {
		t.Fatalf("RequiredFreeMB = %d, want 6144 (4096 agent + 2048 reserve)", lim.RequiredFreeMB)
	}
}

// Every leg turns off independently, and 0 is how you turn it off.
func TestAdmissionLimits_ZeroDisablesEachLegIndependently(t *testing.T) {
	db := setupTestDB(t)
	setSetting(t, db, SettingHostMemoryReserveMB, "0")
	setSetting(t, db, SettingHostMemoryPressurePct, "0")
	setSetting(t, db, SettingMaxConcurrentContainerStarts, "0")
	setSetting(t, db, SettingContainerStartStaggerMs, "0")

	lim := AdmissionLimits(context.Background(), db)
	if lim.RequiredFreeMB != 0 {
		t.Errorf("RequiredFreeMB = %d, want 0 — a zero reserve means no host-memory gate, "+
			"not a gate with no headroom", lim.RequiredFreeMB)
	}
	if lim.MaxPressurePct != 0 {
		t.Errorf("MaxPressurePct = %v, want 0", lim.MaxPressurePct)
	}
	if lim.MaxConcurrentStarts != 0 {
		t.Errorf("MaxConcurrentStarts = %d, want 0", lim.MaxConcurrentStarts)
	}
	if lim.MinStartInterval != 0 {
		t.Errorf("MinStartInterval = %v, want 0", lim.MinStartInterval)
	}
}

// Same convention as agentMinMemoryMB: unusable input yields the compiled
// default rather than being clamped, because a clamped nonsense value is a
// gate silently running on a number nobody chose.
func TestAdmissionLimits_UnusableValuesFallBackToDefaults(t *testing.T) {
	for _, tc := range []struct {
		name  string
		key   string
		value string
	}{
		{"reserve not a number", SettingHostMemoryReserveMB, "lots"},
		{"reserve negative", SettingHostMemoryReserveMB, "-512"},
		{"reserve absurd", SettingHostMemoryReserveMB, "999999999"},
		{"pressure not a number", SettingHostMemoryPressurePct, "high"},
		{"pressure above 100", SettingHostMemoryPressurePct, "150"},
		{"pressure negative", SettingHostMemoryPressurePct, "-1"},
		{"starts not a number", SettingMaxConcurrentContainerStarts, "many"},
		{"starts negative", SettingMaxConcurrentContainerStarts, "-4"},
		{"starts absurd", SettingMaxConcurrentContainerStarts, "100000"},
		{"stagger not a number", SettingContainerStartStaggerMs, "soon"},
		{"stagger negative", SettingContainerStartStaggerMs, "-1"},
		{"stagger absurd", SettingContainerStartStaggerMs, "3600000"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db := setupTestDB(t)
			setSetting(t, db, tc.key, tc.value)
			lim := AdmissionLimits(context.Background(), db)

			def := AdmissionLimits(context.Background(), setupTestDB(t))
			if lim != def {
				t.Fatalf("%s=%q yielded %+v, want the defaults %+v", tc.key, tc.value, lim, def)
			}
		})
	}
}

// A settings read that cannot happen (no DB yet at provider construction,
// a broken connection) must not gate anything shut.
func TestAdmissionLimits_NilDB_YieldsCompiledDefaults(t *testing.T) {
	lim := AdmissionLimits(context.Background(), nil)
	if lim.RequiredFreeMB != int64(defaultAgentMinMemoryMB+defaultHostMemoryReserveMB) {
		t.Errorf("RequiredFreeMB = %d with a nil DB, want the compiled default", lim.RequiredFreeMB)
	}
	if lim.MinStartInterval != defaultContainerStartStagger {
		t.Errorf("MinStartInterval = %v with a nil DB, want the compiled default", lim.MinStartInterval)
	}
}

func TestAdmissionLimits_StaggerIsReadAsMilliseconds(t *testing.T) {
	db := setupTestDB(t)
	setSetting(t, db, SettingContainerStartStaggerMs, "500")
	if got := AdmissionLimits(context.Background(), db).MinStartInterval; got != 500*time.Millisecond {
		t.Fatalf("MinStartInterval = %v, want 500ms", got)
	}
}
