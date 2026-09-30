package backupplan

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/backup"
)

func TestCalendar_RunsAndPlanned(t *testing.T) {
	h := newHarness(t, "2026-09-26T12:00:00Z")
	p := h.plan(func(p *Plan) { p.EnvMode, p.EnvCadence = backup.EnvModeComplete, EnvWeekly })
	ctx := context.Background()
	day := func(s string) *time.Time { v := mustTime(t, s); return &v }
	for _, r := range []*Run{
		{PlanID: p.ID, Trigger: TriggerSchedule, Scope: ScopeWorkspaces, WorkspaceID: "ws_a", Status: StatusDone, DueAt: day("2026-09-27T03:00:00Z"), Environments: true, StartedAt: mustTime(t, "2026-09-27T03:00:00Z")},
		{PlanID: p.ID, Trigger: TriggerSchedule, Scope: ScopeWorkspaces, WorkspaceID: "ws_a", Status: StatusSkipped, DueAt: day("2026-09-28T03:00:00Z"), StartedAt: mustTime(t, "2026-09-28T03:00:00Z")},
		{PlanID: p.ID, Trigger: TriggerCatchup, Scope: ScopeWorkspaces, WorkspaceID: "ws_a", Status: StatusDone, DueAt: day("2026-09-29T03:00:00Z"), StartedAt: mustTime(t, "2026-09-29T09:00:00Z")},
		{PlanID: p.ID, Trigger: TriggerSchedule, Scope: ScopeWorkspaces, WorkspaceID: "ws_a", Status: StatusFailed, DueAt: day("2026-09-30T03:00:00Z"), StartedAt: mustTime(t, "2026-09-30T03:00:00Z")},
	} {
		if _, err := insertRun(ctx, h.db, r); err != nil {
			t.Fatal(err)
		}
	}
	days, err := Calendar(ctx, h.db, p, "2026-09-27", "2026-10-04", mustTime(t, "2026-09-30T12:00:00Z"))
	if err != nil {
		t.Fatal(err)
	}
	if len(days) != 8 {
		t.Fatalf("%d days, want 8", len(days))
	}
	got := map[string][]CalendarEntry{}
	for _, d := range days {
		got[d.Date] = d.Entries
	}
	want := map[string][]string{
		"2026-09-27": {"data:done", "environments:skipped"},
		"2026-09-28": {"data:skipped"},
		"2026-09-29": {"data:catchup"},
		"2026-09-30": {"data:failed"},
		"2026-10-01": {"data:planned"},
		"2026-10-04": {"data:planned", "environments:planned"},
	}
	for date, w := range want {
		var have []string
		for _, e := range got[date] {
			have = append(have, e.Kind+":"+e.Status)
		}
		if len(have) != len(w) {
			t.Fatalf("%s = %v, want %v", date, have, w)
		}
		for i := range w {
			if have[i] != w[i] {
				t.Fatalf("%s = %v, want %v", date, have, w)
			}
		}
	}
	if _, err := Calendar(ctx, h.db, p, "2026-10-04", "2026-09-27", time.Now()); err == nil || !IsValidation(err) {
		t.Fatalf("reversed range: %v", err)
	}
}

func TestNextRuns(t *testing.T) {
	p := Plan{Enabled: true, Cadence: CadenceDaily, TimeOfDay: "03:00", Timezone: "UTC", EnvMode: backup.EnvModeComplete, EnvCadence: EnvWeekly}
	got := NextRuns(&p, mustTime(t, "2026-10-02T12:00:00Z"), 3)
	if len(got) != 3 || got[0].At != "2026-10-03T03:00:00Z" || got[0].Environments || !got[1].Environments {
		t.Fatalf("next = %+v", got)
	}
	p.Enabled = false
	if len(NextRuns(&p, time.Now(), 3)) != 0 {
		t.Fatal("a disabled plan has no next runs")
	}
}

func TestRunView_JoinsTheCatalog(t *testing.T) {
	h := newHarness(t, "2026-09-30T12:00:00Z")
	ctx := context.Background()
	p := h.plan(nil)
	path := "/backups/crewship-workspace-alpha-20260930T030000Z.tar.zst"
	drillAt := mustTime(t, "2026-09-30T05:00:00Z")
	if err := backup.UpsertCatalogEntry(ctx, h.db, backup.CatalogEntry{
		FilePath: path, Scope: "workspace", WorkspaceID: "ws_a", CreatedAt: mustTime(t, "2026-09-30T03:00:00Z"),
		Size: 99, SHA256: "x", Encrypted: true, FormatVersion: backup.FormatVersion, PlanID: p.ID,
		Incomplete: []backup.IncompleteItem{},
	}); err != nil {
		t.Fatal(err)
	}
	if err := backup.SetCatalogDrill(ctx, h.db, path, "partial", json.RawMessage(`{"note":"2 crews without files"}`), drillAt); err != nil {
		t.Fatal(err)
	}
	size := int64(99)
	r := &Run{PlanID: p.ID, Trigger: TriggerSchedule, Scope: ScopeWorkspaces, WorkspaceID: "ws_a", Status: StatusIncomplete,
		BundlePath: path, Size: &size, Recipients: []string{h.key}, StartedAt: mustTime(t, "2026-09-30T03:00:00Z"),
		Incomplete: []backup.IncompleteItem{{Kind: backup.IncompleteAttachmentMissing, Count: 3, Workspace: "ws_a", Detail: "3 files"}}}
	if _, err := insertRun(ctx, h.db, r); err != nil {
		t.Fatal(err)
	}
	views, err := ListRunViews(ctx, h.db, h.svc.Recipients, RunFilter{Scope: ScopeWorkspaces, WorkspaceIDs: []string{"ws_a"}})
	if err != nil || len(views) != 1 {
		t.Fatalf("views = %+v, %v", views, err)
	}
	v := views[0]
	if v.PlanName == nil || *v.PlanName != p.Name || v.WorkspaceName == nil || *v.WorkspaceName != "Alpha" ||
		v.ProofLevel != backup.ProofRestore || v.DrillResult == nil || *v.DrillResult != "partial" ||
		v.DrillNote == nil || *v.DrillNote != "2 crews without files" || v.Restorable == nil || *v.Restorable != "direct" ||
		len(v.Incomplete) != 1 || v.Incomplete[0].WorkspaceID == nil || len(v.Recipients) != 1 || v.Recipients[0] == h.key {
		b, _ := json.Marshal(v)
		t.Fatalf("view = %s", b)
	}
	if views, _ := ListRunViews(ctx, h.db, nil, RunFilter{Scope: ScopeInstance}); len(views) != 0 {
		t.Fatalf("instance filter listed %d workspace runs", len(views))
	}
}

func TestOverview_WorkspacesScope(t *testing.T) {
	h := newHarness(t, "2026-09-30T12:00:00Z")
	ctx := context.Background()
	now := mustTime(t, "2026-09-30T12:00:00Z")
	p := h.plan(nil) // covers ws_a only
	h.plan(func(p *Plan) {
		p.Name, p.Preset, p.WorkspaceIDs, p.Contents = "Memory every 6 h", backup.PresetCustom, []string{"ws_b"}, []string{"memory"}
	})
	// ws_a: a checked bundle today with three missing attachment files;
	// ws_b: only a custom bundle, which never counts; ws_c: nothing.
	for _, e := range []backup.CatalogEntry{
		{FilePath: "/b/a.tar.zst", Scope: "workspace", WorkspaceID: "ws_a", CreatedAt: mustTime(t, "2026-09-30T03:00:00Z"), Size: 1000, SHA256: "a", PlanID: p.ID,
			ProofLevel: backup.ProofContents, Incomplete: []backup.IncompleteItem{{Kind: backup.IncompleteAttachmentMissing, Count: 3, Workspace: "ws_a"}}},
		{FilePath: "/b/b.tar.zst", Scope: "workspace", WorkspaceID: "ws_b", CreatedAt: mustTime(t, "2026-09-30T06:00:00Z"), Size: 10, SHA256: "b", Kind: backup.KindCustom, Incomplete: []backup.IncompleteItem{}},
	} {
		if err := backup.UpsertCatalogEntry(ctx, h.db, e); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := insertRun(ctx, h.db, &Run{PlanID: p.ID, Trigger: TriggerSchedule, Scope: ScopeWorkspaces, WorkspaceID: "ws_a",
		Status: StatusDone, BundlePath: "/b/a.tar.zst", DueAt: ptrTime(mustTime(t, "2026-09-30T03:00:00Z")), StartedAt: mustTime(t, "2026-09-30T03:00:00Z")}); err != nil {
		t.Fatal(err)
	}
	if _, err := insertRun(ctx, h.db, &Run{PlanID: p.ID, Trigger: TriggerSchedule, Scope: ScopeWorkspaces, WorkspaceID: "ws_a",
		Status: StatusSkipped, Error: "skipped: still busy after 120 minutes", DueAt: ptrTime(mustTime(t, "2026-09-29T03:00:00Z")), StartedAt: mustTime(t, "2026-09-29T03:00:00Z")}); err != nil {
		t.Fatal(err)
	}
	ov, err := BuildOverview(ctx, h.db, OverviewInput{Scope: ScopeWorkspaces, BackupsDir: t.TempDir()}, now)
	if err != nil {
		t.Fatal(err)
	}
	if ov.Status.Verdict != VerdictNone {
		t.Fatalf("verdict = %s: two of three workspaces have no full backup", ov.Status.Verdict)
	}
	if len(ov.Nights) != 14 || ov.Nights[13].Date != "2026-09-30" || ov.Nights[13].Status != "ok" || ov.Nights[13].Proof != 2 || ov.Nights[12].Status != "skipped" {
		t.Fatalf("nights tail = %+v %+v", ov.Nights[12], ov.Nights[13])
	}
	ids := map[string]bool{}
	for _, it := range ov.NeedsAttention {
		ids[it.ID] = true
	}
	for _, want := range []string{"incomplete:ws_a", "never:ws_b", "never:ws_c", "noplan:ws_b", "noplan:ws_c"} {
		if !ids[want] {
			t.Errorf("needs_attention lacks %s: %+v", want, ov.NeedsAttention)
		}
	}
	if ov.NeedsAttention[0].Severity != "bad" {
		t.Fatalf("bad items come first: %+v", ov.NeedsAttention[0])
	}
	if len(ov.Workspaces) != 3 || ov.Workspaces[0].Status != "warn" || ov.Workspaces[1].Status != "bad" {
		t.Fatalf("workspaces = %+v", ov.Workspaces)
	}
	if ov.Space.BackupsBytes != 1010 || ov.Space.StagingNeedBytes != 2000 || ov.Space.RestoreNeedBytes != 3000 || ov.Space.TotalBytes == 0 {
		t.Fatalf("space = %+v", ov.Space)
	}
	if ov.Status.OffsiteVerified || ov.Status.Summary == nil {
		t.Fatalf("status = %+v", ov.Status)
	}

	// Only ws_a: its checked bundle is the verdict.
	ov, err = BuildOverview(ctx, h.db, OverviewInput{Scope: ScopeWorkspaces, WorkspaceIDs: []string{"ws_a"}}, now)
	if err != nil {
		t.Fatal(err)
	}
	if ov.Status.Verdict != VerdictContentsChecked || ov.Status.Label != p.Name || len(ov.Workspaces) != 1 {
		t.Fatalf("ws_a overview: verdict %s label %s rows %d", ov.Status.Verdict, ov.Status.Label, len(ov.Workspaces))
	}
}

func TestOverview_InstanceScopeWithoutInstanceBundles(t *testing.T) {
	h := newHarness(t, "2026-09-30T12:00:00Z")
	ov, err := BuildOverview(context.Background(), h.db, OverviewInput{Scope: ScopeInstance}, mustTime(t, "2026-09-30T12:00:00Z"))
	if err != nil {
		t.Fatal(err)
	}
	if ov.Status.Verdict != VerdictNone || ov.Status.Label != "Complete recovery" || ov.Workspaces != nil || ov.InstanceSummary == nil {
		t.Fatalf("instance overview = %+v", ov)
	}
	found := false
	for _, it := range ov.NeedsAttention {
		found = found || it.ID == "noplan:instance"
	}
	if !found {
		t.Fatalf("needs_attention = %+v", ov.NeedsAttention)
	}
}

func ptrTime(t time.Time) *time.Time { return &t }
