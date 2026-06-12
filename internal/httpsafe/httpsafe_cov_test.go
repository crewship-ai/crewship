package httpsafe

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

// TestSafeTransport_BlocksLoopbackDial proves the connect-time guard
// refuses an IP-literal loopback target even though the URL itself would
// be syntactically fine. This is the layer that catches DNS aliases and
// rebinding — the dialer must refuse regardless of what the URL said.
func TestSafeTransport_BlocksLoopbackDial(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("handler reached — SafeTransport let a loopback dial through")
	}))
	defer srv.Close()

	client := &http.Client{Transport: SafeTransport(), Timeout: 5 * time.Second}
	resp, err := client.Get(srv.URL) // srv.URL is http://127.0.0.1:<port>
	if err == nil {
		resp.Body.Close()
		t.Fatal("GET to 127.0.0.1 succeeded, want ErrBlocked")
	}
	if !errors.Is(err, ErrBlocked) {
		t.Errorf("err = %v, want ErrBlocked in chain", err)
	}
}

// TestSafeTransport_BlocksLocalhostName exercises the resolver path:
// "localhost" resolves (via the hosts file, no network) to a loopback
// address, which must be refused.
func TestSafeTransport_BlocksLocalhostName(t *testing.T) {
	t.Parallel()
	tr := SafeTransport()
	_, err := tr.DialContext(context.Background(), "tcp", "localhost:80")
	if err == nil {
		t.Fatal("DialContext(localhost:80) succeeded, want ErrBlocked")
	}
	if !errors.Is(err, ErrBlocked) {
		t.Errorf("err = %v, want ErrBlocked in chain", err)
	}
}

// TestSafeTransport_InvalidAddress covers the SplitHostPort failure path.
func TestSafeTransport_InvalidAddress(t *testing.T) {
	t.Parallel()
	tr := SafeTransport()
	_, err := tr.DialContext(context.Background(), "tcp", "no-port-here")
	if err == nil {
		t.Fatal("DialContext with portless address succeeded, want error")
	}
	if !strings.Contains(err.Error(), "invalid address") {
		t.Errorf("err = %v, want 'invalid address'", err)
	}
}

// TestRewriteRoundTripper_RetargetsRequest proves the test-wiring
// transport reroutes the bytes while preserving path, method, headers,
// body, and the logical Host — and does not mutate the caller's request.
func TestRewriteRoundTripper_RetargetsRequest(t *testing.T) {
	t.Parallel()
	var gotPath, gotHost, gotHeader, gotBody string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotHost = r.Host
		gotHeader = r.Header.Get("X-Test")
		b, _ := io.ReadAll(r.Body)
		gotBody = string(b)
		w.WriteHeader(http.StatusTeapot)
	}))
	defer srv.Close()

	target, err := url.Parse(srv.URL)
	if err != nil {
		t.Fatalf("parse target: %v", err)
	}
	rt := &RewriteRoundTripper{Target: target}

	req, err := http.NewRequest(http.MethodPost, "https://test.example/v1/things?a=1",
		strings.NewReader("payload-bytes"))
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	req.Header.Set("X-Test", "hdr-val")

	resp, err := rt.RoundTrip(req)
	if err != nil {
		t.Fatalf("RoundTrip: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusTeapot {
		t.Errorf("status = %d, want 418", resp.StatusCode)
	}
	if gotPath != "/v1/things" {
		t.Errorf("server saw path %q, want /v1/things", gotPath)
	}
	if gotHost != "test.example" {
		t.Errorf("server saw Host %q, want logical host test.example", gotHost)
	}
	if gotHeader != "hdr-val" {
		t.Errorf("server saw X-Test %q, want hdr-val", gotHeader)
	}
	if gotBody != "payload-bytes" {
		t.Errorf("server saw body %q, want payload-bytes", gotBody)
	}
	// The caller's URL value must not have been retargeted in place.
	if req.URL.Host != "test.example" || req.URL.Scheme != "https" {
		t.Errorf("caller request mutated: %s://%s", req.URL.Scheme, req.URL.Host)
	}
}

func TestSafeClient_TimeoutAndRedirectPolicy(t *testing.T) {
	t.Parallel()
	c := SafeClient(7 * time.Second)
	if c.Timeout != 7*time.Second {
		t.Errorf("Timeout = %v, want 7s", c.Timeout)
	}
	if c.Transport == nil {
		t.Fatal("Transport is nil, want SafeTransport")
	}
	if c.CheckRedirect == nil {
		t.Fatal("CheckRedirect is nil, want re-validating policy")
	}

	mkReq := func(raw string) *http.Request {
		u, err := url.Parse(raw)
		if err != nil {
			t.Fatalf("parse %q: %v", raw, err)
		}
		return &http.Request{URL: u}
	}

	// >=10 hops → refused regardless of destination.
	via := make([]*http.Request, 10)
	for i := range via {
		via[i] = mkReq("https://example.com/hop")
	}
	if err := c.CheckRedirect(mkReq("https://example.com/final"), via); err == nil {
		t.Error("11th redirect accepted, want too-many-redirects error")
	}

	// Redirect into a blocked literal IP → refused via ValidateURL.
	err := c.CheckRedirect(mkReq("https://192.168.1.1/admin"), via[:1])
	if !errors.Is(err, ErrInvalidURL) {
		t.Errorf("redirect to private IP: err = %v, want ErrInvalidURL", err)
	}

	// Default schemes are https-only: an http hop is refused...
	err = c.CheckRedirect(mkReq("http://example.com/x"), via[:1])
	if !errors.Is(err, ErrInvalidURL) {
		t.Errorf("http hop on default client: err = %v, want ErrInvalidURL", err)
	}

	// ...and a public https hop passes.
	if err := c.CheckRedirect(mkReq("https://example.com/ok"), via[:1]); err != nil {
		t.Errorf("https hop refused: %v", err)
	}

	// Widened schemes propagate into the redirect policy.
	wide := SafeClient(time.Second, "http", "https")
	if err := wide.CheckRedirect(mkReq("http://example.com/ok"), via[:1]); err != nil {
		t.Errorf("http hop on widened client refused: %v", err)
	}
}
