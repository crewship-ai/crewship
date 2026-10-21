package api

import (
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/harbormaster"
)

func TestApprovalsLegacyReadsHideNewHostCorrelationWithoutChangingStore(t *testing.T) {
	d := newApprovalDecisionRig(t)
	h := NewApprovalsHandler(d.f.h.db, newTestLogger(), d.r.Journal())
	user, workspace, id := d.f.user, d.f.workspace, d.id
	execOrFatal(t, h.db, `UPDATE approvals_queue SET payload=json_set(payload,'$.args.agent_slug','visible','$.review.user_prompt','existing-review') WHERE id=?`, id)
	for _, role := range []string{"OWNER", "ADMIN", "MEMBER"} {
		for _, detail := range []bool{false, true} {
			req := withWorkspaceUser(httptest.NewRequest("GET", "/api/v1/approvals", nil), user, workspace, role)
			rec := httptest.NewRecorder()
			if detail {
				req.SetPathValue("id", id)
				h.Get(rec, req)
			} else {
				h.List(rec, req)
			}
			if rec.Code != 200 {
				t.Fatalf("%s detail=%v: %d %s", role, detail, rec.Code, rec.Body.String())
			}
			body := rec.Body.String()
			for _, secret := range []string{"correlation", "private-run-reference", "private-chat-reference", "private-trigger-reference"} {
				if strings.Contains(body, secret) {
					t.Fatalf("%s legacy read leaked %s: %s", role, secret, body)
				}
			}
			if !strings.Contains(body, `"gate_mode":"sync"`) || !strings.Contains(body, "existing-review") || !strings.Contains(body, id) {
				t.Fatalf("existing approval fields lost: %s", body)
			}
		}
	}
	stored, err := harbormaster.Get(t.Context(), h.db, workspace, id)
	if err != nil {
		t.Fatal(err)
	}
	if stored.Payload["correlation"].(map[string]any)["chat_id"] != d.f.chat {
		t.Fatal("read mutated authoritative source")
	}
	// Normal legacy approval still resolves through the unchanged authority.
	req := withWorkspaceUser(httptest.NewRequest("POST", "/api/v1/approvals/"+id+"/decide", strings.NewReader(`{"status":"approved"}`)), user, workspace, "OWNER")
	req.SetPathValue("id", id)
	rec := httptest.NewRecorder()
	h.Decide(rec, req)
	if rec.Code != 200 {
		t.Fatalf("legacy decision no longer works: %d %s", rec.Code, rec.Body.String())
	}
	stored, err = harbormaster.Get(t.Context(), h.db, workspace, id)
	if err != nil || stored.Status != harbormaster.StatusApproved || stored.Payload["correlation"] == nil {
		t.Fatalf("decision lost authoritative source: %+v %v", stored, err)
	}
}

func TestApprovalReadProjectionCopiesOnlyNewWholeRunMetadata(t *testing.T) {
	payload := map[string]any{"tool": "agent_run", "correlation": map[string]any{"run_id": "run"}, "gate_mode": "async", "existing": "keep"}
	projected := approvalReadProjection(harbormaster.Request{Payload: payload})
	if projected.Payload["correlation"] != nil || payload["correlation"] == nil || projected.Payload["gate_mode"] != "async" {
		t.Fatal("wrong projection")
	}
	projected.Payload["existing"] = "changed"
	if payload["existing"] != "keep" {
		t.Fatal("serialized copy aliases original payload")
	}
	legacy := approvalReadProjection(harbormaster.Request{Payload: map[string]any{"tool": "shell.exec", "correlation": "legacy-context"}})
	if legacy.Payload["correlation"] != "legacy-context" {
		t.Fatal("per-tool legacy payload changed")
	}
}
