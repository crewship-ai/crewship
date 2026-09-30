package backup

import (
	"context"
	"os"
	"sort"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/testutil"
)

// The UI promises "Always keep N most recent bundles". Rotate used to drop a
// bundle when it was beyond keepLast OR older than keepDays, so a workspace
// whose backups had stopped for longer than keepDays lost EVERY copy on the
// next rotation — the one moment the old copies were all that was left.
func TestRotate_AgeNeverDeletesTheFloor(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	now := time.Now().UTC()
	var paths []string
	for i := 0; i < 4; i++ {
		// All four are older than the 7-day window.
		paths = append(paths, writeBundleFile(t, dir, covManifestForWorkspace("ws_floor", now.AddDate(0, 0, -30-i)), "p"))
	}
	deleted, err := Rotate(ctx, dir, "ws_floor", 3, 7, false)
	if err != nil {
		t.Fatalf("Rotate: %v", err)
	}
	if len(deleted) != 1 || deleted[0] != paths[3] {
		t.Fatalf("deleted = %v, want only the oldest (%s): the newest 3 are the floor age never touches", deleted, paths[3])
	}
	for _, p := range paths[:3] {
		if _, err := os.Stat(p); err != nil {
			t.Errorf("floor bundle %s was deleted", p)
		}
	}
}

// An age-only rule (keepLast 0) still never deletes the last copy.
func TestRotate_AgeOnlyKeepsTheNewestCopy(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	now := time.Now().UTC()
	newest := writeBundleFile(t, dir, covManifestForWorkspace("ws_age", now.AddDate(0, 0, -40)), "p")
	older := writeBundleFile(t, dir, covManifestForWorkspace("ws_age", now.AddDate(0, 0, -50)), "p")
	deleted, err := Rotate(ctx, dir, "ws_age", 0, 7, true)
	if err != nil {
		t.Fatalf("Rotate: %v", err)
	}
	if len(deleted) != 1 || deleted[0] != older {
		t.Fatalf("deleted = %v, want only %s (never %s, the last copy)", deleted, older, newest)
	}
}

func day(n int) time.Time {
	return time.Date(2026, 9, 30, 3, 0, 0, 0, time.UTC).AddDate(0, 0, -n)
}

func cand(path string, at time.Time) RetentionCandidate {
	return RetentionCandidate{Path: path, Scope: ScopeWorkspace, WorkspaceID: "ws", CreatedAt: at, ProofLevel: ProofChecksum}
}

// withProof sets what the catalog knows about a bundle: its proof level,
// its last drill result and whether it recorded gaps (complete=false means
// it did).
func withProof(c RetentionCandidate, proof int, drill string, complete bool) RetentionCandidate {
	c.ProofLevel = proof
	c.DrillResult = drill
	c.Complete = complete
	c.Incomplete = !complete
	return c
}

func paths(cs []RetentionCandidate) []string {
	out := make([]string, 0, len(cs))
	for _, c := range cs {
		out = append(out, c.Path)
	}
	sort.Strings(out)
	return out
}

func TestPlanRetention(t *testing.T) {
	now := day(0).Add(2 * time.Hour)
	tests := []struct {
		name   string
		cands  []RetentionCandidate
		policy RetentionPolicy
		drop   []string
	}{
		{
			name:   "floor survives any age",
			cands:  []RetentionCandidate{cand("a", day(100)), cand("b", day(101)), cand("c", day(102))},
			policy: RetentionPolicy{KeepMin: 2},
			drop:   []string{"c"},
		},
		{
			name:   "a zero floor still keeps the newest",
			cands:  []RetentionCandidate{cand("a", day(100)), cand("b", day(101))},
			policy: RetentionPolicy{KeepMin: 0, Daily: 7},
			drop:   []string{"b"},
		},
		{
			name: "daily keeps the newest per day inside the window",
			cands: []RetentionCandidate{
				cand("d0-late", day(0).Add(time.Hour)), cand("d0-early", day(0)),
				cand("d1", day(1)), cand("d2", day(2)), cand("d9", day(9)),
			},
			policy: RetentionPolicy{KeepMin: 1, Daily: 3},
			drop:   []string{"d0-early", "d9"},
		},
		{
			name: "weekly keeps one per ISO week, monthly one per month",
			cands: []RetentionCandidate{
				cand("w0", day(0)), cand("w1a", day(7)), cand("w1b", day(8)),
				cand("m1", day(35)), cand("m1b", day(40)), cand("m5", day(160)),
			},
			policy: RetentionPolicy{KeepMin: 1, Weekly: 2, Monthly: 3},
			drop:   []string{"m1b", "m5", "w1b"},
		},
		{
			name: "pinned is never deleted",
			cands: []RetentionCandidate{
				cand("new", day(0)),
				func() RetentionCandidate { c := cand("pinned", day(400)); c.Pinned = true; return c }(),
				cand("old", day(300)),
			},
			policy: RetentionPolicy{KeepMin: 1},
			drop:   []string{"old"},
		},
		{
			name: "the floor also protects the newest checked bundles",
			cands: []RetentionCandidate{
				cand("new", day(0)), cand("new2", day(1)),
				func() RetentionCandidate { c := cand("checked", day(20)); c.ProofLevel = ProofContents; return c }(),
				cand("old", day(30)),
			},
			policy: RetentionPolicy{KeepMin: 1},
			drop:   []string{"new2", "old"},
		},
		{
			name: "groups never push each other out",
			cands: []RetentionCandidate{
				cand("ws-new", day(0)), cand("ws-old", day(50)),
				{Path: "crew-a", Scope: ScopeCrew, WorkspaceID: "ws", CrewID: "a", CreatedAt: day(60)},
				{Path: "crew-b", Scope: ScopeCrew, WorkspaceID: "ws", CrewID: "b", CreatedAt: day(70)},
				{Path: "plan", Scope: ScopeWorkspace, WorkspaceID: "ws", PlanID: "p1", CreatedAt: day(80)},
			},
			policy: RetentionPolicy{KeepMin: 1},
			drop:   []string{"ws-old"},
		},
		{
			// Review B4: a newer partial bundle that went through a test
			// restore must not push out the last complete bundle whose
			// test restore succeeded.
			name: "a partial restore never displaces the last complete successful restore",
			cands: []RetentionCandidate{
				withProof(cand("partial", day(0)), ProofRestore, "partial", false),
				withProof(cand("restored", day(90)), ProofRestore, "ok", true),
				withProof(cand("older", day(120)), ProofChecksum, "", true),
			},
			policy: RetentionPolicy{KeepMin: 1},
			drop:   []string{"older"},
		},
		{
			name: "incomplete checked bundles do not fill the checked floor",
			cands: []RetentionCandidate{
				withProof(cand("new", day(0)), ProofChecksum, "", true),
				withProof(cand("gappy-checked", day(1)), ProofContents, "", false),
				withProof(cand("complete-checked", day(2)), ProofContents, "", true),
				cand("old", day(3)),
			},
			policy: RetentionPolicy{KeepMin: 1},
			drop:   []string{"gappy-checked", "old"},
		},
		{
			name: "the newest complete checked bundle outlives newer incomplete ones",
			cands: []RetentionCandidate{
				withProof(cand("gap1", day(0)), ProofContents, "", false),
				withProof(cand("gap2", day(1)), ProofContents, "", false),
				withProof(cand("gap3", day(2)), ProofRestore, "partial", false),
				withProof(cand("complete-checked", day(40)), ProofContents, "", true),
				withProof(cand("complete-older", day(50)), ProofContents, "", true),
			},
			policy: RetentionPolicy{KeepMin: 1},
			drop:   []string{"complete-older", "gap2", "gap3"},
		},
		{
			name: "the newest complete successful restore survives a full checked floor",
			cands: []RetentionCandidate{
				withProof(cand("checked-new", day(0)), ProofContents, "", true),
				withProof(cand("checked-2", day(1)), ProofContents, "", true),
				withProof(cand("restored", day(30)), ProofRestore, "ok", true),
				withProof(cand("restored-older", day(60)), ProofRestore, "ok", true),
			},
			policy: RetentionPolicy{KeepMin: 1},
			drop:   []string{"checked-2", "restored-older"},
		},
		{
			name: "a failed or partial drill does not protect a complete bundle",
			cands: []RetentionCandidate{
				withProof(cand("new", day(0)), ProofChecksum, "", true),
				withProof(cand("drill-failed", day(10)), ProofChecksum, "failed", true),
			},
			policy: RetentionPolicy{KeepMin: 1},
			drop:   []string{"drill-failed"},
		},
		{
			name:   "max age keeps everything younger beyond the floor",
			cands:  []RetentionCandidate{cand("a", day(0)), cand("b", day(3)), cand("c", day(10))},
			policy: RetentionPolicy{KeepMin: 1, MaxAgeDays: 7},
			drop:   []string{"c"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, drop := PlanRetention(tt.cands, tt.policy, now)
			got := paths(drop)
			want := append([]string(nil), tt.drop...)
			sort.Strings(want)
			if len(got) != len(want) {
				t.Fatalf("drop = %v, want %v", got, want)
			}
			for i := range got {
				if got[i] != want[i] {
					t.Fatalf("drop = %v, want %v", got, want)
				}
			}
		})
	}
}

// Rotation through the API path reads pins from the catalog.
func TestRotateWithPolicy_PinnedBundleSurvives(t *testing.T) {
	ctx := context.Background()
	db := testutil.MigratedSQLDB(t)
	dir := t.TempDir()
	now := time.Now().UTC()
	newest := writeBundleFile(t, dir, covManifestForWorkspace("ws_pin", now.Add(-time.Hour)), "p")
	pinned := writeBundleFile(t, dir, covManifestForWorkspace("ws_pin", now.AddDate(0, 0, -200)), "p")
	old := writeBundleFile(t, dir, covManifestForWorkspace("ws_pin", now.AddDate(0, 0, -100)), "p")
	for _, p := range []string{newest, pinned, old} {
		if err := UpsertCatalogEntry(ctx, db, CatalogEntry{FilePath: p, Scope: "workspace", WorkspaceID: "ws_pin", CreatedAt: now, SHA256: p}); err != nil {
			t.Fatal(err)
		}
	}
	if err := PinCatalogEntry(ctx, db, pinned); err != nil {
		t.Fatal(err)
	}
	deleted, err := RotateWithPolicy(ctx, db, dir, "ws_pin", RetentionPolicy{KeepMin: 1}, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(deleted) != 1 || deleted[0] != old {
		t.Fatalf("deleted = %v, want only %s", deleted, old)
	}
	if _, err := os.Stat(pinned); err != nil {
		t.Fatalf("pinned bundle deleted: %v", err)
	}
}

// A plan's keep rules apply to its own bundles only: a manual bundle and
// another plan's bundles in the same workspace are never its to delete.
func TestRotatePlanWithPolicy_OnlyTouchesThePlansBundles(t *testing.T) {
	ctx := context.Background()
	db := testutil.MigratedSQLDB(t)
	dir := t.TempDir()
	now := time.Now().UTC()
	planNew := writeBundleFile(t, dir, covManifestForWorkspace("ws_plan", now.Add(-time.Hour)), "p")
	planOld := writeBundleFile(t, dir, covManifestForWorkspace("ws_plan", now.AddDate(0, 0, -100)), "p")
	manual := writeBundleFile(t, dir, covManifestForWorkspace("ws_plan", now.AddDate(0, 0, -150)), "p")
	other := writeBundleFile(t, dir, covManifestForWorkspace("ws_plan", now.AddDate(0, 0, -200)), "p")
	for p, plan := range map[string]string{planNew: "plan_a", planOld: "plan_a", manual: "", other: "plan_b"} {
		if err := UpsertCatalogEntry(ctx, db, CatalogEntry{FilePath: p, Scope: "workspace", WorkspaceID: "ws_plan", CreatedAt: now, SHA256: p, PlanID: plan}); err != nil {
			t.Fatal(err)
		}
	}
	deleted, err := RotatePlanWithPolicy(ctx, db, dir, "ws_plan", "plan_a", RetentionPolicy{KeepMin: 1}, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(deleted) != 1 || deleted[0] != planOld {
		t.Fatalf("deleted = %v, want only %s", deleted, planOld)
	}
	for _, keep := range []string{planNew, manual, other} {
		if _, err := os.Stat(keep); err != nil {
			t.Errorf("%s deleted: %v", keep, err)
		}
	}
	if _, err := RotatePlanWithPolicy(ctx, nil, dir, "ws_plan", "plan_a", RetentionPolicy{KeepMin: 1}, true); err == nil {
		t.Fatal("rotating a plan without the catalog must be refused")
	}
}

// Rotation after an incomplete run, through the catalog: a newer partial
// bundle whose test restore came back partial must not take the place of
// the last complete bundle whose test restore succeeded (review B4). The
// rotation actually deletes, so the surviving file is checked on disk too.
func TestRotatePlanKeepsLastCompleteSuccessfulRestore(t *testing.T) {
	ctx := context.Background()
	db := testutil.MigratedSQLDB(t)
	dir := t.TempDir()
	now := time.Now().UTC()
	old := writeBundleFile(t, dir, covManifestForWorkspace("review-ws", now.AddDate(0, 0, -90)), "old")
	recent := writeBundleFile(t, dir, covManifestForWorkspace("review-ws", now), "recent")
	for _, e := range []CatalogEntry{
		{FilePath: old, WorkspaceID: "review-ws", Scope: string(ScopeWorkspace), PlanID: "review-plan", CreatedAt: now.AddDate(0, 0, -90), ProofLevel: ProofRestore, DrillResult: "ok", Incomplete: []IncompleteItem{}},
		{FilePath: recent, WorkspaceID: "review-ws", Scope: string(ScopeWorkspace), PlanID: "review-plan", CreatedAt: now, ProofLevel: ProofRestore, DrillResult: "partial", Incomplete: []IncompleteItem{{Kind: IncompleteAttachmentMissing, Count: 1}}},
	} {
		if err := UpsertCatalogEntry(ctx, db, e); err != nil {
			t.Fatal(err)
		}
	}
	if err := SetCatalogDrill(ctx, db, old, "ok", nil, now.AddDate(0, 0, -90)); err != nil {
		t.Fatal(err)
	}
	if err := SetCatalogDrill(ctx, db, recent, "partial", nil, now); err != nil {
		t.Fatal(err)
	}
	for _, dry := range []bool{true, false} {
		drop, err := RotatePlanWithPolicy(ctx, db, dir, "review-ws", "review-plan", RetentionPolicy{KeepMin: 1}, dry)
		if err != nil {
			t.Fatal(err)
		}
		for _, p := range drop {
			if p == old {
				t.Fatalf("dry=%v: the last complete successful restore is dropped in favour of a partial bundle (drop %v)", dry, drop)
			}
		}
	}
	for _, keep := range []string{old, recent} {
		if _, err := os.Stat(keep); err != nil {
			t.Errorf("%s deleted: %v", keep, err)
		}
	}
}
