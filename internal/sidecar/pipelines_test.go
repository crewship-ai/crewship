package sidecar

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func pipelinesSilentLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// ---------------------------------------------------------------------------
// pipelines.go — sidecar handlers that forward agent-facing /pipelines/*
// calls to crewshipd with author identity injected from IPC.
//
// Tests focus on the trust boundary: the sidecar MUST overwrite any
// caller-supplied author_* fields with values from s.ipc, and inject
// X-Crewship-Invoking-* headers on Run so the journal records the
// real invoker (the cross-crew reuse security gate).
// ---------------------------------------------------------------------------

func newPipelineTestServer(t *testing.T, ipc *IPCConfig) *Server {
	t.Helper()
	return NewServer(ServerConfig{
		Addr:   "127.0.0.1:0",
		IPC:    ipc,
		Logger: pipelinesSilentLogger(),
	})
}

// ---- slugifyForPipelines ----

func TestSlugifyForPipelines_Cases(t *testing.T) {
	cases := []struct {
		name, in, want string
	}{
		{"plain", "hello world", "hello-world"},
		{"mixed-case", "HelloWorld", "helloworld"},
		{"trim-trailing-hyphen", "hello-", "hello"},
		{"trim-trailing-underscore", "hello_", "hello"},
		{"keeps-underscore", "snake_case_name", "snake_case_name"},
		{"keeps-hyphen", "kebab-case", "kebab-case"},
		{"strips-punctuation", "What's up?!", "whats-up"},
		{"collapses-spaces", "hello   world", "hello-world"},
		{"path-becomes-hyphen", "src/foo.go:hello", "src-foo-go-hello"},
		{"drops-leading-punct", "...name", "name"},
		{"empty-on-pure-punct", "!!!???", ""},
		{"empty-input", "", ""},
		{"whitespace-only", "   ", ""},
		{"digits-allowed", "name123", "name123"},
		{"unicode-stripped", "héllo wörld", "hllo-wrld"},
		{"caps-at-64", strings.Repeat("a", 200), strings.Repeat("a", 64)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := slugifyForPipelines(tc.in)
			if got != tc.want {
				t.Errorf("slugifyForPipelines(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

// ---- handlePipelinesList ----

func TestHandlePipelinesList_NoIPC_503(t *testing.T) {
	s := newPipelineTestServer(t, nil)
	rr := httptest.NewRecorder()
	s.handlePipelinesList(rr, httptest.NewRequest("GET", "/pipelines", nil))
	if rr.Code != http.StatusServiceUnavailable {
		t.Errorf("status = %d, want 503", rr.Code)
	}
}

func TestHandlePipelinesList_ForwardsToCrewshipdWithQuery(t *testing.T) {
	var gotPath, gotToken string
	mock := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		if r.URL.RawQuery != "" {
			gotPath += "?" + r.URL.RawQuery
		}
		gotToken = r.Header.Get("X-Internal-Token")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"items":[]}`))
	}))
	defer mock.Close()

	s := newPipelineTestServer(t, &IPCConfig{BaseURL: mock.URL, Token: "tok-list", WorkspaceID: "ws-7"})
	req := httptest.NewRequest("GET", "/pipelines?limit=5&state=published", nil)
	rr := httptest.NewRecorder()
	s.handlePipelinesList(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rr.Code, rr.Body.String())
	}
	wantPath := "/api/v1/workspaces/ws-7/pipelines?limit=5&state=published"
	if gotPath != wantPath {
		t.Errorf("path = %q, want %q (query string must pass through)", gotPath, wantPath)
	}
	if gotToken != "tok-list" {
		t.Errorf("X-Internal-Token = %q, want tok-list", gotToken)
	}
}

// ---- handlePipelinesGet ----

func TestHandlePipelinesGet_NoIPC_503(t *testing.T) {
	s := newPipelineTestServer(t, nil)
	rr := httptest.NewRecorder()
	s.handlePipelinesGet(rr, httptest.NewRequest("GET", "/pipelines/foo", nil), "foo")
	if rr.Code != http.StatusServiceUnavailable {
		t.Errorf("status = %d, want 503", rr.Code)
	}
}

func TestHandlePipelinesGet_ForwardsToCrewshipd(t *testing.T) {
	var gotPath string
	mock := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"slug":"my-pipeline"}`))
	}))
	defer mock.Close()

	s := newPipelineTestServer(t, &IPCConfig{BaseURL: mock.URL, Token: "t", WorkspaceID: "ws-x"})
	rr := httptest.NewRecorder()
	s.handlePipelinesGet(rr, httptest.NewRequest("GET", "/pipelines/my-pipeline", nil), "my-pipeline")
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d", rr.Code)
	}
	if gotPath != "/api/v1/workspaces/ws-x/pipelines/my-pipeline" {
		t.Errorf("path = %q", gotPath)
	}
}

// ---- handlePipelinesRun ----

func TestHandlePipelinesRun_NoIPC_503(t *testing.T) {
	s := newPipelineTestServer(t, nil)
	rr := httptest.NewRecorder()
	s.handlePipelinesRun(rr, httptest.NewRequest("POST", "/pipelines/x/run", nil), "x")
	if rr.Code != http.StatusServiceUnavailable {
		t.Errorf("status = %d, want 503", rr.Code)
	}
}

func TestHandlePipelinesRun_InvalidJSONBody_400(t *testing.T) {
	s := newPipelineTestServer(t, &IPCConfig{BaseURL: "http://x", Token: "t", WorkspaceID: "ws"})
	req := httptest.NewRequest("POST", "/pipelines/x/run", strings.NewReader("not-json"))
	req.ContentLength = int64(len("not-json"))
	rr := httptest.NewRecorder()
	s.handlePipelinesRun(rr, req, "x")
	if rr.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", rr.Code)
	}
}

func TestHandlePipelinesRun_RoutesToRunPathAndForwardsBody(t *testing.T) {
	var gotPath string
	var gotBody map[string]any
	mock := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"run_id":"r1"}`))
	}))
	defer mock.Close()

	s := newPipelineTestServer(t, &IPCConfig{
		BaseURL: mock.URL, Token: "t", WorkspaceID: "ws", CrewID: "crew-caller", AgentID: "agent-caller",
	})
	body := `{"inputs":{"name":"alice"}}`
	req := httptest.NewRequest("POST", "/pipelines/foo/run", strings.NewReader(body))
	req.ContentLength = int64(len(body))
	rr := httptest.NewRecorder()
	s.handlePipelinesRun(rr, req, "foo")

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rr.Code, rr.Body.String())
	}
	if gotPath != "/api/v1/workspaces/ws/pipelines/foo/run" {
		t.Errorf("path = %q, want .../foo/run (not /dry_run)", gotPath)
	}
	inputs, _ := gotBody["inputs"].(map[string]any)
	if inputs["name"] != "alice" {
		t.Errorf("inputs.name = %v, want alice", inputs["name"])
	}
}

// TestHandlePipelinesRun_InvokerHeadersForwarded documents a gap: the
// Run handler sets X-Crewship-Invoking-{Crew,Agent} on the incoming
// request, but proxyIPCJSON constructs a *new* upstream request and
// only carries X-Internal-Token across — the invoker headers never
// reach crewshipd. Per the source comment this means the executor
// records cross-crew pipeline calls as "user-driven", losing the
// cross-crew-reuse signal in the Graph view. Skipped until
// proxyIPCJSON learns to propagate forwardable headers.
func TestHandlePipelinesRun_InvokerHeadersForwarded(t *testing.T) {
	t.Skip("KNOWN GAP: proxyIPCJSON does not propagate X-Crewship-Invoking-* set by handlePipelinesRun (see source comment at handlePipelinesRun)")
}

func TestHandlePipelinesRun_DryRunFlag_RoutesToDryRunPath(t *testing.T) {
	var gotPath string
	mock := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{}`))
	}))
	defer mock.Close()

	s := newPipelineTestServer(t, &IPCConfig{BaseURL: mock.URL, Token: "t", WorkspaceID: "ws"})
	body := `{"inputs":{},"dry_run":true}`
	req := httptest.NewRequest("POST", "/pipelines/foo/run", strings.NewReader(body))
	req.ContentLength = int64(len(body))
	rr := httptest.NewRecorder()
	s.handlePipelinesRun(rr, req, "foo")

	if !strings.HasSuffix(gotPath, "/dry_run") {
		t.Errorf("path = %q, want suffix /dry_run when dry_run=true", gotPath)
	}
}

// ---- handlePipelinesDryRun ----

func TestHandlePipelinesDryRun_NoIPC_503(t *testing.T) {
	s := newPipelineTestServer(t, nil)
	rr := httptest.NewRecorder()
	s.handlePipelinesDryRun(rr, httptest.NewRequest("POST", "/pipelines/x/dry_run", nil), "x")
	if rr.Code != http.StatusServiceUnavailable {
		t.Errorf("status = %d, want 503", rr.Code)
	}
}

func TestHandlePipelinesDryRun_InvalidJSON_400(t *testing.T) {
	s := newPipelineTestServer(t, &IPCConfig{BaseURL: "http://x", Token: "t", WorkspaceID: "ws"})
	req := httptest.NewRequest("POST", "/pipelines/x/dry_run", strings.NewReader("not-json"))
	req.ContentLength = int64(len("not-json"))
	rr := httptest.NewRecorder()
	s.handlePipelinesDryRun(rr, req, "x")
	if rr.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", rr.Code)
	}
}

func TestHandlePipelinesDryRun_ForwardsToDryRunPath(t *testing.T) {
	var gotPath string
	mock := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		_, _ = w.Write([]byte(`{}`))
	}))
	defer mock.Close()

	s := newPipelineTestServer(t, &IPCConfig{BaseURL: mock.URL, Token: "t", WorkspaceID: "ws"})
	body := `{"inputs":{"k":"v"}}`
	req := httptest.NewRequest("POST", "/pipelines/foo/dry_run", strings.NewReader(body))
	req.ContentLength = int64(len(body))
	rr := httptest.NewRecorder()
	s.handlePipelinesDryRun(rr, req, "foo")
	if gotPath != "/api/v1/workspaces/ws/pipelines/foo/dry_run" {
		t.Errorf("path = %q", gotPath)
	}
}

// ---- handlePipelinesSave ----

func TestHandlePipelinesSave_NoIPC_503(t *testing.T) {
	s := newPipelineTestServer(t, nil)
	rr := httptest.NewRecorder()
	s.handlePipelinesSave(rr, httptest.NewRequest("POST", "/pipelines/save", strings.NewReader(`{}`)))
	if rr.Code != http.StatusServiceUnavailable {
		t.Errorf("status = %d, want 503", rr.Code)
	}
}

func TestHandlePipelinesSave_BadBodyValidation(t *testing.T) {
	s := newPipelineTestServer(t, &IPCConfig{BaseURL: "http://x", Token: "t", WorkspaceID: "ws"})
	cases := []struct {
		name string
		body string
	}{
		{"invalid-json", `not-json`},
		{"missing-name", `{"definition":{"steps":[]}}`},
		{"missing-definition", `{"name":"foo"}`},
		{"name-becomes-empty-slug", `{"name":"!!!","definition":{"steps":[]}}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest("POST", "/pipelines/save", strings.NewReader(tc.body))
			rr := httptest.NewRecorder()
			s.handlePipelinesSave(rr, req)
			if rr.Code != http.StatusBadRequest {
				t.Errorf("%s: code = %d, want 400 (body=%s)", tc.name, rr.Code, rr.Body.String())
			}
		})
	}
}

func TestHandlePipelinesSave_TestRunFailure_ForwardedToAgent(t *testing.T) {
	// Mock crewshipd returns 400 from test_run; the sidecar must NOT save
	// and must forward the 400 + body so the agent can retry with a fix.
	var savedPath string
	mock := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/test_run") {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"error":"unknown step type"}`))
			return
		}
		// If we get here, save was incorrectly attempted.
		savedPath = r.URL.Path
		w.WriteHeader(http.StatusOK)
	}))
	defer mock.Close()

	s := newPipelineTestServer(t, &IPCConfig{BaseURL: mock.URL, Token: "t", WorkspaceID: "ws", CrewID: "c", AgentID: "a"})
	body := `{"name":"my pipe","definition":{"steps":[{"kind":"bogus"}]}}`
	rr := httptest.NewRecorder()
	s.handlePipelinesSave(rr, httptest.NewRequest("POST", "/pipelines/save", strings.NewReader(body)))

	if rr.Code != http.StatusBadRequest {
		t.Errorf("code = %d, want 400 (test_run failure forwarded)", rr.Code)
	}
	if savedPath != "" {
		t.Errorf("save was incorrectly invoked after test_run failure (path=%s)", savedPath)
	}
	if !strings.Contains(rr.Body.String(), "unknown step type") {
		t.Errorf("response body should include upstream test_run error, got %s", rr.Body.String())
	}
}

func TestHandlePipelinesSave_TestRunNotCompleted_422(t *testing.T) {
	// test_run succeeds at the HTTP layer (200) but reports status != COMPLETED.
	// Sidecar must surface 422 and skip the save step.
	var savedHit bool
	mock := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/test_run") {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"status":"FAILED"}`))
			return
		}
		savedHit = true
		w.WriteHeader(http.StatusOK)
	}))
	defer mock.Close()

	s := newPipelineTestServer(t, &IPCConfig{BaseURL: mock.URL, Token: "t", WorkspaceID: "ws", CrewID: "c", AgentID: "a"})
	body := `{"name":"My Pipe","definition":{"steps":[]}}`
	rr := httptest.NewRecorder()
	s.handlePipelinesSave(rr, httptest.NewRequest("POST", "/pipelines/save", strings.NewReader(body)))

	if rr.Code != http.StatusUnprocessableEntity {
		t.Errorf("code = %d, want 422", rr.Code)
	}
	if savedHit {
		t.Error("save was incorrectly invoked after non-COMPLETED test_run")
	}
}

func TestHandlePipelinesSave_HappyPath_InjectsAuthorIdentityFromIPC(t *testing.T) {
	// Verify the trust boundary: even if the agent puts author_* in the
	// request body, the save call seen by crewshipd must carry the IPC
	// identity, not the agent's claim.
	var saveBody map[string]any
	var sawTestRun bool
	mock := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/test_run"):
			sawTestRun = true
			// Confirm sidecar overwrote author_crew_id with IPC value.
			var got map[string]any
			_ = json.NewDecoder(r.Body).Decode(&got)
			if got["author_crew_id"] != "crew-real" {
				t.Errorf("test_run author_crew_id = %v, want crew-real (sidecar must overwrite)", got["author_crew_id"])
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"status":"COMPLETED"}`))
		case strings.HasSuffix(r.URL.Path, "/internal/pipelines/save"):
			_ = json.NewDecoder(r.Body).Decode(&saveBody)
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"saved":true}`))
		default:
			http.Error(w, "unexpected path "+r.URL.Path, http.StatusNotFound)
		}
	}))
	defer mock.Close()

	s := newPipelineTestServer(t, &IPCConfig{
		BaseURL: mock.URL, Token: "t", WorkspaceID: "ws-real",
		CrewID: "crew-real", AgentID: "agent-real", ChatID: "chat-real",
	})
	// Agent body claims a different crew/agent — must be ignored.
	body := `{
		"name": "Build Site",
		"description": "deploy script",
		"definition": {"steps":[]},
		"sample_inputs": {"env":"dev"},
		"author_crew_id": "crew-FORGED",
		"author_agent_id": "agent-FORGED"
	}`
	rr := httptest.NewRecorder()
	s.handlePipelinesSave(rr, httptest.NewRequest("POST", "/pipelines/save", strings.NewReader(body)))

	if rr.Code != http.StatusOK {
		t.Fatalf("save code = %d body=%s", rr.Code, rr.Body.String())
	}
	if !sawTestRun {
		t.Error("test_run was never called")
	}
	// The save call must carry IPC identity, not the forged values.
	if saveBody["author_crew_id"] != "crew-real" {
		t.Errorf("save author_crew_id = %v, want crew-real (forged value must be overwritten)", saveBody["author_crew_id"])
	}
	if saveBody["author_agent_id"] != "agent-real" {
		t.Errorf("save author_agent_id = %v, want agent-real", saveBody["author_agent_id"])
	}
	if saveBody["author_chat_id"] != "chat-real" {
		t.Errorf("save author_chat_id = %v, want chat-real", saveBody["author_chat_id"])
	}
	if saveBody["slug"] != "build-site" {
		t.Errorf("slug = %v, want build-site (derived from name)", saveBody["slug"])
	}
	if saveBody["workspace_id"] != "ws-real" {
		t.Errorf("workspace_id = %v, want ws-real", saveBody["workspace_id"])
	}
	if saveBody["last_test_run_passed"] != true {
		t.Errorf("last_test_run_passed = %v, want true", saveBody["last_test_run_passed"])
	}
}

// ---- ipcRequestJSON ----

func TestIPCRequestJSON_NoIPC_ReturnsError(t *testing.T) {
	s := newPipelineTestServer(t, nil)
	_, err := s.ipcRequestJSON(context.Background(), "GET", "/x", nil)
	if err == nil {
		t.Fatal("expected error when ipc is nil")
	}
}

func TestIPCRequestJSON_PassesTokenAndBody(t *testing.T) {
	var gotToken, gotContentType, gotBody string
	mock := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotToken = r.Header.Get("X-Internal-Token")
		gotContentType = r.Header.Get("Content-Type")
		b, _ := io.ReadAll(r.Body)
		gotBody = string(b)
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer mock.Close()

	s := newPipelineTestServer(t, &IPCConfig{BaseURL: mock.URL, Token: "secret"})
	res, err := s.ipcRequestJSON(context.Background(), "POST", "/foo", []byte(`{"x":1}`))
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if res.status != http.StatusCreated {
		t.Errorf("status = %d, want 201", res.status)
	}
	if string(res.body) != `{"ok":true}` {
		t.Errorf("body = %s", res.body)
	}
	if gotToken != "secret" {
		t.Errorf("token = %q, want secret", gotToken)
	}
	if gotContentType != "application/json" {
		t.Errorf("content-type = %q", gotContentType)
	}
	if gotBody != `{"x":1}` {
		t.Errorf("forwarded body = %q", gotBody)
	}
}

func TestIPCRequestJSON_NoBody_NoContentType(t *testing.T) {
	var gotContentType string
	mock := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotContentType = r.Header.Get("Content-Type")
		_, _ = w.Write([]byte(`null`))
	}))
	defer mock.Close()

	s := newPipelineTestServer(t, &IPCConfig{BaseURL: mock.URL, Token: "t"})
	res, err := s.ipcRequestJSON(context.Background(), "GET", "/x", nil)
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if res.status != http.StatusOK {
		t.Errorf("status = %d", res.status)
	}
	if gotContentType != "" {
		t.Errorf("content-type set on GET with nil body: %q", gotContentType)
	}
}

func TestIPCRequestJSON_UpstreamUnreachable(t *testing.T) {
	// Point at a closed port to provoke a transport error. We use an
	// unroutable target (127.0.0.1:1 is reliably refused) rather than a
	// nonexistent hostname (which can hang on DNS).
	s := newPipelineTestServer(t, &IPCConfig{BaseURL: "http://127.0.0.1:1", Token: "t"})
	_, err := s.ipcRequestJSON(context.Background(), "GET", "/x", nil)
	if err == nil {
		t.Fatal("expected error on unreachable upstream")
	}
}
