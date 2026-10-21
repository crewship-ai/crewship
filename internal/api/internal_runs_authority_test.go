package api

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/crewship-ai/crewship/internal/auth/internaltoken"
	"github.com/crewship-ai/crewship/internal/chataudience"
	"github.com/crewship-ai/crewship/internal/runauthority"
)

func TestCreateRunHumanAuthorityHostOnlyAndDurable(t *testing.T) {
	h, wsID, agentID := covIRunFixture(t)
	wireTestJournalForHandler(t, h.db, h)
	var user string
	if err := h.db.QueryRowContext(t.Context(), `SELECT user_id FROM workspace_members WHERE workspace_id=? LIMIT 1`, wsID).Scan(&user); err != nil {
		t.Fatal(err)
	}
	execOrFatal(t, h.db, `INSERT INTO chats(id,workspace_id,agent_id,created_by,visibility) VALUES('authority-chat',?,?,?,'private')`, wsID, agentID, user)
	receipt, err := chataudience.CaptureTrusted(t.Context(), h.db, "authority-chat", user, nil)
	if err != nil || receipt == nil {
		t.Fatalf("capture: %+v %v", receipt, err)
	}
	call := func(id, token, trigger string, r *chataudience.Receipt) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, "/api/v1/internal/runs", jsonBody(map[string]any{"id": id, "agent_id": agentID, "workspace_id": wsID, "chat_id": "authority-chat", "human_authority": r, "trigger_message_id": trigger, "metadata": map[string]any{"human_authority": "untrusted"}}))
		req.RemoteAddr = "127.0.0.1:12345"
		req.Header.Set("X-Internal-Token", token)
		rr := httptest.NewRecorder()
		h.requireInternal(http.HandlerFunc(h.CreateRun)).ServeHTTP(rr, req)
		return rr
	}
	for _, tc := range []struct {
		id, token, trigger string
		receipt            *chataudience.Receipt
		want               int
	}{
		{"workspace", "", "message", receipt, 403},
		{"missing-trigger", "tok", "", receipt, 403},
		{"missing-receipt", "tok", "message", nil, 403},
		{"valid", "tok", "message", receipt, 201},
	} {
		if tc.token == "" {
			tc.token = internaltoken.DeriveWorkspaceToken("tok", wsID)
		}
		rr := call(tc.id, tc.token, tc.trigger, tc.receipt)
		if rr.Code != tc.want {
			t.Fatalf("%s: status=%d want=%d %s", tc.id, rr.Code, tc.want, rr.Body.String())
		}
	}
	a, err := runauthority.Resolve(t.Context(), h.db, "valid", agentID, wsID)
	if err != nil || a.TriggerMessageID != "message" || a.HumanAuthority == nil || *a.HumanAuthority != *receipt {
		t.Fatalf("durable provenance: %+v %v", a, err)
	}
	var payload string
	if err := h.db.QueryRow(`SELECT payload FROM journal_entries WHERE trace_id='valid' AND entry_type='run.started'`).Scan(&payload); err != nil {
		t.Fatal(err)
	}
	var parsed map[string]any
	if err := json.Unmarshal([]byte(payload), &parsed); err != nil {
		t.Fatal(err)
	}
	if parsed["host_admitted"] != true {
		t.Fatalf("host admission lost: %s", payload)
	}
	execOrFatal(t, h.db, `UPDATE workspace_members SET role='VIEWER' WHERE workspace_id=? AND user_id=?`, wsID, user)
	if rr := call("stale", "tok", "message", receipt); rr.Code != 403 {
		t.Fatalf("stale admission: %d %s", rr.Code, rr.Body.String())
	}
	if _, err := runauthority.Resolve(t.Context(), h.db, "valid", agentID, wsID); !errors.Is(err, runauthority.ErrRequesterStale) {
		t.Fatalf("stale durable authority accepted: %v", err)
	}
	var n int
	if err := h.db.QueryRow(`SELECT COUNT(*) FROM journal_entries WHERE entry_type='run.started'`).Scan(&n); err != nil || n != 1 {
		t.Fatalf("rejected starts persisted: n=%d %v", n, err)
	}
}
