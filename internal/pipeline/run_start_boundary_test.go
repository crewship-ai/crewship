package pipeline

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
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
	rig := newEffectRig(t, "uncertain-setup", effectDSL(server.URL, 0))
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
