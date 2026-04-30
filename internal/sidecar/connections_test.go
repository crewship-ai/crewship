package sidecar

import (
	"bytes"
	"encoding/json"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

// --- extractConnectionSlug (pure) ---

func TestExtractConnectionSlug(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{"/connections/peer-crew/message", "peer-crew"},
		{"/connections/peer-crew/messages", "peer-crew"},
		{"/connections/peer-crew/files", "peer-crew"},
		{"/connections/abc", "abc"},                          // 2 parts after trim
		{"/connections/abc/", "abc"},                         // trailing slash trimmed
		{"connections/peer/message", "peer"},                 // no leading slash
		{"/connections/", ""},                                // only 1 part
		{"/connections", ""},                                 // only 1 part
		{"/other/peer/message", ""},                          // wrong prefix
		{"", ""},                                             // empty
		{"/", ""},                                            // root
		{"/connections/peer/message/extra/segments", "peer"}, // first slug after prefix
	}
	for _, tc := range cases {
		t.Run(tc.in, func(t *testing.T) {
			got := extractConnectionSlug(tc.in)
			if got != tc.want {
				t.Errorf("extractConnectionSlug(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

// --- handleConnectionsList ---

func TestHandleConnectionsList_NoIPC(t *testing.T) {
	srv := newQueryServer(t, nil, nil)
	req := httptest.NewRequest(http.MethodGet, "/connections", nil)
	w := httptest.NewRecorder()

	srv.handleConnectionsList(w, req)

	if w.Code != http.StatusServiceUnavailable {
		t.Errorf("expected 503, got %d", w.Code)
	}
}

func TestHandleConnectionsList_ForwardsWithIDs(t *testing.T) {
	var receivedURL string
	mock := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedURL = r.URL.String()
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`[{"id":"c1","slug":"peer"}]`))
	}))
	defer mock.Close()

	srv := newQueryServer(t, &IPCConfig{
		BaseURL: mock.URL, Token: "tok",
		CrewID: "my-crew", WorkspaceID: "ws-1",
	}, nil)

	req := httptest.NewRequest(http.MethodGet, "/connections", nil)
	w := httptest.NewRecorder()

	srv.handleConnectionsList(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d; body: %s", w.Code, w.Body.String())
	}
	if !strings.Contains(receivedURL, "workspace_id=ws-1") {
		t.Errorf("expected workspace_id=ws-1 in forwarded URL, got %q", receivedURL)
	}
	if !strings.Contains(receivedURL, "crew_id=my-crew") {
		t.Errorf("expected crew_id=my-crew in forwarded URL, got %q", receivedURL)
	}
	if !strings.HasPrefix(receivedURL, "/api/v1/internal/crew-connections") {
		t.Errorf("expected forwarded path /api/v1/internal/crew-connections, got %q", receivedURL)
	}
}

func TestHandleConnectionsList_EscapesQueryParams(t *testing.T) {
	var receivedURL string
	mock := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedURL = r.URL.String()
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`[]`))
	}))
	defer mock.Close()

	// Use values that need escaping (& and space).
	srv := newQueryServer(t, &IPCConfig{
		BaseURL: mock.URL, Token: "tok",
		CrewID: "crew with space", WorkspaceID: "ws&special",
	}, nil)

	req := httptest.NewRequest(http.MethodGet, "/connections", nil)
	w := httptest.NewRecorder()

	srv.handleConnectionsList(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
	// '&' must be encoded as %26 in the value, not break the query string.
	if !strings.Contains(receivedURL, "ws%26special") {
		t.Errorf("expected workspace_id value to be url-escaped, got %q", receivedURL)
	}
	if !strings.Contains(receivedURL, url.QueryEscape("crew with space")) {
		t.Errorf("expected crew_id with space to be url-escaped, got %q", receivedURL)
	}
}

// --- handleConnectionSendMessage ---

func TestHandleConnectionSendMessage_NoIPC(t *testing.T) {
	srv := newQueryServer(t, nil, nil)
	req := httptest.NewRequest(http.MethodPost, "/connections/peer/message",
		strings.NewReader(`{"content":"hi"}`))
	w := httptest.NewRecorder()

	srv.handleConnectionSendMessage(w, req)

	if w.Code != http.StatusServiceUnavailable {
		t.Errorf("expected 503, got %d", w.Code)
	}
}

func TestHandleConnectionSendMessage_MissingSlug(t *testing.T) {
	srv := newQueryServer(t, &IPCConfig{BaseURL: "http://x", Token: "tok"}, nil)
	// Path without slug.
	req := httptest.NewRequest(http.MethodPost, "/connections", strings.NewReader(`{"content":"hi"}`))
	w := httptest.NewRecorder()

	srv.handleConnectionSendMessage(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", w.Code)
	}
	var body map[string]string
	json.NewDecoder(w.Body).Decode(&body)
	if !strings.Contains(body["error"], "slug") {
		t.Errorf("expected error about slug, got %q", body["error"])
	}
}

func TestHandleConnectionSendMessage_InvalidJSON(t *testing.T) {
	srv := newQueryServer(t, &IPCConfig{BaseURL: "http://x", Token: "tok"}, nil)
	req := httptest.NewRequest(http.MethodPost, "/connections/peer/message",
		strings.NewReader(`not-json`))
	w := httptest.NewRecorder()

	srv.handleConnectionSendMessage(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", w.Code)
	}
}

func TestHandleConnectionSendMessage_MissingContent(t *testing.T) {
	srv := newQueryServer(t, &IPCConfig{BaseURL: "http://x", Token: "tok"}, nil)
	req := httptest.NewRequest(http.MethodPost, "/connections/peer/message",
		strings.NewReader(`{}`))
	w := httptest.NewRecorder()

	srv.handleConnectionSendMessage(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", w.Code)
	}
	var body map[string]string
	json.NewDecoder(w.Body).Decode(&body)
	if !strings.Contains(body["error"], "content") {
		t.Errorf("expected error about content, got %q", body["error"])
	}
}

func TestHandleConnectionSendMessage_TargetCrewNotFound(t *testing.T) {
	mock := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Resolver returns empty list — slug won't be matched.
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`[]`))
	}))
	defer mock.Close()

	srv := newQueryServer(t, &IPCConfig{
		BaseURL: mock.URL, Token: "tok",
		CrewID: "me", WorkspaceID: "ws", AgentID: "a1",
	}, nil)

	req := httptest.NewRequest(http.MethodPost, "/connections/missing/message",
		strings.NewReader(`{"content":"hi"}`))
	w := httptest.NewRecorder()

	srv.handleConnectionSendMessage(w, req)

	if w.Code != http.StatusNotFound {
		t.Errorf("expected 404 when target crew not found, got %d", w.Code)
	}
}

func TestHandleConnectionSendMessage_ForwardsWithInjectedIdentity(t *testing.T) {
	var resolveURL string
	var sendBody map[string]interface{}
	mock := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasPrefix(r.URL.Path, "/api/v1/internal/crews"):
			resolveURL = r.URL.String()
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			w.Write([]byte(`[{"id":"target-crew-id","slug":"peer"}]`))
		case r.URL.Path == "/api/v1/internal/crew-messages":
			b, _ := io.ReadAll(r.Body)
			json.Unmarshal(b, &sendBody)
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusCreated)
			w.Write([]byte(`{"message_id":"m1"}`))
		default:
			http.Error(w, "not found", http.StatusNotFound)
		}
	}))
	defer mock.Close()

	srv := newQueryServer(t, &IPCConfig{
		BaseURL: mock.URL, Token: "secret",
		CrewID: "my-crew", WorkspaceID: "ws-1", AgentID: "agent-7",
	}, nil)

	body := `{"content":"hello peer","metadata":{"thread":"t1"}}`
	req := httptest.NewRequest(http.MethodPost, "/connections/peer/message",
		strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	srv.handleConnectionSendMessage(w, req)

	if w.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d; body: %s", w.Code, w.Body.String())
	}
	if !strings.Contains(resolveURL, "workspace_id=ws-1") {
		t.Errorf("expected resolve URL to carry workspace_id=ws-1, got %q", resolveURL)
	}
	if sendBody["from_crew_id"] != "my-crew" {
		t.Errorf("expected from_crew_id injected from IPC, got %v", sendBody["from_crew_id"])
	}
	if sendBody["to_crew_id"] != "target-crew-id" {
		t.Errorf("expected to_crew_id resolved from slug, got %v", sendBody["to_crew_id"])
	}
	if sendBody["from_agent_id"] != "agent-7" {
		t.Errorf("expected from_agent_id from IPC, got %v", sendBody["from_agent_id"])
	}
	if sendBody["workspace_id"] != "ws-1" {
		t.Errorf("expected workspace_id from IPC, got %v", sendBody["workspace_id"])
	}
	// Metadata is forwarded as raw JSON (object).
	if _, ok := sendBody["metadata"]; !ok {
		t.Errorf("expected metadata forwarded, got none")
	}
}

func TestHandleConnectionSendMessage_UnquotesJSONStringContent(t *testing.T) {
	var sendBody map[string]interface{}
	mock := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasPrefix(r.URL.Path, "/api/v1/internal/crews"):
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			w.Write([]byte(`[{"id":"target-id","slug":"peer"}]`))
		case r.URL.Path == "/api/v1/internal/crew-messages":
			b, _ := io.ReadAll(r.Body)
			json.Unmarshal(b, &sendBody)
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			w.Write([]byte(`{}`))
		}
	}))
	defer mock.Close()

	srv := newQueryServer(t, &IPCConfig{
		BaseURL: mock.URL, Token: "tok",
		CrewID: "me", WorkspaceID: "ws", AgentID: "a1",
	}, nil)

	// content is a JSON string (quoted).
	req := httptest.NewRequest(http.MethodPost, "/connections/peer/message",
		strings.NewReader(`{"content":"plain text here"}`))
	w := httptest.NewRecorder()

	srv.handleConnectionSendMessage(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
	// The string should be unquoted: "plain text here", not "\"plain text here\"".
	if sendBody["content"] != "plain text here" {
		t.Errorf("expected content unquoted to plain string, got %v", sendBody["content"])
	}
}

// --- handleConnectionListMessages ---

func TestHandleConnectionListMessages_NoIPC(t *testing.T) {
	srv := newQueryServer(t, nil, nil)
	req := httptest.NewRequest(http.MethodGet, "/connections/peer/messages", nil)
	w := httptest.NewRecorder()

	srv.handleConnectionListMessages(w, req)

	if w.Code != http.StatusServiceUnavailable {
		t.Errorf("expected 503, got %d", w.Code)
	}
}

func TestHandleConnectionListMessages_TargetNotFound(t *testing.T) {
	mock := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`[]`))
	}))
	defer mock.Close()

	srv := newQueryServer(t, &IPCConfig{
		BaseURL: mock.URL, Token: "tok",
		CrewID: "me", WorkspaceID: "ws",
	}, nil)

	req := httptest.NewRequest(http.MethodGet, "/connections/nope/messages", nil)
	w := httptest.NewRecorder()

	srv.handleConnectionListMessages(w, req)

	if w.Code != http.StatusNotFound {
		t.Errorf("expected 404, got %d", w.Code)
	}
}

func TestHandleConnectionListMessages_ForwardsSinceAndLimit(t *testing.T) {
	var receivedURL string
	mock := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasPrefix(r.URL.Path, "/api/v1/internal/crews"):
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			w.Write([]byte(`[{"id":"target-id","slug":"peer"}]`))
		case r.URL.Path == "/api/v1/internal/crew-messages":
			receivedURL = r.URL.String()
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			w.Write([]byte(`[]`))
		}
	}))
	defer mock.Close()

	srv := newQueryServer(t, &IPCConfig{
		BaseURL: mock.URL, Token: "tok",
		CrewID: "my-crew", WorkspaceID: "ws-1",
	}, nil)

	req := httptest.NewRequest(http.MethodGet,
		"/connections/peer/messages?since=2026-01-01T00%3A00%3A00Z&limit=50", nil)
	w := httptest.NewRecorder()

	srv.handleConnectionListMessages(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
	if !strings.Contains(receivedURL, "crew_id=my-crew") {
		t.Errorf("expected crew_id forwarded, got %q", receivedURL)
	}
	if !strings.Contains(receivedURL, "peer_crew_id=target-id") {
		t.Errorf("expected peer_crew_id resolved, got %q", receivedURL)
	}
	if !strings.Contains(receivedURL, "direction=all") {
		t.Errorf("expected direction=all, got %q", receivedURL)
	}
	if !strings.Contains(receivedURL, "since=") {
		t.Errorf("expected since forwarded, got %q", receivedURL)
	}
	if !strings.Contains(receivedURL, "limit=50") {
		t.Errorf("expected limit forwarded, got %q", receivedURL)
	}
}

// --- handleConnectionReadFiles ---

func TestHandleConnectionReadFiles_NoIPC(t *testing.T) {
	srv := newQueryServer(t, nil, nil)
	req := httptest.NewRequest(http.MethodGet, "/connections/peer/files", nil)
	w := httptest.NewRecorder()

	srv.handleConnectionReadFiles(w, req)

	if w.Code != http.StatusServiceUnavailable {
		t.Errorf("expected 503, got %d", w.Code)
	}
}

func TestHandleConnectionReadFiles_DefaultsPath(t *testing.T) {
	var receivedURL string
	mock := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasPrefix(r.URL.Path, "/api/v1/internal/crews"):
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			w.Write([]byte(`[{"id":"target-id","slug":"peer"}]`))
		case strings.HasPrefix(r.URL.Path, "/api/v1/internal/crew-files/"):
			receivedURL = r.URL.String()
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			w.Write([]byte(`{"files":[]}`))
		}
	}))
	defer mock.Close()

	srv := newQueryServer(t, &IPCConfig{
		BaseURL: mock.URL, Token: "tok",
		CrewID: "me", WorkspaceID: "ws",
	}, nil)

	// No ?path= → handler defaults to "."
	req := httptest.NewRequest(http.MethodGet, "/connections/peer/files", nil)
	w := httptest.NewRecorder()

	srv.handleConnectionReadFiles(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
	if !strings.Contains(receivedURL, "path=.") {
		t.Errorf("expected default path=., got %q", receivedURL)
	}
	if !strings.Contains(receivedURL, "/api/v1/internal/crew-files/target-id") {
		t.Errorf("expected target-id in URL path, got %q", receivedURL)
	}
	if !strings.Contains(receivedURL, "requester_crew_id=me") {
		t.Errorf("expected requester_crew_id forwarded, got %q", receivedURL)
	}
}

func TestHandleConnectionReadFiles_StreamsResponseBody(t *testing.T) {
	mock := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasPrefix(r.URL.Path, "/api/v1/internal/crews"):
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			w.Write([]byte(`[{"id":"tid","slug":"peer"}]`))
		default:
			w.Header().Set("X-Custom", "yes")
			w.Header().Set("Content-Type", "application/octet-stream")
			w.WriteHeader(http.StatusOK)
			w.Write([]byte("\x00\x01\x02binary"))
		}
	}))
	defer mock.Close()

	srv := newQueryServer(t, &IPCConfig{
		BaseURL: mock.URL, Token: "tok",
		CrewID: "me", WorkspaceID: "ws",
	}, nil)

	req := httptest.NewRequest(http.MethodGet, "/connections/peer/files?path=foo.bin", nil)
	w := httptest.NewRecorder()

	srv.handleConnectionReadFiles(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
	if !bytes.Equal(w.Body.Bytes(), []byte("\x00\x01\x02binary")) {
		t.Errorf("expected body streamed verbatim, got %q", w.Body.String())
	}
	if w.Header().Get("X-Custom") != "yes" {
		t.Errorf("expected upstream header X-Custom=yes forwarded")
	}
}

// --- handleConnectionWriteFiles ---

func TestHandleConnectionWriteFiles_NoIPC(t *testing.T) {
	srv := newQueryServer(t, nil, nil)
	req := httptest.NewRequest(http.MethodPost, "/connections/peer/files", nil)
	w := httptest.NewRecorder()

	srv.handleConnectionWriteFiles(w, req)

	if w.Code != http.StatusServiceUnavailable {
		t.Errorf("expected 503, got %d", w.Code)
	}
}

func TestHandleConnectionWriteFiles_InvalidMultipart(t *testing.T) {
	mock := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`[{"id":"tid","slug":"peer"}]`))
	}))
	defer mock.Close()

	srv := newQueryServer(t, &IPCConfig{
		BaseURL: mock.URL, Token: "tok",
		CrewID: "me", WorkspaceID: "ws",
	}, nil)

	req := httptest.NewRequest(http.MethodPost, "/connections/peer/files",
		strings.NewReader("not multipart"))
	req.Header.Set("Content-Type", "text/plain")
	w := httptest.NewRecorder()

	srv.handleConnectionWriteFiles(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", w.Code)
	}
}

func TestHandleConnectionWriteFiles_MissingFileField(t *testing.T) {
	mock := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`[{"id":"tid","slug":"peer"}]`))
	}))
	defer mock.Close()

	srv := newQueryServer(t, &IPCConfig{
		BaseURL: mock.URL, Token: "tok",
		CrewID: "me", WorkspaceID: "ws",
	}, nil)

	// Multipart form without "file" field.
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	mw.WriteField("path", "foo.txt")
	mw.Close()

	req := httptest.NewRequest(http.MethodPost, "/connections/peer/files", &buf)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	w := httptest.NewRecorder()

	srv.handleConnectionWriteFiles(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", w.Code)
	}
	var body map[string]string
	json.NewDecoder(w.Body).Decode(&body)
	if !strings.Contains(body["error"], "file field") {
		t.Errorf("expected error about file field, got %q", body["error"])
	}
}

func TestHandleConnectionWriteFiles_ForwardsMultipart(t *testing.T) {
	var receivedRequester, receivedPath string
	var receivedFile []byte
	mock := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasPrefix(r.URL.Path, "/api/v1/internal/crews"):
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			w.Write([]byte(`[{"id":"target-id","slug":"peer"}]`))
		case strings.HasPrefix(r.URL.Path, "/api/v1/internal/crew-files/"):
			r.ParseMultipartForm(10 << 20)
			receivedRequester = r.FormValue("requester_crew_id")
			receivedPath = r.FormValue("path")
			f, _, _ := r.FormFile("file")
			if f != nil {
				receivedFile, _ = io.ReadAll(f)
				f.Close()
			}
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			w.Write([]byte(`{"ok":true}`))
		}
	}))
	defer mock.Close()

	srv := newQueryServer(t, &IPCConfig{
		BaseURL: mock.URL, Token: "tok",
		CrewID: "my-crew", WorkspaceID: "ws-1",
	}, nil)

	// Build multipart body with file + path.
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	mw.WriteField("path", "out/result.txt")
	fw, _ := mw.CreateFormFile("file", "result.txt")
	fw.Write([]byte("hello peer"))
	mw.Close()

	req := httptest.NewRequest(http.MethodPost, "/connections/peer/files", &buf)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	w := httptest.NewRecorder()

	srv.handleConnectionWriteFiles(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d; body: %s", w.Code, w.Body.String())
	}
	if receivedRequester != "my-crew" {
		t.Errorf("expected requester_crew_id=my-crew injected, got %q", receivedRequester)
	}
	if receivedPath != "out/result.txt" {
		t.Errorf("expected path forwarded, got %q", receivedPath)
	}
	if string(receivedFile) != "hello peer" {
		t.Errorf("expected file body forwarded, got %q", string(receivedFile))
	}
}

func TestHandleConnectionWriteFiles_DefaultsPathToFilename(t *testing.T) {
	var receivedPath string
	mock := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasPrefix(r.URL.Path, "/api/v1/internal/crews"):
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			w.Write([]byte(`[{"id":"tid","slug":"peer"}]`))
		case strings.HasPrefix(r.URL.Path, "/api/v1/internal/crew-files/"):
			r.ParseMultipartForm(10 << 20)
			receivedPath = r.FormValue("path")
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			w.Write([]byte(`{}`))
		}
	}))
	defer mock.Close()

	srv := newQueryServer(t, &IPCConfig{
		BaseURL: mock.URL, Token: "tok",
		CrewID: "me", WorkspaceID: "ws",
	}, nil)

	// Multipart without explicit path field — handler must default to filename.
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	fw, _ := mw.CreateFormFile("file", "auto.txt")
	fw.Write([]byte("x"))
	mw.Close()

	req := httptest.NewRequest(http.MethodPost, "/connections/peer/files", &buf)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	w := httptest.NewRecorder()

	srv.handleConnectionWriteFiles(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
	if receivedPath != "auto.txt" {
		t.Errorf("expected path defaulted to filename auto.txt, got %q", receivedPath)
	}
}

// --- resolveCrewIDBySlug (via list endpoint) ---

func TestResolveCrewIDBySlug_NotFoundReturnsEmpty(t *testing.T) {
	mock := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`[{"id":"a","slug":"alpha"},{"id":"b","slug":"beta"}]`))
	}))
	defer mock.Close()

	srv := newQueryServer(t, &IPCConfig{
		BaseURL: mock.URL, Token: "tok", WorkspaceID: "ws",
	}, nil)

	id, err := srv.resolveCrewIDBySlug(httptest.NewRequest(http.MethodGet, "/", nil).Context(), "ghost")
	if err != nil {
		t.Fatalf("expected nil error on miss, got %v", err)
	}
	if id != "" {
		t.Errorf("expected empty id on miss, got %q", id)
	}
}

func TestResolveCrewIDBySlug_FoundReturnsID(t *testing.T) {
	mock := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`[{"id":"a","slug":"alpha"},{"id":"b","slug":"beta"}]`))
	}))
	defer mock.Close()

	srv := newQueryServer(t, &IPCConfig{
		BaseURL: mock.URL, Token: "tok", WorkspaceID: "ws",
	}, nil)

	id, err := srv.resolveCrewIDBySlug(httptest.NewRequest(http.MethodGet, "/", nil).Context(), "beta")
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if id != "b" {
		t.Errorf("expected id=b for slug=beta, got %q", id)
	}
}

func TestResolveCrewIDBySlug_NetworkErrorPropagates(t *testing.T) {
	srv := newQueryServer(t, &IPCConfig{
		BaseURL: "http://127.0.0.1:1", Token: "tok", WorkspaceID: "ws",
	}, nil)

	_, err := srv.resolveCrewIDBySlug(httptest.NewRequest(http.MethodGet, "/", nil).Context(), "x")
	if err == nil {
		t.Errorf("expected network error to propagate, got nil")
	}
}

func TestResolveCrewIDBySlug_InvalidJSONIsError(t *testing.T) {
	mock := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`not json`))
	}))
	defer mock.Close()

	srv := newQueryServer(t, &IPCConfig{
		BaseURL: mock.URL, Token: "tok", WorkspaceID: "ws",
	}, nil)

	_, err := srv.resolveCrewIDBySlug(httptest.NewRequest(http.MethodGet, "/", nil).Context(), "x")
	if err == nil {
		t.Errorf("expected JSON decode error, got nil")
	}
}
