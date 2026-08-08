package api

// Provenance on the run-records feed: how deep a run sits in a composed
// chain, and — when a rule started it — which rule.
//
// Both facts already exist on the row. Neither reached a caller: the DTO
// ListRunRecords answers with was written before chain_depth existed and
// before an automation could park a run, so the only surface a user has for
// "why did this run" reported `triggered_via: "schedule"` for a rule-fired
// run and nothing at all for a composed one.
//
// The second half of that is the finding worth naming. PendingRunDispatcher
// fires EVERY deferred run with TriggeredVia = "schedule"
// (internal/pipeline/pending_dispatcher.go), automations included, so
// triggered_via alone cannot tell a cron from a rule. The rule's identity
// survives only in the run's metadata_json, where the automation flusher put
// it. That is what these tests pin: the distinction is recovered from
// metadata, not invented from the enum.

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/pipeline"
)

// seedProvenanceRun inserts one pipeline_runs row through the real store, so the
// test exercises the same columns the dispatcher writes.
func seedProvenanceRun(t *testing.T, h *PipelineHandler, wsID, pipelineID, slug string, mutate func(*pipeline.RunRecord)) *pipeline.RunRecord {
	t.Helper()
	rec := &pipeline.RunRecord{
		ID:           "run-" + slug + "-" + time.Now().Format("150405.000000000"),
		WorkspaceID:  wsID,
		PipelineID:   pipelineID,
		PipelineSlug: slug,
		Status:       pipeline.RunStatusCompleted,
		Mode:         pipeline.ModeRun,
		StartedAt:    time.Now().UTC(),
	}
	if mutate != nil {
		mutate(rec)
	}
	if err := pipeline.NewRunStore(h.db).Insert(t.Context(), rec); err != nil {
		t.Fatalf("insert run: %v", err)
	}
	return rec
}

// listRunRecords drives the handler and decodes its array body.
func listProvenanceRunRecords(t *testing.T, h *PipelineHandler, userID, wsID, slug string) []map[string]any {
	t.Helper()
	rr := httptest.NewRecorder()
	h.ListRunRecords(rr, covPE2Req(t, "GET", "/x?limit=50", "", userID, wsID, slug))
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rr.Code, rr.Body.String())
	}
	var out []map[string]any
	if err := json.Unmarshal(rr.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode body %q: %v", rr.Body.String(), err)
	}
	return out
}

// A run a person started is a chain root, and the feed must not decorate it.
// chain_depth 0 is the overwhelmingly common case; emitting an origin or an
// automation name for it would put "composed" chrome on every manual run.
func TestListRunRecords_DirectRunCarriesNoChainProvenance(t *testing.T) {
	h, userID, wsID := newPipelineHandlerForCRUDTest(t)
	h.SetRunStore(pipeline.NewRunStore(h.db))
	seedPipelineRowDef(t, h.db, wsID, "pipe-prov-direct", "prov-direct", agentlessProbeDef)
	seedProvenanceRun(t, h, wsID, "pipe-prov-direct", "prov-direct", func(r *pipeline.RunRecord) {
		r.TriggeredVia = pipeline.TriggeredViaManual
	})

	rows := listProvenanceRunRecords(t, h, userID, wsID, "prov-direct")
	if len(rows) != 1 {
		t.Fatalf("rows = %d, want 1", len(rows))
	}
	if got := rows[0]["chain_depth"]; got != float64(0) {
		t.Errorf("chain_depth = %v, want 0", got)
	}
	if _, present := rows[0]["chain_origin"]; present {
		t.Errorf("chain_origin present on a chain root: %v", rows[0]["chain_origin"])
	}
	if _, present := rows[0]["automation_name"]; present {
		t.Errorf("automation_name present on a manual run: %v", rows[0]["automation_name"])
	}
}

// A composed run reports how far it sits from whatever a human did, and what
// started the chain. Without this the page cannot answer "why did this run"
// for exactly the runs nobody asked for.
func TestListRunRecords_ComposedRunReportsDepthAndOrigin(t *testing.T) {
	h, userID, wsID := newPipelineHandlerForCRUDTest(t)
	h.SetRunStore(pipeline.NewRunStore(h.db))
	seedPipelineRowDef(t, h.db, wsID, "pipe-prov-chain", "prov-chain", agentlessProbeDef)
	seedProvenanceRun(t, h, wsID, "pipe-prov-chain", "prov-chain", func(r *pipeline.RunRecord) {
		r.TriggeredVia = pipeline.TriggeredViaCallPipeline
		r.ChainDepth = 3
		r.ChainOrigin = "run-that-started-it"
	})

	rows := listProvenanceRunRecords(t, h, userID, wsID, "prov-chain")
	if len(rows) != 1 {
		t.Fatalf("rows = %d, want 1", len(rows))
	}
	if got := rows[0]["chain_depth"]; got != float64(3) {
		t.Errorf("chain_depth = %v, want 3", got)
	}
	if got := rows[0]["chain_origin"]; got != "run-that-started-it" {
		t.Errorf("chain_origin = %v, want run-that-started-it", got)
	}
}

// The finding, pinned. An automation-fired run is written with
// triggered_via="schedule" by the shared pending-run dispatcher, so the feed
// must recover the rule from metadata_json or the UI will call a rule a cron.
func TestListRunRecords_AutomationFiredRunNamesTheRule(t *testing.T) {
	h, userID, wsID := newPipelineHandlerForCRUDTest(t)
	h.SetRunStore(pipeline.NewRunStore(h.db))
	seedPipelineRowDef(t, h.db, wsID, "pipe-prov-auto", "prov-auto", agentlessProbeDef)
	seedProvenanceRun(t, h, wsID, "pipe-prov-auto", "prov-auto", func(r *pipeline.RunRecord) {
		// Exactly what internal/automation's flusher writes.
		r.TriggeredVia = pipeline.TriggeredViaSchedule
		r.MetadataJSON = `{"automation_id":"auto-7","automation_name":"Triage new bugs",` +
			`"trigger_event_type":"mission.status_change","coalesced_events":4}`
	})

	rows := listProvenanceRunRecords(t, h, userID, wsID, "prov-auto")
	if len(rows) != 1 {
		t.Fatalf("rows = %d, want 1", len(rows))
	}
	if got := rows[0]["triggered_via"]; got != "schedule" {
		t.Fatalf("triggered_via = %v, want schedule (the dispatcher's value)", got)
	}
	if got := rows[0]["automation_id"]; got != "auto-7" {
		t.Errorf("automation_id = %v, want auto-7", got)
	}
	if got := rows[0]["automation_name"]; got != "Triage new bugs" {
		t.Errorf("automation_name = %v, want %q", got, "Triage new bugs")
	}
	if got := rows[0]["trigger_event_type"]; got != "mission.status_change" {
		t.Errorf("trigger_event_type = %v, want mission.status_change", got)
	}
}

// A cron-fired run has metadata too (the scheduler writes its own), and it
// must not be mistaken for a rule. Only an automation_name key promotes a run
// to "started by a rule".
func TestListRunRecords_ScheduleMetadataIsNotAnAutomation(t *testing.T) {
	h, userID, wsID := newPipelineHandlerForCRUDTest(t)
	h.SetRunStore(pipeline.NewRunStore(h.db))
	seedPipelineRowDef(t, h.db, wsID, "pipe-prov-cron", "prov-cron", agentlessProbeDef)
	seedProvenanceRun(t, h, wsID, "pipe-prov-cron", "prov-cron", func(r *pipeline.RunRecord) {
		r.TriggeredVia = pipeline.TriggeredViaSchedule
		r.MetadataJSON = `{"schedule_id":"sch-1","cron_expr":"0 9 * * *"}`
	})

	rows := listProvenanceRunRecords(t, h, userID, wsID, "prov-cron")
	if len(rows) != 1 {
		t.Fatalf("rows = %d, want 1", len(rows))
	}
	if _, present := rows[0]["automation_name"]; present {
		t.Errorf("automation_name present on a cron run: %v", rows[0]["automation_name"])
	}
	if _, present := rows[0]["automation_id"]; present {
		t.Errorf("automation_id present on a cron run: %v", rows[0]["automation_id"])
	}
}

// Metadata is an opaque scratchpad a routine can write to from a step
// ({{ run.metadata.X }}). A malformed or hostile value must not sink the
// whole feed, and must not smuggle a non-string into a string field.
func TestListRunRecords_UnusableMetadataIsIgnoredNotFatal(t *testing.T) {
	h, userID, wsID := newPipelineHandlerForCRUDTest(t)
	h.SetRunStore(pipeline.NewRunStore(h.db))
	seedPipelineRowDef(t, h.db, wsID, "pipe-prov-junk", "prov-junk", agentlessProbeDef)
	seedProvenanceRun(t, h, wsID, "pipe-prov-junk", "prov-junk", func(r *pipeline.RunRecord) {
		r.MetadataJSON = `not json at all`
	})
	seedProvenanceRun(t, h, wsID, "pipe-prov-junk", "prov-junk", func(r *pipeline.RunRecord) {
		r.MetadataJSON = `{"automation_name":{"nested":"object"},"automation_id":42}`
	})

	rows := listProvenanceRunRecords(t, h, userID, wsID, "prov-junk")
	if len(rows) != 2 {
		t.Fatalf("rows = %d, want 2", len(rows))
	}
	for i, row := range rows {
		if v, present := row["automation_name"]; present {
			t.Errorf("row %d: automation_name = %v, want absent", i, v)
		}
		if v, present := row["automation_id"]; present {
			t.Errorf("row %d: automation_id = %v, want absent", i, v)
		}
	}
}
