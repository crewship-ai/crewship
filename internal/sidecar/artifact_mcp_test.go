package sidecar

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/auth/internaltoken"
)

func artifactMCPCall(t *testing.T, s *Server, token, name, args string) *httptest.ResponseRecorder {
	t.Helper()
	r := httptest.NewRequest("POST", "/mcp/routines", strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"`+name+`","arguments":`+args+`}}`))
	r.Header.Set("Authorization", "Bearer "+token)
	r.Host = "127.0.0.1:9119"
	w := httptest.NewRecorder()
	s.handleRoutinesMCP(w, r)
	return w
}

func TestArtifactMCPForwardsActualRunAndPreservesConflict(t *testing.T) {
	s, key := newRunIdentityServer(t)
	token := internaltoken.DeriveAgentRunToken(key, "ws-1", "agent-boot", "run-boot")
	calls := 0
	host := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "run-status") {
			w.WriteHeader(503)
			return
		}
		calls++
		if r.Header.Get("Authorization") != "Bearer "+token || r.Header.Get("X-Internal-Token") != "crew-ipc" {
			t.Error("actual run/crew capability lost")
		}
		if r.URL.Path != "/api/v1/internal/artifacts/report/revisions" {
			t.Errorf("path=%s", r.URL.Path)
		}
		body, _ := io.ReadAll(r.Body)
		var fields map[string]any
		_ = json.Unmarshal(body, &fields)
		if fields["base_rev"] != float64(4) || fields["mutation_id"] != "exact-retry" || fields["html"] != "<h1>safe</h1>" {
			t.Errorf("changed body %s", body)
		}
		if _, ok := fields["slug"]; ok {
			t.Error("routing slug leaked into body")
		}
		w.Header().Set("Retry-After", "3")
		w.WriteHeader(409)
		_, _ = w.Write([]byte(`{"error":"stale","code":"stale_revision"}`))
	}))
	defer host.Close()
	s.ipc.BaseURL = host.URL
	s.ipc.Token = "crew-ipc"
	w := artifactMCPCall(t, s, token, "artifact_publish", `{"slug":"report","html":"<h1>safe</h1>","base_rev":4,"mutation_id":"exact-retry"}`)
	if calls != 1 || !strings.Contains(w.Body.String(), `"isError":true`) || !strings.Contains(w.Body.String(), "stale_revision") || !strings.Contains(w.Body.String(), "retry_after") {
		t.Fatalf("calls=%d %s", calls, w.Body.String())
	}
}

func TestArtifactMCPRejectsIdentityAndAmbiguousArgumentsBeforeTransport(t *testing.T) {
	s, key := newRunIdentityServer(t)
	token := internaltoken.DeriveAgentRunToken(key, "ws-1", "agent-boot", "run-boot")
	for _, args := range []string{`null`, `[]`, `{"slug":"report","run_id":"forged"}`, `{"slug":"report","slug":"other"}`, `{"slug":"../other"}`, `{"slug":null}`, `{"Slug":"report"}`, `{"slug":"report","revision":"1"}`, `{"slug":"report","revision":1.5}`, `{"slug":"report","revision":true}`} {
		w := artifactMCPCall(t, s, token, "artifact_read", args)
		if !strings.Contains(w.Body.String(), `"isError":true`) {
			t.Fatalf("accepted %s: %s", args, w.Body.String())
		}
	}
	for _, token := range []string{"", internaltoken.DeriveAgentToken(runTestMaster, "ws-1", "agent-boot"), "agtv2.forged"} {
		w := artifactMCPCall(t, s, token, "artifact_read", `{"slug":"report"}`)
		if !strings.Contains(w.Body.String(), `"isError":true`) {
			t.Fatalf("accepted non-run capability: %s", w.Body.String())
		}
	}
}

func TestArtifactMCPOuterBudgetAndBoundedResult(t *testing.T) {
	s, _ := newRunIdentityServer(t)
	w := artifactMCPCall(t, s, "", "artifact_create", `{"slug":"report","name":"Report","html":"`+strings.Repeat("x", 1<<20)+`","mutation_id":"one"}`)
	if w.Code != 400 {
		t.Fatalf("oversized outer request=%d", w.Code)
	}
	recorder := &artifactMCPRecorder{header: http.Header{}}
	recorder.WriteHeader(200)
	_, _ = recorder.Write([]byte(strings.Repeat("x", (1<<20)+1)))
	if !recorder.overflow || recorder.body.Len() != 0 {
		t.Fatal("oversized result was retained")
	}
}

func TestArtifactMCPRequestUpdatePreservesRunAndTypedBody(t *testing.T) {
	s, key := newRunIdentityServer(t)
	token := internaltoken.DeriveAgentRunToken(key, "ws-1", "agent-boot", "run-boot")
	calls := 0
	denied := false
	host := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "run-status") {
			w.WriteHeader(503)
			return
		}
		calls++
		if r.Method != "POST" || r.URL.Path != "/api/v1/internal/artifact-requests/job/updates" || r.Header.Get("Authorization") != "Bearer "+token {
			t.Fatal("lost exact request transport")
		}
		var body map[string]any
		if json.NewDecoder(r.Body).Decode(&body) != nil || body["operation"] != "reply" || body["body"] != "Answer" || body["mutation_id"] != "same" {
			t.Errorf("wrong body %v", body)
		}
		if denied {
			w.WriteHeader(403)
			_, _ = w.Write([]byte(`{"error":"run revoked"}`))
			return
		}
		_, _ = w.Write([]byte(`{"status":"succeeded","result_kind":"reply"}`))
	}))
	defer host.Close()
	s.ipc.BaseURL = host.URL
	args := `{"request_id":"job","operation":"reply","body":"Answer","mutation_id":"same"}`
	w := artifactMCPCall(t, s, token, "artifact_request_update", args)
	if strings.Contains(w.Body.String(), `"isError":true`) || calls != 1 {
		t.Fatal(w.Body.String())
	}
	denied = true
	w = artifactMCPCall(t, s, token, "artifact_request_update", args)
	if !strings.Contains(w.Body.String(), "run revoked") {
		t.Fatal(w.Body.String())
	}
	w = artifactMCPCall(t, s, token, "artifact_request_update", strings.TrimSuffix(args, "}")+`,"run_id":"other"}`)
	if calls != 2 || !strings.Contains(w.Body.String(), `"isError":true`) {
		t.Fatal("accepted caller identity")
	}
}

func TestArtifactRequestUpdateRouterTerminatesAfterControlHandler(t *testing.T) {
	s, key := newRunIdentityServer(t)
	token := internaltoken.DeriveAgentRunToken(key, "ws-1", "agent-boot", "run-boot")
	calls := 0
	host := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "run-status") {
			w.WriteHeader(503)
			return
		}
		calls++
		if r.Method != "POST" || r.URL.Path != "/api/v1/internal/artifact-requests/job/updates" {
			t.Errorf("unexpected control dispatch %s %s", r.Method, r.URL.Path)
		}
		_, _ = w.Write([]byte(`{"status":"succeeded","result_kind":"reply"}`))
	}))
	defer host.Close()
	s.ipc.BaseURL = host.URL
	r := httptest.NewRequest("POST", "/artifact-requests/job/updates", strings.NewReader(`{"operation":"reply","body":"Answer","mutation_id":"same"}`))
	r.Host = "127.0.0.1:9119"
	r.RemoteAddr = "127.0.0.1:12345"
	r.Header.Set("Authorization", "Bearer "+token)
	w := httptest.NewRecorder()
	s.buildHandler(nil).ServeHTTP(w, r)
	if calls != 1 || w.Code != 200 || w.Body.String() != `{"status":"succeeded","result_kind":"reply"}` {
		t.Fatalf("calls=%d status=%d body=%s", calls, w.Code, w.Body.String())
	}
}
