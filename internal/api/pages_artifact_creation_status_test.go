package api

import (
	"encoding/json"
	"net/http/httptest"
	"testing"
)

func TestArtifactCreationStatusResolvesOnlyOwnLiveRun(t *testing.T) {
	h, workspace, user, _, _ := artifactRunFixture(t)
	read := func(chat string) *httptest.ResponseRecorder {
		request := pagesGrantRequest(t, "GET", "/", workspace, user, "OWNER", "")
		request.SetPathValue("chatId", chat)
		response := httptest.NewRecorder()
		h.ArtifactCreationStatus(response, request)
		return response
	}
	check := func(authorized, canAuthorize bool, run string) {
		t.Helper()
		response := read("artifact-chat")
		artifactExpect(t, response, 200)
		var state struct {
			RunID        string `json:"run_id"`
			Authorized   bool   `json:"authorized"`
			CanAuthorize bool   `json:"can_authorize"`
		}
		if err := json.Unmarshal(response.Body.Bytes(), &state); err != nil {
			t.Fatal(err)
		}
		if state.RunID != run || state.Authorized != authorized || state.CanAuthorize != canAuthorize {
			t.Fatalf("unexpected authority: %s", response.Body.String())
		}
	}
	check(false, true, "artifact-run")
	artifactExpect(t, artifactCreationOptIn(t, h, workspace, user, "POST"), 201)
	check(true, true, "artifact-run")
	artifactExpect(t, artifactCreationOptIn(t, h, workspace, user, "DELETE"), 204)
	check(false, false, "artifact-run")
	execOrFatal(t, h.db, `INSERT INTO journal_entries(id,workspace_id,agent_id,entry_type,actor_type,summary,trace_id) VALUES('status-end',?,'artifact-agent','run.completed','system','done','artifact-run')`, workspace)
	check(false, false, "")
	artifactExpect(t, read("missing-chat"), 404)
}

func TestArtifactCreationStatusDoesNotExposeAnotherHumanOrSharedChat(t *testing.T) {
	h, workspace, user, _, _ := artifactRunFixture(t)
	execOrFatal(t, h.db, `UPDATE chats SET visibility='workspace' WHERE id='artifact-chat'`)
	r := pagesGrantRequest(t, "GET", "/", workspace, user, "OWNER", "")
	r.SetPathValue("chatId", "artifact-chat")
	rr := httptest.NewRecorder()
	h.ArtifactCreationStatus(rr, r)
	artifactExpect(t, rr, 200)
	var state map[string]any
	if err := json.Unmarshal(rr.Body.Bytes(), &state); err != nil {
		t.Fatal(err)
	}
	if state["run_id"] != "" || state["can_authorize"] != false {
		t.Fatalf("shared chat exposed creation authority: %s", rr.Body.String())
	}
	execOrFatal(t, h.db, `UPDATE chats SET created_by=NULL WHERE id='artifact-chat'`)
	rr = httptest.NewRecorder()
	h.ArtifactCreationStatus(rr, r)
	artifactExpect(t, rr, 404)
}
