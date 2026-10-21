package sidecar

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// A constructor transport must replace only the upstream network boundary:
// credential selection and managed host admission still run before it.
func TestServerProxyTransportPreservesCredentialInjectionAndAdmission(t *testing.T) {
	for _, allowed := range []bool{true, false} {
		t.Run(map[bool]string{true: "allowed", false: "denied"}[allowed], func(t *testing.T) {
			admissions, upstreamCalls := 0, 0
			host := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/api/v1/internal/cost/admit" {
					w.WriteHeader(http.StatusNoContent)
					return
				}
				admissions++
				var body map[string]string
				if r.Method != http.MethodPost || r.Header.Get("X-Internal-Token") != "synthetic-ipc" || json.NewDecoder(r.Body).Decode(&body) != nil || body["agent_id"] != "actor" || body["credential_id"] != "payer" || body["provider"] != string(ProviderAnthropic) {
					t.Error("admission lost host-selected actor or credential")
				}
				json.NewEncoder(w).Encode(map[string]bool{"allowed": allowed})
			}))
			defer host.Close()
			transport := roundTripFunc(func(r *http.Request) (*http.Response, error) {
				upstreamCalls++
				if r.URL.Scheme != "https" || r.URL.Host != "api.anthropic.com" || r.URL.Path != "/v1/messages" || r.Header.Get("x-api-key") != "synthetic-provider-key" {
					t.Error("transport bypassed provider routing or credential injection")
				}
				return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": []string{"application/json"}}, Body: io.NopCloser(strings.NewReader(`{"type":"message","content":[]}`)), Request: r}, nil
			})
			s := NewServer(ServerConfig{
				Credentials:    []Credential{{ID: "payer", Provider: ProviderAnthropic, Token: "synthetic-provider-key"}},
				IPC:            &IPCConfig{BaseURL: host.URL, Token: "synthetic-ipc", WorkspaceID: "workspace", AgentID: "actor"},
				ProxyTransport: transport,
			})
			defer s.memoryExec.Close(time.Second)
			r := httptest.NewRequest(http.MethodPost, "http://127.0.0.1:9119/v1/messages", strings.NewReader(`{"model":"fixture-model","messages":[]}`))
			r.RemoteAddr = "127.0.0.1:54321"
			r.Header.Set("x-api-key", "client-placeholder")
			w := httptest.NewRecorder()
			s.httpServer.Handler.ServeHTTP(w, r)
			wantCode, wantCalls := http.StatusForbidden, 0
			if allowed {
				wantCode, wantCalls = http.StatusOK, 1
			}
			if w.Code != wantCode || upstreamCalls != wantCalls || admissions != 1 {
				t.Fatalf("status=%d upstream=%d admissions=%d body=%s", w.Code, upstreamCalls, admissions, w.Body.String())
			}
		})
	}
}

func TestServerProxyTransportIsNotSerialized(t *testing.T) {
	data, err := json.Marshal(ServerConfig{ProxyTransport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		t.Fatal("serialization invoked transport")
		return nil, nil
	})})
	if err != nil || strings.Contains(string(data), "ProxyTransport") {
		t.Fatalf("constructor-only transport entered configuration: %s %v", data, err)
	}
	var cfg ServerConfig
	if err := json.Unmarshal([]byte(`{"ProxyTransport":{}}`), &cfg); err != nil || cfg.ProxyTransport != nil {
		t.Fatalf("JSON enabled constructor-only transport: %v", err)
	}
}
