package api

import (
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/access"
)

func TestGuestIssueActivePipelineBlocksMutations(t *testing.T) {
	for _, state := range []string{"queued", "running", "waiting", "completed"} {
		for _, operation := range []string{"update", "delete", "project move"} {
			t.Run(state+"/"+operation, func(t *testing.T) {
				f := setupDeletionFixture(t)
				db := f.h.db
				execOrFatal(t, db, `INSERT INTO projects(id,workspace_id,name,slug) VALUES('source-project',?,'Source','source'),('target-project',?,'Target','target')`, f.workspace, f.workspace)
				execOrFatal(t, db, `UPDATE missions SET project_id='source-project',status='IN_PROGRESS' WHERE id=?`, f.issue)
				seedPipelineRow(t, db, f.workspace, "busy-pipeline", "busy-pipeline")
				seedRunRow(t, db, f.workspace, "busy-pipeline", "busy-pipeline", "linked-run", state)
				var lead string
				if err := db.QueryRow(`SELECT lead_agent_id FROM missions WHERE id=?`, f.issue).Scan(&lead); err != nil {
					t.Fatal(err)
				}
				execOrFatal(t, db, `INSERT INTO issue_executions(id,mission_id,work_revision,brief_revision,stage,reviewer_agent_id,routine_run_id,created_at,updated_at) VALUES('linked-execution',?,0,0,'working',?,'linked-run',datetime('now'),datetime('now'))`, f.issue, lead)
				// The shipped status trigger supersedes the execution without
				// terminating its routine. Visible Backlog is not proof of idle work.
				execOrFatal(t, db, `UPDATE missions SET status='BACKLOG' WHERE id=?`, f.issue)
				var stage string
				if err := db.QueryRow(`SELECT stage FROM issue_executions WHERE id='linked-execution'`).Scan(&stage); err != nil || stage != "superseded" {
					t.Fatalf("execution stage=%q err=%v", stage, err)
				}
				execOrFatal(t, db, `INSERT INTO users(id,email) VALUES('busy-guest','busy-guest@example.test')`)
				execOrFatal(t, db, `INSERT INTO workspace_members(id,user_id,workspace_id,role) VALUES('busy-member','busy-guest',?,'VIEWER')`, f.workspace)
				store := access.Store{DB: db}
				member, err := store.Membership(t.Context(), "busy-guest", f.workspace)
				if err != nil {
					t.Fatal(err)
				}
				var rights []access.Right
				for _, project := range []string{"source-project", "target-project"} {
					for _, right := range []string{"read", "contribute", "manage", "delete_issues"} {
						rights = append(rights, access.Right{Kind: "project", ID: project, Operation: right})
					}
				}
				if _, err = store.Replace(t.Context(), f.user, "busy-guest", f.workspace, "restricted", member, rights); err != nil {
					t.Fatal(err)
				}
				router, err := NewRouter(db, "active-pipeline-regression-secret-long-enough", newTestLogger())
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(router.Shutdown)
				token := mintTokenFor(t, db, "busy-guest", strings.Repeat("b", 48))
				base := "/api/v1/workspaces/" + f.workspace + "/restricted-issues/" + f.issue
				call := func(method, path, body string) *httptest.ResponseRecorder {
					req := httptest.NewRequest(method, path, strings.NewReader(body))
					req.Header.Set("Authorization", "Bearer "+token)
					req.Header.Set("X-Workspace-ID", f.workspace)
					rr := httptest.NewRecorder()
					router.ServeHTTP(rr, req)
					return rr
				}
				active := state != "completed"
				method, body, want := "PATCH", `{"status":"TODO"}`, 200
				if active {
					want = 409
				}
				if operation == "delete" {
					preview := call("GET", base+"/deletion", "")
					var snapshot guestIssueDeletion
					if preview.Code != 200 || json.Unmarshal(preview.Body.Bytes(), &snapshot) != nil {
						t.Fatalf("preview: %d %s", preview.Code, preview.Body.String())
					}
					if snapshot.CanDelete == active {
						t.Errorf("can_delete=%t with run %s", snapshot.CanDelete, state)
					}
					raw, _ := json.Marshal(map[string]any{"project_id": snapshot.ProjectID, "version": snapshot.Version, "confirm": true})
					method, body = "DELETE", string(raw)
					if !active {
						want = 204
					}
				} else if operation == "project move" {
					// Guest intake cannot reassign projects, even with shares on both.
					body, want = `{"status":"TODO","project_id":"target-project"}`, 400
				}
				rr := call(method, base, body)
				if rr.Code != want {
					t.Errorf("%s = %d want %d: %s", operation, rr.Code, want, rr.Body.String())
				}
				if active || operation == "project move" {
					var status, project, currentStage string
					if err := db.QueryRow(`SELECT m.status,m.project_id,x.stage FROM missions m JOIN issue_executions x ON x.mission_id=m.id WHERE m.id=?`, f.issue).Scan(&status, &project, &currentStage); err != nil || status != "BACKLOG" || project != "source-project" || currentStage != "superseded" {
						t.Errorf("rejected mutation changed/cascaded work: status=%s project=%s execution=%s err=%v", status, project, currentStage, err)
					}
				}
				var runStatus string
				if err := db.QueryRow(`SELECT status FROM pipeline_runs WHERE id='linked-run'`).Scan(&runStatus); err != nil || runStatus != state {
					t.Errorf("routine changed: %q %v", runStatus, err)
				}
			})
		}
	}
}
