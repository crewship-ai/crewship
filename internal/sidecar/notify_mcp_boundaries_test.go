package sidecar

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

func notifyTestRequest(s *Server, body io.Reader, token string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(http.MethodPost, "/mcp/notify", body)
	r.Host = "127.0.0.1:9119"
	r.RemoteAddr = "127.0.0.1:50000"
	if token != "" {
		r.Header.Set("Authorization", "Bearer "+token)
	}
	w := httptest.NewRecorder()
	s.buildHandler(nil).ServeHTTP(w, r)
	return w
}

func TestNotifyMCPProtocolPreservesIDsAndDistinguishesNotifications(t *testing.T) {
	s := newRoutineMCPTestServer(t, nil)
	for _, tc := range []struct {
		name, body   string
		status, code int
	}{
		{"malformed", "not-json", 400, -32700},
		{"wrong version", `{"jsonrpc":"1.0","id":7}`, 400, -32600},
		{"missing version", `{}`, 400, -32600},
		{"unknown method", `{"jsonrpc":"2.0","id":7,"method":"missing"}`, 200, -32601},
		{"bad params", `{"jsonrpc":"2.0","id":7,"method":"tools/call","params":[]}`, 200, -32602},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := notifyTestRequest(s, strings.NewReader(tc.body), "")
			var got memoryMCPResponse
			if w.Code != tc.status || json.Unmarshal(w.Body.Bytes(), &got) != nil || got.Error == nil || got.Error.Code != tc.code {
				t.Fatalf("wrong protocol error: %d %s", w.Code, w.Body.String())
			}
			if strings.Contains(tc.body, `"id":7`) && string(got.ID) != "7" {
				t.Fatalf("request id lost: %s", got.ID)
			}
		})
	}
	w := notifyTestRequest(s, failingReader{}, "")
	if w.Code != 400 || !strings.Contains(w.Body.String(), "-32700") {
		t.Fatalf("read failure hidden: %d %s", w.Code, w.Body.String())
	}
	for _, method := range []string{"notifications/initialized", "notifications/cancelled"} {
		w := notifyTestRequest(s, strings.NewReader(`{"jsonrpc":"2.0","method":"`+method+`"}`), "")
		if w.Code != 202 || w.Body.Len() != 0 {
			t.Fatalf("notification returned an RPC body: %d %s", w.Code, w.Body.String())
		}
	}
	w = notifyTestRequest(s, strings.NewReader(`{"jsonrpc":"2.0","id":"client","method":"initialize"}`), "")
	if w.Code != 200 || !strings.Contains(w.Body.String(), NotifyMCPServerName) || !strings.Contains(w.Body.String(), MemoryMCPProtocolVersion) {
		t.Fatalf("invalid initialization: %s", w.Body.String())
	}
	w = notifyTestRequest(s, strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"tools/list"}`), "")
	var envelope struct {
		Result struct {
			Tools []memoryMCPToolDescriptor `json:"tools"`
		} `json:"result"`
	}
	if json.Unmarshal(w.Body.Bytes(), &envelope) != nil || len(envelope.Result.Tools) != 2 {
		t.Fatalf("invalid tool catalog: %s", w.Body.String())
	}
	for _, tool := range envelope.Result.Tools {
		var schema map[string]any
		if json.Unmarshal(tool.InputSchema, &schema) != nil || schema["type"] != "object" {
			t.Fatalf("tool schema invalid: %+v", tool)
		}
	}
}

func TestNotifyMCPRefusesUnconfiguredUnidentifiedAndMalformedCalls(t *testing.T) {
	for _, tc := range []struct {
		name                string
		ipc                 bool
		token, params, part string
	}{
		{"no IPC", false, "", `{"name":"notify_send"}`, "IPC not configured"},
		{"unknown token", true, "foreign", `{"name":"notify_send"}`, "unrecognized agent token"},
		{"bad arguments", true, "agent-token", `{"name":"notify_send","arguments":[]}`, "invalid arguments"},
		{"missing channel", true, "agent-token", `{"name":"notify_send"}`, "channel_id required"},
		{"missing title", true, "agent-token", `{"name":"notify_send","arguments":{"channel_id":"channel"}}`, "title required"},
		{"unknown tool", true, "agent-token", `{"name":"unsupported"}`, "unknown tool"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var ipc *IPCConfig
			if tc.ipc {
				ipc = &IPCConfig{BaseURL: "http://127.0.0.1:1", AgentID: "trusted-agent", AgentToken: "agent-token"}
			}
			s := newRoutineMCPTestServer(t, ipc)
			w := notifyTestRequest(s, strings.NewReader(`{"jsonrpc":"2.0","id":7,"method":"tools/call","params":`+tc.params+`}`), tc.token)
			var got struct {
				Result memoryMCPToolCallResult `json:"result"`
			}
			if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &got) != nil || !got.Result.IsError || len(got.Result.Content) != 1 || !strings.Contains(got.Result.Content[0].Text, tc.part) {
				t.Fatalf("refusal not exposed as tool error: %d %s", w.Code, w.Body.String())
			}
		})
	}
}

func TestNotifyMCPForwardsOnlyBoundIdentityAndPreservesUpstreamRefusals(t *testing.T) {
	for _, mode := range []string{"list", "send", "rate limited", "transport failure"} {
		t.Run(mode, func(t *testing.T) {
			var calls atomic.Int32
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				if r.Header.Get("X-Internal-Token") != "ipc-token" {
					t.Error("IPC authority missing")
				}
				if mode == "list" {
					q := r.URL.Query()
					if r.Method != "GET" || r.URL.Path != "/api/v1/internal/notifications/channels" || q.Get("workspace_id") != "ws &" || q.Get("crew_id") != "crew /" || q.Get("agent_id") != "trusted-agent" {
						t.Errorf("wrong discovery scope: %s", r.URL)
					}
				} else {
					var b map[string]any
					if json.NewDecoder(r.Body).Decode(&b) != nil || b["workspace_id"] != "ws &" || b["crew_id"] != "crew /" || b["agent_id"] != "trusted-agent" || b["title"] != "Synthetic test" || b["channel_id"] != "channel" {
						t.Errorf("unbound send body: %+v", b)
					}
					if r.Method != "POST" || r.URL.Path != "/api/v1/internal/notifications/send" {
						t.Errorf("wrong send route: %s %s", r.Method, r.URL)
					}
				}
				if mode == "rate limited" {
					w.WriteHeader(429)
					io.WriteString(w, `{"error":"rate limited"}`)
				} else {
					io.WriteString(w, `{"ok":true}`)
				}
			}))
			defer upstream.Close()
			if mode == "transport failure" {
				upstream.Close()
			}
			s := newRoutineMCPTestServer(t, &IPCConfig{BaseURL: upstream.URL, Token: "ipc-token", WorkspaceID: "ws &", CrewID: "crew /", AgentID: "trusted-agent", AgentToken: "agent-token"})
			name := "notify_send"
			if mode == "list" {
				name = "list_notification_channels"
			}
			body := `{"jsonrpc":"2.0","id":7,"method":"tools/call","params":{"name":"` + name + `","arguments":{"channel_id":"channel","title":"Synthetic test","agent_id":"spoofed","workspace_id":"foreign","crew_id":"foreign"}}}`
			w := notifyTestRequest(s, strings.NewReader(body), "agent-token")
			var got struct {
				Result memoryMCPToolCallResult `json:"result"`
			}
			failed := mode == "rate limited" || mode == "transport failure"
			if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &got) != nil || got.Result.IsError != failed || len(got.Result.Content) != 1 {
				t.Fatalf("upstream status lost: %d %s", w.Code, w.Body.String())
			}
			if mode == "transport failure" {
				if calls.Load() != 0 || !strings.Contains(got.Result.Content[0].Text, "notification send failed") {
					t.Fatalf("transport refusal lost: %s", w.Body.String())
				}
			} else if calls.Load() != 1 {
				t.Fatalf("unexpected replay: %d", calls.Load())
			}
			if mode == "rate limited" && !strings.Contains(got.Result.Content[0].Text, "rate limited") {
				t.Fatalf("retry hint lost: %s", w.Body.String())
			}
		})
	}
}
