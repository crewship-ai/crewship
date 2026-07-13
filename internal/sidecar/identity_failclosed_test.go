package sidecar

import (
	"io"
	"log/slog"
	"net/http/httptest"
	"testing"
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
