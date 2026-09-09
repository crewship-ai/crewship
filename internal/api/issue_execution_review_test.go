package api

import (
	"net/http/httptest"
	"testing"
)

func TestIssueReview_ActiveAndStaleResultsCannotBeApproved(t *testing.T) {
	h, user, ws, crew, lead, _ := newTestIssueHandler(t)
	id := seedIssue(t, h.db, ws, crew, lead, "ENG-77", "IN_PROGRESS")
	if _, err := h.db.Exec(`INSERT INTO issue_executions(id,mission_id,work_revision,brief_revision,stage,reviewer_agent_id,created_at,updated_at) SELECT 'execution',mission_id,revision,brief_revision,'reviewing',?,'2026-09-08','2026-09-08' FROM issue_work WHERE mission_id=?`, lead, id); err != nil {
		t.Fatal(err)
	}
	call := func(body string, want int) {
		t.Helper()
		rec := httptest.NewRecorder()
		h.Review(rec, covIWReq(user, ws, "OWNER", "POST", body, crew, "ENG-77"))
		if rec.Code != want {
			t.Fatalf("got %d want %d: %s", rec.Code, want, rec.Body.String())
		}
	}
	call(`{"action":"approve"}`, 409)
	if _, err := h.db.Exec(`UPDATE issue_executions SET stage='accepted' WHERE id='execution'; UPDATE missions SET status='REVIEW' WHERE id=?`, id); err != nil {
		t.Fatal(err)
	}
	call(`{"action":"approve","revision":999}`, 409)
	call(`{"action":"approve"}`, 200)
	call(`{"action":"approve"}`, 400)
	var count int
	if err := h.db.QueryRow(`SELECT COUNT(*) FROM mission_activity WHERE mission_id=? AND action='review_approved'`, id).Scan(&count); err != nil || count != 1 {
		t.Fatalf("approval receipt count %d: %v", count, err)
	}
}

func TestIssueReviewPolicy_CannotChangeDuringWork(t *testing.T) {
	h, user, ws, crew, lead, _ := newTestIssueHandler(t)
	id := seedIssue(t, h.db, ws, crew, lead, "ENG-78", "TODO")
	var revision int
	h.db.QueryRow(`SELECT revision FROM issue_work WHERE mission_id=?`, id).Scan(&revision)
	req := covIWReq(user, ws, "OWNER", "PUT", `{"revision":0,"client_review_required":false}`, crew, "ENG-78")
	// New issues start at revision zero.
	if revision != 0 {
		t.Fatalf("unexpected initial revision %d", revision)
	}
	rec := httptest.NewRecorder()
	h.ReviewPolicy(rec, req)
	if rec.Code != 200 {
		t.Fatal(rec.Code, rec.Body.String())
	}
	if _, err := h.db.Exec(`UPDATE missions SET status='IN_PROGRESS' WHERE id=?`, id); err != nil {
		t.Fatal(err)
	}
	rec = httptest.NewRecorder()
	h.ReviewPolicy(rec, covIWReq(user, ws, "OWNER", "PUT", `{"revision":1,"client_review_required":true}`, crew, "ENG-78"))
	if rec.Code != 409 {
		t.Fatal(rec.Code, rec.Body.String())
	}
}

func TestIssueRoutineStart_ValidatesStoredInputsBeforeDispatch(t *testing.T) {
	h, user, ws, crew, lead, _ := newTestIssueHandler(t)
	id := seedIssue(t, h.db, ws, crew, lead, "ENG-79", "TODO")
	routine := seedTestPipeline(t, h, ws, "requires-input")
	if _, err := h.db.Exec(`UPDATE pipelines SET definition_json='{"dsl_version":"1.0","name":"requires-input","inputs":[{"name":"repository","type":"string","required":true}],"steps":[{"id":"work","type":"agent_run","agent_slug":"worker","prompt":"Review"}]}' WHERE id=?`, routine); err != nil {
		t.Fatal(err)
	}
	if _, err := h.db.Exec(`UPDATE missions SET routine_id=?,routine_inputs_json='{}' WHERE id=?`, routine, id); err != nil {
		t.Fatal(err)
	}
	h.routines = &PipelineHandler{}
	rec := httptest.NewRecorder()
	h.Start(rec, covIWReq(user, ws, "OWNER", "POST", "", crew, "ENG-79"))
	if rec.Code != 422 {
		t.Fatal(rec.Code, rec.Body.String())
	}
	var status string
	h.db.QueryRow(`SELECT status FROM missions WHERE id=?`, id).Scan(&status)
	if status != "TODO" {
		t.Fatal(status)
	}
}

func TestIssueExecutionDetailReadsTheRealRunProjection(t *testing.T) {
	h, user, ws, crew, lead, _ := newTestIssueHandler(t)
	id := seedIssue(t, h.db, ws, crew, lead, "ENG-81", "IN_PROGRESS")
	if _, err := h.db.Exec(`INSERT INTO issue_executions(id,mission_id,work_revision,brief_revision,stage,reviewer_agent_id,created_at,updated_at) SELECT 'detail-execution',mission_id,revision,brief_revision,'working',?,'2026-09-08','2026-09-08' FROM issue_work WHERE mission_id=?`, lead, id); err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	h.Get(rec, covIWReq(user, ws, "OWNER", "GET", "", crew, "ENG-81"))
	if rec.Code != 200 {
		t.Fatal(rec.Code, rec.Body.String())
	}
}

func TestIssueTakeoverCancelsParkedRoutineAndItsApproval(t *testing.T) {
	h, user, ws, crew, lead, _ := newTestIssueHandler(t)
	id := seedIssue(t, h.db, ws, crew, lead, "ENG-82", "IN_PROGRESS")
	routine := seedTestPipeline(t, h, ws, "parked-work")
	seedRunRow(t, h.db, ws, routine, "parked-work", "parked-run", "waiting")
	if _, err := h.db.Exec(`INSERT INTO issue_executions(id,mission_id,work_revision,brief_revision,stage,reviewer_agent_id,routine_run_id,created_at,updated_at) SELECT 'parked-execution',mission_id,revision,brief_revision,'working',?,'parked-run','2026-09-08','2026-09-08' FROM issue_work WHERE mission_id=?`, lead, id); err != nil {
		t.Fatal(err)
	}
	if _, err := h.db.Exec(`INSERT INTO pipeline_waitpoints(token,workspace_id,pipeline_run_id,step_id,kind,status,timeout_at) VALUES('parked-token',?,'parked-run','approve','approval','pending','2099-01-01T00:00:00Z')`, ws); err != nil {
		t.Fatal(err)
	}
	if _, err := h.db.Exec(`INSERT INTO inbox_items(id,workspace_id,kind,source_id,title,state) VALUES('parked-inbox',?,'waitpoint','parked-token','Approve test','unread')`, ws); err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	h.Work(rec, covIWReq(user, ws, "OWNER", "POST", `{"action":"take_over","revision":0,"operation_id":"take-over-parked"}`, crew, "ENG-82"))
	if rec.Code != 200 {
		t.Fatal(rec.Code, rec.Body.String())
	}
	var run, point, item string
	if err := h.db.QueryRow(`SELECT r.status,w.status,i.state FROM pipeline_runs r JOIN pipeline_waitpoints w ON w.pipeline_run_id=r.id JOIN inbox_items i ON i.source_id=w.token WHERE r.id='parked-run'`).Scan(&run, &point, &item); err != nil {
		t.Fatal(err)
	}
	if run != "cancelled" || point != "cancelled" || item != "resolved" {
		t.Fatalf("orphaned state: %s %s %s", run, point, item)
	}
}

// A mission without an issue_work row must still read. The row comes from a
// trigger and a backfill, neither of which covers a mission_type flipped by an
// UPDATE or a bundle restored without the table — and losing the whole issue
// detail to a 500 is a far worse failure than reporting revision 0.
func TestIssueDetail_SurvivesMissingWorkRow(t *testing.T) {
	h, user, ws, crew, lead, _ := newTestIssueHandler(t)
	id := seedIssue(t, h.db, ws, crew, lead, "ENG-82", "IN_PROGRESS")
	if _, err := h.db.Exec(`DELETE FROM issue_work WHERE mission_id=?`, id); err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	h.Get(rec, covIWReq(user, ws, "OWNER", "GET", "", crew, "ENG-82"))
	if rec.Code != 200 {
		t.Fatal(rec.Code, rec.Body.String())
	}
}
