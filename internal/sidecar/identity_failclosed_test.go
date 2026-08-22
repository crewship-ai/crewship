package sidecar

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/crewship-ai/crewship/internal/auth/internaltoken"
)

// #1059: actingAgentID's legacy (token-less) fallback returned ("", true) when
// there was no usable boot identity (s.ipc nil or AgentID empty), conflating
// "no identity" with "resolved". Callers happen to pre-check ipc today, but the
// primitive must fail closed so a future caller can't be silently attributed to
// an empty agent id.
func TestActingAgentID_FailsClosedWithoutIdentity(t *testing.T) {
	silent := slog.New(slog.NewTextHandler(io.Discard, nil))
	req := httptest.NewRequest("POST", "/x", nil) // no Authorization header

	// ipc == nil → cannot attribute → ("", false).
	s := &Server{logger: silent, ipc: nil}
	if id, ok := s.actingAgentID(req); ok || id != "" {
		t.Errorf("ipc=nil: got (%q,%v), want (\"\",false)", id, ok)
	}

	// ipc present but AgentID empty → still cannot attribute → ("", false).
	s2 := &Server{logger: silent, ipc: &IPCConfig{AgentID: ""}}
	if id, ok := s2.actingAgentID(req); ok || id != "" {
		t.Errorf("ipc.AgentID empty: got (%q,%v), want (\"\",false)", id, ok)
	}

	// Legacy legit fallback preserved: no tokens provisioned + a real boot
	// AgentID → attribute to the boot agent.
	s3 := &Server{logger: silent, ipc: &IPCConfig{AgentID: "boot-agent"}}
	if id, ok := s3.actingAgentID(req); !ok || id != "boot-agent" {
		t.Errorf("legacy fallback: got (%q,%v), want (\"boot-agent\",true)", id, ok)
	}
}

func TestLLMRouteIdentity_AllProviderAuthShapes(t *testing.T) {
	const routeKey = "crew-bound-route-key"
	token := internaltoken.DeriveLLMRouteToken(routeKey, "agent-a")
	const fingerprint = "abcdef123456"
	s := &Server{
		routeAuth: &RouteAuth{Key: routeKey},
	}
	tests := []struct {
		name  string
		apply func(*http.Request)
	}{
		{"openai bearer", func(r *http.Request) {
			r.Header.Set("Authorization", "Bearer sk-dummy."+token+internaltoken.RouteFingerprintDelimiter+fingerprint)
		}},
		{"anthropic header", func(r *http.Request) {
			r.Header.Set("x-api-key", "sk-ant-dummy."+token+internaltoken.RouteFingerprintDelimiter+fingerprint)
		}},
		{"google header", func(r *http.Request) {
			r.Header.Set("x-goog-api-key", "dummy."+token+internaltoken.RouteFingerprintDelimiter+fingerprint)
		}},
		{"google query", func(r *http.Request) {
			q := r.URL.Query()
			q.Set("key", "dummy."+token+internaltoken.RouteFingerprintDelimiter+fingerprint)
			r.URL.RawQuery = q.Encode()
		}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest("POST", "http://127.0.0.1/llm", nil)
			tc.apply(req)
			id, fp, present, ok := s.llmRouteIdentity(req)
			if !present || !ok || id != "agent-a" || fp != fingerprint {
				t.Fatalf("identity = (%q,%q,%v,%v), want (agent-a,%s,true,true)", id, fp, present, ok, fingerprint)
			}
		})
	}
}

func TestLLMRouteIdentity_DoesNotRequireIPC(t *testing.T) {
	const routeKey = "crew-bound-route-key"
	token := internaltoken.DeriveLLMRouteToken(routeKey, "solo")
	const fingerprint = "abcdef123456"
	s := &Server{routeAuth: &RouteAuth{Key: routeKey}}
	req := httptest.NewRequest("POST", "http://127.0.0.1/llm", nil)
	req.Header.Set("Authorization", "Bearer dummy."+token+internaltoken.RouteFingerprintDelimiter+fingerprint)

	id, fp, present, ok := s.llmRouteIdentity(req)
	if !present || !ok || id != "solo" || fp != fingerprint {
		t.Fatalf("identity = (%q,%q,%v,%v), want (solo,%s,true,true)", id, fp, present, ok, fingerprint)
	}
}
