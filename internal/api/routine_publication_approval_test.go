package api

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestPublicationScheduleApprovalCannotBypassSelectedAuthority(t *testing.T) {
	for _, selection := range []string{"legacy", "head", "pin", "wake"} {
		t.Run(selection, func(t *testing.T) {
			db, workspace, crew, user, _ := autonomyRig(t, "guided")
			seedPipelineRow(t, db, workspace, "approval-routine", "approval-routine")
			// This fresh minimal fixture has no archive: explicitly select the
			// existing legacy HEAD0 contract before creating the held schedule.
			execOrFatal(t, db, `UPDATE pipelines SET head_version=0 WHERE id='approval-routine'`)
			adapter := gatedRoutineAdapter(t, db, nil)
			created := httptest.NewRecorder()
			adapter.CreateSchedule(created, boundInternalReq("POST", "/?workspace_id="+workspace,
				`{"cron_expr":"*/5 * * * *","target_pipeline_slug":"approval-routine","enabled":true}`, workspace, crew))
			if created.Code != http.StatusAccepted {
				t.Fatalf("real held-schedule calibration: %d %s", created.Code, created.Body.String())
			}
			body := decodeGateBody(t, created.Body.Bytes())
			schedule, ok := body["id"].(string)
			if !ok || schedule == "" {
				t.Fatal("missing held schedule")
			}
			approval, ok := body["approval_id"].(string)
			if !ok || approval == "" {
				t.Fatal("missing real approval")
			}
			if selection != "legacy" {
				// A fresh immutable negative marker, never a valid publication
				// or a fabricated model-test/execution receipt.
				target := "approval-routine"
				if selection == "wake" {
					seedPipelineRow(t, db, workspace, "approval-wake", "approval-wake")
					target = "approval-wake"
				}
				execOrFatal(t, db, `INSERT INTO pipeline_versions(id,pipeline_id,version,definition_json,definition_hash,author_type,author_id,authority_fingerprint,publication_authority_json)
SELECT id||'-bounded',id,1,definition_json,definition_hash,'user',?,?,'{}' FROM pipelines WHERE id=?`, user, strings.Repeat("a", 64), target)
				switch selection {
				case "head":
					execOrFatal(t, db, `UPDATE pipelines SET head_version=1 WHERE id=?`, target)
				case "pin":
					execOrFatal(t, db, `UPDATE pipeline_schedules SET target_pipeline_version=1 WHERE id=?`, schedule)
				case "wake":
					execOrFatal(t, db, `UPDATE pipelines SET head_version=1 WHERE id=?`, target)
					execOrFatal(t, db, `UPDATE pipeline_schedules SET wake_pipeline_id=? WHERE id=?`, target, schedule)
				}
			}
			req := withWorkspaceUser(httptest.NewRequest("POST", "/api/v1/approvals/"+approval+"/decide",
				strings.NewReader(`{"status":"approved"}`)), user, workspace, "OWNER")
			req.SetPathValue("id", approval)
			response := httptest.NewRecorder()
			NewApprovalsHandler(db, testLogger(), nil).Decide(response, req)
			want, enabled, status := http.StatusConflict, 0, "pending"
			if selection == "legacy" {
				want, enabled, status = http.StatusOK, 1, "approved"
			}
			if response.Code != want {
				t.Fatalf("approval bypassed %s boundary: HTTP%d want%d", selection, response.Code, want)
			}
			var actualEnabled int
			var actualStatus string
			if err := db.QueryRow(`SELECT enabled FROM pipeline_schedules WHERE id=?`, schedule).Scan(&actualEnabled); err != nil {
				t.Fatal(err)
			}
			if err := db.QueryRow(`SELECT status FROM approvals_queue WHERE id=?`, approval).Scan(&actualStatus); err != nil {
				t.Fatal(err)
			}
			if actualEnabled != enabled || actualStatus != status {
				t.Fatalf("schedule/approval did not roll back together: enabled=%d status=%s", actualEnabled, actualStatus)
			}
		})
	}
}
