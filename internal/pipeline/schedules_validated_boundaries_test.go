package pipeline

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func TestValidatedScheduleRejectsInvalidPresetAtomically(t *testing.T) {
	db := openStoreTestDB(t)
	t.Cleanup(func() { _ = db.Close() })
	seedPipeline(t, db, "recipe", "recipe")
	if _, err := db.Exec(`UPDATE pipelines SET definition_json=? WHERE id='recipe'`, presetGateV1); err != nil {
		t.Fatal(err)
	}
	s := NewScheduleStore(db)
	in := SaveScheduleInput{WorkspaceID: "ws_test", Name: "Daily", TargetPipelineID: "recipe", CronExpr: "0 9 * * *", Timezone: "UTC", Enabled: true, Inputs: map[string]any{"who": "Alice"}}
	bad := in
	bad.Inputs = map[string]any{"recipient": "Alice"}
	if _, err := s.SaveValidated(t.Context(), bad); !errors.Is(err, ErrInvalidTrigger) {
		t.Fatalf("create error = %v", err)
	}
	var count int
	if err := db.QueryRow(`SELECT count(*) FROM pipeline_schedules`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("rejected create persisted %d rows, %v", count, err)
	}
	saved, err := s.SaveValidated(t.Context(), in)
	if err != nil {
		t.Fatal(err)
	}
	bad.ID = saved.ID
	bad.Name = "Changed"
	if _, err := s.SaveValidated(t.Context(), bad); !errors.Is(err, ErrInvalidTrigger) {
		t.Fatalf("update error = %v", err)
	}
	var name, preset string
	if err := db.QueryRow(`SELECT name,inputs_json FROM pipeline_schedules WHERE id=?`, saved.ID).Scan(&name, &preset); err != nil {
		t.Fatal(err)
	}
	if name != "Daily" || preset != `{"who":"Alice"}` {
		t.Fatalf("rejected update changed row: %s %s", name, preset)
	}
	in.ID = saved.ID
	in.Name = "Renamed"
	if got, err := s.SaveValidated(t.Context(), in); err != nil || got.Name != "Renamed" {
		t.Fatalf("metadata update = %+v, %v", got, err)
	}
	bad.Enabled = false
	if got, err := s.SaveValidated(t.Context(), bad); err != nil || got.Enabled {
		t.Fatalf("disable with stale preset = %+v, %v", got, err)
	}
}

func TestValidatedScheduleActivationRechecksDraftAndPreservesFailedDraft(t *testing.T) {
	db := openStoreTestDB(t)
	t.Cleanup(func() { _ = db.Close() })
	seedPipeline(t, db, "recipe", "recipe")
	s := NewScheduleStore(db)
	in := SaveScheduleInput{WorkspaceID: "ws_test", Name: "Draft", TargetPipelineID: "recipe", CronExpr: "0 9 * * *", Timezone: "UTC", Enabled: true, Activation: TriggerActivationDraft, Inputs: map[string]any{"who": "Alice"}}
	draft, err := s.SaveValidated(t.Context(), in)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE pipelines SET definition_json=? WHERE id='recipe'`, presetGateV2); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ActivateValidated(t.Context(), draft.ID); !errors.Is(err, ErrInvalidTrigger) {
		t.Fatalf("activation error = %v", err)
	}
	var enabled bool
	var activation string
	if err := db.QueryRow(`SELECT enabled,activation FROM pipeline_schedules WHERE id=?`, draft.ID).Scan(&enabled, &activation); err != nil {
		t.Fatal(err)
	}
	if enabled || activation != TriggerActivationDraft {
		t.Fatalf("failed activation changed draft: %v %q", enabled, activation)
	}
	if _, err := db.Exec(`UPDATE pipelines SET definition_json=? WHERE id='recipe'`, presetGateV1); err != nil {
		t.Fatal(err)
	}
	got, err := s.ActivateValidated(t.Context(), draft.ID)
	if err != nil || !got.Enabled || got.Activation != "" {
		t.Fatalf("activate = %+v, %v", got, err)
	}
	if _, err := s.ActivateValidated(t.Context(), draft.ID); !errors.Is(err, ErrScheduleNotDraft) {
		t.Fatalf("repeat activation = %v", err)
	}
	if _, err := s.ActivateValidated(t.Context(), "missing"); err == nil {
		t.Fatal("missing draft activated")
	}
}

func TestStoredSchedulePresetValidatesPinnedVersionAndStorageErrors(t *testing.T) {
	db := openStoreTestDB(t)
	t.Cleanup(func() { _ = db.Close() })
	seedPipeline(t, db, "recipe", "recipe")
	if _, err := db.Exec(`UPDATE pipelines SET definition_json=? WHERE id='recipe'`, presetGateV2); err != nil {
		t.Fatal(err)
	}
	// Only the version identity and definition are needed by this read boundary.
	if _, err := db.Exec(`DROP TABLE IF EXISTS pipeline_versions; CREATE TABLE pipeline_versions (pipeline_id TEXT,version INTEGER,definition_json TEXT); INSERT INTO pipeline_versions VALUES ('recipe',1,?)`, presetGateV1); err != nil {
		t.Fatal(err)
	}
	v := 1
	if err := validateStoredSchedulePreset(t.Context(), db, "recipe", &v, `{"who":"Alice"}`); err != nil {
		t.Fatalf("pinned preset followed changed head: %v", err)
	}
	for _, tc := range []struct {
		name, id, raw string
		version       *int
	}{
		{"missing recipe", "missing", `{}`, nil},
		{"missing version", "missing", `{}`, &v},
		{"wrong head input", "recipe", `{"who":"Alice"}`, nil},
		{"malformed inputs", "recipe", `{`, &v},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := validateStoredSchedulePreset(t.Context(), db, tc.id, tc.version, tc.raw); !errors.Is(err, ErrInvalidTrigger) {
				t.Fatalf("invalid preset error = %v", err)
			}
		})
	}
	if _, err := db.Exec(`UPDATE pipelines SET definition_json='{' WHERE id='recipe'`); err != nil {
		t.Fatal(err)
	}
	if err := validateStoredSchedulePreset(t.Context(), db, "recipe", nil, `{}`); !errors.Is(err, ErrInvalidTrigger) {
		t.Fatalf("malformed recipe = %v", err)
	}
	if _, err := db.Exec(`DROP TABLE pipelines`); err != nil {
		t.Fatal(err)
	}
	if err := validateStoredSchedulePreset(t.Context(), db, "recipe", nil, `{}`); err == nil || !strings.Contains(err.Error(), "load schedule recipe") || errors.Is(err, ErrInvalidTrigger) {
		t.Fatalf("storage error classified as caller error: %v", err)
	}
}

func TestScheduleInputsCompareSemanticObjects(t *testing.T) {
	for _, tc := range []struct {
		a, b  string
		equal bool
	}{
		{`{}`, `{}`, true}, {`{"a":1,"b":2}`, `{ "b":2, "a":1 }`, true},
		{`{"a":1}`, `{"a":"1"}`, false}, {`{`, `{}`, false}, {`{}`, `{`, false},
	} {
		if got := sameScheduleInputs(tc.a, tc.b); got != tc.equal {
			t.Errorf("%s vs %s = %v", tc.a, tc.b, got)
		}
	}
}

func TestValidatedScheduleStorageAndWriteFailuresReachCaller(t *testing.T) {
	db := openStoreTestDB(t)
	s := NewScheduleStore(db)
	if _, err := s.SaveValidated(t.Context(), SaveScheduleInput{ID: "missing"}); err == nil {
		t.Error("missing update accepted")
	}
	if _, err := s.SaveValidated(t.Context(), SaveScheduleInput{}); err == nil {
		t.Error("invalid create accepted")
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SaveValidated(context.Background(), SaveScheduleInput{}); err == nil {
		t.Error("closed store accepted save")
	}
	if _, err := s.ActivateValidated(context.Background(), "missing"); err == nil {
		t.Error("closed store accepted activation")
	}
}

func TestEnabledScheduleRechecksChangedWakeInputsAndTargetIdentity(t *testing.T) {
	db := openStoreTestDB(t)
	t.Cleanup(func() { _ = db.Close() })
	for _, id := range []string{"main", "wake"} {
		seedPipeline(t, db, id, id)
		if _, err := db.Exec(`UPDATE pipelines SET definition_json=? WHERE id=?`, presetGateV1, id); err != nil {
			t.Fatal(err)
		}
	}
	base := Schedule{Enabled: true, TargetPipelineID: "main", InputsJSON: `{"who":"Alice"}`, WakePipelineID: "wake", WakeInputsJSON: `{"who":"Alice"}`}
	for _, tc := range []struct {
		name    string
		change  func(*Schedule)
		invalid bool
	}{
		{"unchanged", func(*Schedule) {}, false},
		{"formatting only", func(s *Schedule) { s.InputsJSON = `{ "who": "Alice" }`; s.WakeInputsJSON = `{ "who": "Alice" }` }, false},
		{"changed wake inputs", func(s *Schedule) { s.WakeInputsJSON = `{}` }, true},
		{"changed wake identity", func(s *Schedule) { s.WakePipelineID = "missing" }, true},
		{"removed wake", func(s *Schedule) { s.WakePipelineID = "" }, false},
		{"changed target", func(s *Schedule) { s.TargetPipelineID = "missing" }, true},
		{"new enable", func(s *Schedule) { s.InputsJSON = `{}` }, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			after := base
			tc.change(&after)
			err := validateEnabledSchedule(t.Context(), db, &base, &after)
			if tc.invalid && !errors.Is(err, ErrInvalidTrigger) {
				t.Fatalf("invalid schedule = %v", err)
			}
			if !tc.invalid && err != nil {
				t.Fatalf("unchanged valid schedule rejected: %v", err)
			}
		})
	}
	before := base
	before.Enabled = false
	after := base
	after.WakeInputsJSON = `{}`
	if err := validateEnabledSchedule(t.Context(), db, &before, &after); !errors.Is(err, ErrInvalidTrigger) {
		t.Fatalf("enable skipped wake validation: %v", err)
	}
}
