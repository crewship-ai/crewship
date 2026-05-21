package sidecar

import (
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// --- Per-handler NoIPC + forward smoke tests (table-driven) ---

type coordHandlerCase struct {
	name       string
	handler    func(s *Server) http.HandlerFunc
	method     string
	body       string
	wantPath   string // expected forwarded crewshipd path (without query)
	wantQuery  string // substring expected in forwarded query
	wantMethod string // expected forwarded method
}

func coordHandlerCases() []coordHandlerCase {
	return []coordHandlerCase{
		{
			name:       "ListCrews",
			handler:    func(s *Server) http.HandlerFunc { return s.handleListCrews },
			method:     http.MethodGet,
			wantPath:   "/api/v1/internal/crews",
			wantQuery:  "workspace_id=ws-1",
			wantMethod: http.MethodGet,
		},
		{
			name:       "ListCrewConnections",
			handler:    func(s *Server) http.HandlerFunc { return s.handleListCrewConnections },
			method:     http.MethodGet,
			wantPath:   "/api/v1/internal/crew-connections",
			wantQuery:  "workspace_id=ws-1",
			wantMethod: http.MethodGet,
		},
		{
			name:       "ListCredentials",
			handler:    func(s *Server) http.HandlerFunc { return s.handleListCredentials },
			method:     http.MethodGet,
			wantPath:   "/api/v1/internal/credentials",
			wantQuery:  "workspace_id=ws-1",
			wantMethod: http.MethodGet,
		},
		{
			name:       "AssignAgentCredential",
			handler:    func(s *Server) http.HandlerFunc { return s.handleAssignAgentCredential },
			method:     http.MethodPost,
			body:       `{"agent_id":"a1","credential_id":"c1"}`,
			wantPath:   "/api/v1/internal/agent-credentials",
			wantQuery:  "workspace_id=ws-1",
			wantMethod: http.MethodPost,
		},
		{
			name:       "CreateCrewConnection",
			handler:    func(s *Server) http.HandlerFunc { return s.handleCreateCrewConnection },
			method:     http.MethodPost,
			body:       `{"from_crew_id":"a","to_crew_id":"b"}`,
			wantPath:   "/api/v1/internal/crew-connections",
			wantQuery:  "workspace_id=ws-1",
			wantMethod: http.MethodPost,
		},
		{
			name:       "CreateCrew",
			handler:    func(s *Server) http.HandlerFunc { return s.handleCreateCrew },
			method:     http.MethodPost,
			body:       `{"slug":"new-crew","name":"New"}`,
			wantPath:   "/api/v1/internal/crews",
			wantQuery:  "workspace_id=ws-1",
			wantMethod: http.MethodPost,
		},
		{
			name:       "CreateAgent",
			handler:    func(s *Server) http.HandlerFunc { return s.handleCreateAgent },
			method:     http.MethodPost,
			body:       `{"slug":"new-agent","crew_id":"c1"}`,
			wantPath:   "/api/v1/internal/agents",
			wantQuery:  "workspace_id=ws-1",
			wantMethod: http.MethodPost,
		},
	}
}

func TestCoordinatorHandlers_NoIPC(t *testing.T) {
	srv := newQueryServer(t, nil, nil)
	for _, tc := range coordHandlerCases() {
		t.Run(tc.name, func(t *testing.T) {
			var bodyReader io.Reader
			if tc.body != "" {
				bodyReader = strings.NewReader(tc.body)
			}
			req := httptest.NewRequest(tc.method, "/", bodyReader)
			w := httptest.NewRecorder()

			tc.handler(srv).ServeHTTP(w, req)

			if w.Code != http.StatusServiceUnavailable {
				t.Errorf("expected 503, got %d", w.Code)
			}
		})
	}
}

func TestCoordinatorHandlers_Forward(t *testing.T) {
	for _, tc := range coordHandlerCases() {
		t.Run(tc.name, func(t *testing.T) {
			var receivedPath, receivedMethod, receivedToken, receivedCT string
			var receivedBody []byte
			mock := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				receivedPath = r.URL.RequestURI()
				receivedMethod = r.Method
				receivedToken = r.Header.Get("X-Internal-Token")
				receivedCT = r.Header.Get("Content-Type")
				receivedBody, _ = io.ReadAll(r.Body)
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusOK)
				w.Write([]byte(`{"ok":true}`))
			}))
			defer mock.Close()

			srv := newQueryServer(t, &IPCConfig{
				BaseURL: mock.URL, Token: "tok",
				WorkspaceID: "ws-1", CrewID: "c1",
			}, nil)

			var bodyReader io.Reader
			if tc.body != "" {
				bodyReader = strings.NewReader(tc.body)
			}
			req := httptest.NewRequest(tc.method, "/", bodyReader)
			w := httptest.NewRecorder()

			tc.handler(srv).ServeHTTP(w, req)

			if w.Code != http.StatusOK {
				t.Fatalf("expected 200, got %d; body: %s", w.Code, w.Body.String())
			}
			if receivedMethod != tc.wantMethod {
				t.Errorf("forwarded method: got %q, want %q", receivedMethod, tc.wantMethod)
			}
			if !strings.HasPrefix(receivedPath, tc.wantPath) {
				t.Errorf("forwarded path prefix: got %q, want prefix %q", receivedPath, tc.wantPath)
			}
			if !strings.Contains(receivedPath, tc.wantQuery) {
				t.Errorf("forwarded query: got %q, want substring %q", receivedPath, tc.wantQuery)
			}
			if receivedToken != "tok" {
				t.Errorf("X-Internal-Token: got %q, want %q", receivedToken, "tok")
			}
			if tc.body != "" {
				if string(receivedBody) != tc.body {
					t.Errorf("body forwarded verbatim: got %q, want %q", string(receivedBody), tc.body)
				}
				if receivedCT != "application/json" {
					t.Errorf("Content-Type set on body forward: got %q, want application/json", receivedCT)
				}
			} else {
				if len(receivedBody) != 0 {
					t.Errorf("expected no body forwarded for GET, got %q", string(receivedBody))
				}
			}
		})
	}
}

// --- proxyToAPI direct branches ---

func TestProxyToAPI_NoIPC(t *testing.T) {
	srv := newQueryServer(t, nil, nil)
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	w := httptest.NewRecorder()

	srv.proxyToAPI(w, req, http.MethodGet, "/x")

	if w.Code != http.StatusServiceUnavailable {
		t.Errorf("expected 503, got %d", w.Code)
	}
}

func TestProxyToAPI_ForwardsUpstreamStatus(t *testing.T) {
	mock := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusTeapot)
		w.Write([]byte(`{"hint":"short and stout"}`))
	}))
	defer mock.Close()

	srv := newQueryServer(t, &IPCConfig{BaseURL: mock.URL, Token: "tok"}, nil)

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	w := httptest.NewRecorder()

	srv.proxyToAPI(w, req, http.MethodGet, "/api/v1/test")

	if w.Code != http.StatusTeapot {
		t.Errorf("expected upstream 418 forwarded, got %d", w.Code)
	}
	if !strings.Contains(w.Body.String(), `"hint":"short and stout"`) {
		t.Errorf("expected upstream body forwarded, got %q", w.Body.String())
	}
	if w.Header().Get("Content-Type") != "application/json" {
		t.Errorf("expected Content-Type=application/json, got %q", w.Header().Get("Content-Type"))
	}
}

func TestProxyToAPI_GETDoesNotForwardBody(t *testing.T) {
	var receivedBody []byte
	mock := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedBody, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{}`))
	}))
	defer mock.Close()

	srv := newQueryServer(t, &IPCConfig{BaseURL: mock.URL, Token: "tok"}, nil)

	// Even with a body on the inbound request, GET must not forward it.
	req := httptest.NewRequest(http.MethodGet, "/", strings.NewReader(`{"hidden":"body"}`))
	w := httptest.NewRecorder()

	srv.proxyToAPI(w, req, http.MethodGet, "/x")

	if len(receivedBody) != 0 {
		t.Errorf("GET must not forward body, got %q", string(receivedBody))
	}
}

func TestProxyToAPI_PATCHForwardsBody(t *testing.T) {
	var receivedBody []byte
	var receivedCT string
	mock := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedBody, _ = io.ReadAll(r.Body)
		receivedCT = r.Header.Get("Content-Type")
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{}`))
	}))
	defer mock.Close()

	srv := newQueryServer(t, &IPCConfig{BaseURL: mock.URL, Token: "tok"}, nil)

	req := httptest.NewRequest(http.MethodPatch, "/", strings.NewReader(`{"x":1}`))
	w := httptest.NewRecorder()

	srv.proxyToAPI(w, req, http.MethodPatch, "/x")

	if string(receivedBody) != `{"x":1}` {
		t.Errorf("PATCH must forward body, got %q", string(receivedBody))
	}
	if receivedCT != "application/json" {
		t.Errorf("expected Content-Type=application/json on body forward, got %q", receivedCT)
	}
}

func TestProxyToAPI_PUTForwardsBody(t *testing.T) {
	var receivedBody []byte
	mock := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedBody, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{}`))
	}))
	defer mock.Close()

	srv := newQueryServer(t, &IPCConfig{BaseURL: mock.URL, Token: "tok"}, nil)

	req := httptest.NewRequest(http.MethodPut, "/", strings.NewReader(`{"y":2}`))
	w := httptest.NewRecorder()

	srv.proxyToAPI(w, req, http.MethodPut, "/x")

	if string(receivedBody) != `{"y":2}` {
		t.Errorf("PUT must forward body, got %q", string(receivedBody))
	}
}

func TestProxyToAPI_BodyReadFailureReturns400(t *testing.T) {
	srv := newQueryServer(t, &IPCConfig{BaseURL: "http://x", Token: "tok"}, nil)

	req := httptest.NewRequest(http.MethodPost, "/", errReader{})
	w := httptest.NewRecorder()

	srv.proxyToAPI(w, req, http.MethodPost, "/x")

	if w.Code != http.StatusBadRequest {
		t.Errorf("expected 400 on body read failure, got %d", w.Code)
	}
}

// TestProxyToAPI_BodyOverflowReturns413 verifies the maxProxyBodyBytes
// cap fires before the sidecar buffers a runaway body into memory. An
// agent process inside the container could otherwise crash its own
// sidecar by POSTing a multi-GB body — the proxy buffered the whole
// thing via io.ReadAll. Sending one byte past the cap is enough to
// trip http.MaxBytesReader's *http.MaxBytesError.
func TestProxyToAPI_BodyOverflowReturns413(t *testing.T) {
	upstreamHit := false
	mock := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		upstreamHit = true
		w.WriteHeader(http.StatusOK)
	}))
	defer mock.Close()

	srv := newQueryServer(t, &IPCConfig{BaseURL: mock.URL, Token: "tok"}, nil)

	oversized := strings.Repeat("a", maxProxyBodyBytes+1)
	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(oversized))
	w := httptest.NewRecorder()

	srv.proxyToAPI(w, req, http.MethodPost, "/x")

	if w.Code != http.StatusRequestEntityTooLarge {
		t.Errorf("expected 413 on body overflow, got %d (body=%q)", w.Code, w.Body.String())
	}
	if upstreamHit {
		t.Error("upstream must not be reached when body exceeds sidecar cap")
	}
}

func TestProxyToAPI_InvalidUpstreamJSONIsBadGateway(t *testing.T) {
	mock := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`<not-json>`))
	}))
	defer mock.Close()

	srv := newQueryServer(t, &IPCConfig{BaseURL: mock.URL, Token: "tok"}, nil)

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	w := httptest.NewRecorder()

	srv.proxyToAPI(w, req, http.MethodGet, "/x")

	if w.Code != http.StatusBadGateway {
		t.Errorf("expected 502 on invalid upstream JSON, got %d", w.Code)
	}
}

func TestProxyToAPI_UpstreamUnreachable(t *testing.T) {
	srv := newQueryServer(t, &IPCConfig{
		BaseURL: "http://127.0.0.1:1", Token: "tok",
	}, nil)

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	w := httptest.NewRecorder()

	srv.proxyToAPI(w, req, http.MethodGet, "/x")

	if w.Code != http.StatusBadGateway {
		t.Errorf("expected 502 when upstream unreachable, got %d", w.Code)
	}
}

// errReader implements io.Reader returning an error on first read — used to
// trigger the io.ReadAll failure branch in proxyToAPI.
type errReader struct{}

func (errReader) Read(p []byte) (int, error) { return 0, errors.New("synthetic read error") }

// TestHandleListCredentials_StripsAccessTokenFromAgentResponse pins Patch D:
// even if crewshipd regresses and emits plaintext access_token /
// refresh_token / etc., the sidecar's /credentials proxy must hide them
// before the agent process inside the container sees the response.
func TestHandleListCredentials_StripsAccessTokenFromAgentResponse(t *testing.T) {
	// Mock crewshipd that emits a list response WITH plaintext token
	// fields — the scenario Patch D defends against.
	mock := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		// Includes every banned key + some safe metadata fields. The
		// scrubber must remove only the banned ones.
		w.Write([]byte(`[
			{
				"id": "cred-1",
				"name": "anthropic-prod",
				"type": "API_KEY",
				"provider": "ANTHROPIC",
				"status": "ACTIVE",
				"access_token": "sk-ant-LEAK-1234567890",
				"refresh_token": "rt-LEAK-abcdef",
				"encrypted_value": "v1:should-not-leave-host",
				"token": "another-leak",
				"secret": "yet-another-leak"
			},
			{
				"id": "cred-2",
				"name": "openai-prod",
				"type": "API_KEY",
				"provider": "OPENAI",
				"status": "ACTIVE",
				"plain_value": "sk-PLAIN-9876"
			}
		]`))
	}))
	defer mock.Close()

	srv := newQueryServer(t, &IPCConfig{
		BaseURL:     mock.URL,
		Token:       "tok",
		WorkspaceID: "ws-1",
	}, nil)

	req := httptest.NewRequest(http.MethodGet, "/credentials", nil)
	w := httptest.NewRecorder()
	srv.handleListCredentials(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%s", w.Code, w.Body.String())
	}
	body := w.Body.String()

	// Every banned literal MUST be gone.
	bannedLiterals := []string{
		"sk-ant-LEAK-1234567890",
		"rt-LEAK-abcdef",
		"v1:should-not-leave-host",
		"another-leak",
		"yet-another-leak",
		"sk-PLAIN-9876",
		`"access_token"`,
		`"refresh_token"`,
		`"encrypted_value"`,
		`"token"`,
		`"secret"`,
		`"plain_value"`,
	}
	for _, lit := range bannedLiterals {
		if strings.Contains(body, lit) {
			t.Errorf("response leaks %q to agent\nbody: %s", lit, body)
		}
	}

	// Metadata fields the LEAD discovery flow legitimately needs MUST
	// survive — id / name / provider / type / status.
	keptLiterals := []string{`"cred-1"`, `"anthropic-prod"`, `"ANTHROPIC"`, `"ACTIVE"`, `"cred-2"`, `"openai-prod"`}
	for _, lit := range keptLiterals {
		if !strings.Contains(body, lit) {
			t.Errorf("response missing required metadata %q\nbody: %s", lit, body)
		}
	}
}

// TestStripCredentialValues_ObjectShape covers non-array crewshipd response
// shapes (error envelopes, single-object responses) so the scrubber never
// blindly passes through and assumes "this is JSON I don't recognise, must
// be safe". It still scrubs banned keys inside an object.
func TestStripCredentialValues_ObjectShape(t *testing.T) {
	// Object shape with a banned key at the top level.
	in := []byte(`{"id":"x","access_token":"LEAK","extra":{"refresh_token":"LEAK2"}}`)
	out := stripCredentialValues(in)
	s := string(out)
	if strings.Contains(s, "LEAK") {
		t.Errorf("object-shape scrubber leaked: %s", s)
	}
	if !strings.Contains(s, `"id":"x"`) {
		t.Errorf("object-shape scrubber dropped legitimate field: %s", s)
	}
}

// TestStripCredentialValues_GarbagePassthrough — non-JSON or malformed
// JSON should pass through unchanged. The sidecar would have already
// returned a 502 for those cases via the upstream-decode branch in
// proxyToAPIFiltered, but the scrubber itself must be a safe no-op.
func TestStripCredentialValues_GarbagePassthrough(t *testing.T) {
	in := []byte(`<not-json>`)
	out := stripCredentialValues(in)
	if string(out) != string(in) {
		t.Errorf("garbage input was modified: got %q, want %q", string(out), string(in))
	}
}

// TestBuildHandler_CrossCrewBypassRejected pins Patch E: an attacker on
// the shared crew bridge who reaches a peer sidecar's TCP port and spoofs
// `Host: localhost:9119` via curl --resolve must NOT hit /credentials —
// the underlying TCP source IP gates the control plane independently.
//
// The pre-Patch-E gate was `isLocalhost(r.Host)` only, which a peer
// agent on the same Docker bridge could pass with a spoofed Host header.
// Post-Patch-E both have to agree: Host header parses as localhost AND
// RemoteAddr is loopback.
func TestBuildHandler_CrossCrewBypassRejected(t *testing.T) {
	// Mock upstream that should NEVER be called (test fails if it is).
	mockHit := false
	mock := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mockHit = true
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`[]`))
	}))
	defer mock.Close()

	srv := newQueryServer(t, &IPCConfig{
		BaseURL:     mock.URL,
		Token:       "tok",
		WorkspaceID: "ws-1",
	}, nil)
	handler := srv.buildHandler(srv.proxy)

	// Attacker request: Host header spoofed to localhost, but the TCP
	// source is a Docker bridge IP (172.18.0.5) — i.e. a peer crew's
	// agent reaching across the shared bridge.
	req := httptest.NewRequest(http.MethodGet, "http://localhost:9119/credentials", nil)
	req.Host = "localhost:9119"
	req.RemoteAddr = "172.18.0.5:54321"

	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	// The request falls through to the forward proxy because the
	// control-plane gate refused it. The proxy then 403s on the
	// non-allowed domain (or 502s trying to reach it) — either way the
	// /credentials handler never ran and upstream crewshipd was not
	// contacted.
	if mockHit {
		t.Fatalf("PATCH-E REGRESSION: peer-crew curl --resolve reached /credentials handler")
	}
	if w.Code == http.StatusOK {
		t.Errorf("expected refusal status, got 200")
	}
}
