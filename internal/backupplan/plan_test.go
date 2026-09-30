package backupplan

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/backup"
)

func TestNormalize(t *testing.T) {
	h := newHarness(t, "2026-09-30T02:00:00Z")
	ctx := context.Background()
	base := func() Plan {
		p := Defaults(backup.PresetWorkspace)
		p.RecipientIDs = []string{h.key}
		return p
	}
	cases := []struct {
		name    string
		mut     func(*Plan)
		wantErr string
		check   func(*testing.T, Plan)
	}{
		{name: "defaults are valid", check: func(t *testing.T, p Plan) {
			if !p.CountsAsProtection || p.Scope != ScopeWorkspaces {
				t.Fatalf("plan %+v", p)
			}
		}},
		{name: "bad timezone", mut: func(p *Plan) { p.Timezone = "Mars/Olympus" }, wantErr: "IANA"},
		{name: "Local is not a zone", mut: func(p *Plan) { p.Timezone = "Local" }, wantErr: "IANA"},
		{name: "bad cron", mut: func(p *Plan) { p.Cadence = CadenceCustom; p.CronExpr = str("61 * * * *") }, wantErr: "cron"},
		{name: "custom cadence needs cron", mut: func(p *Plan) { p.Cadence = CadenceCustom }, wantErr: "cron_expr is required"},
		{name: "cron with a zone is refused", mut: func(p *Plan) { p.Cadence = CadenceCustom; p.CronExpr = str("CRON_TZ=UTC 0 3 * * *") }, wantErr: "timezone"},
		{name: "keep_min 0", mut: func(p *Plan) { p.KeepMin = 0 }, wantErr: "keep_min"},
		{name: "no recipients", mut: func(p *Plan) { p.RecipientIDs = nil }, wantErr: "recipient_ids"},
		{name: "unknown recipient", mut: func(p *Plan) { p.RecipientIDs = []string{"rcp_nope"} }, wantErr: "unknown recipient"},
		{name: "bad time of day", mut: func(p *Plan) { p.TimeOfDay = "25:00" }, wantErr: "time_of_day"},
		{name: "unknown workspace", mut: func(p *Plan) { p.WorkspaceIDs = []string{"ws_zzz"} }, wantErr: "no workspace"},
		{name: "custom needs a category", mut: func(p *Plan) { p.Preset = backup.PresetCustom }, wantErr: "at least one"},
		{name: "custom keeps its contents, drops env", mut: func(p *Plan) {
			p.Preset, p.Contents = backup.PresetCustom, []string{"memory", "env"}
		}, check: func(t *testing.T, p Plan) {
			if len(p.Contents) != 1 || p.Contents[0] != "memory" || p.CountsAsProtection {
				t.Fatalf("custom plan %+v", p)
			}
		}},
		{name: "complete recovery covers the whole instance", mut: func(p *Plan) {
			p.Preset, p.Scope, p.WorkspaceIDs, p.Contents = backup.PresetComplete, ScopeWorkspaces, []string{"ws_a"}, []string{"memory"}
		}, check: func(t *testing.T, p Plan) {
			if p.Scope != ScopeInstance || len(p.WorkspaceIDs) != 0 || len(p.Contents) != 0 {
				t.Fatalf("complete plan %+v", p)
			}
		}},
		{name: "weekly defaults to Sunday", mut: func(p *Plan) { p.Cadence = CadenceWeekly }, check: func(t *testing.T, p Plan) {
			if p.Weekday == nil || *p.Weekday != 0 {
				t.Fatalf("weekday %v", p.Weekday)
			}
		}},
		{name: "valid cron in a zone", mut: func(p *Plan) {
			p.Cadence, p.CronExpr, p.Timezone = CadenceCustom, str(" 0 */6 * * * "), "Europe/Prague"
		}, check: func(t *testing.T, p Plan) {
			if *p.CronExpr != "0 */6 * * *" {
				t.Fatalf("cron %q", *p.CronExpr)
			}
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p := base()
			if tc.mut != nil {
				tc.mut(&p)
			}
			err := Normalize(ctx, &p, DBWorkspaces{DB: h.db}, h.svc.Recipients)
			if tc.wantErr != "" {
				if err == nil || !IsValidation(err) || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("err = %v, want a validation error containing %q", err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if tc.check != nil {
				tc.check(t, p)
			}
		})
	}
}

// End to end with the real workspace executor: a plan's run writes an
// encrypted bundle, catalogues it with its plan, and applies the plan's keep
// rules to the plan's own bundles after it.
func TestWorkspaceExecutor_WritesBundleAndAppliesRetention(t *testing.T) {
	h := newHarness(t, "2026-09-30T12:00:00Z")
	out := t.TempDir()
	h.svc.Workspace = &WorkspaceExecutor{DB: h.db, OutputDir: out}
	h.svc.Verify = nil // the real checksum check
	if _, err := h.db.Exec(`INSERT INTO users(id,email,full_name) VALUES('u1','admin@ex.com','Admin')`); err != nil {
		t.Fatal(err)
	}
	p := h.plan(func(p *Plan) { p.KeepMin, p.KeepDaily, p.KeepWeekly, p.KeepMonthly = 1, 0, 0, 0 })
	ctx := context.Background()

	ids, err := h.svc.StartManual(ctx, ManualRequest{PlanID: p.ID}, "u1")
	if err != nil {
		t.Fatal(err)
	}
	h.svc.Wait()
	first, _ := GetRun(ctx, h.db, ids[0])
	if first.Status != StatusDone || first.BundlePath == "" {
		t.Fatalf("first run = status %s error %q", first.Status, first.Error)
	}
	for _, ph := range first.Phases {
		if ph.Name == PhaseCheck && ph.Status != "done" {
			t.Fatalf("check phase = %+v", ph)
		}
	}
	// Bundle names carry a one-second stamp; give the first copy an older
	// name so the second run cannot land on the same file.
	older := filepath.Join(out, "crewship-workspace-alpha-20260101T000000Z.tar.zst")
	if err := os.Rename(first.BundlePath, older); err != nil {
		t.Fatal(err)
	}
	if _, err := h.db.Exec(`UPDATE backup_catalog SET file_path = ? WHERE file_path = ?`, older, first.BundlePath); err != nil {
		t.Fatal(err)
	}
	ids, err = h.svc.StartManual(ctx, ManualRequest{PlanID: p.ID}, "u1")
	if err != nil {
		t.Fatal(err)
	}
	h.svc.Wait()
	second, _ := GetRun(ctx, h.db, ids[0])
	if second.Status != StatusDone {
		t.Fatalf("second run = status %s error %q", second.Status, second.Error)
	}
	if _, err := os.Stat(older); !os.IsNotExist(err) {
		t.Fatalf("keep_min 1: the older copy of the plan should be gone (stat err %v)", err)
	}
	if _, err := os.Stat(second.BundlePath); err != nil {
		t.Fatalf("newest copy missing: %v", err)
	}
	m, err := backup.Inspect(ctx, second.BundlePath)
	if err != nil || !m.Encryption.Enabled {
		t.Fatalf("bundle manifest %+v, %v", m, err)
	}
}
