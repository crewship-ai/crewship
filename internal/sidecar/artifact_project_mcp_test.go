package sidecar

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/auth/internaltoken"
)

func TestArtifactProjectMCPRejectsMissingCASAndSpoofedIdentity(t *testing.T) {
	for _, raw := range []string{
		`{"slug":"demo","entrypoint":"index.html","files":[]}`,
		`{"slug":"demo","base_version":1,"base_version":2,"entrypoint":"index.html","files":[]}`,
		`{"slug":"demo","base_version":1,"entrypoint":"index.html","files":[],"human_user_id":"victim"}`,
		`{"slug":"../demo","base_version":1,"entrypoint":"index.html","files":[]}`,
	} {
		if _, _, err := artifactProjectMCPPacket("artifact_publish", []byte(raw)); err == nil {
			t.Errorf("accepted invalid tool packet %s", raw)
		}
	}
}

func TestArtifactProjectMCPForwardsOriginalRunAndCAS(t *testing.T) {
	s, key := newRunIdentityServer(t)
	token := internaltoken.DeriveAgentRunToken(key, "ws-1", "agent-boot", "run-boot")
	calls := 0
	host := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.URL.Path != "/api/v1/internal/pages/artifact-tool" || r.Method != "POST" || r.Header.Get("Authorization") != "Bearer "+token || r.Header.Get("X-Internal-Token") != "synthetic-crew-ipc" {
			t.Error("lost immutable run transport")
		}
		var body struct {
			Operation string `json:"operation"`
			Slug      string `json:"slug"`
			Payload   struct {
				Base int64 `json:"base_version"`
			} `json:"payload"`
		}
		if json.NewDecoder(r.Body).Decode(&body) != nil || body.Operation != "artifact_publish" || body.Slug != "demo" || body.Payload.Base != 7 {
			t.Error("manifest CAS changed")
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(409)
		_, _ = w.Write([]byte(`{"error":"conflict; keep draft"}`))
	}))
	defer host.Close()
	s.ipc.BaseURL, s.ipc.Token = host.URL, "synthetic-crew-ipc"
	r := httptest.NewRequest("POST", "/mcp/routines", strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"artifact_publish","arguments":{"slug":"demo","base_version":7,"entrypoint":"index.html","files":[{"path":"index.html","content":"<h1>Hello</h1>","media_type":"text/html"}]}}}`))
	r.Header.Set("Authorization", "Bearer "+token)
	w := httptest.NewRecorder()
	s.handleRoutinesMCP(w, r)
	isError, text := documentCreateMCPResult(t, w)
	if !isError || !strings.Contains(text, "keep draft") || calls != 1 {
		t.Fatalf("conflict retried or hidden: calls=%d error=%v response=%s", calls, isError, text)
	}
}
