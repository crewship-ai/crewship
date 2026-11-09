package sidecar

import (
	"encoding/json"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/artifacts/kindguide"
	"github.com/crewship-ai/crewship/internal/auth/internaltoken"
)

func kindGuideMCPCall(t *testing.T, s *Server, token, arguments string) (bool, string) {
	t.Helper()
	r := httptest.NewRequest("POST", "/mcp/routines", strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"artifact_kind_guide","arguments":`+arguments+`}}`))
	if token != "" {
		r.Header.Set("Authorization", "Bearer "+token)
	}
	w := httptest.NewRecorder()
	s.handleRoutinesMCP(w, r)
	return documentCreateMCPResult(t, w)
}

func TestArtifactKindGuideToolIsListed(t *testing.T) {
	for _, tool := range routineMCPTools {
		if tool.Name == "artifact_kind_guide" {
			if !strings.Contains(tool.Description, "Comment is not Propose") {
				t.Fatalf("description does not carry the mode rule: %s", tool.Description)
			}
			return
		}
	}
	t.Fatal("artifact_kind_guide missing from the routines MCP catalog")
}

// The tool serves the embedded registry byte for byte, makes no host call
// and accepts only {kind}. It still requires a recognised agent token.
func TestArtifactKindGuideToolServesTheEmbeddedGuide(t *testing.T) {
	s, key := newRunIdentityServer(t)
	s.ipc.BaseURL = "http://127.0.0.1:1" // any host call would fail the test below
	token := internaltoken.DeriveAgentRunToken(key, "ws-1", "agent-boot", "run-boot")
	for _, kind := range kindguide.Kinds() {
		isError, text := kindGuideMCPCall(t, s, token, `{"kind":"`+kind+`"}`)
		if isError {
			t.Fatalf("%s: %s", kind, text)
		}
		var got kindguide.Guide
		if err := json.Unmarshal([]byte(text), &got); err != nil {
			t.Fatal(err)
		}
		want, _ := kindguide.Lookup(kind)
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("%s: tool guide differs from the registry", kind)
		}
	}
	for _, bad := range []string{`{"kind":"word"}`, `{"kind":"project"}`, `{"kind":" docs"}`, `{"KIND":"docs"}`, `{}`, `{"kind":"docs","mode":"edit"}`, `{"kind":7}`, `[]`} {
		if isError, text := kindGuideMCPCall(t, s, token, bad); !isError {
			t.Fatalf("accepted %s: %s", bad, text)
		}
	}
	if isError, _ := kindGuideMCPCall(t, s, "", `{"kind":"docs"}`); !isError {
		t.Fatal("served a guide without an agent token")
	}
}
