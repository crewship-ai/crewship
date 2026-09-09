package api

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/pipeline"
)

func TestDecisionFormAPIRequiresNamedTypedAnswerAcrossDoors(t *testing.T) {
	h, user, ws := newPipelineHandlerForCRUDTest(t)
	store := pipeline.NewSQLWaitpointStore(h.db)
	defer store.Close()
	h.SetWaitpointStore(store)
	form := &pipeline.DecisionForm{Fields: []pipeline.InputSpec{{Name: "count", Type: "integer", Required: true}, {Name: "enabled", Type: "boolean", Required: true}}, Actions: []pipeline.DecisionAction{{ID: "go", Label: "Go", Approved: true}, {ID: "stop", Label: "Stop", Approved: false}}}
	token, err := store.CreateApproval(context.Background(), pipeline.WaitpointApprovalRequest{WorkspaceID: ws, PipelineRunID: "run-form", StepID: "gate", DecisionForm: form})
	if err != nil {
		t.Fatal(err)
	}
	// Both list and Inbox project the same frozen form.
	list := httptest.NewRecorder()
	h.ListPendingWaitpoints(list, withWorkspaceUser(httptest.NewRequest("GET", "/x", nil), user, ws, "MANAGER"))
	if list.Code != 200 || !strings.Contains(list.Body.String(), `"decision_form"`) {
		t.Fatalf("list: %d %s", list.Code, list.Body)
	}
	var inboxPayload string
	if err := h.db.QueryRow(`SELECT payload_json FROM inbox_items WHERE source_id=?`, token).Scan(&inboxPayload); err != nil || !strings.Contains(inboxPayload, `"decision_form"`) {
		t.Fatalf("inbox: %s %v", inboxPayload, err)
	}
	for _, tc := range []struct {
		body, role string
		external   bool
		want       int
	}{
		{`{"approved":true}`, "MANAGER", false, 400},
		{`{"approved":true,"payload":{"action_id":"go","data":{"count":0,"enabled":false}}}`, "", true, 403},
		{`{}`, "", true, 403},
		{`{"approved":true,"action_id":"go","data":{"count":0,"enabled":false}}`, "MEMBER", false, 403},
		{`{"approved":true,"action_id":"stop","data":{"count":0,"enabled":false}}`, "MANAGER", false, 400},
		{`{"approved":true,"action_id":"go","data":{"count":"0","enabled":false}}`, "MANAGER", false, 400},
		{`{"approved":true,"action_id":"go","data":{"count":0,"enabled":false}}`, "MANAGER", false, 200},
		{`{"approved":true,"action_id":"go","data":{"count":0,"enabled":false}}`, "MANAGER", false, 409},
	} {
		r := httptest.NewRequest("POST", "/x", strings.NewReader(tc.body))
		r.SetPathValue("token", token)
		w := httptest.NewRecorder()
		if tc.external {
			h.CompleteWaitpointToken(w, r)
		} else {
			h.ApproveWaitpoint(w, withWorkspaceUser(r, user, ws, tc.role))
		}
		if w.Code != tc.want {
			t.Fatalf("%s: %d wanted %d: %s", tc.body, w.Code, tc.want, w.Body)
		}
	}
	output, err := store.ApprovalOutput(context.Background(), ws, token)
	var answer pipeline.DecisionAnswer
	if err != nil || json.Unmarshal([]byte(output), &answer) != nil || answer.Data["count"] != float64(0) || answer.Data["enabled"] != false {
		t.Fatalf("output: %s %v", output, err)
	}
}
