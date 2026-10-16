package server

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestPageRuntimeDoesNotWidenDefaultStudioPolicy(t *testing.T) {
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(200) })
	for _, origin := range []string{"", "https://pages.example.net"} {
		w := httptest.NewRecorder()
		securityHeadersMiddleware(inner, origin).ServeHTTP(w, httptest.NewRequest("GET", "/pages", nil))
		policy := w.Header().Get("Content-Security-Policy")
		if origin == "" && strings.Contains(policy, "pages.example.net") {
			t.Fatal(policy)
		}
		if origin != "" && !strings.Contains(policy, origin) {
			t.Fatal(policy)
		}
		if w.Header().Get("X-Frame-Options") != "DENY" {
			t.Fatal("Studio embedding protection removed")
		}
		if strings.Contains(policy, "frame-src *") {
			t.Fatal("wildcard runtime origin")
		}
	}
}

func TestBoardConverterOnlyPublicProgramAllowsOpaqueLoading(t *testing.T) {
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(200) })
	for _, path := range []string{"/board-converter/runner.js", "/board-converter/other.js", "/api/v1/pages/design/board", "/pages"} {
		w := httptest.NewRecorder()
		securityHeadersMiddleware(inner).ServeHTTP(w, httptest.NewRequest("GET", path, nil))
		expected := "same-origin"
		if path == "/board-converter/runner.js" {
			expected = "cross-origin"
		}
		if path != "/board-converter/runner.js" && w.Header().Get("Access-Control-Allow-Origin") != "" {
			t.Fatal("private CORS widened")
		}
		if path == "/board-converter/runner.js" && w.Header().Get("Access-Control-Allow-Origin") != "*" {
			t.Fatal("public converter CORS missing")
		}
		if got := w.Header().Get("Cross-Origin-Resource-Policy"); got != expected {
			t.Errorf("%s: %s", path, got)
		}
		if w.Header().Get("X-Frame-Options") != "DENY" {
			t.Fatal("Studio frame policy widened")
		}
	}
}
