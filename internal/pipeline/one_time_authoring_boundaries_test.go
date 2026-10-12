package pipeline

import (
	"database/sql"
	"errors"
	"testing"
	"time"
)

func oneTimeAuthoringStore(t *testing.T) (*Store, *sql.DB) {
	t.Helper()
	db := openVersioningTestDB(t)
	t.Cleanup(func() { _ = db.Close() })
	_, err := db.Exec(`CREATE TABLE pending_runs (
 id TEXT PRIMARY KEY,workspace_id TEXT,pipeline_id TEXT,pipeline_slug TEXT,
 inputs_json TEXT,tags_json TEXT,metadata_json TEXT,priority INTEGER,
 fire_at TEXT,invoking_user_id TEXT,triggered_via TEXT,triggered_by_id TEXT,
 pinned_version INTEGER,status TEXT,created_at TEXT,updated_at TEXT,
 dispatch_attempts INTEGER NOT NULL DEFAULT 0,last_error TEXT NOT NULL DEFAULT '',next_attempt_at TEXT,fired_run_id TEXT)`)
	if err != nil {
		t.Fatal(err)
	}
	s := NewStore(db)
	s.now = func() time.Time { return time.Date(2026, 10, 3, 12, 0, 0, 0, time.UTC) }
	return s, db
}

func TestOneTimeAuthoringPinsAndUpdatesOneDurableStart(t *testing.T) {
	s, db := oneTimeAuthoringStore(t)
	in := validSaveInput("one-time")
	trigger := &TriggerInput{Kind: TriggerKindOnce, FireAt: s.now().Add(time.Hour)}
	p, schedule, err := s.SaveWithTrigger(t.Context(), in, trigger)
	if err != nil || schedule != nil {
		t.Fatalf("one-time save = %+v, %v", schedule, err)
	}
	var id, status, inputs string
	var pin int
	if err := db.QueryRow(`SELECT id,status,inputs_json,pinned_version FROM pending_runs`).Scan(&id, &status, &inputs, &pin); err != nil {
		t.Fatal(err)
	}
	if id != "pnd_once_"+p.ID || status != "pending" || inputs != "{}" || pin != 1 {
		t.Fatalf("invalid one-time receipt: %s %s %s %d", id, status, inputs, pin)
	}
	in.DefinitionJSON = `{"name":"one-time","steps":[{"id":"updated","type":"transform","transform":{"input":"new","expression":"."}}]}`
	trigger.FireAt = trigger.FireAt.Add(time.Hour)
	if _, _, err := s.SaveWithTrigger(t.Context(), in, trigger); err != nil {
		t.Fatal(err)
	}
	var count int
	if err := db.QueryRow(`SELECT count(*),max(pinned_version) FROM pending_runs`).Scan(&count, &pin); err != nil || count != 1 || pin != 2 {
		t.Fatalf("duplicate or stale start: count=%d pin=%d err=%v", count, pin, err)
	}
	if _, err := db.Exec(`UPDATE pending_runs SET status='fired' WHERE id=?`, id); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.SaveWithTrigger(t.Context(), in, trigger); !errors.Is(err, ErrInvalidTrigger) {
		t.Fatalf("same fired start rearmed: %v", err)
	}
	trigger.FireAt = trigger.FireAt.Add(time.Hour)
	if _, _, err := s.SaveWithTrigger(t.Context(), in, trigger); err != nil {
		t.Fatalf("new date after fired start refused: %v", err)
	}
	if _, err := db.Exec(`UPDATE pending_runs SET status='cancelled' WHERE id=?`, id); err != nil {
		t.Fatal(err)
	}
	trigger.FireAt = trigger.FireAt.Add(time.Hour)
	if _, _, err := s.SaveWithTrigger(t.Context(), in, trigger); !errors.Is(err, ErrInvalidTrigger) {
		t.Fatalf("cancelled start rearmed by editing: %v", err)
	}
}

func TestOneTimeAuthoringRefusalsRollBackRoutineAndVersion(t *testing.T) {
	for _, tc := range []struct {
		name string
		edit func(*SaveInput, *TriggerInput, time.Time)
	}{
		{"past date", func(_ *SaveInput, tr *TriggerInput, now time.Time) { tr.FireAt = now.Add(-time.Second) }},
		{"unbounded date", func(_ *SaveInput, tr *TriggerInput, now time.Time) { tr.FireAt = now.AddDate(3, 0, 0) }},
		{"draft activation", func(_ *SaveInput, tr *TriggerInput, _ time.Time) { tr.Activation = TriggerActivationDraft }},
		{"unapproved recipe", func(in *SaveInput, _ *TriggerInput, _ time.Time) { in.Status = "proposed" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, db := oneTimeAuthoringStore(t)
			in := validSaveInput("one-time-invalid")
			tr := &TriggerInput{Kind: TriggerKindOnce, FireAt: s.now().Add(time.Hour)}
			tc.edit(&in, tr, s.now())
			if _, _, err := s.SaveWithTrigger(t.Context(), in, tr); !errors.Is(err, ErrInvalidTrigger) {
				t.Fatalf("invalid start accepted: %v", err)
			}
			for _, table := range []string{"pipelines", "pipeline_versions", "pending_runs"} {
				var count int
				if err := db.QueryRow(`SELECT count(*) FROM ` + table).Scan(&count); err != nil || count != 0 {
					t.Fatalf("refused save left %s rows: %d, %v", table, count, err)
				}
			}
		})
	}
}

func TestOneTimeAuthoringStorageRefusalRollsBackPublishedRecipe(t *testing.T) {
	s, db := oneTimeAuthoringStore(t)
	if _, err := db.Exec(`DROP TABLE pending_runs`); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.SaveWithTrigger(t.Context(), validSaveInput("one-time-storage"), &TriggerInput{Kind: TriggerKindOnce, FireAt: s.now().Add(time.Hour)}); err == nil || errors.Is(err, ErrInvalidTrigger) {
		t.Fatalf("storage failure misclassified: %v", err)
	}
	var count int
	if err := db.QueryRow(`SELECT count(*) FROM pipelines`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("failed pending write published recipe: %d, %v", count, err)
	}
}

// A capacity retry has already accepted its recipe. Re-saving one-time
// authoring must not reset attempts, replace inputs or repin it to new HEAD.
func TestOneTimeAuthoringDoesNotRewriteAnAttemptedStart(t *testing.T) {
	s, db := oneTimeAuthoringStore(t)
	in := validSaveInput("attempted-one-time")
	in.DefinitionJSON = `{"name":"attempted-one-time","steps":[{"id":"result","type":"transform","transform":{"input":"original","expression":"."}}]}`
	trigger := &TriggerInput{Kind: TriggerKindOnce, FireAt: s.now().Add(time.Hour)}
	p, _, err := s.SaveWithTrigger(t.Context(), in, trigger)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE pending_runs SET dispatch_attempts=1,last_error='Waiting for capacity.' WHERE id=?`, "pnd_once_"+p.ID); err != nil {
		t.Fatal(err)
	}
	in.DefinitionJSON = `{"name":"attempted-one-time","steps":[{"id":"result","type":"transform","transform":{"input":"replacement","expression":"."}}]}`
	if _, _, err := s.SaveWithTrigger(t.Context(), in, trigger); !errors.Is(err, ErrInvalidTrigger) {
		t.Fatalf("attempted receipt rewritten: %v", err)
	}
	var pin, attempts int
	if err := db.QueryRow(`SELECT pinned_version,dispatch_attempts FROM pending_runs WHERE id=?`, "pnd_once_"+p.ID).Scan(&pin, &attempts); err != nil || pin != 1 || attempts != 1 {
		t.Fatalf("accepted pin/attempts changed: pin=%d attempts=%d err=%v", pin, attempts, err)
	}
	saved, err := s.GetByID(t.Context(), p.ID)
	if err != nil || saved.DefinitionJSON != p.DefinitionJSON {
		t.Fatalf("refused authoring was partially committed: %+v %v", saved, err)
	}
}
