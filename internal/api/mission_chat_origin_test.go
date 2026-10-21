package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/crewship-ai/crewship/internal/runauthority"
)

func TestMissionChatOriginPersistsAndRevocationBlocksStart(t *testing.T) {
	f := newChatIssueFixture(t)
	h := NewInternalMissionHandler(f.h.db, nil, nil, newTestLogger())
	body := map[string]any{"workspace_id": f.workspace, "crew_id": f.crew, "lead_agent_id": f.agent, "title": "Delegated mission", "source_run_id": f.run}
	rr := httptest.NewRecorder()
	h.Create(rr, httptest.NewRequest(http.MethodPost, "/", jsonBody(body)))
	if rr.Code != 201 && rr.Code != 202 {
		t.Fatalf("mission admission %d %s", rr.Code, rr.Body.String())
	}
	var response struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	var origin string
	if err := f.h.db.QueryRow(`SELECT COALESCE(chat_origin_id,'') FROM missions WHERE id=?`, response.ID).Scan(&origin); err != nil {
		t.Fatal(err)
	}
	if origin == "" {
		t.Fatal("mission lost source origin")
	}
	if _, err := runauthority.ValidateOrigin(t.Context(), f.h.db, origin, f.agent, f.workspace); err != nil {
		t.Fatal(err)
	}
	// Guided creation may stage an approval. Removing the hold models a genuine
	// operator approval; the separately revoked requester must still block start.
	execOrFatal(t, f.h.db, `UPDATE approvals_queue SET status='approved' WHERE workspace_id=?`, f.workspace)
	execOrFatal(t, f.h.db, `DELETE FROM workspace_members WHERE workspace_id=? AND user_id=?`, f.workspace, f.user)
	req := httptest.NewRequest(http.MethodPost, "/start?workspace_id="+f.workspace+"&crew_id="+f.crew, nil)
	req.SetPathValue("missionId", response.ID)
	rr = httptest.NewRecorder()
	h.Start(rr, req)
	if rr.Code != 403 {
		t.Fatalf("revoked mission start %d %s", rr.Code, rr.Body.String())
	}
	if got := missionStatus(t, h, response.ID); got != "PLANNING" {
		t.Fatalf("revoked mission dispatched: %s", got)
	}
}
