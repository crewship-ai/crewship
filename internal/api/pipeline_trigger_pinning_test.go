package api

// Version pinning + WAITING outcome tests for the trigger dispatch
// handlers (FireWebhook + RunSchedule force-fire).
//
// Defect 1 (dead pin): webhooks persist target_pipeline_version but
// FireWebhook always executed the routine's HEAD definition. Pinned
// contract: a pinned webhook executes the pinned version's definition
// (run row stamped with the executed version); a pin pointing at a
// missing version fails the fire legibly — never a silent head
// fallback. The schedule force-fire endpoint honors the pin the same
// way.
//
// Defect 2 (WAITING misrecorded): a webhook-fired routine that parks on
// a wait:approval step recorded last_status=FAILED; it must record
// WAITING.

import (
	"crypto/hmac"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/pipeline"
)

const (
	pinnedV1DSL = `{"dsl_version":"1.0","name":"pin-hook","steps":[{"id":"v1step","type":"transform","transform":{"input":"v1-out","expression":"."}}]}`
	pinnedV2DSL = `{"dsl_version":"1.0","name":"pin-hook","steps":[{"id":"v2step","type":"transform","transform":{"input":"v2-out","expression":"."}}]}`
)

// seedVersionedPipeline inserts a pipelines row whose HEAD is v2 plus
// the two immutable pipeline_versions rows, so pinned dispatch can be
// distinguished from head dispatch by step ids and hashes.
func seedVersionedPipeline(t *testing.T, db *sql.DB, wsID, id, slug string) {
	t.Helper()
	now := time.Now().UTC().Format(time.RFC3339)
	if _, err := db.Exec(`
		INSERT INTO pipelines (id, workspace_id, slug, name, definition_json, definition_hash, head_version, created_at, updated_at, last_test_run_at)
		VALUES (?, ?, ?, ?, ?, 'h2', 2, ?, ?, ?)`,
		id, wsID, slug, slug, pinnedV2DSL, now, now, now); err != nil {
		t.Fatalf("seed pipeline: %v", err)
	}
	for _, v := range []struct {
		n    int
		def  string
		hash string
	}{{1, pinnedV1DSL, "h1"}, {2, pinnedV2DSL, "h2"}} {
		if _, err := db.Exec(`
			INSERT INTO pipeline_versions (id, pipeline_id, version, definition_json, definition_hash, author_type, author_id, created_at)
			VALUES (?, ?, ?, ?, ?, 'user', 'u_test', ?)`,
			"plnv_"+id+"_v"+string(rune('0'+v.n)), id, v.n, v.def, v.hash, now); err != nil {
			t.Fatalf("seed version %d: %v", v.n, err)
		}
	}
}

// seedPinnedWebhook mints a webhook pinned to the given version.
func seedPinnedWebhook(t *testing.T, db *sql.DB, wsID, pipelineID string, pinned *int) *pipeline.Webhook {
	t.Helper()
	wh, err := pipeline.NewWebhookStore(db).Save(t.Context(), pipeline.SaveWebhookInput{
		WorkspaceID:           wsID,
		Name:                  "pinned-hook",
		TargetPipelineID:      pipelineID,
		TargetPipelineVersion: pinned,
		SigningSecret:         "pin-secret",
		Enabled:               true,
	})
	if err != nil {
		t.Fatalf("seed webhook: %v", err)
	}
	return wh
}

func fireSignedWebhook(t *testing.T, h *PipelineHandler, token, body string) *httptest.ResponseRecorder {
	t.Helper()
	mac := hmac.New(sha256.New, []byte("pin-secret"))
	_, _ = mac.Write([]byte(body))
	sig := hex.EncodeToString(mac.Sum(nil))
	req := httptest.NewRequest("POST", "/api/v1/webhooks/"+token, strings.NewReader(body))
	req.SetPathValue("token", token)
	req.Header.Set("X-Crewship-Signature", sig)
	rr := httptest.NewRecorder()
	h.FireWebhook(rr, req)
	return rr
}

// TestPipelineWebhooks_Fire_PinnedVersion_ExecutesPinned: webhook
// pinned to v1 + head at v2 → the dispatch executes v1 and the run row
// records the executed version.
func TestPipelineWebhooks_Fire_PinnedVersion_ExecutesPinned(t *testing.T) {
	h, db, _, wsID := webhookHandlerRig(t)
	h.SetRunner(&stubRunner{output: "ok"})
	runStore := pipeline.NewRunStore(db)
	h.SetRunStore(runStore)
	seedVersionedPipeline(t, db, wsID, "pln_pin", "pin-hook")
	one := 1
	wh := seedPinnedWebhook(t, db, wsID, "pln_pin", &one)

	rr := fireSignedWebhook(t, h, wh.Token, `{"e":1}`)
	if rr.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want 202; body=%s", rr.Code, rr.Body.String())
	}
	var resp map[string]any
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	runID, _ := resp["run_id"].(string)
	if runID == "" {
		t.Fatalf("no run_id in response: %v", resp)
	}
	// Fire is async since the 202 rewrite — drain the dispatch before
	// asserting on the run row.
	h.WaitWebhookDispatches()
	rec, err := runStore.Get(t.Context(), runID)
	if err != nil {
		t.Fatalf("load run row: %v", err)
	}
	if rec.PipelineVersion == nil || *rec.PipelineVersion != 1 {
		t.Errorf("run pipeline_version: got %v, want 1", rec.PipelineVersion)
	}
	if rec.DefinitionHash != "h1" {
		t.Errorf("run definition_hash: got %q, want the pinned v1 hash %q", rec.DefinitionHash, "h1")
	}
	outputs, err := runStore.GetStepOutputs(t.Context(), rec.ID)
	if err != nil {
		t.Fatalf("get step outputs: %v", err)
	}
	if _, ok := outputs["v1step"]; !ok {
		t.Errorf("step outputs %#v missing v1step — pinned definition did not execute", outputs)
	}
	if _, ok := outputs["v2step"]; ok {
		t.Errorf("step outputs %#v contain v2step — HEAD executed despite the pin", outputs)
	}
}

// TestPipelineWebhooks_Fire_PinnedVersionMissing_FailsLegibly: pin at a
// version that doesn't exist → legible failure, FAILED fire record, no
// execution. Never a silent head fallback.
func TestPipelineWebhooks_Fire_PinnedVersionMissing_FailsLegibly(t *testing.T) {
	h, db, _, wsID := webhookHandlerRig(t)
	h.SetRunner(&stubRunner{output: "ok"})
	h.SetRunStore(pipeline.NewRunStore(db))
	seedVersionedPipeline(t, db, wsID, "pln_pin_ghost", "pin-ghost")
	ninetyNine := 99
	wh := seedPinnedWebhook(t, db, wsID, "pln_pin_ghost", &ninetyNine)

	rr := fireSignedWebhook(t, h, wh.Token, `{"e":1}`)
	if rr.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409 (policy error, not a silent 202 head run); body=%s", rr.Code, rr.Body.String())
	}
	if !strings.Contains(rr.Body.String(), "version") {
		t.Errorf("error body %q should name the missing version so the operator can fix the pin", rr.Body.String())
	}

	// The fire is recorded FAILED and nothing executed.
	got, err := pipeline.NewWebhookStore(db).GetByID(t.Context(), wh.ID)
	if err != nil {
		t.Fatalf("reload webhook: %v", err)
	}
	if got.LastStatus != "FAILED" {
		t.Errorf("webhook last_status: got %q, want FAILED", got.LastStatus)
	}
	var runCount int
	if err := db.QueryRow(`SELECT COUNT(*) FROM pipeline_runs`).Scan(&runCount); err != nil {
		t.Fatal(err)
	}
	if runCount != 0 {
		t.Errorf("pipeline_runs rows: got %d, want 0 — missing pinned version must not execute anything", runCount)
	}
}

// TestPipelineWebhooks_Fire_WaitingRun_RecordsWaiting: a dispatched
// routine that parks on a wait:approval step is a healthy in-flight
// run; the webhook's last_status must say WAITING, not FAILED.
func TestPipelineWebhooks_Fire_WaitingRun_RecordsWaiting(t *testing.T) {
	h, db, _, wsID := webhookHandlerRig(t)
	h.SetRunner(&stubRunner{output: "ok"})
	h.SetRunStore(pipeline.NewRunStore(db))
	wpStore := pipeline.NewSQLWaitpointStore(db)
	t.Cleanup(wpStore.Close)
	h.SetWaitpointStore(wpStore)

	now := time.Now().UTC().Format(time.RFC3339)
	waitDSL := `{"dsl_version":"1.0","name":"wait-hook","steps":[` +
		`{"id":"t","type":"transform","transform":{"input":"x","expression":"."}},` +
		`{"id":"gate","type":"wait","wait":{"kind":"approval","approval_prompt":"ok?"},"timeout_seconds":3600}]}`
	if _, err := db.Exec(`
		INSERT INTO pipelines (id, workspace_id, slug, name, definition_json, definition_hash, created_at, updated_at, last_test_run_at)
		VALUES ('pln_waithook', ?, 'wait-hook', 'wait-hook', ?, 'hw', ?, ?, ?)`,
		wsID, waitDSL, now, now, now); err != nil {
		t.Fatalf("seed pipeline: %v", err)
	}
	wh := seedPinnedWebhook(t, db, wsID, "pln_waithook", nil)

	rr := fireSignedWebhook(t, h, wh.Token, `{"e":1}`)
	if rr.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want 202; body=%s", rr.Code, rr.Body.String())
	}
	// Async fire: the 202 hands back a pollable handle (status PENDING);
	// the parked outcome shows up on the webhook record once the
	// dispatch drains.
	h.WaitWebhookDispatches()

	got, err := pipeline.NewWebhookStore(db).GetByID(t.Context(), wh.ID)
	if err != nil {
		t.Fatalf("reload webhook: %v", err)
	}
	if got.LastStatus != "WAITING" {
		t.Errorf("webhook last_status: got %q, want WAITING — a parked approval gate is not a failure", got.LastStatus)
	}
}

// TestPipelineSchedules_Run_PinnedVersionMissing_FailsLegibly: the
// force-fire endpoint (`crewship routine schedules now`) honors the pin
// the same way the cron path does — a missing pinned version is a
// legible policy error, not a head run.
func TestPipelineSchedules_Run_PinnedVersionMissing_FailsLegibly(t *testing.T) {
	h, db, userID, wsID := scheduleHandlerRig(t)
	h.SetRunner(&stubRunner{output: "ok"})
	h.SetRunStore(pipeline.NewRunStore(db))
	seedVersionedPipeline(t, db, wsID, "pln_pin_sched", "pin-sched")
	ninetyNine := 99
	saved, err := pipeline.NewScheduleStore(db).Save(t.Context(), pipeline.SaveScheduleInput{
		WorkspaceID:           wsID,
		Name:                  "pinned",
		TargetPipelineID:      "pln_pin_sched",
		TargetPipelineVersion: &ninetyNine,
		CronExpr:              "0 8 * * *",
		Timezone:              "UTC",
		Enabled:               true,
	})
	if err != nil {
		t.Fatalf("seed schedule: %v", err)
	}

	req := withWorkspaceUser(httptest.NewRequest("POST",
		"/api/v1/workspaces/"+wsID+"/pipeline-schedules/"+saved.ID+"/run", nil),
		userID, wsID, "OWNER")
	req.SetPathValue("scheduleId", saved.ID)
	rr := httptest.NewRecorder()
	h.RunSchedule(rr, req)
	if rr.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409; body=%s", rr.Code, rr.Body.String())
	}
	if !strings.Contains(rr.Body.String(), "version") {
		t.Errorf("error body %q should name the missing version", rr.Body.String())
	}
	var runCount int
	if err := db.QueryRow(`SELECT COUNT(*) FROM pipeline_runs`).Scan(&runCount); err != nil {
		t.Fatal(err)
	}
	if runCount != 0 {
		t.Errorf("pipeline_runs rows: got %d, want 0", runCount)
	}
}

// TestPipelineSchedules_Run_PinnedVersion_ExecutesPinned: force-fire on
// a pinned schedule executes the pinned definition.
func TestPipelineSchedules_Run_PinnedVersion_ExecutesPinned(t *testing.T) {
	h, db, userID, wsID := scheduleHandlerRig(t)
	h.SetRunner(&stubRunner{output: "ok"})
	runStore := pipeline.NewRunStore(db)
	h.SetRunStore(runStore)
	seedVersionedPipeline(t, db, wsID, "pln_pin_sched2", "pin-sched2")
	one := 1
	saved, err := pipeline.NewScheduleStore(db).Save(t.Context(), pipeline.SaveScheduleInput{
		WorkspaceID:           wsID,
		Name:                  "pinned",
		TargetPipelineID:      "pln_pin_sched2",
		TargetPipelineVersion: &one,
		CronExpr:              "0 8 * * *",
		Timezone:              "UTC",
		Enabled:               true,
	})
	if err != nil {
		t.Fatalf("seed schedule: %v", err)
	}

	req := withWorkspaceUser(httptest.NewRequest("POST",
		"/api/v1/workspaces/"+wsID+"/pipeline-schedules/"+saved.ID+"/run", nil),
		userID, wsID, "OWNER")
	req.SetPathValue("scheduleId", saved.ID)
	rr := httptest.NewRecorder()
	h.RunSchedule(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rr.Code, rr.Body.String())
	}
	var res struct {
		RunID string `json:"run_id"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &res); err != nil || res.RunID == "" {
		t.Fatalf("decode run result (err=%v): %s", err, rr.Body.String())
	}
	rec, err := runStore.Get(t.Context(), res.RunID)
	if err != nil {
		t.Fatalf("load run row: %v", err)
	}
	if rec.PipelineVersion == nil || *rec.PipelineVersion != 1 {
		t.Errorf("run pipeline_version: got %v, want 1", rec.PipelineVersion)
	}
	outputs, err := runStore.GetStepOutputs(t.Context(), rec.ID)
	if err != nil {
		t.Fatalf("get step outputs: %v", err)
	}
	_, hasV1 := outputs["v1step"]
	_, hasV2 := outputs["v2step"]
	if !hasV1 || hasV2 {
		t.Errorf("force-fire executed the wrong definition; outputs=%#v", outputs)
	}
}

// TestPipelineSchedules_Update_PreservesPinWhenAbsent: a PATCH that
// doesn't mention target_pipeline_version must keep the existing pin —
// otherwise every unrelated update (cron tweak, rename, disable)
// silently unpins a production schedule back onto head, which is the
// exact hazard pinning exists to prevent. An explicit null clears it.
func TestPipelineSchedules_Update_PreservesPinWhenAbsent(t *testing.T) {
	h, db, userID, wsID := scheduleHandlerRig(t)
	seedVersionedPipeline(t, db, wsID, "pln_pin_keep", "pin-keep")
	one := 1
	saved, err := pipeline.NewScheduleStore(db).Save(t.Context(), pipeline.SaveScheduleInput{
		WorkspaceID:           wsID,
		Name:                  "pinned",
		TargetPipelineID:      "pln_pin_keep",
		TargetPipelineVersion: &one,
		CronExpr:              "0 8 * * *",
		Timezone:              "UTC",
		Enabled:               true,
	})
	if err != nil {
		t.Fatalf("seed schedule: %v", err)
	}

	patch := func(body string) *httptest.ResponseRecorder {
		req := withWorkspaceUser(httptest.NewRequest("PATCH",
			"/api/v1/workspaces/"+wsID+"/pipeline-schedules/"+saved.ID,
			strings.NewReader(body)), userID, wsID, "OWNER")
		req.SetPathValue("scheduleId", saved.ID)
		rr := httptest.NewRecorder()
		h.UpdateSchedule(rr, req)
		return rr
	}

	// Unrelated PATCH (cron only) → pin preserved.
	if rr := patch(`{"cron_expr":"0 9 * * *"}`); rr.Code != http.StatusOK {
		t.Fatalf("patch status = %d; body=%s", rr.Code, rr.Body.String())
	}
	got, err := pipeline.NewScheduleStore(db).GetByID(t.Context(), saved.ID)
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	if got.TargetPipelineVersion == nil || *got.TargetPipelineVersion != 1 {
		t.Fatalf("pin after unrelated PATCH: got %v, want 1 (pin must survive updates that don't mention it)", got.TargetPipelineVersion)
	}

	// Explicit null → pin cleared (back to head).
	if rr := patch(`{"target_pipeline_version":null}`); rr.Code != http.StatusOK {
		t.Fatalf("unpin patch status = %d; body=%s", rr.Code, rr.Body.String())
	}
	got, _ = pipeline.NewScheduleStore(db).GetByID(t.Context(), saved.ID)
	if got.TargetPipelineVersion != nil {
		t.Errorf("pin after explicit null: got %d, want cleared", *got.TargetPipelineVersion)
	}
}
