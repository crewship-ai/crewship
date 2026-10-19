package pipeline

import (
	"errors"
	"strings"
	"testing"
)

// This negative classification fixture is not a valid publication or runtime
// admission. The producer's canonical-manifest tests establish that separately.
func guardPublishedVersion(t *testing.T, store *Store, p *Pipeline) int {
	t.Helper()
	if _, err := store.db.Exec(`INSERT INTO pipeline_versions(id,pipeline_id,version,definition_json,definition_hash,author_type,author_id,authority_fingerprint,publication_authority_json) VALUES(?,?,2,?,?,'user','user_test',?,'{}')`, p.ID+"-bounded", p.ID, p.DefinitionJSON, p.DefinitionHash, strings.Repeat("a", 64)); err != nil {
		t.Fatal(err)
	}
	return 2
}

func TestGenericPublicationGuardSelectsExactVersion(t *testing.T) {
	db := openStoreTestDB(t)
	t.Cleanup(func() { _ = db.Close() })
	store := NewStore(db)
	p, err := store.Save(t.Context(), validSaveInput("selected"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := NewExecutor(store, NewResolver(db), newMockRunner(), nil).Run(t.Context(), RunInput{PipelineID: p.ID, WorkspaceID: p.WorkspaceID, Mode: ModeRun}); err != nil {
		t.Fatalf("legacy positive: %v", err)
	}
	bounded := guardPublishedVersion(t, store, p)
	exec := NewExecutor(store, NewResolver(db), newMockRunner(), nil)
	if _, err := exec.Run(t.Context(), RunInput{PipelineID: p.ID, WorkspaceID: p.WorkspaceID, PinnedVersion: &bounded, Mode: ModeRun}); !errors.Is(err, ErrPublicationGenericRun) {
		t.Fatalf("bounded pin reached generic run: %v", err)
	}
	if _, err := db.Exec(`UPDATE pipelines SET head_version=2 WHERE id=?`, p.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := exec.Run(t.Context(), RunInput{PipelineID: p.ID, WorkspaceID: p.WorkspaceID, Mode: ModeRun}); !errors.Is(err, ErrPublicationGenericRun) {
		t.Fatalf("bounded HEAD reached generic run: %v", err)
	}
	legacy := 1
	if _, err := exec.Run(t.Context(), RunInput{PipelineID: p.ID, WorkspaceID: p.WorkspaceID, PinnedVersion: &legacy, Mode: ModeRun}); err != nil {
		t.Fatalf("legacy pin followed bounded HEAD: %v", err)
	}
}

func TestGenericPublicationGuardRejectsDifferentWorkspace(t *testing.T) {
	db := openStoreTestDB(t)
	t.Cleanup(func() { _ = db.Close() })
	store := NewStore(db)
	p, err := store.Save(t.Context(), validSaveInput("workspace-boundary"))
	if err != nil {
		t.Fatal(err)
	}
	runner := newMockRunner()
	_, err = NewExecutor(store, NewResolver(db), runner, nil).Run(t.Context(), RunInput{
		PipelineID: p.ID, WorkspaceID: "another-workspace", Mode: ModeRun,
	})
	if !errors.Is(err, ErrNotFound) || len(runner.calls) != 0 {
		t.Fatalf("recipe crossed workspace boundary: err=%v calls=%d", err, len(runner.calls))
	}
}

func TestGenericPublicationGuardPrewarmAndNested(t *testing.T) {
	db := openStoreTestDB(t)
	t.Cleanup(func() { _ = db.Close() })
	store := NewStore(db)
	in := validSaveInput("child")
	in.DefinitionJSON = `{"name":"child","steps":[{"id":"a","type":"agent_run","agent_slug":"agent_lead","prompt":"hi"}]}`
	child, err := store.Save(t.Context(), in)
	if err != nil {
		t.Fatal(err)
	}
	guardPublishedVersion(t, store, child)
	if _, err := db.Exec(`UPDATE pipelines SET head_version=2 WHERE id=?`, child.ID); err != nil {
		t.Fatal(err)
	}
	runner := &prewarmRunner{}
	exec := NewExecutor(store, NewResolver(db), runner, nil)
	exec.PrewarmForRun(t.Context(), child.ID, child.WorkspaceID)
	if len(runner.warmed()) != 0 {
		t.Fatal("bounded publication prewarmed generic container")
	}
	outer := &DSL{Name: "outer", Steps: []Step{{ID: "call", Type: StepCallPipeline, PipelineSlug: "child"}}}
	nestedRunner := newMockRunner()
	result, err := NewExecutor(store, NewResolver(db), nestedRunner, nil).RunDefinition(t.Context(), outer, RunInput{WorkspaceID: child.WorkspaceID, AuthorCrewID: "crew_a", Mode: ModeRun})
	if err != nil {
		t.Fatalf("nested fixture failed before recorded refusal: %v", err)
	}
	if result == nil || result.Status != "FAILED" || !strings.Contains(result.ErrorMessage, ErrPublicationGenericRun.Error()) || len(nestedRunner.calls) != 0 {
		t.Fatalf("nested publication not refused: %+v %v", result, err)
	}
	if _, err := exec.RunDefinition(t.Context(), outer, RunInput{PipelineID: child.ID, WorkspaceID: child.WorkspaceID, AuthorCrewID: "crew_a", Mode: ModeRun}); !errors.Is(err, ErrPublicationGenericRun) {
		t.Fatalf("direct DSL bypass: %v", err)
	}
}

func TestGenericPublicationGuardTriggerWritesRollBack(t *testing.T) {
	t.Setenv("ENCRYPTION_KEY", strings.Repeat("12", 32))
	db := openStoreTestDB(t)
	t.Cleanup(func() { _ = db.Close() })
	if _, err := db.Exec(hashedWebhookSchemaSQL); err != nil {
		t.Fatal(err)
	}
	store := NewStore(db)
	p, err := store.Save(t.Context(), validSaveInput("trigger-target"))
	if err != nil {
		t.Fatal(err)
	}
	bounded := guardPublishedVersion(t, store, p)
	schedules := NewScheduleStore(db)
	in := SaveScheduleInput{WorkspaceID: p.WorkspaceID, Name: "kept", TargetPipelineID: p.ID, CronExpr: "0 9 * * *", Timezone: "UTC", Enabled: true}
	saved, err := schedules.SaveValidated(t.Context(), in)
	if err != nil {
		t.Fatalf("legacy schedule positive: %v", err)
	}
	in.ID, in.Name, in.TargetPipelineVersion = saved.ID, "must roll back", &bounded
	if _, err := schedules.SaveValidated(t.Context(), in); !errors.Is(err, ErrInvalidTrigger) {
		t.Fatalf("bounded schedule: %v", err)
	}
	got, err := schedules.GetByID(t.Context(), saved.ID)
	if err != nil || got.Name != "kept" || got.TargetPipelineVersion != nil {
		t.Fatalf("schedule rollback: %+v %v", got, err)
	}
	webhooks := NewWebhookStore(db)
	wi := SaveWebhookInput{WorkspaceID: p.WorkspaceID, Name: "kept", TargetPipelineID: p.ID, Enabled: true}
	wh, err := webhooks.Save(t.Context(), wi)
	if err != nil {
		t.Fatalf("legacy webhook positive: %v", err)
	}
	wi.ID, wi.Name, wi.TargetPipelineVersion = wh.ID, "must roll back", &bounded
	if _, err := webhooks.Save(t.Context(), wi); !errors.Is(err, ErrInvalidTrigger) {
		t.Fatalf("bounded webhook: %v", err)
	}
	gotWH, err := webhooks.GetByID(t.Context(), wh.ID)
	if err != nil || gotWH.Name != "kept" || gotWH.TargetPipelineVersion != nil {
		t.Fatalf("webhook rollback: %+v %v", gotWH, err)
	}
	wake, err := store.Save(t.Context(), validSaveInput("bounded-wake"))
	if err != nil {
		t.Fatal(err)
	}
	guardPublishedVersion(t, store, wake)
	if _, err := db.Exec(`UPDATE pipelines SET head_version=2 WHERE id=?`, wake.ID); err != nil {
		t.Fatal(err)
	}
	in.Name, in.TargetPipelineVersion, in.WakePipelineID = "must roll back wake", nil, wake.ID
	if _, err := schedules.SaveValidated(t.Context(), in); !errors.Is(err, ErrInvalidTrigger) {
		t.Fatalf("bounded wake: %v", err)
	}
	got, err = schedules.GetByID(t.Context(), saved.ID)
	if err != nil || got.Name != "kept" || got.WakePipelineID != "" {
		t.Fatalf("wake rollback: %+v %v", got, err)
	}
	in.WakePipelineID = ""
	if _, err := db.Exec(`UPDATE pipelines SET head_version=2 WHERE id=?`, p.ID); err != nil {
		t.Fatal(err)
	}
	in.ID, in.TargetPipelineVersion = "", nil
	if _, err := schedules.SaveValidated(t.Context(), in); !errors.Is(err, ErrInvalidTrigger) {
		t.Fatalf("bounded schedule create: %v", err)
	}
	wi.ID, wi.TargetPipelineVersion = "", nil
	if _, err := webhooks.Save(t.Context(), wi); !errors.Is(err, ErrInvalidTrigger) {
		t.Fatalf("bounded webhook create: %v", err)
	}
	var n int
	if err := db.QueryRow(`SELECT (SELECT count(*) FROM pipeline_schedules)+(SELECT count(*) FROM pipeline_webhooks)`).Scan(&n); err != nil || n != 2 {
		t.Fatalf("refused create changed rows: %d %v", n, err)
	}
}

func TestGenericPublicationGuardStorageAndSnapshot(t *testing.T) {
	db := openStoreTestDB(t)
	t.Cleanup(func() { _ = db.Close() })
	store := NewStore(db)
	p, err := store.Save(t.Context(), validSaveInput("snapshot"))
	if err != nil {
		t.Fatal(err)
	}
	p.DefinitionJSON += " "
	if err := store.CheckGenericRun(t.Context(), p, nil); !errors.Is(err, ErrGenericRecipeChanged) {
		t.Fatalf("stale body accepted: %v", err)
	}
	if _, err := db.Exec(`DROP TABLE pipeline_versions`); err != nil {
		t.Fatal(err)
	}
	p.DefinitionJSON = strings.TrimSpace(p.DefinitionJSON)
	if err := store.CheckGenericRun(t.Context(), p, nil); err == nil || errors.Is(err, ErrPublicationGenericRun) {
		t.Fatalf("storage error became legacy/refusal: %v", err)
	}
}

func TestGenericPublicationGuardResume(t *testing.T) {
	db := openResumeTestDB(t)
	t.Cleanup(func() { _ = db.Close() })
	store := NewStore(db)
	p, err := store.Save(t.Context(), validSaveInput("resume-selected"))
	if err != nil {
		t.Fatal(err)
	}
	exec := NewExecutor(store, NewResolver(db), newMockRunner(), nil).WithRunStore(NewRunStore(db))
	rec := &RunRecord{ID: "old-run", WorkspaceID: p.WorkspaceID, PipelineID: p.ID, Mode: ModeRun, DefinitionHash: p.DefinitionHash}
	if _, reason := exec.buildResumePlan(t.Context(), rec); reason != "" {
		t.Fatalf("legacy resume positive: %s", reason)
	}
	bounded := guardPublishedVersion(t, store, p)
	rec.PipelineVersion = &bounded
	if plan, reason := exec.buildResumePlan(t.Context(), rec); plan != nil || !strings.Contains(reason, ErrPublicationGenericRun.Error()) {
		t.Fatalf("bounded pin resumed: %+v %q", plan, reason)
	}
	if _, err := db.Exec(`UPDATE pipelines SET head_version=2 WHERE id=?`, p.ID); err != nil {
		t.Fatal(err)
	}
	rec.PipelineVersion = nil
	if plan, reason := exec.buildResumePlan(t.Context(), rec); plan != nil || !strings.Contains(reason, ErrPublicationGenericRun.Error()) {
		t.Fatalf("bounded HEAD resumed: %+v %q", plan, reason)
	}
}

func TestGenericPublicationGuardLegacyHeadZero(t *testing.T) {
	db := openStoreTestDB(t)
	t.Cleanup(func() { _ = db.Close() })
	store := NewStore(db)
	p, err := store.Save(t.Context(), validSaveInput("head-zero"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`DELETE FROM pipeline_versions WHERE pipeline_id=?; UPDATE pipelines SET head_version=0 WHERE id=?`, p.ID, p.ID); err != nil {
		t.Fatal(err)
	}
	if err := store.CheckGenericRun(t.Context(), p, nil); err != nil {
		t.Fatalf("legacy HEAD0 denied: %v", err)
	}
	if _, err := db.Exec(`UPDATE pipelines SET head_version=1 WHERE id=?`, p.ID); err != nil {
		t.Fatal(err)
	}
	if err := store.CheckGenericRun(t.Context(), p, nil); !errors.Is(err, ErrPinnedVersionNotFound) {
		t.Fatalf("missing archive became legacy: %v", err)
	}
}

func TestGenericPublicationGuardReadsWriterSnapshot(t *testing.T) {
	db := openStoreTestDB(t)
	t.Cleanup(func() { _ = db.Close() })
	store := NewStore(db)
	p, err := store.Save(t.Context(), validSaveInput("writer-snapshot"))
	if err != nil {
		t.Fatal(err)
	}
	guardPublishedVersion(t, store, p)
	tx, err := db.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(t.Context(), `UPDATE pipelines SET head_version=2 WHERE id=?`, p.ID); err != nil {
		t.Fatal(err)
	}
	if err := checkGenericTrigger(t.Context(), tx, p.WorkspaceID, p.ID, nil); !errors.Is(err, ErrInvalidTrigger) {
		t.Fatalf("writer snapshot ignored own publication: %v", err)
	}
	if err := tx.Rollback(); err != nil {
		t.Fatal(err)
	}
	if err := store.CheckGenericRun(t.Context(), p, nil); err != nil {
		t.Fatalf("rolled-back publication changed legacy selection: %v", err)
	}
}

// Disabling an existing unchanged selection removes execution capacity even if
// its recipe has since become bounded. A disabled write must not introduce a
// new target, pin or wake selection, nor bypass the ordinary enable gate.
func TestGenericPublicationScheduleSafeDisable(t *testing.T) {
	for _, tc := range []struct {
		name    string
		change  func(*SaveScheduleInput, *Pipeline, int)
		allowed bool
	}{
		{"unchanged selection", func(in *SaveScheduleInput, _ *Pipeline, _ int) {}, true},
		{"new disabled schedule", func(in *SaveScheduleInput, _ *Pipeline, _ int) { in.ID = "" }, false},
		{"retarget while disabling", func(in *SaveScheduleInput, other *Pipeline, _ int) { in.TargetPipelineID = other.ID }, false},
		{"new pin while disabling", func(in *SaveScheduleInput, _ *Pipeline, pin int) { in.TargetPipelineVersion = &pin }, false},
		{"new wake while disabling", func(in *SaveScheduleInput, other *Pipeline, _ int) { in.WakePipelineID = other.ID }, false},
		{"enable bounded selection", func(in *SaveScheduleInput, _ *Pipeline, _ int) { in.Enabled = true }, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db := openStoreTestDB(t)
			t.Cleanup(func() { _ = db.Close() })
			store := NewStore(db)
			target, err := store.Save(t.Context(), validSaveInput("disable-target"))
			if err != nil {
				t.Fatal(err)
			}
			other, err := store.Save(t.Context(), validSaveInput("disable-other"))
			if err != nil {
				t.Fatal(err)
			}
			schedules := NewScheduleStore(db)
			in := SaveScheduleInput{WorkspaceID: target.WorkspaceID, Name: "Original", TargetPipelineID: target.ID, CronExpr: "0 9 * * *", Timezone: "UTC", Enabled: true}
			before, err := schedules.SaveValidated(t.Context(), in)
			if err != nil {
				t.Fatalf("legacy calibration: %v", err)
			}
			pin := guardPublishedVersion(t, store, target)
			guardPublishedVersion(t, store, other)
			if _, err := db.Exec(`UPDATE pipelines SET head_version=2 WHERE id IN (?,?)`, target.ID, other.ID); err != nil {
				t.Fatal(err)
			}
			in.ID, in.Enabled, in.Name = before.ID, false, "Changed"
			tc.change(&in, other, pin)
			got, err := schedules.SaveValidated(t.Context(), in)
			if tc.allowed {
				if err != nil || got.Enabled {
					t.Fatalf("safe disable: %+v, %v", got, err)
				}
				return
			}
			if !errors.Is(err, ErrInvalidTrigger) {
				t.Fatalf("unsafe disabled/enable write: %+v, %v", got, err)
			}
			persisted, err := schedules.GetByID(t.Context(), before.ID)
			if err != nil {
				t.Fatal(err)
			}
			if !persisted.Enabled || persisted.Name != before.Name || persisted.TargetPipelineID != before.TargetPipelineID || persisted.TargetPipelineVersion != nil || persisted.WakePipelineID != "" {
				t.Fatalf("refused write changed original schedule: %+v", persisted)
			}
			var count int
			if err := db.QueryRow(`SELECT count(*) FROM pipeline_schedules`).Scan(&count); err != nil || count != 1 {
				t.Fatalf("refused write persisted extra schedule: %d, %v", count, err)
			}
		})
	}
}
