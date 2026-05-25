package sidecar

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestRoutineScheduleCreate_Forwards asserts the slash-action route
// translates into the right internal API path and carries
// X-Caller-User-Id through to the upstream — that header is what
// the backend's dual-path handler uses to choose the capability
// gate over the autonomy gate.
func TestRoutineScheduleCreate_Forwards(t *testing.T) {
	var seenPath, seenMethod, seenCaller string
	mock := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seenPath = r.URL.Path
		seenMethod = r.Method
		seenCaller = r.Header.Get("X-Caller-User-Id")
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"id":"sched_1"}`))
	}))
	defer mock.Close()

	srv := newQueryServer(t, &IPCConfig{BaseURL: mock.URL, Token: "tok", WorkspaceID: "ws-1"}, nil)

	req := httptest.NewRequest(http.MethodPost, "/routines/schedules/create",
		strings.NewReader(`{"name":"nightly","target_pipeline_slug":"x","cron_expr":"0 7 * * *","timezone":"UTC"}`))
	req.Header.Set("X-Caller-User-Id", "ludmila")
	w := httptest.NewRecorder()
	srv.handleRoutineScheduleCreate(w, req)

	if w.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201", w.Code)
	}
	if seenPath != "/api/v1/internal/routines/schedules" {
		t.Errorf("upstream path = %q", seenPath)
	}
	if seenMethod != http.MethodPost {
		t.Errorf("upstream method = %q", seenMethod)
	}
	if seenCaller != "ludmila" {
		t.Errorf("X-Caller-User-Id = %q, want ludmila — slash-action MUST carry user attribution", seenCaller)
	}
}

// TestRoutineScheduleCreate_NoIPC: sidecar must 503 when IPC not
// configured rather than panic. Same defensive shape as /spawn.
func TestRoutineScheduleCreate_NoIPC(t *testing.T) {
	srv := newQueryServer(t, nil, nil)
	req := httptest.NewRequest(http.MethodPost, "/routines/schedules/create", strings.NewReader(`{}`))
	w := httptest.NewRecorder()
	srv.handleRoutineScheduleCreate(w, req)
	if w.Code != http.StatusServiceUnavailable {
		t.Errorf("status = %d, want 503", w.Code)
	}
}

// TestSkillGenerate_Forwards mirrors the routine smoke — the LLM
// authoring path lands on the internal mirror.
func TestSkillGenerate_Forwards(t *testing.T) {
	var seenPath string
	mock := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seenPath = r.URL.Path
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"skill_id":"sk_1"}`))
	}))
	defer mock.Close()

	srv := newQueryServer(t, &IPCConfig{BaseURL: mock.URL, Token: "tok", WorkspaceID: "ws-1"}, nil)

	req := httptest.NewRequest(http.MethodPost, "/skills/generate",
		strings.NewReader(`{"slug":"x","prompt":"Use when ..."}`))
	req.Header.Set("X-Caller-User-Id", "ludmila")
	w := httptest.NewRecorder()
	srv.handleSkillGenerate(w, req)

	if w.Code != http.StatusCreated {
		t.Fatalf("status = %d", w.Code)
	}
	if seenPath != "/api/v1/internal/skills/generate" {
		t.Errorf("upstream path = %q", seenPath)
	}
}

// TestCredentialCreate_Forwards covers the upstream landing.
func TestCredentialCreate_Forwards(t *testing.T) {
	var seenPath, seenCaller string
	mock := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seenPath = r.URL.Path
		seenCaller = r.Header.Get("X-Caller-User-Id")
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{}`))
	}))
	defer mock.Close()

	srv := newQueryServer(t, &IPCConfig{BaseURL: mock.URL, Token: "tok", WorkspaceID: "ws-1"}, nil)

	req := httptest.NewRequest(http.MethodPost, "/credentials/create",
		strings.NewReader(`{"name":"x","type":"SECRET","value":"v"}`))
	req.Header.Set("X-Caller-User-Id", "pavel")
	w := httptest.NewRecorder()
	srv.handleCredentialCreate(w, req)

	if w.Code != http.StatusCreated {
		t.Fatalf("status = %d", w.Code)
	}
	if seenPath != "/api/v1/internal/credentials" {
		t.Errorf("upstream path = %q", seenPath)
	}
	if seenCaller != "pavel" {
		t.Errorf("X-Caller-User-Id = %q", seenCaller)
	}
}

// TestCredentialRotate_PreservesCredentialID asserts the {credentialId}
// path segment from the inbound URL is stitched onto the internal
// URL — a sidecar that dropped or mangled this would land the
// rotate call on the wrong row.
func TestCredentialRotate_PreservesCredentialID(t *testing.T) {
	var seenPath string
	mock := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seenPath = r.URL.Path
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{}`))
	}))
	defer mock.Close()

	srv := newQueryServer(t, &IPCConfig{BaseURL: mock.URL, Token: "tok", WorkspaceID: "ws-1"}, nil)

	req := httptest.NewRequest(http.MethodPost, "/credentials/cred_abc/rotate",
		strings.NewReader(`{"value":"new"}`))
	req.Header.Set("X-Caller-User-Id", "pavel")
	w := httptest.NewRecorder()
	srv.handleCredentialRotate(w, req)

	if w.Code != http.StatusOK {
		body, _ := io.ReadAll(w.Body)
		t.Fatalf("status = %d, body = %s", w.Code, string(body))
	}
	want := "/api/v1/internal/credentials/cred_abc/rotate"
	if seenPath != want {
		t.Errorf("upstream path = %q, want %q", seenPath, want)
	}
}

// TestCredentialRotate_RejectsMalformedPath: a path with a slash or
// querystring char in the would-be credentialId is a sidecar caller
// bug. We reject with 400 rather than forward something the backend
// would have to figure out.
func TestCredentialRotate_RejectsMalformedPath(t *testing.T) {
	srv := newQueryServer(t, &IPCConfig{BaseURL: "http://x", Token: "tok", WorkspaceID: "ws-1"}, nil)

	cases := []string{
		"/credentials//rotate",          // empty id
		"/credentials/a/b/rotate",       // extra segment
		"/credentials/foo?bar=1/rotate", // querystring smuggled into id
	}
	for _, p := range cases {
		t.Run(p, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, p, strings.NewReader(`{}`))
			w := httptest.NewRecorder()
			srv.handleCredentialRotate(w, req)
			if w.Code != http.StatusBadRequest {
				t.Errorf("path %q: status = %d, want 400", p, w.Code)
			}
		})
	}
}
