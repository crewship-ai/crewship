package backupplan

import (
	"errors"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/backup"
)

func TestBackupScheduleAdmissionRollsBackPartialWorkspaceBatch(t *testing.T) {
	h := newHarness(t, "2026-09-30T02:00:00Z")
	ctx := t.Context()
	p := h.plan(func(p *Plan) { p.WorkspaceIDs = []string{"ws_a", "ws_b"} })
	original := *p.NextRunAt
	h.clock.Set(mustTime(t, "2026-09-30T03:00:20Z"))
	for _, query := range []string{
		`CREATE TRIGGER refuse_run BEFORE INSERT ON backup_runs WHEN NEW.workspace_id='ws_b' BEGIN SELECT RAISE(ABORT,'injected run refusal'); END`,
		`CREATE TRIGGER refuse_plan BEFORE UPDATE ON backup_plans BEGIN SELECT RAISE(ABORT,'injected plan refusal'); END`,
	} {
		if _, err := h.db.Exec(query); err != nil {
			t.Fatal(err)
		}
		if err := h.svc.schedulePlans(ctx); err != nil {
			t.Fatal(err)
		} // Per-plan errors are logged; other plans still run.
		if got := h.runs(p.ID); len(got) != 0 {
			t.Fatalf("partial workspace batch committed: %#v", got)
		}
		saved, err := GetPlan(ctx, h.db, p.ID)
		if err != nil || saved.NextRunAt == nil || *saved.NextRunAt != original {
			t.Fatalf("lost due time after refusal: %#v %v", saved, err)
		}
		name := "refuse_run"
		if strings.Contains(query, "refuse_plan") {
			name = "refuse_plan"
		}
		if _, err := h.db.Exec("DROP TRIGGER " + name); err != nil {
			t.Fatal(err)
		}
	}
	if err := h.svc.schedulePlans(ctx); err != nil {
		t.Fatal(err)
	}
	if got := h.runs(p.ID); len(got) != 2 {
		t.Fatalf("retry did not admit complete batch: %#v", got)
	}
	if err := h.svc.schedulePlans(ctx); err != nil {
		t.Fatal(err)
	}
	if got := h.runs(p.ID); len(got) != 2 {
		t.Fatalf("retry duplicated scheduled work: %#v", got)
	}
}

func TestBackupManualAdmissionRefusesPartialBatchWithoutLeakingPassphrase(t *testing.T) {
	h := newHarness(t, "2026-09-30T12:00:00Z")
	ctx := t.Context()
	if _, err := h.db.Exec(`CREATE TRIGGER refuse_run BEFORE INSERT ON backup_runs WHEN NEW.workspace_id='ws_b' BEGIN SELECT RAISE(ABORT,'injected admission refusal'); END`); err != nil {
		t.Fatal(err)
	}
	ids, err := h.svc.StartManual(ctx, ManualRequest{Scope: ScopeWorkspaces, WorkspaceIDs: []string{"ws_a", "ws_b"}, Passphrase: "synthetic-recovery-passphrase"}, "u1")
	if err == nil || len(ids) != 0 {
		t.Fatalf("partial batch acknowledged: %v %v", ids, err)
	}
	if runs, err := ListRuns(ctx, h.db, RunFilter{}); err != nil || len(runs) != 0 {
		t.Fatalf("partial batch persisted: %#v %v", runs, err)
	}
	h.svc.keysMu.Lock()
	remaining := len(h.svc.passphrases)
	h.svc.keysMu.Unlock()
	if remaining != 0 || len(h.exec.calls()) != 0 {
		t.Fatalf("failed admission retained secret or executed work: %d", remaining)
	}
}

func TestBackupRecipientDefaultsRespectEnabledPlans(t *testing.T) {
	h := newHarness(t, "2026-09-30T12:00:00Z")
	ctx := t.Context()
	if _, err := h.svc.defaultRecipients(ctx, &Plan{Scope: ScopeWorkspaces}); !IsValidation(err) {
		t.Fatalf("missing encryption recipients accepted: %v", err)
	}
	disabled := h.plan(func(p *Plan) { p.Enabled = false })
	if _, err := h.svc.defaultRecipients(ctx, &Plan{Scope: ScopeWorkspaces}); !IsValidation(err) {
		t.Fatalf("disabled plan supplied keys: %v", err)
	}
	h.plan(func(p *Plan) {
		p.Preset = backup.PresetCustom
		p.Contents = []string{"memory"}
		p.Name = "Custom fallback"
	})
	ids, err := h.svc.defaultRecipients(ctx, &Plan{Scope: ScopeInstance})
	if err != nil || len(ids) != 1 || ids[0] != h.key {
		t.Fatalf("fallback keys: %v %v", ids, err)
	}
	if err := DeletePlan(ctx, h.db, disabled.ID); err != nil {
		t.Fatal(err)
	}
	if err := h.db.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := h.svc.defaultRecipients(ctx, &Plan{Scope: ScopeInstance}); err == nil || errors.Is(err, ErrNotFound) {
		t.Fatalf("failed key inventory invented a default: %v", err)
	}
}
