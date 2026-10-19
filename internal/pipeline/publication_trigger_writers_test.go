package pipeline

import (
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"
)

// Snapshot every column of the owned fixture rows, including the archive and
// schedule/pending fields the failed authoring transaction must not change.
func publicationWriterRows(t *testing.T, db *sql.DB, query string, args ...any) string {
	t.Helper()
	rows, err := db.QueryContext(t.Context(), query, args...)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	columns, err := rows.Columns()
	if err != nil {
		t.Fatal(err)
	}
	var result [][]any
	for rows.Next() {
		values, destinations := make([]any, len(columns)), make([]any, len(columns))
		for i := range values {
			destinations[i] = &values[i]
		}
		if err := rows.Scan(destinations...); err != nil {
			t.Fatal(err)
		}
		result = append(result, values)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	return string(encoded)
}

func publicationWriterState(t *testing.T, db *sql.DB, pipelineID, triggerTable string) [3]string {
	t.Helper()
	if triggerTable != "pipeline_schedules" && triggerTable != "pending_runs" {
		t.Fatal("unexpected fixture table")
	}
	return [3]string{
		publicationWriterRows(t, db, `SELECT * FROM pipelines WHERE id=?`, pipelineID),
		publicationWriterRows(t, db, `SELECT * FROM pipeline_versions WHERE pipeline_id=? ORDER BY version`, pipelineID),
		publicationWriterRows(t, db, `SELECT * FROM `+triggerTable+` WHERE `+map[string]string{"pipeline_schedules": "target_pipeline_id", "pending_runs": "pipeline_id"}[triggerTable]+`=? ORDER BY id`, pipelineID),
	}
}

func TestPublicationInlineTriggerRetainedBoundedPinRollsBack(t *testing.T) {
	db := openScheduleTestDB(t)
	t.Cleanup(func() { _ = db.Close() })
	s := NewStore(db)
	in := validSaveInput("retained-published-pin")
	trigger := &TriggerInput{Kind: TriggerKindSchedule, Name: "original schedule", CronExpr: "0 9 * * *", Timezone: "UTC"}
	p, schedule, err := s.SaveWithTrigger(t.Context(), in, trigger)
	if err != nil || schedule == nil || !schedule.Enabled || schedule.TargetPipelineVersion != nil {
		t.Fatalf("legacy inline calibration: %+v %v", schedule, err)
	}
	bounded := guardPublishedVersion(t, s, p)
	// A negative stored target marker, kept inert before the attempted re-enable.
	if _, err := db.ExecContext(t.Context(), `UPDATE pipeline_schedules SET target_pipeline_version=?,enabled=0 WHERE id=?`, bounded, schedule.ID); err != nil {
		t.Fatal(err)
	}
	before := publicationWriterState(t, db, p.ID, "pipeline_schedules")
	in.Name, in.Description = "must roll back routine", "must roll back description"
	in.DefinitionJSON += "\n" // Valid new bytes force a new LEGACY archive, not bounded dedup.
	trigger.Name, trigger.CronExpr = "must roll back schedule", "0 10 * * *"
	if _, _, err := s.SaveWithTrigger(t.Context(), in, trigger); !errors.Is(err, ErrInvalidTrigger) {
		t.Fatalf("retained bounded pin enabled by inline save: %v", err)
	}
	if after := publicationWriterState(t, db, p.ID, "pipeline_schedules"); after != before {
		t.Fatalf("refused inline pin changed routine, archive or schedule")
	}
}

func TestPublicationInlineTriggerRetainedBoundedWakeRollsBack(t *testing.T) {
	db := openScheduleTestDB(t)
	t.Cleanup(func() { _ = db.Close() })
	s := NewStore(db)
	in := validSaveInput("retained-published-wake")
	trigger := &TriggerInput{Kind: TriggerKindSchedule, Name: "original schedule", CronExpr: "0 9 * * *", Timezone: "UTC"}
	p, schedule, err := s.SaveWithTrigger(t.Context(), in, trigger)
	if err != nil || schedule == nil || !schedule.Enabled {
		t.Fatalf("legacy inline calibration: %+v %v", schedule, err)
	}
	wake, err := s.Save(t.Context(), validSaveInput("bounded-retained-wake"))
	if err != nil {
		t.Fatal(err)
	}
	guardPublishedVersion(t, s, wake)
	if _, err := db.ExecContext(t.Context(), `UPDATE pipelines SET head_version=2 WHERE id=?`, wake.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(t.Context(), `UPDATE pipeline_schedules SET wake_pipeline_id=?,enabled=0 WHERE id=?`, wake.ID, schedule.ID); err != nil {
		t.Fatal(err)
	}
	before := publicationWriterState(t, db, p.ID, "pipeline_schedules")
	in.Name, in.Description = "must roll back routine", "must roll back description"
	in.DefinitionJSON += "\n"
	trigger.Name, trigger.CronExpr = "must roll back schedule", "0 10 * * *"
	if _, _, err := s.SaveWithTrigger(t.Context(), in, trigger); !errors.Is(err, ErrInvalidTrigger) {
		t.Fatalf("retained bounded wake enabled by inline save: %v", err)
	}
	if after := publicationWriterState(t, db, p.ID, "pipeline_schedules"); after != before {
		t.Fatalf("refused inline wake changed routine, archive or schedule")
	}
}

func TestPublicationOnceTriggerRejectsBoundedSelectedHead(t *testing.T) {
	s, db := oneTimeAuthoringStore(t)
	in := validSaveInput("bounded-once-consumer")
	trigger := &TriggerInput{Kind: TriggerKindOnce, FireAt: s.now().Add(time.Hour)}
	p, schedule, err := s.SaveWithTrigger(t.Context(), in, trigger)
	if err != nil || schedule != nil {
		t.Fatalf("legacy once calibration: %+v %v", schedule, err)
	}
	var originalPin int
	if err := db.QueryRowContext(t.Context(), `SELECT pinned_version FROM pending_runs WHERE pipeline_id=?`, p.ID).Scan(&originalPin); err != nil || originalPin != 1 {
		t.Fatalf("legacy once pin: %d %v", originalPin, err)
	}
	guardPublishedVersion(t, s, p)
	if _, err := db.ExecContext(t.Context(), `UPDATE pipelines SET head_version=2 WHERE id=?`, p.ID); err != nil {
		t.Fatal(err)
	}
	before := publicationWriterState(t, db, p.ID, "pending_runs")
	trigger.FireAt = trigger.FireAt.Add(time.Hour)
	// This exercises the actual lower writer, not whole SaveWithTrigger after
	// trusted authoring has selected a new legacy authority.
	err = func() error {
		tx, err := db.BeginTx(t.Context(), nil)
		if err != nil {
			return err
		}
		defer tx.Rollback()
		if _, err := tx.ExecContext(t.Context(), `UPDATE pipelines SET name='must roll back lower writer' WHERE id=?`, p.ID); err != nil {
			return err
		}
		if _, err := s.createTriggerTx(t.Context(), tx, in, p.ID, trigger); err != nil {
			return err
		}
		return tx.Commit()
	}()
	if !errors.Is(err, ErrInvalidTrigger) {
		t.Fatalf("bounded selected HEAD reached lower once writer: %v", err)
	}
	if after := publicationWriterState(t, db, p.ID, "pending_runs"); after != before {
		t.Fatalf("refused lower once writer changed routine, archive or pending start")
	}
}

func TestPublicationOnceSaveSelectsLegacyAuthority(t *testing.T) {
	s, db := oneTimeAuthoringStore(t)
	in := validSaveInput("trusted-once-authoring")
	trigger := &TriggerInput{Kind: TriggerKindOnce, FireAt: s.now().Add(time.Hour)}
	p, _, err := s.SaveWithTrigger(t.Context(), in, trigger)
	if err != nil {
		t.Fatalf("legacy once calibration: %v", err)
	}
	// New bounded content has no equal legacy archive. Trusted Save must create
	// its own legacy identity, rather than adopting this stored fingerprint.
	raw := p.DefinitionJSON + "\n"
	if _, err := db.ExecContext(t.Context(), `INSERT INTO pipeline_versions(id,pipeline_id,version,definition_json,definition_hash,author_type,author_id,authority_fingerprint,publication_authority_json) VALUES(?,?,2,?,?,'user','user_test',?,'{}')`, p.ID+"-once-bounded", p.ID, raw, definitionHash(raw), strings.Repeat("b", 64)); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(t.Context(), `UPDATE pipelines SET head_version=2,definition_json=?,definition_hash=? WHERE id=?`, raw, definitionHash(raw), p.ID); err != nil {
		t.Fatal(err)
	}
	boundedBefore := publicationWriterRows(t, db, `SELECT * FROM pipeline_versions WHERE pipeline_id=? AND version=2`, p.ID)
	in.DefinitionJSON = raw
	trigger.FireAt = trigger.FireAt.Add(time.Hour)
	if _, _, err := s.SaveWithTrigger(t.Context(), in, trigger); err != nil {
		t.Fatalf("trusted legacy authoring refused: %v", err)
	}
	var pin, head int
	var fingerprint string
	var noManifest bool
	if err := db.QueryRowContext(t.Context(), `SELECT r.pinned_version,p.head_version,v.authority_fingerprint,v.publication_authority_json IS NULL FROM pending_runs r JOIN pipelines p ON p.id=r.pipeline_id JOIN pipeline_versions v ON v.pipeline_id=p.id AND v.version=r.pinned_version WHERE r.pipeline_id=?`, p.ID).Scan(&pin, &head, &fingerprint, &noManifest); err != nil {
		t.Fatal(err)
	}
	if pin == 2 || pin != head || fingerprint != "legacy-trusted:v1" || !noManifest {
		t.Fatalf("trusted once adopted bounded identity: pin=%d head=%d fingerprint=%q no_manifest=%t", pin, head, fingerprint, noManifest)
	}
	if after := publicationWriterRows(t, db, `SELECT * FROM pipeline_versions WHERE pipeline_id=? AND version=2`, p.ID); after != boundedBefore {
		t.Fatal("trusted authoring rewrote bounded archive")
	}
}
