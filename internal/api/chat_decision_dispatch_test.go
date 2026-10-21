package api

import (
	"encoding/json"
	"github.com/crewship-ai/crewship/internal/auth/internaltoken"
	"github.com/crewship-ai/crewship/internal/harbormaster"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

func TestDecisionDispatcherWholeRunSourceMutationAndReceipt(t *testing.T) {
	d := newApprovalDecisionRig(t)
	version := d.read(t).Ref.Decision.Source.Version
	answer := func(key, action string) *httptest.ResponseRecorder {
		req := withWorkspaceUser(httptest.NewRequest("POST", "/", jsonBody(map[string]any{"source_id": d.id, "expected_version": version, "action": action, "values": map[string]any{}})), d.f.user, d.f.workspace, "OWNER")
		req.SetPathValue("chatId", d.f.chat)
		req.SetPathValue("sourceType", "run_approval")
		req.SetPathValue("sourceId", d.id)
		req.Header.Set("Idempotency-Key", key)
		rr := httptest.NewRecorder()
		d.r.chatDecisionAnswer(rr, req)
		return rr
	}
	first := answer("actual-source-answer", "approve")
	if first.Code != 200 {
		t.Fatalf("actual dispatcher%d %s", first.Code, first.Body.String())
	}
	var card ChatCardResponse
	if err := json.Unmarshal(first.Body.Bytes(), &card); err != nil {
		t.Fatal(err)
	}
	if card.Decision.Status != "approved" || card.Decision.Resolution.AnsweredBy != d.f.user {
		t.Fatalf("receipt %+v", card.Decision)
	}
	if retry := answer("actual-source-answer", "approve"); retry.Code != 200 {
		t.Fatalf("receipt retry%d %s", retry.Code, retry.Body.String())
	}
	if changed := answer("actual-source-answer", "reject"); changed.Code != 409 || !strings.Contains(changed.Body.String(), errDecisionKeyCollision.Error()) {
		t.Fatalf("changed intent%d %s", changed.Code, changed.Body.String())
	}
	if stale := answer("new-key", "reject"); stale.Code != 409 || !strings.Contains(stale.Body.String(), "decision_version_conflict") {
		t.Fatalf("stale%d %s", stale.Code, stale.Body.String())
	}
	if got := d.count(t, "gate_reward_history"); got != 1 {
		t.Fatalf("duplicate effects%d", got)
	}
}

func TestDecisionDispatcherPaginationDoesNotCountUnsupportedApprovals(t *testing.T) {
	d := newApprovalDecisionRig(t)
	// Insert candidates with a valid host envelope but malformed unsupported
	// frozen card text. They must not create public empty pages or has_more hints.
	for i := 0; i < 103; i++ {
		execOrFatal(t, d.f.h.db, `INSERT INTO approvals_queue(id,workspace_id,crew_id,agent_id,requested_by,kind,reason,payload,status,created_at,timeout_at)
 SELECT 'unsupported-'||?,workspace_id,crew_id,agent_id,requested_by,kind,'',payload,status,created_at,timeout_at FROM approvals_queue WHERE id=?`, i, d.id)
	}
	list := func(cursor string) ChatCardPage {
		req := withWorkspaceUser(httptest.NewRequest("GET", "/?limit=1&cursor="+url.QueryEscape(cursor), nil), d.f.user, d.f.workspace, "OWNER")
		req.SetPathValue("chatId", d.f.chat)
		rr := httptest.NewRecorder()
		d.r.chatDecisionList(rr, req)
		if rr.Code != 200 {
			t.Fatalf("list%d %s", rr.Code, rr.Body.String())
		}
		var page ChatCardPage
		if err := json.Unmarshal(rr.Body.Bytes(), &page); err != nil {
			t.Fatal(err)
		}
		return page
	}
	page := list("")
	if len(page.Refs) != 1 || page.Refs[0].Entity.ID != d.id || page.HasMore || !page.SnapshotComplete || page.NextCursor != "" {
		t.Fatalf("unsupported candidates influenced boundary: %+v", page)
	}
}

func TestDecisionCursorLengthDoesNotRevealCandidateHorizon(t *testing.T) {
	d := newApprovalDecisionRig(t)
	cursor := decisionPageCursor{Audience: d.a, Snapshot: "opaque-snapshot", Expires: time.Now().Add(time.Minute).Unix(), Sources: []decisionSourcePageCursor{{Type: "escalation"}, {Type: "run_approval"}}}
	first, err := d.r.encodeDecisionCursor(cursor)
	if err != nil {
		t.Fatal(err)
	}
	cursor.Sources[1].Upper = 9223372036854775807
	second, err := d.r.encodeDecisionCursor(cursor)
	if err != nil {
		t.Fatal(err)
	}
	if len(first) != len(second) {
		t.Fatalf("candidate horizon disclosed by cursor length: %d vs %d", len(first), len(second))
	}
	decoded, err := d.r.decodeDecisionCursor(second)
	if err != nil || decoded.Sources[1].Upper != cursor.Sources[1].Upper {
		t.Fatalf("opaque horizon roundtrip %+v %v", decoded, err)
	}
}

func TestDecisionDispatcherWorkflowSourceMutationAndReceipt(t *testing.T) {
	d := newWaitpointCardRig(t, waitpointCardForm())
	version := d.read(t).Decision.Source.Version
	for i := 0; i < 2; i++ {
		req := withWorkspaceUser(httptest.NewRequest("POST", "/", jsonBody(map[string]any{"source_id": d.id, "expected_version": version, "action": "answer", "action_id": "ship", "values": map[string]any{"timing": "now", "ready": false}})), d.f.user, d.f.workspace, "OWNER")
		req.SetPathValue("chatId", d.f.chat)
		req.SetPathValue("sourceType", "waitpoint")
		req.SetPathValue("sourceId", d.id)
		req.Header.Set("Idempotency-Key", "actual-workflow-key")
		rr := httptest.NewRecorder()
		d.r.chatDecisionAnswer(rr, req)
		var card ChatCardResponse
		if rr.Code != 200 || json.Unmarshal(rr.Body.Bytes(), &card) != nil || card.Decision == nil || card.Decision.Status != "approved" || card.Decision.Resolution.AnsweredBy != d.f.user {
			t.Fatalf("workflow answer/replay%d %s", rr.Code, rr.Body.String())
		}
		if strings.Contains(rr.Body.String(), d.token) {
			t.Fatal("workflow bearer token exposed")
		}
	}
	var status, payload string
	if err := d.f.h.db.QueryRow(`SELECT status,decision_payload FROM pipeline_waitpoints WHERE token=?`, d.token).Scan(&status, &payload); err != nil || status != "approved" || !strings.Contains(payload, `"action_id":"ship"`) {
		t.Fatalf("canonical workflow source %s %s %v", status, payload, err)
	}
}

func TestDecisionDispatcherMixedSourcesPaginateWithoutDuplication(t *testing.T) {
	d := newWaitpointCardRig(t, waitpointCardForm())
	h := NewQueryHandler(d.f.h.db, nil, nil, "decision-test", newTestLogger())
	d.r.queryHandler = h
	req := httptest.NewRequest("POST", "/", jsonBody(map[string]string{"workspace_id": d.f.workspace, "crew_id": d.f.crew, "chat_id": d.f.chat, "actor_agent_id": d.f.agent, "source_run_id": d.f.run, "reason": "Mixed source question"}))
	req.Header.Set("X-Crewship-Run-Token", internaltoken.DeriveAgentRunToken(internaltoken.DeriveAgentRunKey(h.internalToken, d.f.workspace, d.f.crew), d.f.workspace, d.f.agent, d.f.run))
	rr := httptest.NewRecorder()
	h.CreateEscalation(rr, req)
	if rr.Code != 201 {
		t.Fatalf("TEXT source%d %s", rr.Code, rr.Body.String())
	}
	var created map[string]any
	if err := json.Unmarshal(rr.Body.Bytes(), &created); err != nil {
		t.Fatal(err)
	}
	approval, err := harbormaster.Enqueue(t.Context(), d.f.h.db, nil, harbormaster.Request{WorkspaceID: d.f.workspace, CrewID: d.f.crew, AgentID: d.f.agent, RequestedBy: d.f.agent, Kind: harbormaster.KindCustom, Reason: "Mixed run gate", Payload: map[string]any{"tool": "agent_run", "args": map[string]any{}, "gate_mode": "sync", "correlation": map[string]any{"run_id": d.f.run, "chat_id": d.f.chat, "trigger_message_id": "trigger-" + d.f.run, "trigger_type": "human_chat"}}})
	if err != nil {
		t.Fatal(err)
	}
	expected := []string{created["escalation_id"].(string), d.id, approval}
	cursor, snapshot := "", ""
	for i, id := range expected {
		req = withWorkspaceUser(httptest.NewRequest("GET", "/?limit=1&cursor="+url.QueryEscape(cursor), nil), d.f.user, d.f.workspace, "OWNER")
		req.SetPathValue("chatId", d.f.chat)
		rr = httptest.NewRecorder()
		d.r.chatDecisionList(rr, req)
		var page ChatCardPage
		if rr.Code != 200 || json.Unmarshal(rr.Body.Bytes(), &page) != nil || len(page.Refs) != 1 || page.Refs[0].Entity.ID != id || page.HasMore != (i < 2) || page.SnapshotComplete != (i == 2) || page.Reset != (i == 0) || strings.Contains(rr.Body.String(), d.token) {
			t.Fatalf("mixed page%d: %d %s", i, rr.Code, rr.Body.String())
		}
		if i == 0 {
			snapshot = page.SnapshotID
		}
		if page.SnapshotID != snapshot {
			t.Fatal("mixed page changed snapshot")
		}
		cursor = page.NextCursor
	}
	if cursor != "" {
		t.Fatal("terminal mixed page retained cursor")
	}
}
