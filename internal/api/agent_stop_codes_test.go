package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// #2879: the public stop route forwards the daemon's outcome on success and
// one of two stable codes on refusal, whatever sentence the daemon sent.
func TestAgentStop_OutcomeAndRefusalCodes(t *testing.T) {
	for _, tc := range []struct {
		name        string
		status      int
		body        string
		wantStatus  int
		wantOutcome string
		wantCode    string
		wantDB      string // "" = unchanged
	}{
		{"stopped", 200, `{"agent_id":"agent-pxc","status":"stopped","outcome":"stopped"}`, 200, "stopped", "", "STOPPED"},
		{"already stopped", 200, `{"agent_id":"agent-pxc","status":"stopped","outcome":"already_stopped"}`, 200, "already_stopped", "", "STOPPED"},
		{"daemon without outcome", 200, `{"agent_id":"agent-pxc","status":"stopped"}`, 200, "stopped", "", "STOPPED"},
		{"unknown outcome", 200, `{"agent_id":"agent-pxc","status":"stopped","outcome":"maybe"}`, 502, "", "stop_not_confirmed", ""},
		{"live unowned", 503, `{"error":"runtime stop not confirmed","code":"stop_not_confirmed"}`, 502, "", "stop_not_confirmed", ""},
		{"runtime unavailable", 503, `{"error":"runtime stop unavailable","code":"runtime_unavailable"}`, 502, "", "runtime_unavailable", ""},
		{"uncoded daemon refusal", 503, `{"error":"cannot stop"}`, 502, "", "stop_not_confirmed", ""},
		{"unknown code", 503, `{"error":"x","code":"something_else"}`, 502, "", "stop_not_confirmed", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			sock := newUnixIPCServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(tc.status)
				_, _ = w.Write([]byte(tc.body))
			}))
			h, user, ws, _, id := covProxyRig(t, sock)
			var before string
			if err := h.db.QueryRow(`SELECT status FROM agents WHERE id=?`, id).Scan(&before); err != nil {
				t.Fatal(err)
			}
			req := httptest.NewRequest(http.MethodPost, "/", nil)
			req.SetPathValue("agentId", id)
			req = withWorkspaceUser(req, user, ws, "OWNER")
			out := httptest.NewRecorder()
			h.AgentStop(out, req)
			if out.Code != tc.wantStatus {
				t.Fatalf("status = %d, want %d; body %s", out.Code, tc.wantStatus, out.Body.String())
			}
			var body map[string]string
			if err := json.Unmarshal(out.Body.Bytes(), &body); err != nil {
				t.Fatal(err)
			}
			if body["outcome"] != tc.wantOutcome || body["code"] != tc.wantCode {
				t.Errorf("body = %v, want outcome %q code %q", body, tc.wantOutcome, tc.wantCode)
			}
			if tc.wantStatus == 200 && body["status"] != "STOPPED" {
				t.Errorf("status field = %q, want STOPPED", body["status"])
			}
			var after string
			if err := h.db.QueryRow(`SELECT status FROM agents WHERE id=?`, id).Scan(&after); err != nil {
				t.Fatal(err)
			}
			want := tc.wantDB
			if want == "" {
				want = before
			}
			if after != want {
				t.Errorf("db status %s -> %s, want %s", before, after, want)
			}
		})
	}
}

func TestAgentStop_DaemonUnreachableIsRuntimeUnavailable(t *testing.T) {
	h, user, ws, _, id := covProxyRig(t, t.TempDir()+"/missing.sock")
	req := httptest.NewRequest(http.MethodPost, "/", nil)
	req.SetPathValue("agentId", id)
	req = withWorkspaceUser(req, user, ws, "OWNER")
	out := httptest.NewRecorder()
	h.AgentStop(out, req)
	if out.Code != http.StatusBadGateway {
		t.Fatalf("status = %d, want 502", out.Code)
	}
	var body map[string]string
	_ = json.Unmarshal(out.Body.Bytes(), &body)
	if body["code"] != "runtime_unavailable" || body["error"] != "runtime stop unavailable" {
		t.Errorf("body = %v", body)
	}
}
