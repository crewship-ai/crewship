package pipeline

import (
	"errors"
	"strings"
	"testing"
)

// Negative classification fixture; publication admission has separate canonical tests.
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
	if _, err := db.Exec(`UPDATE pipelines SET head_version=1 WHERE id=?`, p.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := exec.Run(t.Context(), RunInput{PipelineID: p.ID, WorkspaceID: p.WorkspaceID, PinnedVersion: &bounded, Mode: ModeRun}); !errors.Is(err, ErrPublicationGenericRun) {
		t.Fatalf("bounded pin followed legacy HEAD: %v", err)
	}
	if _, err := db.Exec(`UPDATE pipelines SET head_version=2 WHERE id=?`, p.ID); err != nil {
		t.Fatal(err)
	}
	legacy := 1
	snapshot, err := store.loadGenericSelected(t.Context(), p.ID, p.WorkspaceID, &legacy)
	if err != nil || snapshot.DefinitionJSON != p.DefinitionJSON {
		t.Fatalf("pin lost archived body: %v", err)
	}
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

func TestGenericPublicationGuardNested(t *testing.T) {
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
	outer := &DSL{Name: "outer", Steps: []Step{{ID: "call", Type: StepCallPipeline, PipelineSlug: "child"}}}
	nestedRunner := newMockRunner()
	result, err := NewExecutor(store, NewResolver(db), nestedRunner, nil).RunDefinition(t.Context(), outer, RunInput{WorkspaceID: child.WorkspaceID, AuthorCrewID: "crew_a", Mode: ModeRun})
	if err != nil {
		t.Fatalf("nested fixture failed before recorded refusal: %v", err)
	}
	if result == nil || result.Status != "FAILED" || !strings.Contains(result.ErrorMessage, ErrPublicationGenericRun.Error()) || len(nestedRunner.calls) != 0 {
		t.Fatalf("nested publication not refused: %+v %v", result, err)
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

func TestGenericPublicationTrustedSaveRestoresLegacyBody(t *testing.T) {
	db := openStoreTestDB(t)
	t.Cleanup(func() { _ = db.Close() })
	store := NewStore(db)
	in := validSaveInput("trusted-rewrite")
	p, err := store.Save(t.Context(), in)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(`UPDATE pipelines SET publication_authority_fingerprint=? WHERE id=?`, strings.Repeat("b", 64), p.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = store.loadGenericSelected(t.Context(), p.ID, p.WorkspaceID, nil); !errors.Is(err, ErrPublicationGenericRun) {
		t.Fatal("bounded marker was ignored", err)
	}
	if _, err = store.Save(t.Context(), in); err != nil {
		t.Fatal("native trusted rewrite", err)
	}
	if _, err = store.loadGenericSelected(t.Context(), p.ID, p.WorkspaceID, nil); err != nil {
		t.Fatal("trusted rewrite did not restore legacy authority", err)
	}
}
