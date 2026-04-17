package sidecar

import (
	"bytes"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func newPortExposeServer(t *testing.T, ipc *IPCConfig) *Server {
	t.Helper()
	return NewServer(ServerConfig{
		Addr:   "127.0.0.1:0",
		Logger: slog.Default(),
		IPC:    ipc,
	})
}

func postExposeBody(body string) *http.Request {
	return httptest.NewRequest(http.MethodPost, "/expose-port", strings.NewReader(body))
}

func decodeErr(t *testing.T, w *httptest.ResponseRecorder) string {
	t.Helper()
	var out map[string]string
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode: %v (body=%s)", err, w.Body.String())
	}
	return out["error"]
}

func TestHandleExposePort_NoIPC(t *testing.T) {
	t.Parallel()
	srv := newPortExposeServer(t, nil)

	w := httptest.NewRecorder()
	srv.handleExposePort(w, postExposeBody(`{"port":8080,"description":"ok"}`))

	if w.Code != http.StatusServiceUnavailable {
		t.Errorf("code: got %d want 503", w.Code)
	}
	if !strings.Contains(decodeErr(t, w), "not configured") {
		t.Errorf("error body: %s", w.Body.String())
	}
}

func TestHandleExposePort_InvalidJSON(t *testing.T) {
	t.Parallel()
	srv := newPortExposeServer(t, &IPCConfig{BaseURL: "http://x", Token: "tok"})

	w := httptest.NewRecorder()
	srv.handleExposePort(w, postExposeBody(`not-json`))

	if w.Code != http.StatusBadRequest {
		t.Errorf("code: got %d want 400", w.Code)
	}
	if !strings.Contains(decodeErr(t, w), "invalid JSON") {
		t.Errorf("error body: %s", w.Body.String())
	}
}

func TestHandleExposePort_PortRange(t *testing.T) {
	t.Parallel()
	srv := newPortExposeServer(t, &IPCConfig{BaseURL: "http://x", Token: "tok"})

	cases := []struct {
		name string
		port int
	}{
		{"zero", 0},
		{"negative", -1},
		{"too_large", 65536},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			body, _ := json.Marshal(map[string]any{"port": tc.port, "description": "ok"})
			w := httptest.NewRecorder()
			srv.handleExposePort(w, postExposeBody(string(body)))

			if w.Code != http.StatusBadRequest {
				t.Errorf("port=%d: code %d want 400", tc.port, w.Code)
			}
			if !strings.Contains(decodeErr(t, w), "port must be between") {
				t.Errorf("port=%d: body %s", tc.port, w.Body.String())
			}
		})
	}
}

func TestHandleExposePort_DescriptionTooLong(t *testing.T) {
	t.Parallel()
	srv := newPortExposeServer(t, &IPCConfig{BaseURL: "http://x", Token: "tok"})

	longDesc := strings.Repeat("a", 201)
	body, _ := json.Marshal(map[string]any{"port": 8080, "description": longDesc})
	w := httptest.NewRecorder()
	srv.handleExposePort(w, postExposeBody(string(body)))

	if w.Code != http.StatusBadRequest {
		t.Errorf("code: got %d want 400", w.Code)
	}
	if !strings.Contains(decodeErr(t, w), "too long") {
		t.Errorf("error body: %s", w.Body.String())
	}
}

// Description must reject control characters so a malicious agent cannot
// smuggle headers, terminal escapes, or log-injection content through the
// audit trail (server re-validates but fast-fail gives a cleaner 400).
func TestHandleExposePort_DescriptionForbiddenChars(t *testing.T) {
	t.Parallel()
	srv := newPortExposeServer(t, &IPCConfig{BaseURL: "http://x", Token: "tok"})

	cases := []struct {
		name string
		desc string
	}{
		{"null_byte", "bad\x00desc"},
		{"newline", "bad\ndesc"},
		{"carriage_return", "bad\rdesc"},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			body, _ := json.Marshal(map[string]any{"port": 8080, "description": tc.desc})
			w := httptest.NewRecorder()
			srv.handleExposePort(w, postExposeBody(string(body)))

			if w.Code != http.StatusBadRequest {
				t.Errorf("code: got %d want 400", w.Code)
			}
			if !strings.Contains(decodeErr(t, w), "forbidden characters") {
				t.Errorf("error body: %s", w.Body.String())
			}
		})
	}
}

func TestHandleExposePort_TTLRange(t *testing.T) {
	t.Parallel()
	srv := newPortExposeServer(t, &IPCConfig{BaseURL: "http://x", Token: "tok"})

	cases := []struct {
		name string
		ttl  int
	}{
		{"negative", -1},
		{"above_24h", 24*60*60 + 1},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			body, _ := json.Marshal(map[string]any{
				"port":        8080,
				"description": "ok",
				"ttl_seconds": tc.ttl,
			})
			w := httptest.NewRecorder()
			srv.handleExposePort(w, postExposeBody(string(body)))

			if w.Code != http.StatusBadRequest {
				t.Errorf("ttl=%d: code %d want 400", tc.ttl, w.Code)
			}
			if !strings.Contains(decodeErr(t, w), "ttl_seconds must be between") {
				t.Errorf("ttl=%d: body %s", tc.ttl, w.Body.String())
			}
		})
	}
}

// Success path: the handler must forward the crewshipd response verbatim and
// inject the IPC-scoped context ids (workspace/crew/agent/container) so the
// agent cannot spoof its own identity.
func TestHandleExposePort_SuccessForwardsAndInjectsContext(t *testing.T) {
	t.Parallel()

	var capturedPath string
	var capturedAuth string
	var capturedBody map[string]any

	mock := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		capturedPath = r.URL.Path
		capturedAuth = r.Header.Get("X-Internal-Token")
		defer r.Body.Close()
		b, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(b, &capturedBody)

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"token":"tok-1","url":"https://ex.example/xyz","expires_at":"2026-04-17T12:00:00Z"}`))
	}))
	defer mock.Close()

	srv := newPortExposeServer(t, &IPCConfig{
		BaseURL:     mock.URL,
		Token:       "secret-internal",
		WorkspaceID: "ws-1",
		CrewID:      "crew-eng",
		AgentID:     "agent-lucie",
		ContainerID: "container-abc",
		ChatID:      "chat-7",
	})

	reqBody, _ := json.Marshal(map[string]any{
		"port":        8080,
		"description": "grafana probe",
		"ttl_seconds": 3600,
	})
	w := httptest.NewRecorder()
	srv.handleExposePort(w, httptest.NewRequest(http.MethodPost, "/expose-port", bytes.NewReader(reqBody)))

	if w.Code != http.StatusCreated {
		t.Fatalf("code: got %d want 201 (body=%s)", w.Code, w.Body.String())
	}

	// Forwarded response body is passed through verbatim.
	var resp map[string]string
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if resp["token"] != "tok-1" || resp["url"] != "https://ex.example/xyz" {
		t.Errorf("response not forwarded verbatim: %v", resp)
	}

	// IPC request was authenticated with the internal token.
	if capturedAuth != "secret-internal" {
		t.Errorf("X-Internal-Token: got %q want %q", capturedAuth, "secret-internal")
	}
	// Correct IPC endpoint.
	if capturedPath != "/api/v1/internal/port-expose" {
		t.Errorf("path: got %q", capturedPath)
	}
	// Context ids are injected from s.ipc, not from agent payload.
	for _, want := range []struct {
		key string
		val any
	}{
		{"workspace_id", "ws-1"},
		{"crew_id", "crew-eng"},
		{"agent_id", "agent-lucie"},
		{"container_id", "container-abc"},
		{"chat_id", "chat-7"},
		{"description", "grafana probe"},
		{"port", float64(8080)},       // JSON numbers decode to float64.
		{"ttl_seconds", float64(3600)}, // included only because caller set non-zero.
	} {
		got := capturedBody[want.key]
		if got != want.val {
			t.Errorf("ipc body[%q]: got %v (%T) want %v", want.key, got, got, want.val)
		}
	}
}

func TestHandleExposePort_OmitsOptionalsWhenZeroOrEmpty(t *testing.T) {
	t.Parallel()

	var capturedBody map[string]any
	mock := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(b, &capturedBody)
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{}`))
	}))
	defer mock.Close()

	srv := newPortExposeServer(t, &IPCConfig{
		BaseURL:     mock.URL,
		Token:       "tok",
		WorkspaceID: "ws-1",
		CrewID:      "crew-1",
		AgentID:     "a-1",
		ContainerID: "c-1",
		// ChatID intentionally empty.
	})

	reqBody, _ := json.Marshal(map[string]any{
		"port":        9000,
		"description": "no ttl",
		// ttl_seconds omitted.
	})
	w := httptest.NewRecorder()
	srv.handleExposePort(w, httptest.NewRequest(http.MethodPost, "/expose-port", bytes.NewReader(reqBody)))

	if _, ok := capturedBody["chat_id"]; ok {
		t.Errorf("chat_id should be omitted when empty; body=%v", capturedBody)
	}
	if _, ok := capturedBody["ttl_seconds"]; ok {
		t.Errorf("ttl_seconds should be omitted when zero; body=%v", capturedBody)
	}
}

func TestHandleExposePort_UpstreamFailureBadGateway(t *testing.T) {
	t.Parallel()

	// Close immediately so Do() returns an error.
	mock := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	mock.Close()

	srv := newPortExposeServer(t, &IPCConfig{
		BaseURL:     mock.URL,
		Token:       "tok",
		WorkspaceID: "ws-1",
		CrewID:      "crew-1",
		AgentID:     "a-1",
	})

	reqBody, _ := json.Marshal(map[string]any{"port": 8080, "description": "x"})
	w := httptest.NewRecorder()
	srv.handleExposePort(w, httptest.NewRequest(http.MethodPost, "/expose-port", bytes.NewReader(reqBody)))

	if w.Code != http.StatusBadGateway {
		t.Errorf("code: got %d want 502", w.Code)
	}
	if !strings.Contains(decodeErr(t, w), "crewshipd unreachable") {
		t.Errorf("error body: %s", w.Body.String())
	}
}

// Description is trimmed before length check, so a payload that is long only
// due to surrounding whitespace should succeed rather than 400.
func TestHandleExposePort_DescriptionTrimmedBeforeLengthCheck(t *testing.T) {
	t.Parallel()

	var capturedBody map[string]any
	mock := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(b, &capturedBody)
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{}`))
	}))
	defer mock.Close()

	srv := newPortExposeServer(t, &IPCConfig{
		BaseURL:     mock.URL,
		Token:       "tok",
		WorkspaceID: "ws-1",
		CrewID:      "crew-1",
		AgentID:     "a-1",
	})

	// 195 "a" chars + 10 spaces on each side = 215 total; trimmed to 195 (<= 200).
	padded := strings.Repeat(" ", 10) + strings.Repeat("a", 195) + strings.Repeat(" ", 10)
	reqBody, _ := json.Marshal(map[string]any{"port": 8080, "description": padded})
	w := httptest.NewRecorder()
	srv.handleExposePort(w, httptest.NewRequest(http.MethodPost, "/expose-port", bytes.NewReader(reqBody)))

	if w.Code != http.StatusCreated {
		t.Fatalf("code: got %d want 201 (body=%s)", w.Code, w.Body.String())
	}
	if got, _ := capturedBody["description"].(string); got != strings.Repeat("a", 195) {
		t.Errorf("description was not trimmed: got %q", got)
	}
}

// Security boundary: even if a malicious agent stuffs spoofed identity ids
// into the request body, the bridge MUST overwrite them with values from
// s.ipc. The agent container is untrusted; the only trusted source of
// workspace/crew/agent/container/chat ids is the IPC config injected at
// sidecar startup by crewshipd.
func TestHandleExposePort_AgentSuppliedIdsAreIgnored(t *testing.T) {
	t.Parallel()

	var capturedBody map[string]any
	mock := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(b, &capturedBody)
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{}`))
	}))
	defer mock.Close()

	srv := newPortExposeServer(t, &IPCConfig{
		BaseURL:     mock.URL,
		Token:       "tok",
		WorkspaceID: "ws-trusted",
		CrewID:      "crew-trusted",
		AgentID:     "agent-trusted",
		ContainerID: "container-trusted",
		ChatID:      "chat-trusted",
	})

	// Agent sends a payload that ALSO contains identity ids — a privilege
	// escalation attempt. These must be silently dropped (json decoder
	// ignores unknown fields on exposePortRequestBody).
	reqBody, _ := json.Marshal(map[string]any{
		"port":         8080,
		"description":  "spoof attempt",
		"workspace_id": "ws-EVIL",
		"crew_id":      "crew-EVIL",
		"agent_id":     "agent-EVIL",
		"container_id": "container-EVIL",
		"chat_id":      "chat-EVIL",
	})
	w := httptest.NewRecorder()
	srv.handleExposePort(w, httptest.NewRequest(http.MethodPost, "/expose-port", bytes.NewReader(reqBody)))

	if w.Code != http.StatusCreated {
		t.Fatalf("code: got %d want 201 (body=%s)", w.Code, w.Body.String())
	}
	for _, want := range []struct {
		key string
		val string
	}{
		{"workspace_id", "ws-trusted"},
		{"crew_id", "crew-trusted"},
		{"agent_id", "agent-trusted"},
		{"container_id", "container-trusted"},
		{"chat_id", "chat-trusted"},
	} {
		got, _ := capturedBody[want.key].(string)
		if got != want.val {
			t.Errorf("ipc body[%q]: got %q want %q (agent spoof leaked through)",
				want.key, got, want.val)
		}
	}
}
