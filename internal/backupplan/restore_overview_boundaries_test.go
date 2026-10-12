package backupplan

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/backup"
)

func TestOverviewReportsActualRestoreDrillOutcome(t *testing.T) {
	h := newHarness(t, "2026-10-03T12:00:00Z")
	p := h.plan(nil)
	dir := t.TempDir()
	path := filepath.Join(dir, "owned-backup.tar.zst")
	if err := backup.UpsertCatalogEntry(t.Context(), h.db, backup.CatalogEntry{
		FilePath: path, Scope: "workspace", WorkspaceID: "ws_a", CreatedAt: h.clock.Now(),
		Size: 99, SHA256: "fixture", Encrypted: true, FormatVersion: backup.FormatVersion, PlanID: p.ID,
		Incomplete: []backup.IncompleteItem{},
	}); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct{ result, tone string }{{"failed", "bad"}, {"partial", "warn"}, {"ok", "ok"}} {
		t.Run(tc.result, func(t *testing.T) {
			if err := backup.SetCatalogDrill(t.Context(), h.db, path, tc.result, json.RawMessage(`{"note":"owned restore drill evidence"}`), h.clock.Now()); err != nil {
				t.Fatal(err)
			}
			got, err := BuildOverview(t.Context(), h.db, OverviewInput{Scope: ScopeWorkspaces, WorkspaceIDs: []string{"ws_a"}, BackupsDir: dir}, h.clock.Now())
			if err != nil {
				t.Fatal(err)
			}
			row := got.Status.ReallyRestored
			if row.DetailTone == nil || *row.DetailTone != tc.tone || row.Detail == nil || *row.Detail != "owned restore drill evidence" || !strings.Contains(row.Value, "Test restore "+tc.result) {
				t.Fatalf("restore outcome misrepresented: %+v", row)
			}
		})
	}
}

func TestCalendarRejectsInvalidRangesAndReportsUnavailableStorage(t *testing.T) {
	db := busyFixtureDB(t)
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	p := &Plan{Timezone: "UTC"}
	now := mustTime(t, "2026-10-03T12:00:00Z")
	for _, dates := range [][2]string{{"invalid", "2026-10-03"}, {"2026-10-03", "invalid"}, {"2026-10-03", "2026-10-02"}, {"2026-01-01", "2028-01-01"}} {
		if _, err := Calendar(t.Context(), db, p, dates[0], dates[1], now); !IsValidation(err) {
			t.Fatalf("invalid dates %v: %v", dates, err)
		}
	}
	if _, err := Calendar(t.Context(), db, p, "2026-10-01", "2026-10-03", now); err == nil || !strings.Contains(err.Error(), "closed") {
		t.Fatalf("storage failure hidden: %v", err)
	}
}
