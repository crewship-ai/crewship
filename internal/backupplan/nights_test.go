package backupplan

import (
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/backup"
)

// A night whose backup was created but recorded gaps is "incomplete", never
// the green "ok" of a complete backup (review: nights strip).
func TestBuildNights_IncompleteIsNotOK(t *testing.T) {
	now := time.Date(2026, 9, 30, 10, 0, 0, 0, time.UTC)
	due := time.Date(2026, 9, 30, 3, 0, 0, 0, time.UTC)
	gap := []backup.IncompleteItem{{Kind: backup.IncompleteAttachmentMissing, Count: 2}}
	cases := []struct {
		name    string
		runs    []*Run
		bundles []backup.CatalogEntry
		want    string
	}{
		{
			name: "a done run is ok",
			runs: []*Run{{Status: StatusDone, DueAt: &due, StartedAt: due, BundlePath: "/b/a"}},
			want: "ok",
		},
		{
			name: "an incomplete run is incomplete",
			runs: []*Run{{Status: StatusIncomplete, DueAt: &due, StartedAt: due, BundlePath: "/b/a", Incomplete: gap}},
			want: "incomplete",
		},
		{
			name: "one incomplete workspace of two makes the night incomplete",
			runs: []*Run{
				{Status: StatusDone, DueAt: &due, StartedAt: due, WorkspaceID: "a", BundlePath: "/b/a"},
				{Status: StatusIncomplete, DueAt: &due, StartedAt: due, WorkspaceID: "b", BundlePath: "/b/b", Incomplete: gap},
			},
			want: "incomplete",
		},
		{
			name: "an incomplete catch-up is incomplete, not merely late",
			runs: []*Run{{Status: StatusIncomplete, Trigger: TriggerCatchup, DueAt: &due, StartedAt: due, Incomplete: gap}},
			want: "incomplete",
		},
		{
			name:    "a manual bundle that recorded gaps is incomplete",
			bundles: []backup.CatalogEntry{{FilePath: "/b/m", CreatedAt: due, Incomplete: gap}},
			want:    "incomplete",
		},
		{
			name:    "a bundle with no recorded gaps list is ok",
			bundles: []backup.CatalogEntry{{FilePath: "/b/m", CreatedAt: due}},
			want:    "ok",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			nights := buildNights(tc.runs, tc.bundles, now, time.UTC)
			if got := nights[13]; got.Status != tc.want {
				t.Fatalf("night %s status = %q, want %q", got.Date, got.Status, tc.want)
			}
		})
	}
}
