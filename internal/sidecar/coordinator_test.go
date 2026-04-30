package sidecar

import (
	"encoding/json"
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
			name:       "CreateProposal",
			handler:    func(s *Server) http.HandlerFunc { return s.handleCreateProposal },
			method:     http.MethodPost,
			body:       `{"title":"Migrate auth"}`,
			wantPath:   "/api/v1/internal/mission-proposals",
			wantQuery:  "workspace_id=ws-1",
			wantMethod: http.MethodPost,
		},
		{
			name:       "ListProposals",
			handler:    func(s *Server) http.HandlerFunc { return s.handleListProposals },
			method:     http.MethodGet,
			wantPath:   "/api/v1/internal/mission-proposals",
			wantQuery:  "workspace_id=ws-1",
			wantMethod: http.MethodGet,
		},
		{
			name:       "ListAllMissions",
			handler:    func(s *Server) http.HandlerFunc { return s.handleListAllMissions },
			method:     http.MethodGet,
			wantPath:   "/api/v1/missions",
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

// --- handleAllMissionsSummary ---

func TestHandleAllMissionsSummary_NoIPC(t *testing.T) {
	srv := newQueryServer(t, nil, nil)
	req := httptest.NewRequest(http.MethodGet, "/missions/summary", nil)
	w := httptest.NewRecorder()

	srv.handleAllMissionsSummary(w, req)

	if w.Code != http.StatusServiceUnavailable {
		t.Errorf("expected 503, got %d", w.Code)
	}
}

func TestHandleAllMissionsSummary_AggregatesCounts(t *testing.T) {
	mock := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Sanity: handler hits /api/v1/missions with workspace_id and limit=100.
		if r.URL.Path != "/api/v1/missions" {
			http.Error(w, "wrong path", http.StatusBadRequest)
			return
		}
		if !strings.Contains(r.URL.RawQuery, "workspace_id=ws-1") {
			http.Error(w, "missing workspace_id", http.StatusBadRequest)
			return
		}
		if !strings.Contains(r.URL.RawQuery, "limit=100") {
			http.Error(w, "missing limit", http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`[
			{"status":"PLANNING"},
			{"status":"PLANNING"},
			{"status":"IN_PROGRESS"},
			{"status":"REVIEW"},
			{"status":"COMPLETED"},
			{"status":"COMPLETED"},
			{"status":"COMPLETED"},
			{"status":"FAILED"},
			{"status":"CANCELLED"},
			{"status":"WEIRD_STATUS"}
		]`))
	}))
	defer mock.Close()

	srv := newQueryServer(t, &IPCConfig{
		BaseURL: mock.URL, Token: "tok", WorkspaceID: "ws-1",
	}, nil)

	req := httptest.NewRequest(http.MethodGet, "/missions/summary", nil)
	w := httptest.NewRecorder()

	srv.handleAllMissionsSummary(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d; body: %s", w.Code, w.Body.String())
	}
	var got map[string]int
	if err := json.NewDecoder(w.Body).Decode(&got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	wantPairs := map[string]int{
		"total":       10, // includes the unknown status
		"planning":    2,
		"in_progress": 1,
		"review":      1,
		"completed":   3,
		"failed":      1,
		"cancelled":   1,
	}
	for k, want := range wantPairs {
		if got[k] != want {
			t.Errorf("summary[%q] = %d, want %d", k, got[k], want)
		}
	}
}

func TestHandleAllMissionsSummary_EmptyList(t *testing.T) {
	mock := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`[]`))
	}))
	defer mock.Close()

	srv := newQueryServer(t, &IPCConfig{BaseURL: mock.URL, Token: "tok", WorkspaceID: "ws"}, nil)

	req := httptest.NewRequest(http.MethodGet, "/missions/summary", nil)
	w := httptest.NewRecorder()

	srv.handleAllMissionsSummary(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
	var got map[string]int
	json.NewDecoder(w.Body).Decode(&got)
	if got["total"] != 0 {
		t.Errorf("expected total=0 for empty list, got %d", got["total"])
	}
	// All status buckets present even when zero.
	for _, k := range []string{"planning", "in_progress", "review", "completed", "failed", "cancelled"} {
		if _, ok := got[k]; !ok {
			t.Errorf("expected key %q present in summary even at zero", k)
		}
	}
}

func TestHandleAllMissionsSummary_UpstreamUnreachable(t *testing.T) {
	srv := newQueryServer(t, &IPCConfig{
		BaseURL: "http://127.0.0.1:1", Token: "tok", WorkspaceID: "ws",
	}, nil)

	req := httptest.NewRequest(http.MethodGet, "/missions/summary", nil)
	w := httptest.NewRecorder()

	srv.handleAllMissionsSummary(w, req)

	if w.Code != http.StatusBadGateway {
		t.Errorf("expected 502 on unreachable upstream, got %d", w.Code)
	}
}

func TestHandleAllMissionsSummary_InvalidUpstreamJSON(t *testing.T) {
	mock := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`not-json-array`))
	}))
	defer mock.Close()

	srv := newQueryServer(t, &IPCConfig{BaseURL: mock.URL, Token: "tok", WorkspaceID: "ws"}, nil)

	req := httptest.NewRequest(http.MethodGet, "/missions/summary", nil)
	w := httptest.NewRecorder()

	srv.handleAllMissionsSummary(w, req)

	if w.Code != http.StatusBadGateway {
		t.Errorf("expected 502 on invalid upstream JSON, got %d", w.Code)
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
