package sidecar

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// #2899: every admission failure used to surface as a 403 hard-budget refusal,
// so a host firewall blocking the sidecar read to the agent (and its operator)
// as a budget decision on a workspace with no budget. Each failure kind is
// injected here and must refuse the request (fail closed) under its own
// status and message, and never reach the provider.
func TestManagedBudgetAdmissionClassifiesFailures(t *testing.T) {
	answer := func(status int, body string) http.HandlerFunc {
		return func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(status)
			_, _ = w.Write([]byte(body))
		}
	}
	tests := []struct {
		name       string
		host       http.HandlerFunc // nil = no reachable host
		ipc        func(base string) *IPCConfig
		wantKind   llmAdmissionFailure
		wantStatus int
		wantText   string
	}{
		{
			name:       "no IPC binding",
			ipc:        func(string) *IPCConfig { return &IPCConfig{} },
			wantKind:   admissionConfiguration,
			wantStatus: http.StatusServiceUnavailable,
			wantText:   "(configuration)",
		},
		{
			name:       "host unreachable from the container",
			ipc:        nil, // a closed listener: connection refused
			wantKind:   admissionTransport,
			wantStatus: http.StatusServiceUnavailable,
			wantText:   "unreachable",
		},
		{
			name:       "host rejects the internal token",
			host:       answer(http.StatusForbidden, `{"error":"Forbidden"}`),
			wantKind:   admissionAuthentication,
			wantStatus: http.StatusServiceUnavailable,
			wantText:   "(authentication)",
		},
		{
			name:       "host refuses the network origin",
			host:       answer(http.StatusNotFound, `{"error":"Not Found"}`),
			wantKind:   admissionAuthentication,
			wantStatus: http.StatusServiceUnavailable,
			wantText:   "(authentication)",
		},
		{
			name:       "host cannot evaluate budgets",
			host:       answer(http.StatusServiceUnavailable, `{"error":"budget admission unavailable"}`),
			wantKind:   admissionHostUnavailable,
			wantStatus: http.StatusServiceUnavailable,
			wantText:   "could not evaluate budgets",
		},
		{
			name:       "host answers garbage",
			host:       answer(http.StatusOK, `<html>proxy error</html>`),
			wantKind:   admissionProtocol,
			wantStatus: http.StatusBadGateway,
			wantText:   "(protocol)",
		},
		{
			name:       "host answers an unexpected status",
			host:       answer(http.StatusBadRequest, `{"error":"invalid JSON"}`),
			wantKind:   admissionProtocol,
			wantStatus: http.StatusBadGateway,
			wantText:   "(protocol)",
		},
		{
			name:       "host refuses the agent scope",
			host:       answer(http.StatusForbidden, `{"error":"agent scope unavailable"}`),
			wantKind:   admissionPolicy,
			wantStatus: http.StatusForbidden,
			wantText:   "host policy: agent scope unavailable",
		},
		{
			name:       "hard budget applies",
			host:       answer(http.StatusForbidden, `{"error":"hard-budget traffic requires the restricted broker"}`),
			wantKind:   admissionBudget,
			wantStatus: http.StatusForbidden,
			wantText:   "requires the restricted broker",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			base := ""
			if tc.host != nil {
				host := httptest.NewServer(tc.host)
				defer host.Close()
				base = host.URL
			} else {
				host := httptest.NewServer(http.NotFoundHandler())
				base = host.URL
				host.Close()
			}
			ipc := &IPCConfig{BaseURL: base, Token: "token", WorkspaceID: "w", AgentID: "agent"}
			if tc.ipc != nil {
				ipc = tc.ipc(base)
			}
			admit := (&Server{ipc: ipc}).buildLLMAdmission()

			err := admit(context.Background(), "agent", "c1", "ANTHROPIC")
			var refused *llmAdmissionError
			if !errors.As(err, &refused) {
				t.Fatalf("admission error %v (%T), want *llmAdmissionError — and never nil", err, err)
			}
			if refused.kind != tc.wantKind {
				t.Errorf("kind = %q, want %q (detail %q)", refused.kind, tc.wantKind, refused.detail)
			}

			// Through the proxy: the agent sees the classified refusal and
			// the provider is never called.
			p, _, up := newGraceProxy(t, []Credential{anthropicCred("c1", "sk-new", "", time.Time{})}, []int{200}, nil, nil)
			p.onLLMAdmission = admit
			rr := httptest.NewRecorder()
			req := httptest.NewRequest("POST", "http://api.anthropic.com/v1/messages", strings.NewReader(`{"model":"claude"}`))
			req.RemoteAddr = "127.0.0.1:54321"
			p.ServeHTTP(rr, req)
			if rr.Code != tc.wantStatus {
				t.Errorf("status = %d, want %d: %s", rr.Code, tc.wantStatus, rr.Body.String())
			}
			if !strings.Contains(rr.Body.String(), tc.wantText) {
				t.Errorf("body %q lacks %q", rr.Body.String(), tc.wantText)
			}
			if tc.wantKind != admissionBudget && strings.Contains(rr.Body.String(), "hard-budget") {
				t.Errorf("a %s failure still reads as a budget refusal: %s", tc.wantKind, rr.Body.String())
			}
			if strings.Contains(rr.Body.String(), "127.0.0.1") || strings.Contains(rr.Body.String(), "token") {
				t.Errorf("refusal leaks host detail to the agent: %s", rr.Body.String())
			}
			if len(up.calls) != 0 {
				t.Fatalf("provider reached %d time(s) without admission", len(up.calls))
			}
		})
	}
}
