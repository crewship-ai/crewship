package api

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/inbox"
	"github.com/crewship-ai/crewship/internal/pipeline"
)

func TestManualRun_PinnedVersionChecksHistoricalCredentials(t *testing.T) {
	h, user, ws := newPipelineHandlerForCRUDTest(t)
	h.SetRunner(&stubRunner{output: "ok"})
	seedPipelineWithVersions(t, h, ws, "pin_cred", "pin-cred", 2)
	old := credGateDef("STRIPE_API_KEY")
	if _, err := h.db.Exec(`UPDATE pipeline_versions SET definition_json=? WHERE pipeline_id=? AND version=1`, old, "pin_cred"); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		body   string
		status int
	}{
		{`{"pinned_version":1,"inputs":{}}`, 422},
		{`{"pinned_version":99}`, 404},
		{`{"pinned_version":0}`, 400},
		{`{"pinned_version":1,"delay_seconds":60}`, 400},
	} {
		req := withWorkspaceUser(httptest.NewRequest("POST", "/run", strings.NewReader(tc.body)), user, ws, "OWNER")
		req.SetPathValue("slug", "pin-cred")
		rr := httptest.NewRecorder()
		h.Run(rr, req)
		if rr.Code != tc.status {
			t.Errorf("%s: got %d want %d: %s", tc.body, rr.Code, tc.status, rr.Body.String())
		}
	}
}

func TestManualRun_PinnedVersionSnapshotsSelectedRecipeWithoutMovingHead(t *testing.T) {
	h, db, user, ws := runsHandlerRig(t)
	h.SetRunner(&stubRunner{output: "ok"})
	h.SetRunStore(pipeline.NewRunStore(db))
	seedPipelineWithVersions(t, h, ws, "pin_run", "pin-run", 2)
	old := `{"dsl_version":"1.0","name":"pin-run","steps":[{"id":"result","type":"transform","transform":{"expression":"'old recipe'"}}]}`
	if _, err := db.Exec(`UPDATE pipeline_versions SET definition_json=? WHERE pipeline_id=? AND version=1`, old, "pin_run"); err != nil {
		t.Fatal(err)
	}
	req := withWorkspaceUser(httptest.NewRequest("POST", "/run", strings.NewReader(`{"pinned_version":1,"inputs":{}}`)), user, ws, "OWNER")
	req.SetPathValue("slug", "pin-run")
	rr := httptest.NewRecorder()
	h.Run(rr, req)
	if rr.Code != 200 {
		t.Fatalf("%d: %s", rr.Code, rr.Body.String())
	}
	var v, head int
	var definition string
	if err := db.QueryRow(`SELECT pipeline_version,executed_definition_json FROM pipeline_runs WHERE pipeline_id=?`, "pin_run").Scan(&v, &definition); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT head_version FROM pipelines WHERE id=?`, "pin_run").Scan(&head); err != nil {
		t.Fatal(err)
	}
	if v != 1 || head != 2 || !strings.Contains(definition, "old recipe") {
		t.Fatalf("version=%d head=%d recipe=%s", v, head, definition)
	}
}

func TestRoutineRecordedState_AgreesAcrossCatalogDetailAndRun(t *testing.T) {
	h, db, user, ws := runsHandlerRig(t)
	seedPipelineWithVersions(t, h, ws, "recorded_summary", "recorded-summary", 2)
	rec := &pipeline.RunRecord{ID: "recorded_failure", WorkspaceID: ws, PipelineID: "recorded_summary", PipelineSlug: "recorded-summary", Status: pipeline.RunStatusRunning, StartedAt: time.Now().UTC()}
	if err := pipeline.NewRunStore(db).Insert(context.Background(), rec); err != nil {
		t.Fatal(err)
	}
	if err := pipeline.NewRunStore(db).MarkTerminal(context.Background(), pipeline.MarkTerminalInput{RunID: rec.ID, Status: pipeline.RunStatusCompleted, Output: "Missing outcome", HasOutcomeCapableStep: true}); err != nil {
		t.Fatal(err)
	}
	for _, list := range []bool{false, true} {
		req := withWorkspaceUser(httptest.NewRequest("GET", "/pipelines", nil), user, ws, "OWNER")
		req.SetPathValue("slug", "recorded-summary")
		rr := httptest.NewRecorder()
		var row pipelineResponse
		if list {
			h.List(rr, req)
			var rows []pipelineResponse
			if err := json.Unmarshal(rr.Body.Bytes(), &rows); err != nil {
				t.Fatal(err)
			}
			for _, r := range rows {
				if r.Slug == "recorded-summary" {
					row = r
				}
			}
		} else {
			h.Get(rr, req)
			if err := json.Unmarshal(rr.Body.Bytes(), &row); err != nil {
				t.Fatal(err)
			}
		}
		if rr.Code != 200 || row.LastRecordedRunID != rec.ID || row.LastInvocationStatus != "failed" || row.HeadVersion == nil || *row.HeadVersion != 2 {
			t.Fatalf("list=%v response=%s", list, rr.Body.String())
		}
	}
}

func TestRoutineInputLabel_IsSharedWithChatForm(t *testing.T) {
	fields := slashFormSchemaForInputs([]pipeline.InputSpec{{Name: "focus", Label: "Briefing focus", Description: "What should the agent focus on?", Type: "string"}})
	if len(fields) != 1 || fields[0].Name != "focus" || fields[0].Label != "Briefing focus" || fields[0].Help == "" {
		t.Fatalf("lost input identity or presentation: %+v", fields)
	}
}

func TestRoutinePendingDecision_LinksExistingInboxItem(t *testing.T) {
	h, user, ws := newPipelineHandlerForCRUDTest(t)
	insertWaitpointRow(t, h.db, "decision-link", ws, "approval", "pending", "Review result", time.Now().UTC().Format(time.RFC3339))
	if err := inbox.Insert(context.Background(), h.db, h.logger, inbox.Item{WorkspaceID: ws, Kind: "waitpoint", SourceID: "decision-link", Title: "Review result"}); err != nil {
		t.Fatal(err)
	}
	var id string
	if err := h.db.QueryRow(`SELECT id FROM inbox_items WHERE workspace_id=? AND kind='waitpoint' AND source_id='decision-link'`, ws).Scan(&id); err != nil {
		t.Fatal(err)
	}
	req := withWorkspaceUser(httptest.NewRequest("GET", "/waitpoints", nil), user, ws, "OWNER")
	rr := httptest.NewRecorder()
	h.ListPendingWaitpoints(rr, req)
	var rows []struct {
		Token       string `json:"token"`
		InboxItemID string `json:"inbox_item_id"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &rows); err != nil {
		t.Fatal(err)
	}
	if rr.Code != 200 || len(rows) != 1 || rows[0].Token != "decision-link" || rows[0].InboxItemID != id {
		t.Fatalf("decision lost its Inbox identity: %s", rr.Body.String())
	}
}
