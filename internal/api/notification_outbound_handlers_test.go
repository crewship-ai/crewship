package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/crewship-ai/crewship/internal/notify"
)

func TestNotifyChannelAgentsHandler_AllowsListsAndDenies(t *testing.T) {
	t.Setenv("ENCRYPTION_KEY", testNotifyEncKey)
	db := setupTestDB(t)
	mustExec(t, db, `INSERT INTO workspaces (id, name, slug) VALUES ('ws-pair', 'Pairing', 'pairing')`)
	mustExec(t, db, `INSERT INTO crews (id, workspace_id, name, slug) VALUES ('crew-pair', 'ws-pair', 'Ops', 'ops')`)
	mustExec(t, db, `INSERT INTO agents (id, workspace_id, crew_id, name, slug) VALUES ('agent-pair', 'ws-pair', 'crew-pair', 'Release bot', 'release-bot')`)

	ch, err := notify.NewChannelStore(db).Create(t.Context(), notify.ChannelInput{
		WorkspaceID: "ws-pair", Type: notify.ChannelWebhook, URL: "https://hooks.example.test/crewship",
	})
	if err != nil {
		t.Fatal(err)
	}
	h := NewNotifyChannelAgentsHandler(db, newTestLogger())

	req := withWorkspaceUser(httptest.NewRequest(http.MethodPost, "/api/v1/notification-channels/"+ch.ID+"/agents", bytes.NewBufferString(`{"agent_id":"agent-pair"}`)), "user-admin", "ws-pair", "ADMIN")
	req.SetPathValue("id", ch.ID)
	allow := httptest.NewRecorder()
	h.Allow(allow, req)
	if allow.Code != http.StatusOK {
		t.Fatalf("allow status = %d, body=%s", allow.Code, allow.Body.String())
	}

	listReq := withWorkspaceUser(httptest.NewRequest(http.MethodGet, "/api/v1/notification-channels/"+ch.ID+"/agents", nil), "user-admin", "ws-pair", "ADMIN")
	listReq.SetPathValue("id", ch.ID)
	list := httptest.NewRecorder()
	h.List(list, listReq)
	var listed struct {
		Agents []map[string]any `json:"agents"`
	}
	if list.Code != http.StatusOK || json.Unmarshal(list.Body.Bytes(), &listed) != nil || len(listed.Agents) != 1 {
		t.Fatalf("list status/body = %d/%s", list.Code, list.Body.String())
	}
	if listed.Agents[0]["agent_slug"] != "release-bot" {
		t.Errorf("agent_slug = %v, want release-bot", listed.Agents[0]["agent_slug"])
	}

	denyReq := withWorkspaceUser(httptest.NewRequest(http.MethodDelete, "/api/v1/notification-channels/"+ch.ID+"/agents/agent-pair", nil), "user-admin", "ws-pair", "ADMIN")
	denyReq.SetPathValue("id", ch.ID)
	denyReq.SetPathValue("agentId", "agent-pair")
	deny := httptest.NewRecorder()
	h.Deny(deny, denyReq)
	if deny.Code != http.StatusOK || !contains(deny.Body.String(), `"allowed":false`) {
		t.Fatalf("deny status/body = %d/%s", deny.Code, deny.Body.String())
	}
}

func TestNotifyTemplateHandler_ValidatesAndScopesTemplates(t *testing.T) {
	db := setupTestDB(t)
	mustExec(t, db, `INSERT INTO workspaces (id, name, slug) VALUES ('ws-template', 'Templates', 'templates')`)
	h := NewNotifyTemplateHandler(db, newTestLogger())

	put := httptest.NewRequest(http.MethodPut, "/api/v1/notification-templates", bytes.NewBufferString(`{"category":"routines.failed","title":"[failed] {{ source.title }}","body":"{{ vars.run_id }}"}`))
	put = withWorkspaceUser(put, "user-admin", "ws-template", "ADMIN")
	res := httptest.NewRecorder()
	h.Put(res, put)
	if res.Code != http.StatusOK {
		t.Fatalf("put status = %d, body=%s", res.Code, res.Body.String())
	}

	list := httptest.NewRecorder()
	get := withWorkspaceUser(httptest.NewRequest(http.MethodGet, "/api/v1/notification-templates", nil), "user-admin", "ws-template", "ADMIN")
	h.List(list, get)
	if list.Code != http.StatusOK || !contains(list.Body.String(), `"routines.failed"`) {
		t.Fatalf("list status/body = %d/%s", list.Code, list.Body.String())
	}

	bad := httptest.NewRequest(http.MethodPut, "/api/v1/notification-templates", bytes.NewBufferString(`{"category":"not.a.real.category","title":"x"}`))
	bad = withWorkspaceUser(bad, "user-admin", "ws-template", "ADMIN")
	badRes := httptest.NewRecorder()
	h.Put(badRes, bad)
	if badRes.Code != http.StatusBadRequest {
		t.Fatalf("invalid category status = %d, body=%s", badRes.Code, badRes.Body.String())
	}

	del := httptest.NewRecorder()
	deleteReq := withWorkspaceUser(httptest.NewRequest(http.MethodDelete, "/api/v1/notification-templates?category=routines.failed", nil), "user-admin", "ws-template", "ADMIN")
	h.Delete(del, deleteReq)
	if del.Code != http.StatusNoContent {
		t.Fatalf("delete status = %d, body=%s", del.Code, del.Body.String())
	}
}
