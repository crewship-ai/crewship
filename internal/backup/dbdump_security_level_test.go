package backup

// Restore is the one writer of credentials.security_level that never ran the
// value past keeper.SecurityLevel.Valid() (#1603). Every other writer does:
// POST/PATCH gate in internal/api/credentials_mutate.go, both CLI paths, and
// the agent-proposed-credential INSERT which hardcodes L1. RestoreDump
// whitelists column *names* against the target schema — names, not values — so
// a bundle could land any integer at all in a column that has no CHECK
// constraint behind it.
//
// These tests pin the two halves of the fix:
//
//  1. a level outside the tier table never reaches the database;
//  2. a level inside it is never touched — a restore that quietly re-tiers a
//     credential the admin deliberately marked L3 would be its own bug.

import (
	"context"
	"database/sql"
	"fmt"
	"testing"

	"github.com/crewship-ai/crewship/internal/keeper"

	_ "modernc.org/sqlite"
)

// credBundleRow builds one credentials row as it would appear inside a
// bundle's dump.json. level is deliberately `any`: JSON decoding yields
// float64, a dump taken straight from the collector yields int64, and a
// tampered bundle can carry anything at all.
func credBundleRow(id, wsID, userID string, level any) map[string]any {
	return map[string]any{
		"id":              id,
		"workspace_id":    wsID,
		"name":            id,
		"type":            "SECRET",
		"provider":        "NONE",
		"scope":           "WORKSPACE",
		"status":          "ACTIVE",
		"encrypted_value": "v1:ciphertext",
		"created_by":      userID,
		"security_level":  level,
	}
}

// storedSecurityLevel reads the level back as the database actually holds it.
// sql.NullInt64 rather than int so a NULL is a distinguishable failure instead
// of a silent zero.
func storedSecurityLevel(t *testing.T, db *sql.DB, credID string) int {
	t.Helper()
	var raw sql.NullInt64
	err := db.QueryRowContext(context.Background(),
		`SELECT security_level FROM credentials WHERE id = ?`, credID).Scan(&raw)
	if err != nil {
		// A dropped row is a failure mode of its own: an admin who restores a
		// bundle and silently loses a credential is worse off than one who
		// gets it back at the strictest tier with a warning.
		t.Fatalf("credential %s did not survive the restore: %v", credID, err)
	}
	if !raw.Valid {
		t.Fatalf("credential %s stored a NULL security_level", credID)
	}
	return int(raw.Int64)
}

func TestRestoreDump_SecurityLevelOutsideTierTableIsClamped(t *testing.T) {
	ctx := context.Background()
	db := openMigratedDBCov(t)
	wsID, _ := seedCovWorkspace(t, db, "seclvl")
	userID := "u_cov_seclvl"

	levels := keeper.SecurityLevels()
	strictest := int(levels[len(levels)-1])

	cases := []struct {
		id      string
		bundle  any
		want    int
		clamped bool
		why     string
	}{
		{"cred_l1", float64(1), 1, false, "a defined tier passes through untouched"},
		{"cred_l3", float64(3), 3, false, "…including one an admin set deliberately"},
		{"cred_int64", int64(2), 2, false, "collector dumps carry int64, not float64"},
		{"cred_text", "4", 4, false, "SQLite's INTEGER affinity would coerce this anyway"},
		{"cred_zero", float64(0), strictest, true, "0 is below every tier"},
		{"cred_negative", float64(-1), strictest, true, "negative is below every tier"},
		{"cred_future", float64(99), strictest, true, "above every tier — a future or corrupt value"},
		{"cred_fractional", float64(2.5), strictest, true, "not an integer, so not a tier"},
		{"cred_garbage", "not-a-level", strictest, true, "unparseable"},
		{"cred_null", nil, strictest, true, "NOT NULL would make INSERT OR IGNORE drop the whole row"},
	}

	rows := make([]map[string]any, 0, len(cases))
	for _, c := range cases {
		rows = append(rows, credBundleRow(c.id, wsID, userID, c.bundle))
	}

	stats, err := RestoreDumpTxHooks(ctx, db, &DBDump{
		WorkspaceID: wsID,
		Tables:      map[string][]map[string]any{"credentials": rows},
	}, nil)
	if err != nil {
		t.Fatalf("restore: %v", err)
	}

	wantClamped := 0
	for _, c := range cases {
		if c.clamped {
			wantClamped++
		}
	}
	if stats.SecurityLevelClamped != wantClamped {
		t.Errorf("stats.SecurityLevelClamped = %d, want %d — the admin has to be told, "+
			"a silent re-tier is exactly what this fix is not allowed to be",
			stats.SecurityLevelClamped, wantClamped)
	}
	if len(stats.SecurityLevelClamps) != wantClamped {
		t.Errorf("stats.SecurityLevelClamps = %d entries, want %d", len(stats.SecurityLevelClamps), wantClamped)
	}
	for _, c := range stats.SecurityLevelClamps {
		if c.CredentialID == "" {
			t.Errorf("clamp record without a credential id: %+v", c)
		}
		if c.To != strictest {
			t.Errorf("clamp %s landed at %d, want the strictest tier %d", c.CredentialID, c.To, strictest)
		}
		if c.From == "" {
			t.Errorf("clamp %s does not record what the bundle carried", c.CredentialID)
		}
	}

	for _, c := range cases {
		got := storedSecurityLevel(t, db, c.id)
		if got != c.want {
			t.Errorf("%s (%s): stored security_level = %d, want %d", c.id, c.why, got, c.want)
		}
		if !keeper.SecurityLevel(got).Valid() {
			t.Errorf("%s: stored level %d is outside the tier table — restore must never land one", c.id, got)
		}
	}
}

// A row that omits the column entirely is schema skew (a bundle taken before
// security_level existed), not a tampered value. The column stays out of the
// INSERT so the DB DEFAULT applies, and nothing is reported as clamped.
func TestRestoreDump_SecurityLevelAbsentFromBundleUsesSchemaDefault(t *testing.T) {
	ctx := context.Background()
	db := openMigratedDBCov(t)
	wsID, _ := seedCovWorkspace(t, db, "seclvlabsent")

	row := credBundleRow("cred_absent", wsID, "u_cov_seclvlabsent", nil)
	delete(row, "security_level")

	stats, err := RestoreDumpTxHooks(ctx, db, &DBDump{
		WorkspaceID: wsID,
		Tables:      map[string][]map[string]any{"credentials": {row}},
	}, nil)
	if err != nil {
		t.Fatalf("restore: %v", err)
	}
	if stats.SecurityLevelClamped != 0 {
		t.Errorf("SecurityLevelClamped = %d, want 0 for an absent column", stats.SecurityLevelClamped)
	}
	if got := storedSecurityLevel(t, db, "cred_absent"); !keeper.SecurityLevel(got).Valid() {
		t.Errorf("schema default %d is not a defined tier", got)
	}
}

// Derived from SecurityLevels() rather than a literal 1..4 so a fifth tier
// added to the table is covered the day it lands.
func TestRestoreDump_EveryDefinedTierSurvivesRestore(t *testing.T) {
	ctx := context.Background()
	db := openMigratedDBCov(t)
	wsID, _ := seedCovWorkspace(t, db, "seclvlall")
	userID := "u_cov_seclvlall"

	rows := make([]map[string]any, 0, len(keeper.SecurityLevels()))
	for _, l := range keeper.SecurityLevels() {
		rows = append(rows, credBundleRow(fmt.Sprintf("cred_tier_%d", int(l)), wsID, userID, float64(l)))
	}
	stats, err := RestoreDumpTxHooks(ctx, db, &DBDump{
		WorkspaceID: wsID,
		Tables:      map[string][]map[string]any{"credentials": rows},
	}, nil)
	if err != nil {
		t.Fatalf("restore: %v", err)
	}
	if stats.SecurityLevelClamped != 0 {
		t.Fatalf("SecurityLevelClamped = %d, want 0 — no defined tier may be rewritten", stats.SecurityLevelClamped)
	}
	for _, l := range keeper.SecurityLevels() {
		id := fmt.Sprintf("cred_tier_%d", int(l))
		if got := storedSecurityLevel(t, db, id); got != int(l) {
			t.Errorf("%s: stored %d, want %d", id, got, int(l))
		}
	}
}

// InspectSecurityLevels is what the --dry-run path reports from: a dry run
// documents what WOULD happen, and "this bundle carries three credentials at a
// level that does not exist" is the single most useful thing it can tell an
// admin before they commit to the restore.
func TestInspectSecurityLevels(t *testing.T) {
	dump := &DBDump{Tables: map[string][]map[string]any{
		"credentials": {
			credBundleRow("ok", "ws", "u", float64(2)),
			credBundleRow("bad_high", "ws", "u", float64(7)),
			credBundleRow("bad_zero", "ws", "u", float64(0)),
		},
		// A different table that happens to carry the same column name must be
		// left entirely alone — this guard is only about credentials.
		"agents": {{"id": "a1", "security_level": float64(9)}},
	}}

	clamps, total := InspectSecurityLevels(dump)
	if total != 2 {
		t.Fatalf("total = %d, want 2", total)
	}
	if len(clamps) != 2 {
		t.Fatalf("clamps = %d, want 2", len(clamps))
	}
	seen := map[string]bool{}
	for _, c := range clamps {
		seen[c.CredentialID] = true
	}
	if !seen["bad_high"] || !seen["bad_zero"] {
		t.Errorf("clamps = %+v, want bad_high and bad_zero", clamps)
	}

	if got, total := InspectSecurityLevels(nil); total != 0 || got != nil {
		t.Errorf("nil dump = (%v, %d), want (nil, 0)", got, total)
	}
}
