package api

// Coverage for the container_ttl_hours default (#1662).
//
// crews.container_ttl_hours is nullable with no DEFAULT — the next line of the
// same migration gives container_memory_mb a NOT NULL DEFAULT 4096. Create
// stored the field only when > 0, agent_config yielded 0 for NULL, and the
// reaper skips 0. Out of the box no crew container was ever stopped; dev1 had
// three that had been running for days with zero agent runs.
//
// The sentinel here deliberately does NOT match the one memory_mb and cpus
// use, and these tests pin the difference. For a size, 0 is physically
// meaningless, so it can safely mean "reset to the server default". For a TTL,
// 0 is a value the product already publishes — `crewship crew get` prints
// "TTL: Never stop" for it and checkTTLs has always skipped it. Repurposing it
// would silently convert every crew an operator deliberately pinned to
// never-stop into a 4-hour auto-stop. NULL, which no API request can produce
// and which means "never configured", is the only safe carrier for the default.

import (
	"context"
	"database/sql"
	"net/http"
	"testing"
)

func ttlPtr(v int) *int { return &v }

func TestResolveCrewContainerTTLHours(t *testing.T) {
	cases := []struct {
		name   string
		stored *int
		want   int
	}{
		{"NULL means the server default", nil, defaultCrewContainerTTLHours},
		{"explicit 0 means never stop", ttlPtr(0), 0},
		{"explicit value is kept", ttlPtr(12), 12},
		{"a negative that got past validation is treated as never stop", ttlPtr(-3), 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := resolveCrewContainerTTLHours(tc.stored); got != tc.want {
				t.Errorf("resolveCrewContainerTTLHours(%v) = %d, want %d", tc.stored, got, tc.want)
			}
		})
	}
}

func TestDefaultCrewContainerTTLHours_IsAPositiveNumberOfHours(t *testing.T) {
	// The whole point of #1662: shipping 0 here would restore the bug.
	if defaultCrewContainerTTLHours <= 0 {
		t.Fatalf("defaultCrewContainerTTLHours = %d; a non-positive default means no crew container is ever stopped",
			defaultCrewContainerTTLHours)
	}
	// A default measured in days is indistinguishable from no default for the
	// fleet this is designed for (20-50 crews, a handful active).
	if defaultCrewContainerTTLHours > 24 {
		t.Errorf("defaultCrewContainerTTLHours = %d; want a same-day value", defaultCrewContainerTTLHours)
	}
}

// ---------------------------------------------------------------------------
// Create
// ---------------------------------------------------------------------------

func TestTTLCreate_OmittedLeavesNullSoTheDefaultStaysServerSide(t *testing.T) {
	// The default is resolved on read, not written into the row. Writing it
	// would freeze every existing crew at whatever the default was on the day
	// it was created, and a later change to the default could only reach them
	// through a backfill that cannot tell a stamped default from a deliberate
	// choice.
	h, db, userID, wsID := covCruNewCrew(t)
	rr := covCruDoCreate(h, userID, wsID, "OWNER", `{"name":"Ops","slug":"ops"}`)
	if rr.Code != http.StatusCreated {
		t.Fatalf("create = %d, want 201, body: %s", rr.Code, rr.Body.String())
	}

	var ttl sql.NullInt64
	if err := db.QueryRow(`SELECT container_ttl_hours FROM crews WHERE slug = 'ops'`).Scan(&ttl); err != nil {
		t.Fatalf("read crew: %v", err)
	}
	if ttl.Valid {
		t.Errorf("container_ttl_hours = %d, want NULL", ttl.Int64)
	}

	var stored *int
	if ttl.Valid {
		v := int(ttl.Int64)
		stored = &v
	}
	if got := resolveCrewContainerTTLHours(stored); got != defaultCrewContainerTTLHours {
		t.Errorf("effective TTL for an omitted field = %d, want %d", got, defaultCrewContainerTTLHours)
	}
}

func TestTTLCreate_ExplicitZeroIsStoredAsNeverStop(t *testing.T) {
	// Create dropped an explicit 0 on the floor (`> 0` guard), so it became
	// NULL — which now means "the default". An operator asking for never-stop
	// would have got a 4-hour auto-stop.
	h, db, userID, wsID := covCruNewCrew(t)
	rr := covCruDoCreate(h, userID, wsID, "OWNER", `{"name":"Ops","slug":"ops","container_ttl_hours":0}`)
	if rr.Code != http.StatusCreated {
		t.Fatalf("create = %d, want 201, body: %s", rr.Code, rr.Body.String())
	}

	var ttl sql.NullInt64
	if err := db.QueryRow(`SELECT container_ttl_hours FROM crews WHERE slug = 'ops'`).Scan(&ttl); err != nil {
		t.Fatalf("read crew: %v", err)
	}
	if !ttl.Valid || ttl.Int64 != 0 {
		t.Fatalf("container_ttl_hours = %v (valid=%v), want a stored 0", ttl.Int64, ttl.Valid)
	}
	v := int(ttl.Int64)
	if got := resolveCrewContainerTTLHours(&v); got != 0 {
		t.Errorf("effective TTL for a stored 0 = %d, want 0 (never stop)", got)
	}
}

func TestTTLCreate_NegativeRejected(t *testing.T) {
	// Update has returned a 400 for this since it was written; Create silently
	// dropped it via the `> 0` guard, so the same body produced a 201 with a
	// different meaning depending on which verb you used.
	h, _, userID, wsID := covCruNewCrew(t)
	rr := covCruDoCreate(h, userID, wsID, "OWNER", `{"name":"Ops","slug":"ops","container_ttl_hours":-1}`)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("create with a negative TTL = %d, want 400, body: %s", rr.Code, rr.Body.String())
	}
}

func TestTTLCreate_PositiveIsStoredVerbatim(t *testing.T) {
	h, db, userID, wsID := covCruNewCrew(t)
	rr := covCruDoCreate(h, userID, wsID, "OWNER", `{"name":"Ops","slug":"ops","container_ttl_hours":9}`)
	if rr.Code != http.StatusCreated {
		t.Fatalf("create = %d, want 201, body: %s", rr.Code, rr.Body.String())
	}
	var ttl sql.NullInt64
	if err := db.QueryRow(`SELECT container_ttl_hours FROM crews WHERE slug = 'ops'`).Scan(&ttl); err != nil {
		t.Fatalf("read crew: %v", err)
	}
	if !ttl.Valid || ttl.Int64 != 9 {
		t.Errorf("container_ttl_hours = %v (valid=%v), want 9", ttl.Int64, ttl.Valid)
	}
}

// ---------------------------------------------------------------------------
// The run path — agent_config resolves the crew row into the run's TTL.
// ---------------------------------------------------------------------------

func TestTTLResolveContainerResources_NullYieldsTheDefaultNotZero(t *testing.T) {
	// agent_config.go yielded 0 for a NULL column, which the reaper reads as
	// "never stop". This is the line that made the missing DEFAULT visible on
	// every single run.
	h := &InternalHandler{}
	_, _, ttl := h.resolveContainerResources(&agentConfigData{})
	if ttl != defaultCrewContainerTTLHours {
		t.Errorf("ttl for a NULL column = %d, want %d", ttl, defaultCrewContainerTTLHours)
	}
}

func TestTTLResolveContainerResources_StoredZeroStaysNeverStop(t *testing.T) {
	h := &InternalHandler{}
	_, _, ttl := h.resolveContainerResources(&agentConfigData{
		crewTTLHours: sql.NullInt64{Int64: 0, Valid: true},
	})
	if ttl != 0 {
		t.Errorf("ttl for a stored 0 = %d, want 0", ttl)
	}
}

// ---------------------------------------------------------------------------
// Defect 3 — CrewConfig.TTLHours, whose doc comment describes behaviour that
// nothing implemented.
// ---------------------------------------------------------------------------

func TestBuildCrewRuntimeConfig_TTLHoursCarriesTheEffectiveValue(t *testing.T) {
	// This field is how the two wake paths that never reach RunAgent (script
	// steps and prewarm) learn what TTL to register. Handing them the raw 0
	// from a NULL column would register "never stop" for exactly the crews
	// that have never been configured.
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	crewID := seedCrewRow(t, db, "crew-ttl", wsID, "TTL", "ttl")

	cfg, err := buildCrewRuntimeConfig(context.Background(), db, crewID, wsID)
	if err != nil {
		t.Fatalf("buildCrewRuntimeConfig: %v", err)
	}
	if cfg.TTLHours != defaultCrewContainerTTLHours {
		t.Errorf("cfg.TTLHours for a NULL column = %d, want %d", cfg.TTLHours, defaultCrewContainerTTLHours)
	}

	if _, err := db.Exec(`UPDATE crews SET container_ttl_hours = 0 WHERE id = ?`, crewID); err != nil {
		t.Fatalf("set ttl 0: %v", err)
	}
	cfg, err = buildCrewRuntimeConfig(context.Background(), db, crewID, wsID)
	if err != nil {
		t.Fatalf("buildCrewRuntimeConfig: %v", err)
	}
	if cfg.TTLHours != 0 {
		t.Errorf("cfg.TTLHours for a stored 0 = %d, want 0 (never stop)", cfg.TTLHours)
	}
}
