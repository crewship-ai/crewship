package api

import (
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/access"
	"github.com/crewship-ai/crewship/internal/journal"
)

func TestGuestRoutinePublicationReviewPinsActualVersion(t *testing.T) {
	for _, scenario := range []struct {
		name            string
		stale, resubmit bool
		terminal        string
	}{
		{"exact publication and audit rollback", false, false, ""},
		{"changed draft never approves old request", true, false, ""},
		{"new request supersedes older revision", true, true, ""},
		{"decline commits with its audit", false, false, "denied"},
		{"cancel commits with its audit", false, false, "cancelled"},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			stale := scenario.stale
			db := setupTestDB(t)
			owner, workspace := seedInviter(t, db)
			definition := `{"name":"review-routine","steps":[{"id":"prepare","type":"transform","transform":{"input":"PUBLISHED_BASELINE","expression":"."}}]}`
			seedPipelineRowDef(t, db, workspace, "review-routine", "review-routine", definition)
			store := access.Store{DB: db}
			for _, user := range []string{"review-editor", "review-reader"} {
				execOrFatal(t, db, `INSERT INTO users(id,email) VALUES(?,?)`, user, user+"@example.test")
				execOrFatal(t, db, `INSERT INTO workspace_members(id,user_id,workspace_id,role) VALUES(?,?,?,'VIEWER')`, user, user, workspace)
				member, err := store.Membership(t.Context(), user, workspace)
				if err != nil {
					t.Fatal(err)
				}
				rights := []access.Right{{Kind: "routine", ID: "review-routine", Operation: "view"}}
				if user == "review-editor" {
					rights = append(rights, access.Right{Kind: "routine", ID: "review-routine", Operation: "edit"})
				}
				if _, err = store.Replace(t.Context(), owner, user, workspace, "restricted", member, rights); err != nil {
					t.Fatal(err)
				}
			}
			writer := journal.NewWriter(db, newTestLogger(), journal.WriterOptions{})
			defer writer.Close()
			router, err := NewRouter(db, "synthetic-review-router-secret-long-enough", newTestLogger(), WithJournal(writer))
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(router.Shutdown)
			router.PipelinesHandler.runner = &stubRunner{output: "dry-run must not invoke an agent"}
			router.PipelinesHandler.SetSaveTokenSecret([]byte("synthetic-review-save-proof-secret"))
			tokens := map[string]string{}
			for _, user := range []string{owner, "review-editor", "review-reader"} {
				tokens[user] = mintTokenFor(t, db, user, user+strings.Repeat("f", 40))
			}
			call := func(user, method, path string, body any, want int) *httptest.ResponseRecorder {
				t.Helper()
				raw, marshalErr := json.Marshal(body)
				if marshalErr != nil {
					t.Fatal(marshalErr)
				}
				req := httptest.NewRequest(method, path, strings.NewReader(string(raw)))
				req.Header.Set("Authorization", "Bearer "+tokens[user])
				req.Header.Set("X-Workspace-ID", workspace)
				response := httptest.NewRecorder()
				router.ServeHTTP(response, req)
				if response.Code != want {
					t.Fatalf("%s %s %s: got %d want %d: %s", user, method, path, response.Code, want, response.Body.String())
				}
				return response
			}
			base := "/api/v1/workspaces/" + workspace + "/restricted-routine-library/review-routine/draft"
			var draft guestRoutineDraft
			if err = json.Unmarshal(call("review-editor", "GET", base, nil, 200).Body.Bytes(), &draft); err != nil {
				t.Fatal(err)
			}
			draft.Definition = json.RawMessage(strings.Replace(definition, "PUBLISHED_BASELINE", "UNPUBLISHED_REVIEW_CANARY", 1))
			if err = json.Unmarshal(call("review-editor", "PUT", base, draft.guestRoutineDraftContent, 200).Body.Bytes(), &draft); err != nil {
				t.Fatal(err)
			}
			snapshot := routineDraftReviewSnapshot{DraftID: draft.DraftID, Revision: draft.Revision, BaseRevision: draft.BaseRevision}
			call("review-reader", "POST", base+"/reviews", snapshot, 404)
			call("review-editor", "POST", base+"/reviews", map[string]any{"draft_id": snapshot.DraftID, "revision": snapshot.Revision, "base_revision": snapshot.BaseRevision, "requested_by": owner}, 400)
			execOrFatal(t, db, `CREATE TRIGGER reject_review_request_audit BEFORE INSERT ON journal_entries WHEN NEW.summary='Guest requested routine draft publication review' BEGIN SELECT RAISE(ABORT,'synthetic request audit failure'); END`)
			call("review-editor", "POST", base+"/reviews", snapshot, 500)
			var requests int
			if err = db.QueryRow(`SELECT COUNT(*) FROM approvals_queue WHERE kind='routine_draft_review'`).Scan(&requests); err != nil || requests != 0 {
				t.Fatal("failed request audit committed a queue row", requests, err)
			}
			execOrFatal(t, db, `DROP TRIGGER reject_review_request_audit`)
			reviewResponse := call("review-editor", "POST", base+"/reviews", snapshot, 202)
			var review guestRoutineDraftReview
			if err = json.Unmarshal(reviewResponse.Body.Bytes(), &review); err != nil || review.State != "pending" || review.Version != 0 {
				t.Fatal("request falsely claims publication", review, err)
			}
			if strings.Contains(reviewResponse.Body.String(), "UNPUBLISHED_REVIEW_CANARY") || strings.Contains(reviewResponse.Body.String(), "@example.test") || strings.Contains(reviewResponse.Body.String(), "requested_by") {
				t.Fatal("review receipt leaked content or identities", reviewResponse.Body.String())
			}
			var repeated guestRoutineDraftReview
			if err = json.Unmarshal(call("review-editor", "POST", base+"/reviews", snapshot, 202).Body.Bytes(), &repeated); err != nil || repeated.ID != review.ID {
				t.Fatal("same snapshot created duplicate review", repeated, err)
			}
			var requestAudits int
			if err = db.QueryRow(`SELECT COUNT(*) FROM approvals_queue WHERE kind='routine_draft_review'`).Scan(&requests); err != nil || requests != 1 {
				t.Fatal("idempotent submit inserted extra requests", requests, err)
			}
			if err = db.QueryRow(`SELECT COUNT(*) FROM journal_entries WHERE summary='Guest requested routine draft publication review' AND actor_type='user' AND actor_id='review-editor' AND payload NOT LIKE '%UNPUBLISHED_REVIEW_CANARY%'`).Scan(&requestAudits); err != nil || requestAudits != 1 {
				t.Fatal("request audit duplicated or lost human provenance", requestAudits, err)
			}
			call("review-reader", "GET", base+"/reviews/"+review.ID, nil, 404)
			call(owner, "POST", "/api/v1/approvals/"+review.ID+"/decide?workspace_id="+workspace, map[string]any{"status": "approved"}, 409)
			var queueStatus, published string
			if err = db.QueryRow(`SELECT status FROM approvals_queue WHERE id=?`, review.ID).Scan(&queueStatus); err != nil || queueStatus != "pending" {
				t.Fatal("generic Approve committed a publication decision", queueStatus, err)
			}
			if err = db.QueryRow(`SELECT definition_json FROM pipelines WHERE id='review-routine'`).Scan(&published); err != nil || !strings.Contains(published, "PUBLISHED_BASELINE") {
				t.Fatal("review request changed executable recipe", published, err)
			}
			if scenario.terminal != "" {
				endpoint, summary := "/decide", "Routine draft publication review declined"
				decision := map[string]any{"status": "denied", "comment": "PRIVATE_REVIEWER_COMMENT_CANARY"}
				if scenario.terminal == "cancelled" {
					endpoint, summary = "/cancel", "Routine draft publication review cancelled"
					decision = map[string]any{"reason": "PRIVATE_REVIEWER_COMMENT_CANARY"}
				}
				control := "/api/v1/approvals/" + review.ID + endpoint + "?workspace_id=" + workspace
				execOrFatal(t, db, `CREATE TRIGGER reject_review_terminal_audit BEFORE INSERT ON journal_entries WHEN NEW.summary='`+summary+`' BEGIN SELECT RAISE(ABORT,'synthetic decision audit failure'); END`)
				call(owner, "POST", control, decision, 500)
				if err = db.QueryRow(`SELECT status FROM approvals_queue WHERE id=?`, review.ID).Scan(&queueStatus); err != nil || queueStatus != "pending" {
					t.Fatal("failed decision audit committed status", queueStatus, err)
				}
				execOrFatal(t, db, `DROP TRIGGER reject_review_terminal_audit`)
				call(owner, "POST", control, decision, 200)
				response := call("review-editor", "GET", base+"/reviews/"+review.ID, nil, 200)
				var terminal guestRoutineDraftReview
				if err = json.Unmarshal(response.Body.Bytes(), &terminal); err != nil || terminal.State != scenario.terminal || terminal.Version != 0 || strings.Contains(response.Body.String(), "PRIVATE_REVIEWER_COMMENT_CANARY") {
					t.Fatal("terminal receipt leaked private feedback or claimed publication", terminal, err)
				}
				return
			}
			if stale {
				draft.Name = "Latest reviewed metadata"
				if err = json.Unmarshal(call("review-editor", "PUT", base, draft.guestRoutineDraftContent, 200).Body.Bytes(), &draft); err != nil {
					t.Fatal(err)
				}
				if !strings.Contains(call("review-editor", "GET", base+"/reviews/"+review.ID, nil, 200).Body.String(), `"state":"superseded"`) {
					t.Fatal("changed draft still presented as pending exact review")
				}
			}
			if scenario.resubmit {
				old := review
				next := routineDraftReviewSnapshot{DraftID: draft.DraftID, Revision: draft.Revision, BaseRevision: draft.BaseRevision}
				// A failed replacement audit must preserve the earlier pending row.
				execOrFatal(t, db, `CREATE TRIGGER reject_replacement_review_audit BEFORE INSERT ON journal_entries WHEN NEW.summary='Guest requested routine draft publication review' BEGIN SELECT RAISE(ABORT,'synthetic replacement failure'); END`)
				call("review-editor", "POST", base+"/reviews", next, 500)
				if err = db.QueryRow(`SELECT status FROM approvals_queue WHERE id=?`, old.ID).Scan(&queueStatus); err != nil || queueStatus != "pending" {
					t.Fatal("failed replacement cancelled previous request", queueStatus, err)
				}
				execOrFatal(t, db, `DROP TRIGGER reject_replacement_review_audit`)
				if err = json.Unmarshal(call("review-editor", "POST", base+"/reviews", next, 202).Body.Bytes(), &review); err != nil || review.ID == old.ID {
					t.Fatal("new revision did not replace review", review, err)
				}
				if !strings.Contains(call("review-editor", "GET", base+"/reviews/"+old.ID, nil, 200).Body.String(), `"state":"cancelled"`) {
					t.Fatal("old request stayed pending after a new revision submission")
				}
			}
			trusted := "/api/v1/workspaces/" + workspace + "/pipelines"
			var proof struct {
				SaveToken string `json:"save_token"`
			}
			if err = json.Unmarshal(call(owner, "POST", trusted+"/test_run", map[string]any{"definition": draft.Definition}, 200).Body.Bytes(), &proof); err != nil || proof.SaveToken == "" {
				t.Fatal("trusted structural test did not mint a save proof", proof, err)
			}
			publication := map[string]any{"id": draft.DraftID, "revision": draft.Revision, "save_token": proof.SaveToken}
			if !stale {
				// Represent a request authenticated before authority changed. The
				// handler's cached OWNER context must not authorize its commit.
				for _, lost := range []string{"role", "suspension", "guest ceiling", "workspace archived"} {
					switch lost {
					case "role":
						execOrFatal(t, db, `UPDATE workspace_members SET role='VIEWER' WHERE workspace_id=? AND user_id=?`, workspace, owner)
					case "suspension":
						execOrFatal(t, db, `UPDATE users SET suspended_at='' WHERE id=?`, owner)
					case "workspace archived":
						execOrFatal(t, db, `UPDATE workspaces SET deleted_at='2026-10-15T00:00:00Z' WHERE id=?`, workspace)
					case "guest ceiling":
						execOrFatal(t, db, `UPDATE workspace_members SET role='MANAGER' WHERE workspace_id=? AND user_id=?`, workspace, owner)
						execOrFatal(t, db, `INSERT INTO workspaces(id,name,slug) VALUES('review-other','Other','review-other')`)
						execOrFatal(t, db, `INSERT INTO workspace_members(id,workspace_id,user_id,role,access_mode) VALUES('review-other-membership','review-other',?,'VIEWER','restricted')`, owner)
					}
					raw, marshalErr := json.Marshal(publication)
					if marshalErr != nil {
						t.Fatal(marshalErr)
					}
					admitted := withAuthCtx(withWorkspaceCtx(httptest.NewRequest("POST", "/x", strings.NewReader(string(raw))), workspace), owner, "OWNER")
					admitted.SetPathValue("slug", "review-routine")
					denied := httptest.NewRecorder()
					router.PipelinesHandler.PublishDraft(denied, admitted)
					if denied.Code != 409 {
						t.Fatalf("publisher lost %s but cached context committed: %d %s", lost, denied.Code, denied.Body.String())
					}
					switch lost {
					case "role":
						execOrFatal(t, db, `UPDATE workspace_members SET role='OWNER' WHERE workspace_id=? AND user_id=?`, workspace, owner)
					case "suspension":
						execOrFatal(t, db, `UPDATE users SET suspended_at=NULL WHERE id=?`, owner)
					case "workspace archived":
						execOrFatal(t, db, `UPDATE workspaces SET deleted_at=NULL WHERE id=?`, workspace)
					case "guest ceiling":
						execOrFatal(t, db, `DELETE FROM workspace_members WHERE id='review-other-membership'`)
						execOrFatal(t, db, `UPDATE workspace_members SET role='OWNER' WHERE workspace_id=? AND user_id=?`, workspace, owner)
					}
				}
				execOrFatal(t, db, `CREATE TRIGGER reject_review_receipt_audit BEFORE INSERT ON journal_entries WHEN NEW.summary='Tested routine draft published after review' BEGIN SELECT RAISE(ABORT,'synthetic audit failure'); END`)
				call(owner, "POST", trusted+"/review-routine/publish", publication, 500)
				if err = db.QueryRow(`SELECT status FROM approvals_queue WHERE id=?`, review.ID).Scan(&queueStatus); err != nil || queueStatus != "pending" {
					t.Fatal("audit failure committed review receipt", queueStatus, err)
				}
				if err = db.QueryRow(`SELECT definition_json FROM pipelines WHERE id='review-routine'`).Scan(&published); err != nil || !strings.Contains(published, "PUBLISHED_BASELINE") {
					t.Fatal("audit failure committed executable recipe", published, err)
				}
				call("review-editor", "GET", base, nil, 200)
				execOrFatal(t, db, `DROP TRIGGER reject_review_receipt_audit`)
			}
			call(owner, "POST", trusted+"/review-routine/publish", publication, 201)
			var final guestRoutineDraftReview
			if err = json.Unmarshal(call("review-editor", "GET", base+"/reviews/"+review.ID, nil, 200).Body.Bytes(), &final); err != nil {
				t.Fatal(err)
			}
			if stale && !scenario.resubmit {
				if final.State != "cancelled" || final.Version != 0 {
					t.Fatal("newer publication falsely fulfilled older request", final)
				}
			} else {
				var head int
				if err = db.QueryRow(`SELECT head_version FROM pipelines WHERE id='review-routine'`).Scan(&head); err != nil || final.State != "published" || final.Version != head || head < 1 {
					t.Fatal("receipt did not pin actual committed version", final, head, err)
				}
			}
		})
	}
}
