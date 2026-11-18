package sidecar

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestProxyAdmissionUsesOnlyAuthenticatedCurrentRun(t *testing.T) {
	for _, run := range []string{"run-one", "run-two", ""} {
		t.Run("context-"+run, func(t *testing.T) {
			var got map[string]string
			host := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if err := json.NewDecoder(r.Body).Decode(&got); err != nil {
					t.Error(err)
				}
				w.Header().Set("Content-Type", "application/json")
				w.Write([]byte(`{"allowed":true}`))
			}))
			defer host.Close()
			s := &Server{ipc: &IPCConfig{BaseURL: host.URL, Token: "synthetic", WorkspaceID: "ws", AgentID: "boot-agent", RunID: "boot-run"}}
			ctx := context.Background()
			if run != "" {
				ctx = context.WithValue(ctx, authenticatedLLMRunKey{}, run)
			}
			if err := s.buildLLMAdmission()(ctx, "current-agent", "credential", "OPENAI"); err != nil {
				t.Fatal(err)
			}
			if got["agent_id"] != "current-agent" || got["credential_id"] != "credential" || got["run_id"] != run {
				t.Fatalf("wrong request identity: %+v", got)
			}
			if run == "" {
				if _, present := got["run_id"]; present {
					t.Fatal("legacy call invented a run identity")
				}
			}
			if got["run_id"] == "boot-run" {
				t.Fatal("concurrent caller inherited sidecar boot run")
			}
		})
	}
}

// Exercise the managed IPC callback through actual proxy forwarding. A host
// audit failure must prevent even the first upstream attempt in both routes.
func TestProxyAdmissionAuditFailurePreventsForwarding(t *testing.T) {
	for _, reverse := range []bool{false, true} {
		for _, auditOK := range []bool{false, true} {
			t.Run(map[bool]string{false: "forward", true: "reverse"}[reverse]+"/"+map[bool]string{false: "audit-failed", true: "audit-saved"}[auditOK], func(t *testing.T) {
				admissions := 0
				host := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					admissions++
					var body map[string]string
					if json.NewDecoder(r.Body).Decode(&body) != nil || body["credential_id"] != "c1" || body["run_id"] != "current-run" || r.Header.Get("X-Internal-Token") != "synthetic" {
						t.Error("missing authenticated request attribution")
						http.Error(w, "invalid attribution", http.StatusForbidden)
						return
					}
					if !auditOK {
						http.Error(w, "proxy audit unavailable", http.StatusServiceUnavailable)
						return
					}
					w.Header().Set("Content-Type", "application/json")
					w.Write([]byte(`{"allowed":true}`))
				}))
				defer host.Close()
				s := &Server{ipc: &IPCConfig{BaseURL: host.URL, Token: "synthetic", WorkspaceID: "ws", AgentID: "boot-agent", RunID: "boot-run"}}
				p, _, upstream := newGraceProxy(t, []Credential{anthropicCred("c1", "synthetic", "", time.Time{})}, []int{200}, nil, nil)
				p.onLLMAdmission = s.buildLLMAdmission()
				target := "http://api.anthropic.com/v1/messages"
				if reverse {
					target = "http://127.0.0.1:9119/v1/messages"
				}
				req := httptest.NewRequest("POST", target, strings.NewReader(`{"model":"claude"}`))
				req.RemoteAddr = "127.0.0.1:54321"
				req = req.WithContext(context.WithValue(req.Context(), authenticatedLLMRunKey{}, "current-run"))
				rr := httptest.NewRecorder()
				p.ServeHTTP(rr, req)
				wantCode, wantCalls := http.StatusServiceUnavailable, 0
				if auditOK {
					wantCode, wantCalls = http.StatusOK, 1
				}
				if rr.Code != wantCode || len(upstream.calls) != wantCalls || admissions != 1 {
					t.Fatalf("status=%d upstream=%d admissions=%d; want status=%d upstream=%d admission=1", rr.Code, len(upstream.calls), admissions, wantCode, wantCalls)
				}
			})
		}
	}
}
