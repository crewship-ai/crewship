package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestChatRoutineSourceAndSearch(t *testing.T) {
	db := setupTestDB(t)
	ws := chatKindSeed(t, db)
	execOrFatal(t, db, `INSERT INTO pipelines(id,workspace_id,slug,name,definition_json,definition_hash) VALUES('source-pipeline',?,'weekly','Weekly','{}','hash')`, ws)
	execOrFatal(t, db, `INSERT INTO pipeline_runs(id,workspace_id,pipeline_id,pipeline_slug,status,started_at) VALUES('source-run',?,'source-pipeline','weekly','running','2026-09-25')`, ws)
	h := NewInternalHandler(db, "tok", newTestLogger())
	create := func(id, workspace, run string) *httptest.ResponseRecorder {
		req := httptest.NewRequest("POST", "/api/v1/internal/chats", strings.NewReader(fmt.Sprintf(`{"chat_id":%q,"agent_id":"ck-ag","workspace_id":%q,"origin":"ROUTINE","title":"Žluťoučký Needle %% report","pipeline_run_id":%q,"pipeline_step_id":"summarize"}`, id, workspace, run)))
		rr := httptest.NewRecorder()
		h.CreateChat(rr, req)
		return rr
	}
	if rr := create("source-chat", ws, "source-run"); rr.Code != http.StatusCreated {
		t.Fatalf("create %d: %s", rr.Code, rr.Body.String())
	}
	if rr := create("foreign-chat", "foreign-workspace", "source-run"); rr.Code != http.StatusBadRequest {
		t.Fatalf("cross-tenant source accepted: %d %s", rr.Code, rr.Body.String())
	}
	execOrFatal(t, db, `INSERT INTO chats(id,agent_id,workspace_id,title,origin,started_at) VALUES('newer','ck-ag',?,'Other','UI','2099-01-01')`, ws)
	list := func(workspace, query string) []chatResponse {
		req := httptest.NewRequest("GET", "/api/v1/agents/ck-ag/chats?"+query, nil)
		req.SetPathValue("agentId", "ck-ag")
		req = req.WithContext(withWorkspace(req.Context(), workspace, "OWNER"))
		rr := httptest.NewRecorder()
		NewAgentHandler(db, newTestLogger()).ListChats(rr, req)
		var out []chatResponse
		decodeChats(t, rr, &out)
		return out
	}
	rows := list(ws, "q=%C5%BElu%C5%A5&limit=1&source=1")
	if len(rows) != 1 || rows[0].ID != "source-chat" || rows[0].Source == nil || rows[0].Source.RunID != "source-run" || rows[0].Source.StepID != "summarize" {
		t.Fatalf("source/search=%+v", rows)
	}
	// The link survives rename; the display title is never used as a join key.
	execOrFatal(t, db, `UPDATE chats SET title='Completely renamed' WHERE id='source-chat'`)
	rows = list(ws, "routine_id=source-pipeline&source=1")
	if len(rows) != 1 || rows[0].Source == nil || rows[0].Source.Slug != "weekly" {
		t.Fatalf("renamed source=%+v", rows)
	}
	if rows = list("foreign-workspace", "source=1"); len(rows) != 0 {
		t.Fatal("foreign sources leaked")
	}
	execOrFatal(t, db, `UPDATE pipelines SET deleted_at='2026-09-25' WHERE id='source-pipeline'`)
	rows = list(ws, "chat_id=source-chat&source=1")
	if len(rows) != 1 || rows[0].Source != nil {
		t.Fatal("deleted routine exposed")
	}
	// Source fields remain optional for clients that did not request them.
	payload, _ := json.Marshal(rows[0])
	if strings.Contains(string(payload), `"source"`) {
		t.Fatal("absent source serialized")
	}
}

func TestChatIssueSourceUsesMissionIdentity(t *testing.T) {
	db := setupTestDB(t)
	ws := chatKindSeed(t, db)
	execOrFatal(t, db, `INSERT INTO missions(id,workspace_id,crew_id,lead_agent_id,trace_id,title) VALUES('source-issue',?,'ck-crew','ck-ag','source-trace','Check access')`, ws)
	execOrFatal(t, db, `INSERT INTO chats(id,agent_id,workspace_id,mode,title) VALUES('source-issue','ck-ag',?,'MISSION','Renamed title')`, ws)
	req := httptest.NewRequest("GET", "/api/v1/agents/ck-ag/chats?source=1", nil)
	req.SetPathValue("agentId", "ck-ag")
	req = req.WithContext(withWorkspace(req.Context(), ws, "OWNER"))
	rr := httptest.NewRecorder()
	NewAgentHandler(db, newTestLogger()).ListChats(rr, req)
	var rows []chatResponse
	decodeChats(t, rr, &rows)
	if len(rows) != 1 || rows[0].Source == nil || rows[0].Source.Kind != "issue" || rows[0].Source.Name != "Check access" {
		t.Fatalf("issue source %+v", rows)
	}
}
