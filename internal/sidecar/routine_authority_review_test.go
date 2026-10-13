package sidecar

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/auth/internaltoken"
)

func TestRoutineScheduleReservedUnicodeFieldsCannotOverrideToken(t *testing.T) {
	s, key := newRunIdentityServer(t)
	host := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var raw map[string]json.RawMessage
		if err := json.NewDecoder(r.Body).Decode(&raw); err != nil {
			t.Fatal(err)
		}
		var origin struct {
			Run   string `json:"source_run_id"`
			Agent string `json:"source_agent_id"`
		}
		encoded, _ := json.Marshal(raw)
		if err := json.Unmarshal(encoded, &origin); err != nil {
			t.Fatal(err)
		}
		if origin.Run != "verified-run" || origin.Agent != "agent-boot" {
			t.Errorf("forged identity: %s", encoded)
		}
		if string(raw["large"]) != "9007199254740993" {
			t.Errorf("integer changed: %s", raw["large"])
		}
		if r.Header.Get("X-Crewship-Run-Token") != internaltoken.DeriveAgentRunToken(key, "ws-1", "agent-boot", "verified-run") {
			t.Error("token proof missing")
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(201)
		w.Write([]byte(`{"id":"schedule"}`))
	}))
	defer host.Close()
	s.ipc.BaseURL = host.URL
	req := httptest.NewRequest("POST", "/routines/schedules/create", strings.NewReader(`{"name":"nightly","ſource_run_id":"","SOURCE_AGENT_ID":"foreign","chat_origin_id":"forged","large":9007199254740993}`))
	req.Header.Set("Authorization", "Bearer "+internaltoken.DeriveAgentRunToken(key, "ws-1", "agent-boot", "verified-run"))
	rr := httptest.NewRecorder()
	s.handleRoutineScheduleCreate(rr, req)
	if rr.Code != 201 {
		t.Fatalf("%d %s", rr.Code, rr.Body.String())
	}
}
func TestIssuesMCPLegacyCrewDropsUnscopedOperationKey(t *testing.T) {
	s, _ := newRunIdentityServer(t)
	s.ipc.AgentRunKey = ""
	calls := 0
	host := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		var body map[string]any
		json.NewDecoder(r.Body).Decode(&body)
		if body["author_run_id"] != "" && body["author_run_id"] != nil {
			t.Errorf("legacy run invented: %+v", body)
		}
		if body["operation_key"] != "" && body["operation_key"] != nil {
			t.Errorf("unscoped key forwarded: %+v", body)
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(201)
		w.Write([]byte(`{"id":"legacy-issue"}`))
	}))
	defer host.Close()
	s.ipc.BaseURL = host.URL
	req := httptest.NewRequest("POST", "/mcp/issues", strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"create_issue","arguments":{"title":"Legacy","request_key":"legacy-key"}}}`))
	req.Header.Set("Authorization", "Bearer "+s.ipc.AgentToken)
	rr := httptest.NewRecorder()
	s.handleIssuesMCP(rr, req)
	if rr.Code != 200 || calls != 1 || strings.Contains(rr.Body.String(), `"isError":true`) {
		t.Fatalf("legacy MCP %d calls=%d %s", rr.Code, calls, rr.Body.String())
	}
}
