package api

import (
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/access"
)

func TestGuestRoutineResultsExplicitAudience(t *testing.T) {
	db := setupTestDB(t)
	owner, workspace := seedInviter(t, db)
	for _, user := range []string{"result-a", "result-b"} {
		execOrFatal(t, db, `INSERT INTO users(id,email) VALUES(?,?)`, user, user+"@example.test")
		execOrFatal(t, db, `INSERT INTO workspace_members(id,workspace_id,user_id,role) VALUES(?,?,?,'VIEWER')`, user, workspace, user)
	}
	for _, id := range []string{"shared-result", "other-result"} {
		seedPipelineRow(t, db, workspace, id, id)
	}
	execOrFatal(t, db, `INSERT INTO crews(id,workspace_id,name,slug) VALUES('result-crew',?,'Crew','result-crew')`, workspace)
	seedAgentRow(t, db, "result-agent", workspace, "result-crew", "Agent", "result-agent", "AGENT")
	execOrFatal(t, db, `INSERT INTO chats(id,workspace_id,agent_id,created_by,visibility,title) VALUES('result-chat',?,'result-agent',?,'group','PRIVATE_CHAT_TITLE_CANARY')`, workspace, owner)
	execOrFatal(t, db, `INSERT INTO chat_participants(chat_id,user_id,role) VALUES('result-chat','result-a','member')`)
	originReceipt, _ := json.Marshal(map[string]any{"chat_id": "result-chat", "workspace_id": workspace, "user_id": owner})
	execOrFatal(t, db, `INSERT INTO execution_origins(id,workspace_id,root_run_id,chat_id,trigger_message_id,human_authority,created_at) VALUES('result-origin',?,'root-result','result-chat','message-result',?,'2026-10-15')`, workspace, string(originReceipt))
	seedRun := func(id, routine, origin, date string) {
		t.Helper()
		execOrFatal(t, db, `INSERT INTO pipeline_runs(id,workspace_id,pipeline_id,pipeline_slug,status,started_at,ended_at,output,inputs_json,error_message,invoking_user_id,chat_origin_id) VALUES(?,?,?,?,'completed',?,?,'SHARED_FINAL_OUTPUT','{"PRIVATE_INPUT_CANARY":true}','PRIVATE_ERROR_CANARY',?,NULLIF(?,''))`, id, workspace, routine, routine, date, date, owner, origin)
		execOrFatal(t, db, `INSERT INTO pipeline_run_step_outputs(run_id,step_id,output,updated_at) VALUES(?,'answer','SHARED_STEP_OUTPUT',?)`, id, date)
	}
	seedRun("run-a", "shared-result", "", "2026-10-13T00:00:00Z")
	seedRun("run-b", "shared-result", "", "2026-10-14T00:00:00Z")
	seedRun("run-private", "shared-result", "result-origin", "2026-10-15T00:00:00Z")
	seedRun("run-foreign", "other-result", "", "2026-10-16T00:00:00Z")
	for _, id := range []string{"run-issue-canary", "run-legacy-human-canary", "run-invalid-journal-canary", "run-missing-authority-canary", "run-unproven-canary", "run-null-replay-canary"} {
		seedRun(id, "shared-result", "", "2026-10-12T00:00:00Z")
	}
	project := seedProject(t, db, workspace, "unshared-result-project")
	issue := seedIssue(t, db, workspace, "result-crew", "result-agent", "RESULT-1", "BACKLOG")
	execOrFatal(t, db, `UPDATE missions SET project_id=? WHERE id=?`, project, issue)
	execOrFatal(t, db, `UPDATE pipeline_runs SET triggered_via='issue',triggered_by_id='RESULT-1',metadata_json=?,output='PRIVATE_ISSUE_OUTPUT_CANARY' WHERE id='run-issue-canary'`, `{"issue_id":"`+issue+`"}`)
	execOrFatal(t, db, `UPDATE pipeline_runs SET is_replay=1,replay_of='run-private' WHERE id='run-null-replay-canary'`)
	for id, payload := range map[string]string{
		"run-legacy-human-canary":      `{"human_authority":{"chat_id":"private-dm"}}`,
		"run-invalid-journal-canary":   `{broken`,
		"run-missing-authority-canary": `{"requester_authority_missing":true}`,
	} {
		execOrFatal(t, db, `INSERT INTO journal_entries(id,workspace_id,trace_id,entry_type,actor_type,summary,payload) VALUES(?,?,?,'run.started','orchestrator','Private legacy',?)`, "canary-"+id, workspace, id, payload)
	}
	for _, id := range []string{"run-a", "run-b"} {
		execOrFatal(t, db, `INSERT INTO journal_entries(id,workspace_id,actor_id,entry_type,actor_type,summary,payload) VALUES(?,?,?,'pipeline.run.started','orchestrator','Autonomous run','{}')`, "proof-"+id, workspace, id)
	}
	store := access.Store{DB: db}
	setRights := func(user string, rights ...access.Right) {
		t.Helper()
		m, err := store.Membership(t.Context(), user, workspace)
		if err != nil {
			t.Fatal(err)
		}
		if _, err = store.Replace(t.Context(), owner, user, workspace, "restricted", m, rights); err != nil {
			t.Fatal(err)
		}
	}
	view := access.Right{Kind: "routine", ID: "shared-result", Operation: "view"}
	results := access.Right{Kind: "routine", ID: "shared-result", Operation: "results"}
	chat := access.Right{Kind: "agent", ID: "result-agent", Operation: "chat"}
	setRights("result-a", view)
	setRights("result-b", access.Right{Kind: "routine", ID: "other-result", Operation: "view"}, access.Right{Kind: "routine", ID: "other-result", Operation: "results"})
	router, err := NewRouter(db, "routine-results-fixture-secret-long-enough", newTestLogger())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(router.Shutdown)
	tokens := map[string]string{}
	for _, user := range []string{"result-a", "result-b"} {
		tokens[user] = mintTokenFor(t, db, user, user+strings.Repeat("0", 40))
	}
	call := func(user, path string, want int) *httptest.ResponseRecorder {
		t.Helper()
		req := httptest.NewRequest("GET", path, nil)
		req.Header.Set("Authorization", "Bearer "+tokens[user])
		req.Header.Set("X-Workspace-ID", workspace)
		rr := httptest.NewRecorder()
		router.ServeHTTP(rr, req)
		if rr.Code != want {
			t.Fatalf("%s %s: %d want %d: %s", user, path, rr.Code, want, rr.Body.String())
		}
		return rr
	}
	base := "/api/v1/workspaces/" + workspace + "/restricted-routine-library/shared-result/runs"
	call("result-a", base, 404) // Definition-only grants must not widen.
	setRights("result-a", results)
	call("result-a", base, 404) // Results still requires View.
	setRights("result-a", view, results)
	list := call("result-a", base+"?limit=1&offset=1", 200)
	if list.Header().Get("X-Total-Count") != "2" || !strings.Contains(list.Body.String(), "run-a") {
		t.Fatal("pagination/count", list.Header(), list.Body.String())
	}
	for _, canary := range []string{"run-private", "run-foreign", "SHARED_FINAL_OUTPUT", "SHARED_STEP_OUTPUT", "PRIVATE_INPUT_CANARY", "PRIVATE_ERROR_CANARY", owner, "PRIVATE_CHAT_TITLE_CANARY"} {
		if strings.Contains(list.Body.String(), canary) {
			t.Fatalf("directory leak %s: %s", canary, list.Body.String())
		}
	}
	if list.Header().Get("Cache-Control") != "no-store" {
		t.Fatal("missing private cache policy")
	}
	detail := call("result-a", base+"/run-a", 200)
	var result guestRoutineRunResult
	if err = json.Unmarshal(detail.Body.Bytes(), &result); err != nil || result.Output == nil || *result.Output != "SHARED_FINAL_OUTPUT" || result.StepOutputs["answer"] != "SHARED_STEP_OUTPUT" {
		t.Fatal("outputs", result, err)
	}
	secret := "ghp_" + strings.Repeat("a", 36)
	execOrFatal(t, db, `UPDATE pipeline_runs SET output=? WHERE id='run-a'`, "Historical final "+secret)
	execOrFatal(t, db, `UPDATE pipeline_run_step_outputs SET output=? WHERE run_id='run-a'`, "Historical step "+secret)
	scrubbed := call("result-a", base+"/run-a", 200)
	if strings.Contains(scrubbed.Body.String(), secret) {
		t.Fatal("historical credential leaked")
	}
	execOrFatal(t, db, `UPDATE pipeline_runs SET output=? WHERE id='run-a'`, strings.Repeat("x", maxGuestRoutineResultContentBytes+1))
	call("result-a", base+"/run-a", 413)
	execOrFatal(t, db, `UPDATE pipeline_runs SET output='small' WHERE id='run-a'`)
	execOrFatal(t, db, `UPDATE pipeline_run_step_outputs SET output=? WHERE run_id='run-a'`, strings.Repeat("x", maxGuestRoutineResultContentBytes+1))
	call("result-a", base+"/run-a", 413)
	execOrFatal(t, db, `UPDATE pipeline_run_step_outputs SET output='small' WHERE run_id='run-a'`)
	execOrFatal(t, db, `UPDATE pipeline_runs SET output=? WHERE id='run-a'`, strings.Repeat("\x01", 200000))
	call("result-a", base+"/run-a", 413) // JSON escaping also obeys the response ceiling.
	execOrFatal(t, db, `UPDATE pipeline_runs SET output='SHARED_FINAL_OUTPUT' WHERE id='run-a'`)

	for _, canary := range []string{"PRIVATE_INPUT_CANARY", "PRIVATE_ERROR_CANARY", owner, "PRIVATE_CHAT_TITLE_CANARY"} {
		if strings.Contains(detail.Body.String(), canary) {
			t.Fatal("detail leak", canary, detail.Body.String())
		}
	}
	for _, id := range []string{"run-issue-canary", "run-legacy-human-canary", "run-invalid-journal-canary", "run-missing-authority-canary", "run-unproven-canary", "run-null-replay-canary"} {
		call("result-a", base+"/"+id, 404)
		if strings.Contains(list.Body.String(), id) {
			t.Fatalf("private run listed: %s", id)
		}
	}
	call("result-a", base+"/run-foreign", 404)
	call("result-b", base, 404)
	call("result-b", base+"/run-a", 404)
	call("result-a", "/api/v1/workspaces/not-selected/restricted-routine-library/shared-result/runs?workspace_id="+workspace, 404)
	call("result-a", base+"?limit=501", 400)
	call("result-a", base+"?offset=-1", 400)
	call("result-a", base+"/run-private", 404) // A routine share alone cannot share a private chat.
	setRights("result-a", view, results, chat)
	call("result-a", base+"/run-private", 200)
	list = call("result-a", base, 200)
	if list.Header().Get("X-Total-Count") != "3" {
		t.Fatal("readable origin excluded", list.Header(), list.Body.String())
	}
	execOrFatal(t, db, `DELETE FROM chat_participants WHERE chat_id='result-chat' AND user_id='result-a'`)
	call("result-a", base+"/run-private", 404)
	list = call("result-a", base, 200)
	if list.Header().Get("X-Total-Count") != "2" || strings.Contains(list.Body.String(), "run-private") {
		t.Fatal("origin audience revoke", list.Header(), list.Body.String())
	}
	// Pinned and live recipe origins independently constrain history, including
	// after the live recipe is deliberately detached from its old origin.
	execOrFatal(t, db, `INSERT INTO pipeline_versions(id,pipeline_id,version,definition_json,definition_hash,author_type,author_id,created_at,chat_origin_id) VALUES('result-version','shared-result',1,'{}','result-hash','user',?,'2026-10-15','result-origin')`, owner)
	execOrFatal(t, db, `UPDATE pipeline_runs SET pipeline_version=1 WHERE id='run-a'`)
	call("result-a", base+"/run-a", 404)
	execOrFatal(t, db, `UPDATE pipelines SET chat_origin_id='result-origin' WHERE id='shared-result'`)
	list = call("result-a", base, 200)
	if list.Header().Get("X-Total-Count") != "0" || strings.TrimSpace(list.Body.String()) != "[]" {
		t.Fatal("live recipe origin", list.Header(), list.Body.String())
	}
	execOrFatal(t, db, `UPDATE pipelines SET chat_origin_id=NULL WHERE id='shared-result'`)
	call("result-a", base+"/run-a", 404)
	call("result-a", base+"/run-b", 200)
	// Live issuer changes invalidate the entire share, without a token refresh.
	execOrFatal(t, db, `UPDATE workspace_members SET role='MEMBER' WHERE workspace_id=? AND user_id=?`, workspace, owner)
	call("result-a", base, 404)
	call("result-a", base+"/run-b", 404)
	execOrFatal(t, db, `UPDATE users SET suspended_at='' WHERE id='result-a'`)
	call("result-a", base, 401)
}
