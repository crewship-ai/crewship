package api

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

// newProxyTestHandler returns a handler wired to a registry with a single
// entry pointing at an upstream server that echoes request metadata. The
// pattern matches how production mounts the handler under
// "GET /exposed/{token}/".
func newProxyTestHandler(t *testing.T, token string, upstreamURL *url.URL, expiresAt time.Time) http.Handler {
	t.Helper()
	db := newRegistryTestDB(t)
	reg := NewPortExposeRegistry(db, portExposeTestLogger())
	host := upstreamURL.Host
	// Split "host:port" to feed the Entry struct. httptest.NewServer returns
	// a URL whose Host is already host:port.
	ip := host
	port := 0
	if i := strings.LastIndex(host, ":"); i >= 0 {
		ip = host[:i]
		// best-effort port parse; zero-value is fine for the handler path,
		// which only uses ContainerIP+port to build the target URL anyway.
		for _, c := range host[i+1:] {
			port = port*10 + int(c-'0')
		}
	}
	reg.Add(&ExposeEntry{
		Token:         token,
		ContainerIP:   ip,
		ContainerPort: port,
		ExpiresAt:     expiresAt,
	})
	h := NewPortExposeHandler(db, reg, nil, AllowAllPolicy{}, nil, DefaultPortExposeConfig(), portExposeTestLogger())
	mux := http.NewServeMux()
	mux.HandleFunc("GET /exposed/{token}/", h.ServeExposed)
	mux.HandleFunc("GET /exposed/{token}", h.ServeExposed)
	return mux
}

func TestServeExposed_ForwardsAndStripsPrefix(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Upstream must see the path without the /exposed/{token} prefix.
		w.Header().Set("X-Got-Path", r.URL.Path)
		_, _ = w.Write([]byte("hello"))
	}))
	defer upstream.Close()
	u, _ := url.Parse(upstream.URL)

	token := "tok-forward"
	h := newProxyTestHandler(t, token, u, time.Now().Add(time.Hour))

	req := httptest.NewRequest(http.MethodGet, "/exposed/"+token+"/hello/world?x=1", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	resp := rec.Result()
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	body, _ := io.ReadAll(resp.Body)
	if string(body) != "hello" {
		t.Errorf("body = %q, want %q", body, "hello")
	}
	if got := resp.Header.Get("X-Got-Path"); got != "/hello/world" {
		t.Errorf("upstream saw path %q, want /hello/world", got)
	}
}

func TestServeExposed_NotFound(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer upstream.Close()
	u, _ := url.Parse(upstream.URL)

	h := newProxyTestHandler(t, "known-token", u, time.Now().Add(time.Hour))

	req := httptest.NewRequest(http.MethodGet, "/exposed/unknown-token/", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Result().StatusCode != http.StatusNotFound {
		t.Errorf("unknown token should yield 404, got %d", rec.Result().StatusCode)
	}
}

func TestServeExposed_Gone_WhenExpired(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer upstream.Close()
	u, _ := url.Parse(upstream.URL)

	token := "tok-expired"
	h := newProxyTestHandler(t, token, u, time.Now().Add(-time.Minute))

	req := httptest.NewRequest(http.MethodGet, "/exposed/"+token+"/", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Result().StatusCode != http.StatusGone {
		t.Errorf("expired token should yield 410, got %d", rec.Result().StatusCode)
	}
}

func TestServeExposed_BlocksWebSocketUpgrade(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer upstream.Close()
	u, _ := url.Parse(upstream.URL)

	token := "tok-ws"
	h := newProxyTestHandler(t, token, u, time.Now().Add(time.Hour))

	req := httptest.NewRequest(http.MethodGet, "/exposed/"+token+"/", nil)
	req.Header.Set("Upgrade", "websocket")
	req.Header.Set("Connection", "upgrade")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Result().StatusCode != http.StatusUpgradeRequired {
		t.Errorf("ws upgrade should yield 426, got %d", rec.Result().StatusCode)
	}
}
