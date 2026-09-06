package sidecar

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"testing"
)

// #2428: every crewship-hosted MCP server must acknowledge a notification with
// 202 Accepted and an empty body.
//
// The regression this pins is not cosmetic. An empty 200 carries no
// Content-Type, and a strict Rust MCP client treats that as a fatal transport
// error ("Unexpected content type: Some(\"missing-content-type; body: \")"),
// kills the worker and drops the server — so memory, routines and notify were
// announced at initialize and gone one message later. A more permissive
// TypeScript client accepts the empty 200, which is why the
// two adapters silently disagreed about whether Crewship's own tools exist.
func TestMCPNotificationsAckWith202AndNoBody(t *testing.T) {
	handlers := map[string]func(*Server) (string, func(http.ResponseWriter, *http.Request)){
		"memory": func(s *Server) (string, func(http.ResponseWriter, *http.Request)) {
			return "/mcp/memory", s.handleMemoryMCP
		},
		"routines": func(s *Server) (string, func(http.ResponseWriter, *http.Request)) {
			return "/mcp/routines", s.handleRoutinesMCP
		},
		"notify": func(s *Server) (string, func(http.ResponseWriter, *http.Request)) {
			return "/mcp/notify", s.handleNotifyMCP
		},
	}
	for name, build := range handlers {
		for _, method := range []string{"notifications/initialized", "notifications/cancelled"} {
			t.Run(name+"/"+method, func(t *testing.T) {
				s := newMemoryMCPTestServer(t)
				path, handler := build(s)
				req := httptest.NewRequest("POST", path,
					bytes.NewReader([]byte(`{"jsonrpc":"2.0","method":"`+method+`"}`)))
				req.Host = "127.0.0.1:9119"
				w := httptest.NewRecorder()

				handler(w, req)

				if w.Code != http.StatusAccepted {
					t.Errorf("status = %d, want 202 — strict clients reject an empty 200 without Content-Type", w.Code)
				}
				if body := w.Body.String(); body != "" {
					t.Errorf("body = %q, want empty", body)
				}
			})
		}
	}
}
