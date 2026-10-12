package backupplan

import (
	"errors"
	"testing"

	"github.com/crewship-ai/crewship/internal/backup"
	"github.com/crewship-ai/crewship/internal/backup/offsite"
)

func TestBackupProofLabelsDoNotOverstateRecoveryEvidence(t *testing.T) {
	for _, tc := range []struct {
		name  string
		entry *backup.CatalogEntry
		want  string
	}{
		{"none", nil, VerdictNone},
		{"checksum", &backup.CatalogEntry{ProofLevel: backup.ProofChecksum}, VerdictChecksumOnly},
		{"contents", &backup.CatalogEntry{ProofLevel: backup.ProofContents}, VerdictContentsChecked},
		{"failed restore", &backup.CatalogEntry{ProofLevel: backup.ProofRestore, DrillResult: "failed"}, VerdictFailed},
		{"partial restore", &backup.CatalogEntry{ProofLevel: backup.ProofRestore, DrillResult: "partial"}, VerdictPartial},
		{"incomplete", &backup.CatalogEntry{ProofLevel: backup.ProofRestore, Incomplete: []backup.IncompleteItem{{Kind: backup.IncompleteAttachmentMissing, Count: 1}}}, VerdictPartial},
		{"restored", &backup.CatalogEntry{ProofLevel: backup.ProofRestore, DrillResult: "passed"}, VerdictVerified},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := bundleVerdict(tc.entry); got != tc.want {
				t.Fatalf("proof %q want %q", got, tc.want)
			}
		})
	}
	if restorable(0) != nil {
		t.Fatal("unknown format described as restorable")
	}
	for format, want := range map[int]string{backup.FormatVersion: "direct", backup.FormatVersion + 1: "unsupported", 1: "converter"} {
		if got := restorable(format); got == nil || *got != want {
			t.Fatalf("format %d: %v want %s", format, got, want)
		}
	}
	for by, want := range map[string]string{offsite.VerifiedByProviderChecksum: "verified by the provider's checksum", offsite.VerifiedByDownloadRehash: "downloaded and re-hashed", "unknown": "stored bytes not proven"} {
		if got := VerifiedByPhrase(by); got != want {
			t.Fatalf("copy proof %q: %q", by, got)
		}
	}
}

func TestBackupScheduleDescriptionMatchesRetentionAndEnvironmentCadence(t *testing.T) {
	weekday, monthday := 2, 15
	cron := "0 */6 * * *"
	for _, tc := range []struct {
		plan Plan
		want string
	}{
		{Plan{Cadence: CadenceWeekly, TimeOfDay: "03:00"}, "weekly on Sunday 03:00"},
		{Plan{Cadence: CadenceWeekly, Weekday: &weekday, TimeOfDay: "03:00"}, "weekly on Tuesday 03:00"},
		{Plan{Cadence: CadenceMonthly, TimeOfDay: "03:00"}, "monthly, first Sunday 03:00"},
		{Plan{Cadence: CadenceMonthly, Monthday: &monthday, TimeOfDay: "03:00"}, "monthly on day 15 03:00"},
		{Plan{Cadence: CadenceCustom, CronExpr: &cron}, "cron 0 */6 * * *"},
		{Plan{Cadence: CadenceCustom}, ""},
		{Plan{Cadence: CadenceDaily, TimeOfDay: "03:00", EnvMode: backup.EnvModeComplete, EnvCadence: EnvEvery}, "data daily 03:00 · environments with every run"},
	} {
		if got := describeWhen(&tc.plan); got != tc.want {
			t.Fatalf("schedule description %q want %q", got, tc.want)
		}
	}
	if got := describeKeep(&Plan{KeepMin: 3, KeepDaily: 7, KeepWeekly: 4, KeepMonthly: 12}); got != "keep 3 checked, 7 daily, 4 weekly, 12 monthly" {
		t.Fatalf("retention description: %q", got)
	}
	sunday := mustTime(t, "2026-10-04T03:00:00Z")
	monday := mustTime(t, "2026-10-05T03:00:00Z")
	for _, tc := range []struct {
		cadence string
		day     int
		want    bool
	}{{EnvEvery, 0, true}, {EnvEvery, 1, true}, {EnvWeekly, 0, true}, {EnvWeekly, 1, false}, {EnvMonthly, 0, true}, {EnvMonthly, 1, false}, {"unknown", 0, false}} {
		day := sunday
		if tc.day == 1 {
			day = monday
		}
		if got := envDay(&Plan{EnvCadence: tc.cadence}, day); got != tc.want {
			t.Fatalf("environment cadence %s on %s: %v", tc.cadence, day, got)
		}
	}
}

func TestDeletedBackupPlanKeepsReadableRunHistory(t *testing.T) {
	h := newHarness(t, "2026-09-30T12:00:00Z")
	ctx := t.Context()
	plan := h.plan(nil)
	run := &Run{PlanID: plan.ID, Trigger: TriggerManual, Scope: ScopeWorkspaces, WorkspaceID: "ws_a", Status: StatusDone, StartedAt: h.clock.Now(), Recipients: []string{"legacy-key", "legacy-key"}}
	if _, err := insertRun(ctx, h.db, run); err != nil {
		t.Fatal(err)
	}
	before, err := GetRunView(ctx, h.db, nil, run.ID)
	if err != nil || before.PlanName == nil || *before.PlanName != plan.Name {
		t.Fatalf("initial run view: %#v %v", before, err)
	}
	if err := DeletePlan(ctx, h.db, plan.ID); err != nil {
		t.Fatal(err)
	}
	after, err := GetRunView(ctx, h.db, nil, run.ID)
	if err != nil || after.ID != run.ID || after.PlanName != nil || len(after.Recipients) != 2 {
		t.Fatalf("history lost after deletion: %#v %v", after, err)
	}
	if err := DeletePlan(ctx, h.db, plan.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("repeated deletion claimed success: %v", err)
	}
	if err := UpdatePlan(ctx, h.db, plan, "actor", h.clock.Now()); !errors.Is(err, ErrNotFound) {
		t.Fatalf("updated deleted plan: %v", err)
	}
}
