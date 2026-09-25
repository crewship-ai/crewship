package sidecar

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestCrewOnlySidecarAllowsTelemetryAndBlocksOtherIPC(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/internal/crews/telemetry" {
			t.Errorf("unexpected upstream path %s", r.URL.Path)
		}
		_, _ = io.WriteString(w, `{"crews":[]}`)
	}))
	defer upstream.Close()
	s := NewServer(ServerConfig{IPC: &IPCConfig{CrewOnly: true, BaseURL: upstream.URL, Token: "test", WorkspaceID: "ws1"}, Logger: slog.New(slog.NewTextHandler(io.Discard, nil))})
	h := s.buildHandler(nil)
	for _, tc := range []struct {
		method, path string
		want         int
	}{
		{http.MethodGet, "/crews/telemetry", http.StatusOK},
		{http.MethodGet, "/crews", http.StatusForbidden},
		{http.MethodGet, "/credentials", http.StatusForbidden},
		{http.MethodPost, "/crew/create", http.StatusForbidden},
	} {
		r := httptest.NewRequest(tc.method, "http://127.0.0.1:9119"+tc.path, strings.NewReader(""))
		r.RemoteAddr = "127.0.0.1:42000"
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != tc.want {
			t.Errorf("%s %s: got %d, want %d", tc.method, tc.path, w.Code, tc.want)
		}
	}
}
