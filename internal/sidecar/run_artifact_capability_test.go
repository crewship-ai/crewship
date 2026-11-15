package sidecar

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/auth/internaltoken"
)

// #5339: the host-minted capability of a run without a human is registered by
// the run's own token, once, and attached only to that run's artifact calls.

func registerRunCapability(s *Server, token, capability string) int {
	r := httptest.NewRequest(http.MethodPost, "/agent/run/artifact-capability", nil)
	if token != "" {
		r.Header.Set("Authorization", "Bearer "+token)
	}
	if capability != "" {
		r.Header.Set(runCapabilityHeader, capability)
	}
	w := httptest.NewRecorder()
	s.handleRunArtifactCapability(w, r)
	return w.Code
}

func artifactVersionsCall(t *testing.T, s *Server, token string) {
	t.Helper()
	r := httptest.NewRequest("POST", "/mcp/routines", strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"artifact_versions","arguments":{"slug":"demo"}}}`))
	r.Header.Set("Authorization", "Bearer "+token)
	r.Header.Set(runCapabilityHeader, "urcv1."+strings.Repeat("ee", 32)) // an agent's own header is never forwarded
	s.handleRoutinesMCP(httptest.NewRecorder(), r)
}

func TestRunArtifactCapability_AttachedOnlyToItsOwnRun(t *testing.T) {
	s, key := newRunIdentityServer(t)
	own := internaltoken.DeriveAgentRunToken(key, "ws-1", "agent-boot", "run-boot")
	other := internaltoken.DeriveAgentRunToken(key, "ws-1", "agent-boot", "run-other")
	capability := "urcv1." + strings.Repeat("ab", 32)
	var seen []string
	host := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = append(seen, r.Header.Get(runCapabilityHeader))
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{}`))
	}))
	defer host.Close()
	s.ipc.BaseURL, s.ipc.Token = host.URL, "synthetic-crew-ipc"

	if code := registerRunCapability(s, own, capability); code != 200 {
		t.Fatalf("register: %d", code)
	}
	artifactVersionsCall(t, s, own)
	artifactVersionsCall(t, s, other)
	if len(seen) != 2 || seen[0] != capability || seen[1] != "" {
		t.Fatalf("forwarded capabilities %q", seen)
	}
}

func TestRunArtifactCapability_SetOnceAndOnlyByARunToken(t *testing.T) {
	s, key := newRunIdentityServer(t)
	own := internaltoken.DeriveAgentRunToken(key, "ws-1", "agent-boot", "run-boot")
	capability := "urcv1." + strings.Repeat("ab", 32)
	if code := registerRunCapability(s, "", capability); code != http.StatusUnauthorized {
		t.Fatalf("no token: %d", code)
	}
	if code := registerRunCapability(s, "agtv2.forged.x.y.z", capability); code != http.StatusForbidden {
		t.Fatalf("forged token: %d", code)
	}
	if code := registerRunCapability(s, own, "not-a-capability"); code != http.StatusBadRequest {
		t.Fatalf("malformed capability: %d", code)
	}
	if code := registerRunCapability(s, own, capability); code != 200 {
		t.Fatalf("register: %d", code)
	}
	if code := registerRunCapability(s, own, capability); code != 200 {
		t.Fatalf("identical retry: %d", code)
	}
	if code := registerRunCapability(s, own, "urcv1."+strings.Repeat("cd", 32)); code != http.StatusConflict {
		t.Fatalf("replacement: %d", code)
	}
	if got := s.runCapabilities.get("run-boot"); got != capability {
		t.Fatalf("stored %q", got)
	}
}

func TestRunArtifactCapability_ForgottenWhenTheRunEnds(t *testing.T) {
	s, key := newRunIdentityServer(t)
	own := internaltoken.DeriveAgentRunToken(key, "ws-1", "agent-boot", "run-boot")
	if code := registerRunCapability(s, own, "urcv1."+strings.Repeat("ab", 32)); code != 200 {
		t.Fatalf("register: %d", code)
	}
	r := httptest.NewRequest(http.MethodPost, "/agent/run/end", nil)
	r.Header.Set("Authorization", "Bearer "+own)
	w := httptest.NewRecorder()
	s.handleRunEnd(w, r)
	if w.Code != 200 || s.runCapabilities.get("run-boot") != "" {
		t.Fatalf("run end kept the capability: %d", w.Code)
	}
}
