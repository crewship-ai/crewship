package pipeline

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

// A real before_all HTTP effect observes whether its run row already exists.
// Only a loopback test server is called; no external service or agent is used.
func TestRunStartBoundaryPrecedesBeforeAllEffect(t *testing.T) {
	var rig *effectRig
	counts := make(chan int, 1)
	failures := make(chan error, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var count int
		err := rig.store.db.QueryRowContext(r.Context(), "SELECT COUNT(*) FROM pipeline_runs WHERE id=?", "review-before-all").Scan(&count)
		if err != nil {
			failures <- err
			w.WriteHeader(500)
			return
		}
		counts <- count
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"effect":"applied"}`))
	}))
	defer srv.Close()
	definition := fmt.Sprintf(`{"dsl_version":"1.0","name":"review-before-all","hooks":{"before_all":{"id":"before","type":"http","http":{"method":"POST","url":%q}}},"steps":[{"id":"done","type":"transform","transform":{"input":"ok","expression":"."}}]}`, srv.URL)
	rig = newEffectRig(t, "review-before-all", definition)
	result, err := rig.exec.Run(context.Background(), RunInput{PipelineID: rig.pipeline.ID, WorkspaceID: "ws_test", Mode: ModeRun, RunIDOverride: "review-before-all"})
	if err != nil || result == nil {
		t.Fatalf("run: %+v %v", result, err)
	}
	select {
	case err := <-failures:
		t.Fatal(err)
	default:
	}
	select {
	case count := <-counts:
		if count != 1 {
			t.Fatalf("before_all HTTP effect observed %d durable run rows, want 1 before any external effect", count)
		}
	default:
		t.Fatalf("before_all did not execute; result=%+v", result)
	}
}

func TestRunStartBoundaryRejectsPersistenceBeforeEffects(t *testing.T) {
	recorder := newEffectRecorder()
	server := recorder.server(t)
	rig := newEffectRig(t, "start-write-failure", effectDSL(server.URL, 0))
	definition := fmt.Sprintf(`{"dsl_version":"1.0","name":"setup-write-failure","hooks":{"before_all":{"id":"before","type":"http","http":{"method":"POST","url":%q}}},"steps":[{"id":"apply","type":"http","http":{"method":"POST","url":%q}}]}`, server.URL+"/setup", server.URL+"/apply")
	in := validSaveInput("setup-write-failure")
	in.DefinitionJSON = definition
	pipeline, err := rig.store.Save(t.Context(), in)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := rig.store.db.Exec(`CREATE TRIGGER refuse_run BEFORE INSERT ON pipeline_runs BEGIN SELECT RAISE(ABORT,'run storage unavailable'); END`); err != nil {
		t.Fatal(err)
	}
	_, err = rig.exec.Run(t.Context(), RunInput{PipelineID: pipeline.ID, WorkspaceID: "ws_test", Mode: ModeRun})
	if err == nil {
		t.Fatal("run storage failure must reject execution")
	}
	if recorder.count("/setup") != 0 || recorder.count("/apply") != 0 {
		t.Fatal("effect ran before durable start")
	}
}

func TestRunStartBoundaryBootInterruptsUncertainSetup(t *testing.T) {
	recorder := newEffectRecorder()
	server := recorder.server(t)
	definition := fmt.Sprintf(`{"dsl_version":"1.0","name":"uncertain-setup","hooks":{"before_all":{"id":"before","type":"http","http":{"method":"POST","url":%q}}},"steps":[{"id":"apply","type":"http","http":{"method":"POST","url":%q}}]}`, server.URL+"/prepare", server.URL+"/apply")
	rig := newEffectRig(t, "uncertain-setup", definition)
	record := &RunRecord{ID: "setup-crash", WorkspaceID: "ws_test", PipelineID: rig.pipeline.ID, PipelineSlug: rig.pipeline.Slug, Status: RunStatusRunning, Mode: ModeRun, StartedAt: time.Now().Add(-time.Hour), InputsJSON: "{}"}
	if err := rig.runStore.Insert(t.Context(), record); err != nil {
		t.Fatal(err)
	}
	if _, err := rig.store.db.Exec(`UPDATE pipeline_runs SET start_phase='pre_hooks' WHERE id=?`, record.ID); err != nil {
		t.Fatal(err)
	}
	resumed, interrupted, err := rig.exec.ResumeInterruptedRuns(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if resumed != 0 || interrupted != 1 {
		t.Fatalf("uncertain setup resumed=%d interrupted=%d, want 0/1", resumed, interrupted)
	}
	actual, err := rig.runStore.Get(t.Context(), record.ID)
	if err != nil || actual.Status != RunStatusInterrupted {
		t.Fatalf("missing interrupted result: %+v %v", actual, err)
	}
	if recorder.count("/prepare") != 0 || recorder.count("/apply") != 0 {
		t.Fatal("uncertain setup continued body")
	}
}

func TestRunStartBoundaryBodyResumeSkipsSetup(t *testing.T) {
	recorder := newEffectRecorder()
	server := recorder.server(t)
	definition := fmt.Sprintf(`{"dsl_version":"1.0","name":"body-resume","hooks":{"before_all":{"id":"before","type":"http","http":{"method":"POST","url":%q}}},"steps":[{"id":"apply","type":"http","http":{"method":"POST","url":%q}}]}`, server.URL+"/prepare", server.URL+"/apply")
	rig := newEffectRig(t, "body-resume", definition)
	record := &RunRecord{ID: "body-crash", WorkspaceID: "ws_test", PipelineID: rig.pipeline.ID, PipelineSlug: rig.pipeline.Slug, Status: RunStatusRunning, Mode: ModeRun, StartedAt: time.Now().Add(-time.Hour), DefinitionHash: rig.pipeline.DefinitionHash}
	if err := rig.runStore.insertBeforeHooks(t.Context(), record); err != nil {
		t.Fatal(err)
	}
	if err := rig.runStore.enterBody(t.Context(), record.ID); err != nil {
		t.Fatal(err)
	}
	plan, reason := rig.exec.buildResumePlan(t.Context(), record)
	if plan == nil {
		t.Fatalf("body must remain resumable: %s", reason)
	}
	rig.exec.runResumedRun(t.Context(), plan, slog.Default())
	actual, err := rig.runStore.Get(t.Context(), record.ID)
	if err != nil || actual.Status != RunStatusCompleted {
		t.Fatalf("resume: %+v %v", actual, err)
	}
	if recorder.count("/prepare") != 0 || recorder.count("/apply") != 1 {
		t.Fatal("resume must skip setup and execute body")
	}
}

func TestRunStartBoundaryHookFailureIsDurable(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusInternalServerError) }))
	defer srv.Close()
	definition := fmt.Sprintf(`{"dsl_version":"1.0","name":"setup-fails","hooks":{"before_all":{"id":"before","type":"http","http":{"method":"POST","url":%q}}},"steps":[{"id":"done","type":"transform","transform":{"input":"ok","expression":"."}}]}`, srv.URL)
	rig := newEffectRig(t, "setup-fails", definition)
	result, err := rig.exec.Run(t.Context(), RunInput{PipelineID: rig.pipeline.ID, WorkspaceID: "ws_test", Mode: ModeRun})
	if err != nil || result == nil {
		t.Fatalf("run: %+v %v", result, err)
	}
	actual, err := rig.runStore.Get(t.Context(), result.RunID)
	if err != nil || actual.Status != RunStatusFailed {
		t.Fatalf("setup failure must be visible: %+v %v", actual, err)
	}
	phase, err := rig.runStore.startPhase(t.Context(), result.RunID)
	if err != nil || phase != startPhasePreHooks {
		t.Fatalf("failed setup phase=%q err=%v", phase, err)
	}
}

func TestRunStartBoundaryRevocationAfterSetupKeepsIdentity(t *testing.T) {
	var revoked atomic.Bool
	var effects atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		effects.Add(1)
		revoked.Store(true)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{}`))
	}))
	defer srv.Close()
	definition := fmt.Sprintf(`{"dsl_version":"1.0","name":"setup-revocation","hooks":{"before_all":{"id":"before","type":"http","http":{"method":"POST","url":%q}}},"steps":[{"id":"done","type":"transform","transform":{"input":"ok","expression":"."}}]}`, srv.URL)
	rig := newEffectRig(t, "setup-revocation", definition)
	if _, err := rig.store.db.Exec(idempotencySchemaSQL); err != nil {
		t.Fatal(err)
	}
	rig.exec.WithIdempotencyStore(NewIdempotencyStore(rig.store.db))
	rig.exec.memberCheck = func(context.Context, string, string) (bool, error) { return !revoked.Load(), nil }
	input := RunInput{PipelineID: rig.pipeline.ID, WorkspaceID: "ws_test", Mode: ModeRun, InvokingUserID: "human", IdempotencyKey: "setup-once"}
	result, err := rig.exec.Run(t.Context(), input)
	if err == nil || result == nil {
		t.Fatalf("revoked body admission: %+v %v", result, err)
	}
	actual, getErr := rig.runStore.Get(t.Context(), result.RunID)
	if getErr != nil || actual.Status != RunStatusFailed {
		t.Fatalf("revocation must close durable start: %+v %v", actual, getErr)
	}
	revoked.Store(false)
	duplicate, err := rig.exec.Run(t.Context(), input)
	if err != nil || duplicate == nil || duplicate.Status != "DEDUPED" || duplicate.RunID != result.RunID {
		t.Fatalf("retry lost identity: %+v %v", duplicate, err)
	}
	if effects.Load() != 1 {
		t.Fatal("retry repeated setup")
	}
}

func TestRunStartBoundaryCancelBeforeBody(t *testing.T) {
	rig := newEffectRig(t, "cancel-setup", `{"dsl_version":"1.0","name":"cancel-setup","steps":[{"id":"done","type":"transform","transform":{"input":"ok","expression":"."}}]}`)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	result, _ := rig.exec.Run(ctx, RunInput{PipelineID: rig.pipeline.ID, WorkspaceID: "ws_test", Mode: ModeRun, OnStarted: func(string) { cancel() }})
	if result == nil || result.Status != "CANCELLED" {
		t.Fatalf("cancelled admission: %+v", result)
	}
	actual, err := rig.runStore.Get(t.Context(), result.RunID)
	if err != nil || actual.Status != RunStatusCancelled {
		t.Fatalf("durable cancellation: %+v %v", actual, err)
	}
}
