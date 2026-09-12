package pipeline

// #2495 — an existing plan's preset must survive a schema change, or the
// change must be refused, WHICHEVER DOOR the change comes through.
//
// The gate that refuses to publish a recipe an enabled, unpinned plan's
// preset can no longer satisfy lived inside the `Publication != nil` branch
// of consumeDraftTx, so it only ran on Edit → Publish. Every other way an
// active definition changes — `crewship routine save`, the agent/internal
// save door, an import, a manifest apply — walked straight past it and left
// a plan whose stored preset no longer fits the recipe it points at. PRD §9
// calls that out by name: "žádný tichý rozbitý plán".
//
// The exclusions are deliberate and are pinned here too, because widening a
// gate is only safe if what it still lets through is what it was always
// meant to let through: a DISABLED plan is not firing, a PINNED plan keeps
// running the version it names, a legacy input with no form contract has no
// contract to break, and a save that does not change the definition changes
// nothing a preset could trip over.

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

// presetGateRig is one saved routine with one plan pointing at it.
type presetGateRig struct {
	store *Store
	p     *Pipeline
}

const presetGateV1 = `{"dsl_version":"1.0","name":"planned","inputs":[` +
	`{"name":"who","type":"string","widget":"text","required":true}],"steps":[]}`

// presetGateV2 renames the required input, so `{"who":"alice"}` no longer
// satisfies it — the smallest change that genuinely breaks a stored preset.
const presetGateV2 = `{"dsl_version":"1.0","name":"planned","inputs":[` +
	`{"name":"recipient","type":"string","widget":"text","required":true}],"steps":[]}`

func newPresetGateRig(t *testing.T, scheduleCols, scheduleVals string, args ...any) *presetGateRig {
	t.Helper()
	s := draftStore(t)
	in := validSaveInput("planned")
	in.DefinitionJSON = presetGateV1
	p, err := s.Save(t.Context(), in)
	if err != nil {
		t.Fatalf("save v1: %v", err)
	}
	all := append([]any{p.ID}, args...)
	if _, err := s.db.ExecContext(t.Context(),
		`INSERT INTO pipeline_schedules(id,workspace_id,name,target_pipeline_id,cron_expr,inputs_json`+scheduleCols+`)`+
			` VALUES('plan','ws_test','Daily',?,'0 9 * * *','{"who":"alice"}'`+scheduleVals+`)`, all...); err != nil {
		t.Fatalf("seed schedule: %v", err)
	}
	return &presetGateRig{store: s, p: p}
}

// saveV2 pushes the breaking schema change through the PLAIN save door — no
// draft, no publication, the path `crewship routine save` and the agent
// door use.
func (r *presetGateRig) saveV2(t *testing.T) error {
	t.Helper()
	in := validSaveInput("planned")
	in.DefinitionJSON = presetGateV2
	_, err := r.store.Save(t.Context(), in)
	return err
}

func (r *presetGateRig) liveDefinition(t *testing.T) string {
	t.Helper()
	got, err := r.store.GetByID(t.Context(), r.p.ID)
	if err != nil {
		t.Fatalf("reload pipeline: %v", err)
	}
	return got.DefinitionJSON
}

func (r *presetGateRig) planPreset(t *testing.T) string {
	t.Helper()
	var raw string
	if err := r.store.db.QueryRowContext(t.Context(), `SELECT inputs_json FROM pipeline_schedules WHERE id='plan'`).Scan(&raw); err != nil {
		t.Fatalf("read preset: %v", err)
	}
	return raw
}

func (r *presetGateRig) versionCount(t *testing.T) int {
	t.Helper()
	var n int
	if err := r.store.db.QueryRowContext(t.Context(), `SELECT COUNT(*) FROM pipeline_versions WHERE pipeline_id=?`, r.p.ID).Scan(&n); err != nil {
		t.Fatalf("count versions: %v", err)
	}
	return n
}

// TestPresetGate_PlainSaveIsRefusedAndAtomic is the defect.
func TestPresetGate_PlainSaveIsRefusedAndAtomic(t *testing.T) {
	r := newPresetGateRig(t, ",enabled", ",1")
	before, versionsBefore := r.liveDefinition(t), r.versionCount(t)

	err := r.saveV2(t)
	if err == nil {
		t.Fatal("a plain save broke an enabled plan's preset and reported success")
	}
	var conflict *ScheduleDraftConflict
	if !errors.As(err, &conflict) {
		t.Fatalf("error %v is not a ScheduleDraftConflict — the caller cannot tell which plan to repair", err)
	}
	if conflict.ScheduleID != "plan" || conflict.Name != "Daily" || conflict.Reason == "" {
		t.Errorf("conflict %+v does not name the plan and the reason", conflict)
	}
	if !errors.Is(err, ErrDraftConflict) {
		t.Error("the conflict must still unwrap to ErrDraftConflict so existing 409 mapping holds")
	}

	// Atomic: nothing partially written.
	if got := r.liveDefinition(t); got != before {
		t.Errorf("the live recipe changed despite the refusal:\n got %s\nwant %s", got, before)
	}
	if got := r.versionCount(t); got != versionsBefore {
		t.Errorf("version rows = %d, want %d — a refused save must write no history", got, versionsBefore)
	}
	if got := r.planPreset(t); got != `{"who":"alice"}` {
		t.Errorf("the plan's preset was mutated: %s", got)
	}
}

// TestPresetGate_RepairThePresetThenTheSaveSucceeds is the way out the error
// message promises. Without this the gate would be a dead end.
func TestPresetGate_RepairThePresetThenTheSaveSucceeds(t *testing.T) {
	r := newPresetGateRig(t, ",enabled", ",1")
	if err := r.saveV2(t); err == nil {
		t.Fatal("expected the first attempt to be refused")
	}
	if _, err := r.store.db.ExecContext(t.Context(), `UPDATE pipeline_schedules SET inputs_json='{"recipient":"alice"}' WHERE id='plan'`); err != nil {
		t.Fatalf("repair preset: %v", err)
	}
	if err := r.saveV2(t); err != nil {
		t.Fatalf("save after repairing the preset: %v", err)
	}
	if got := r.liveDefinition(t); got != presetGateV2 {
		t.Errorf("live recipe did not advance after the repair: %s", got)
	}
}

// TestPresetGate_ExclusionsStillSaveCleanly pins everything the gate must
// keep letting through.
func TestPresetGate_ExclusionsStillSaveCleanly(t *testing.T) {
	for _, tc := range []struct {
		name       string
		cols, vals string
		args       []any
		why        string
	}{
		{"disabled plan", ",enabled", ",0", nil,
			"a disabled plan is not firing, so its preset cannot break anything today"},
		{"pinned plan", ",enabled,target_pipeline_version", ",1,1", nil,
			"a pinned plan keeps running the version it names, whatever HEAD becomes"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := newPresetGateRig(t, tc.cols, tc.vals, tc.args...)
			if err := r.saveV2(t); err != nil {
				t.Fatalf("%s: %v (%s)", tc.name, err, tc.why)
			}
			if got := r.liveDefinition(t); got != presetGateV2 {
				t.Errorf("%s: live recipe did not advance", tc.name)
			}
		})
	}

	t.Run("legacy untyped input", func(t *testing.T) {
		// An input with no widget has no form contract, so execution does
		// not validate it either — the gate must match execution rather
		// than invent a stricter rule for plans.
		s := draftStore(t)
		in := validSaveInput("legacy")
		in.DefinitionJSON = `{"dsl_version":"1.0","name":"legacy","inputs":[{"name":"topic"}],"steps":[]}`
		p, err := s.Save(t.Context(), in)
		if err != nil {
			t.Fatalf("save v1: %v", err)
		}
		if _, err := s.db.ExecContext(t.Context(), `INSERT INTO pipeline_schedules(id,workspace_id,name,target_pipeline_id,cron_expr,inputs_json,enabled)`+
			` VALUES('plan','ws_test','Daily',?,'0 9 * * *','{"topic":"news"}',1)`, p.ID); err != nil {
			t.Fatalf("seed schedule: %v", err)
		}
		in.DefinitionJSON = `{"dsl_version":"1.0","name":"legacy","inputs":[{"name":"topic"},{"name":"extra"}],"steps":[]}`
		if _, err := s.Save(t.Context(), in); err != nil {
			t.Fatalf("legacy untyped input must not be gated: %v", err)
		}
	})

	t.Run("unchanged definition", func(t *testing.T) {
		// An idempotent re-save — a rename, a description edit, a status
		// flip — must not start failing because a plan's preset was
		// already imperfect before this change existed.
		s := draftStore(t)
		in := validSaveInput("stable")
		in.DefinitionJSON = `{"dsl_version":"1.0","name":"stable","inputs":[` +
			`{"name":"who","type":"string","widget":"text","required":true}],"steps":[]}`
		p, err := s.Save(t.Context(), in)
		if err != nil {
			t.Fatalf("save v1: %v", err)
		}
		// A preset that never satisfied the schema in the first place.
		if _, err := s.db.ExecContext(t.Context(), `INSERT INTO pipeline_schedules(id,workspace_id,name,target_pipeline_id,cron_expr,inputs_json,enabled)`+
			` VALUES('plan','ws_test','Daily',?,'0 9 * * *','{}',1)`, p.ID); err != nil {
			t.Fatalf("seed schedule: %v", err)
		}
		in.Description = "a new description, the same recipe"
		if _, err := s.Save(t.Context(), in); err != nil {
			t.Fatalf("a save that does not touch the definition must not be gated: %v", err)
		}
	})

	t.Run("first save of a new routine", func(t *testing.T) {
		// No routine, therefore no plans. The insert path shares the gate
		// and must not trip over a routine that does not exist yet.
		s := draftStore(t)
		in := validSaveInput("brand-new")
		in.DefinitionJSON = presetGateV2
		if _, err := s.Save(t.Context(), in); err != nil {
			t.Fatalf("inserting a new routine must not be gated: %v", err)
		}
	})
}

// TestPresetGate_PublishPathKeepsItsOwnBehaviour guards against the widening
// quietly changing the door that already worked. The publish path must still
// refuse, still keep the draft, and still leave the live recipe alone.
func TestPresetGate_PublishPathKeepsItsOwnBehaviour(t *testing.T) {
	s := draftStore(t)
	ctx := t.Context()
	in := validSaveInput("planned")
	in.DefinitionJSON = presetGateV1
	p, err := s.Save(ctx, in)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.ExecContext(t.Context(), `INSERT INTO pipeline_schedules(id,workspace_id,name,target_pipeline_id,cron_expr,inputs_json,enabled)`+
		` VALUES('plan','ws_test','Daily',?,'0 9 * * *','{"who":"alice"}',1)`, p.ID); err != nil {
		t.Fatal(err)
	}
	d := saveTestDraft(t, s, p.Slug)
	pub := publishInput(d)
	pub.DefinitionJSON = presetGateV2
	_, err = s.Save(ctx, pub)
	var conflict *ScheduleDraftConflict
	if !errors.As(err, &conflict) || conflict.ScheduleID != "plan" {
		t.Fatalf("publish path lost its actionable conflict: %v", err)
	}
	retained, _ := s.GetDraft(ctx, "ws_test", p.Slug)
	if retained.ID != d.ID {
		t.Error("a blocked publication lost the draft")
	}
	current, _ := s.GetByID(ctx, p.ID)
	if current.DefinitionJSON != presetGateV1 {
		t.Error("a blocked publication changed the live recipe")
	}
	var doc map[string]json.RawMessage
	if err := json.Unmarshal(d.Document, &doc); err != nil {
		t.Fatalf("draft document unreadable after the block: %v", err)
	}
}

// TestPresetGate_SchemaAndPlanChangeTogether is the way out of a deadlock
// the gate would otherwise create with #2496's write-side check.
//
// Changing a required input's name means the recipe and the plan must move
// together. Neither can move first: the plan's new preset does not satisfy
// the recipe still published, and the new recipe does not satisfy the preset
// still stored. A save that carries a trigger writes both in one
// transaction — so the gate runs AFTER createTriggerTx and asks the only
// question that matters, which is whether the plan can still run once this
// save has landed.
func TestPresetGate_SchemaAndPlanChangeTogether(t *testing.T) {
	s := draftStore(t)
	ctx := t.Context()
	in := validSaveInput("planned")
	in.DefinitionJSON = presetGateV1
	p, err := s.Save(ctx, in)
	if err != nil {
		t.Fatalf("save v1: %v", err)
	}
	// The plan the atomic authoring path owns: one per (workspace, pipeline).
	if _, _, err := s.SaveWithTrigger(ctx, in, &TriggerInput{
		Kind: TriggerKindSchedule, CronExpr: "0 9 * * *", Timezone: "UTC",
		Inputs: map[string]any{"who": "alice"},
	}); err != nil {
		t.Fatalf("attach plan: %v", err)
	}

	// Recipe alone: refused, because the stored preset would not satisfy it.
	alone := validSaveInput("planned")
	alone.DefinitionJSON = presetGateV2
	if _, err := s.Save(ctx, alone); err == nil {
		t.Fatal("changing the recipe alone left the plan unsatisfiable and was accepted")
	}

	// Recipe AND plan, in one save: accepted.
	together := validSaveInput("planned")
	together.DefinitionJSON = presetGateV2
	if _, _, err := s.SaveWithTrigger(ctx, together, &TriggerInput{
		Kind: TriggerKindSchedule, CronExpr: "0 9 * * *", Timezone: "UTC",
		Inputs: map[string]any{"recipient": "alice"},
	}); err != nil {
		t.Fatalf("changing the recipe and its plan together: %v", err)
	}

	got, err := s.GetByID(ctx, p.ID)
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	if got.DefinitionJSON != presetGateV2 {
		t.Errorf("recipe did not advance: %s", got.DefinitionJSON)
	}
	var preset string
	if err := s.db.QueryRowContext(t.Context(), `SELECT inputs_json FROM pipeline_schedules WHERE target_pipeline_id=? AND deleted_at IS NULL`, p.ID).Scan(&preset); err != nil {
		t.Fatalf("read preset: %v", err)
	}
	if preset != `{"recipient":"alice"}` {
		t.Errorf("plan preset = %s, want the new one", preset)
	}
}

// TestPresetGate_TriggerCannotSmuggleABrokenPlanPast guards the other
// direction of that ordering: running the gate after the trigger write must
// not let a save attach a plan the recipe rejects.
func TestPresetGate_TriggerCannotSmuggleABrokenPlanPast(t *testing.T) {
	s := draftStore(t)
	ctx := t.Context()
	in := validSaveInput("planned")
	in.DefinitionJSON = presetGateV1
	if _, err := s.Save(ctx, in); err != nil {
		t.Fatalf("save v1: %v", err)
	}
	bad := validSaveInput("planned")
	bad.DefinitionJSON = presetGateV2
	_, _, err := s.SaveWithTrigger(ctx, bad, &TriggerInput{
		Kind: TriggerKindSchedule, CronExpr: "0 9 * * *", Timezone: "UTC",
		Inputs: map[string]any{"who": "alice"}, // still the OLD input name
	})
	if err == nil {
		t.Fatal("a trigger carrying a preset the new recipe rejects was accepted")
	}
	// Which of the two refusals fires depends on ordering, and both are
	// actionable, so the assertion is on substance rather than on type.
	// createTriggerTx validates the trigger's own preset against the
	// definition this save is publishing (#2496) and gets there first, so
	// the caller is told which INPUT is unsatisfied — better here than the
	// plan's name, since they wrote that preset in this very request. If
	// that check ever stops firing, the post-trigger gate (#2495) catches
	// the same save and names the plan instead. Either way: refused, with
	// something to act on.
	var conflict *ScheduleDraftConflict
	if !errors.As(err, &conflict) && !strings.Contains(err.Error(), "recipient") {
		t.Errorf("error %v names neither the plan nor the unsatisfied input", err)
	}
	if !errors.Is(err, ErrDraftConflict) && !errors.Is(err, ErrInvalidTrigger) {
		t.Errorf("error %v maps to neither 409 nor 422", err)
	}
	var def string
	if err := s.db.QueryRowContext(t.Context(), `SELECT definition_json FROM pipelines WHERE workspace_id='ws_test' AND slug='planned'`).Scan(&def); err != nil {
		t.Fatalf("read recipe: %v", err)
	}
	if def != presetGateV1 {
		t.Errorf("the refused save still changed the recipe: %s", def)
	}
}

func TestPresetGate_WakeRecipeChangeIsRefused(t *testing.T) {
	r := newPresetGateRig(t, ",enabled", ",1")
	target, err := r.store.Save(t.Context(), validSaveInput("target"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.store.db.ExecContext(t.Context(), `UPDATE pipeline_schedules SET target_pipeline_id=?,wake_pipeline_id=?,wake_inputs_json='{"who":"alice"}' WHERE id='plan'`, target.ID, r.p.ID); err != nil {
		t.Fatal(err)
	}
	if err := r.saveV2(t); err == nil {
		t.Fatal("changing a wake recipe silently broke the enabled plan's wake preset")
	}
	if got := r.liveDefinition(t); got != presetGateV1 {
		t.Fatalf("refused wake change mutated recipe: %s", got)
	}
}
