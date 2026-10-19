package pipeline

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

func TestPublicationRefusalAfterSetupRetainsDurableIdempotency(t *testing.T) {
	var rig *effectRig
	var publish atomic.Bool
	var setup atomic.Int32
	failures := make(chan error, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		setup.Add(1)
		if publish.Load() {
			if _, err := rig.store.db.ExecContext(r.Context(), `UPDATE pipelines SET head_version=2 WHERE id=?`, rig.pipeline.ID); err != nil {
				failures <- err
				w.WriteHeader(http.StatusInternalServerError)
				return
			}
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{}`))
	}))
	defer server.Close()
	definition := fmt.Sprintf(`{"dsl_version":"1.0","name":"publication-after-setup","hooks":{"before_all":{"id":"prepare","type":"http","http":{"method":"POST","url":%q}}},"steps":[{"id":"done","type":"transform","transform":{"input":"ok","expression":"."}}]}`, server.URL)
	rig = newEffectRig(t, "publication-after-setup", definition)
	if _, err := rig.store.db.Exec(idempotencySchemaSQL); err != nil {
		t.Fatal(err)
	}
	rig.exec.WithIdempotencyStore(NewIdempotencyStore(rig.store.db))
	guardPublishedVersion(t, rig.store, rig.pipeline)
	input := RunInput{PipelineID: rig.pipeline.ID, WorkspaceID: "ws_test", Mode: ModeRun}
	calibration, err := rig.exec.Run(t.Context(), input)
	if err != nil || calibration == nil || calibration.Status != "COMPLETED" || setup.Load() != 1 {
		t.Fatalf("legacy setup calibration: %+v err=%v setup=%d", calibration, err, setup.Load())
	}
	setup.Store(0)
	publish.Store(true)
	input.IdempotencyKey = "publication-setup-once"
	first, err := rig.exec.Run(t.Context(), input)
	select {
	case failure := <-failures:
		t.Fatalf("publication fixture: %v", failure)
	default:
	}
	if !errors.Is(err, ErrPublicationGenericRun) || first == nil || first.Status != "FAILED" || setup.Load() != 1 {
		t.Fatalf("post-setup publication refusal: %+v err=%v setup=%d", first, err, setup.Load())
	}
	stored, err := rig.runStore.Get(t.Context(), first.RunID)
	if err != nil || stored.Status != RunStatusFailed {
		t.Fatalf("missing durable failed identity: %+v %v", stored, err)
	}
	// A later legacy selection may admit the request again, but it must resolve
	// the already-started identity rather than repeat its uncertain setup.
	if _, err := rig.store.db.Exec(`UPDATE pipelines SET head_version=1 WHERE id=?`, rig.pipeline.ID); err != nil {
		t.Fatal(err)
	}
	publish.Store(false)
	retry, err := rig.exec.Run(t.Context(), input)
	if err != nil || retry == nil || retry.Status != "DEDUPED" || retry.RunID != first.RunID {
		t.Errorf("publication refusal released durable idempotency: first=%+v retry=%+v err=%v", first, retry, err)
	}
	if setup.Load() != 1 {
		t.Errorf("publication refusal repeated setup: got=%d want=1", setup.Load())
	}
}

func TestPublicationHookRevalidationPreservesExpiredContextBoundaries(t *testing.T) {
	for _, scenario := range []string{"legacy teardown", "expired before_all", "bounded teardown", "storage failure", "actor revoked"} {
		t.Run(scenario, func(t *testing.T) {
			rig := newEffectRig(t, "expired-hook", `{"name":"expired-hook","steps":[{"id":"done","type":"transform","transform":{"input":"ok","expression":"."}}]}`)
			runner := &recordingCodeRunner{}
			rig.exec.WithCodeRunner(runner)
			in := RunInput{Mode: ModeRun, WorkspaceID: "ws_test", pipeline: rig.pipeline}
			hook := &Step{ID: "cleanup", Type: StepCode, Code: &CodeStep{Runtime: "cel", Code: "1 > 0"}}
			if _, err := rig.exec.runRoutineHook(t.Context(), hook, in, "calibration", rig.pipeline.Slug, true); err != nil || atomic.LoadInt32(&runner.calls) != 1 {
				t.Fatalf("legacy hook calibration: err=%v calls=%d", err, atomic.LoadInt32(&runner.calls))
			}
			atomic.StoreInt32(&runner.calls, 0)
			teardown := scenario != "expired before_all"
			switch scenario {
			case "bounded teardown":
				guardPublishedVersion(t, rig.store, rig.pipeline)
				if _, err := rig.store.db.Exec(`UPDATE pipelines SET head_version=2 WHERE id=?`, rig.pipeline.ID); err != nil {
					t.Fatal(err)
				}
			case "storage failure":
				if _, err := rig.store.db.Exec(`DROP TABLE pipeline_versions`); err != nil {
					t.Fatal(err)
				}
			case "actor revoked":
				in.InvokingUserID = "revoked-human"
				rig.exec.memberCheck = func(ctx context.Context, _, _ string) (bool, error) {
					if !errors.Is(ctx.Err(), context.Canceled) {
						t.Error("dispatch actor check received a detached context")
					}
					return false, nil
				}
			}
			ctx, cancel := context.WithCancel(t.Context())
			cancel()
			_, err := rig.exec.runRoutineHook(ctx, hook, in, "expired", rig.pipeline.Slug, teardown)
			wantCalls := int32(0)
			switch scenario {
			case "legacy teardown":
				wantCalls = 1
				if err != nil {
					t.Errorf("legacy cleanup rejected: %v", err)
				}
			case "expired before_all":
				if !errors.Is(err, context.Canceled) {
					t.Errorf("expired setup error = %v", err)
				}
			case "bounded teardown":
				if !errors.Is(err, ErrPublicationGenericRun) {
					t.Errorf("bounded cleanup error = %v", err)
				}
			case "storage failure":
				if err == nil || errors.Is(err, ErrPublicationGenericRun) || errors.Is(err, context.Canceled) {
					t.Errorf("SQL failure lost operational classification: %v", err)
				}
			case "actor revoked":
				if !errors.Is(err, ErrInvokingUserNotMember) {
					t.Errorf("revoked cleanup actor error = %v", err)
				}
			}
			if got := atomic.LoadInt32(&runner.calls); got != wantCalls {
				t.Errorf("hook calls=%d want=%d", got, wantCalls)
			}
		})
	}
}

func TestPublicationChangeOnStartedRefusesSetupDurably(t *testing.T) {
	var setup atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		setup.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{}`))
	}))
	defer server.Close()
	definition := fmt.Sprintf(`{"dsl_version":"1.0","name":"publication-before-setup","hooks":{"before_all":{"id":"prepare","type":"http","http":{"method":"POST","url":%q}}},"steps":[{"id":"done","type":"transform","transform":{"input":"ok","expression":"."}}]}`, server.URL)
	rig := newEffectRig(t, "publication-before-setup", definition)
	if _, err := rig.store.db.Exec(idempotencySchemaSQL); err != nil {
		t.Fatal(err)
	}
	rig.exec.WithIdempotencyStore(NewIdempotencyStore(rig.store.db))
	guardPublishedVersion(t, rig.store, rig.pipeline)
	input := RunInput{PipelineID: rig.pipeline.ID, WorkspaceID: "ws_test", Mode: ModeRun}
	calibration, err := rig.exec.Run(t.Context(), input)
	if err != nil || calibration == nil || calibration.Status != "COMPLETED" || setup.Load() != 1 {
		t.Fatalf("legacy setup calibration: %+v err=%v setup=%d", calibration, err, setup.Load())
	}
	setup.Store(0)
	var callbackErr error
	input.IdempotencyKey = "publication-before-setup-once"
	input.OnStarted = func(string) {
		_, callbackErr = rig.store.db.Exec(`UPDATE pipelines SET head_version=2 WHERE id=?`, rig.pipeline.ID)
	}
	first, err := rig.exec.Run(t.Context(), input)
	if callbackErr != nil {
		t.Fatalf("publication fixture: %v", callbackErr)
	}
	if first == nil || first.Status != "FAILED" || !strings.Contains(first.ErrorMessage, ErrPublicationGenericRun.Error()) {
		t.Fatalf("publication setup admission: %+v %v", first, err)
	}
	if setup.Load() != 0 {
		t.Errorf("setup crossed publication changed by OnStarted: got=%d want=0", setup.Load())
	}
	stored, getErr := rig.runStore.Get(t.Context(), first.RunID)
	if getErr != nil || stored.Status != RunStatusFailed {
		t.Fatalf("publication setup refusal lost durable outcome: %+v %v", stored, getErr)
	}
	if _, err := rig.store.db.Exec(`UPDATE pipelines SET head_version=1 WHERE id=?`, rig.pipeline.ID); err != nil {
		t.Fatal(err)
	}
	input.OnStarted = nil
	retry, err := rig.exec.Run(t.Context(), input)
	if err != nil || retry == nil || retry.Status != "DEDUPED" || retry.RunID != first.RunID {
		t.Errorf("publication setup refusal lost reservation: first=%+v retry=%+v err=%v", first, retry, err)
	}
}
