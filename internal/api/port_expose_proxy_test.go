package api

import (
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"syscall"
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

// timeoutErr is a minimal net.Error whose Timeout() is true — what the
// kernel hands back when SYNs into a network we cannot route to are simply
// dropped (the Colima symptom in #1710: "dial tcp 172.19.0.2:8000: i/o
// timeout"). Constructed rather than provoked so the test is deterministic
// and instant instead of waiting out a real connect timeout.
type timeoutErr struct{}

func (timeoutErr) Error() string   { return "i/o timeout" }
func (timeoutErr) Timeout() bool   { return true }
func (timeoutErr) Temporary() bool { return true }

// dialErr wraps a cause the way net.Dialer would, so the classifier is
// exercised through the same *net.OpError shell it sees in production.
func dialErr(cause error) error {
	return &net.OpError{Op: "dial", Net: "tcp", Err: cause}
}

// The proxy's 502 has to tell an operator on a VM-based runtime what is
// actually wrong. Before #1710 every failure rendered as a bare "bad
// gateway" and the only clue — the target address — lived in the server
// log, which a self-hoster debugging their own setup has no reason to
// suspect. The body must name the target and, when packets went nowhere
// against a container-network address, name VM routing as a likely cause —
// without asserting it, because a dead container fails identically.
func TestPortExposeProxyError(t *testing.T) {
	tests := []struct {
		name     string
		host     string
		err      error
		contains []string
		absent   []string
	}{
		{
			name: "timeout against bridge IP names VM routing",
			host: "172.19.0.2:8000",
			err:  dialErr(timeoutErr{}),
			// Target, the hedge, and the runtimes it applies to.
			contains: []string{"172.19.0.2:8000", "Colima", "likely"},
		},
		{
			name:     "no route to host against bridge IP names VM routing",
			host:     "10.88.0.5:3000",
			err:      dialErr(os.NewSyscallError("connect", syscall.EHOSTUNREACH)),
			contains: []string{"10.88.0.5:3000", "Colima"},
		},
		{
			name:     "refused means routing works — do not blame the runtime",
			host:     "172.19.0.2:8000",
			err:      dialErr(os.NewSyscallError("connect", syscall.ECONNREFUSED)),
			contains: []string{"172.19.0.2:8000"},
			absent:   []string{"Colima", "virtual machine"},
		},
		{
			name:     "timeout against loopback is not a VM-routing story",
			host:     "127.0.0.1:8000",
			err:      dialErr(timeoutErr{}),
			contains: []string{"127.0.0.1:8000"},
			absent:   []string{"Colima", "virtual machine"},
		},
		{
			name:     "unclassified errors still name the target",
			host:     "172.19.0.2:8000",
			err:      io.ErrUnexpectedEOF,
			contains: []string{"172.19.0.2:8000"},
			absent:   []string{"Colima"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := portExposeProxyError(&url.URL{Scheme: "http", Host: tt.host}, tt.err)
			for _, want := range tt.contains {
				if !strings.Contains(got, want) {
					t.Errorf("message %q does not mention %q", got, want)
				}
			}
			for _, no := range tt.absent {
				if strings.Contains(got, no) {
					t.Errorf("message %q must not mention %q", got, no)
				}
			}
		})
	}
}

// Wiring check: the classified message has to reach the client, not just
// exist as a helper. A closed loopback port fails instantly and
// deterministically, so this asserts the 502 body carries the target
// without depending on the host's routing table.
func TestServeExposed_ErrorBodyNamesTarget(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	addr := ln.Addr().String()
	if err := ln.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	token := "tok-dead-upstream"
	h := newProxyTestHandler(t, token, &url.URL{Scheme: "http", Host: addr}, time.Now().Add(time.Hour))

	req := httptest.NewRequest(http.MethodGet, "/exposed/"+token+"/", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	resp := rec.Result()
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadGateway {
		t.Fatalf("status = %d, want 502", resp.StatusCode)
	}
	body, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(body), addr) {
		t.Errorf("502 body %q does not name the target %q", body, addr)
	}
	if strings.TrimSpace(string(body)) == "bad gateway" {
		t.Errorf("502 body is still the undiagnosable %q", body)
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
