package egresspolicy

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"

	"github.com/crewship-ai/crewship/internal/httpsafe"
)

// allowlistChecker builds a HostChecker allowing exactly the given hosts.
func allowOnly(hosts ...string) HostChecker {
	set := map[string]bool{}
	for _, h := range hosts {
		set[h] = true
	}
	return func(_ context.Context, host string) error {
		if set[host] {
			return nil
		}
		return errBlocked(host)
	}
}

type blockErr struct{ host string }

func (e blockErr) Error() string   { return "host not allowed: " + e.host }
func errBlocked(host string) error { return blockErr{host} }

// TestClient_CheckRedirect_ReGatesCrewAllowlist is the core guarantee: a gated
// first request to an allowlisted host that 3xx-redirects to a NON-allowlisted
// host must be refused — the crew allowlist is re-checked on every hop, not just
// the original URL. This is exactly the hooks/MCP redirect bypass #1367 closes.
func TestClient_CheckRedirect_ReGatesCrewAllowlist(t *testing.T) {
	var collectHit bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/start":
			http.Redirect(w, r, "http://evil.test/collect", http.StatusFound)
		case "/collect":
			collectHit = true
			w.WriteHeader(http.StatusOK)
		}
	}))
	defer srv.Close()
	target, _ := url.Parse(srv.URL)

	// Route both logical hosts to the one loopback server; the allowlist — not
	// the transport — is what must distinguish them.
	client := Client(allowOnly("allowed.test"), Options{
		Schemes:   []string{"http", "https"},
		Transport: &httpsafe.RewriteRoundTripper{Target: target},
	})

	resp, err := client.Get("http://allowed.test/start")
	if err == nil {
		resp.Body.Close()
		t.Fatal("expected the redirect to a non-allowlisted host to be refused, got nil error")
	}
	if collectHit {
		t.Error("request reached the blocked host /collect — crew allowlist was bypassed via redirect")
	}
}

// Positive control: a redirect to a still-allowlisted host is followed.
func TestClient_CheckRedirect_AllowsSameAllowlistedHost(t *testing.T) {
	var landed bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/start":
			http.Redirect(w, r, "http://allowed.test/final", http.StatusFound)
		case "/final":
			landed = true
			w.WriteHeader(http.StatusOK)
		}
	}))
	defer srv.Close()
	target, _ := url.Parse(srv.URL)

	client := Client(allowOnly("allowed.test"), Options{
		Schemes:   []string{"http", "https"},
		Transport: &httpsafe.RewriteRoundTripper{Target: target},
	})
	resp, err := client.Get("http://allowed.test/start")
	if err != nil {
		t.Fatalf("same-host allowlisted redirect must be followed, got %v", err)
	}
	resp.Body.Close()
	if !landed {
		t.Error("expected the request to land on the allowlisted redirect target")
	}
}

// The redirect cap is enforced.
func TestClient_CheckRedirect_CapsRedirects(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "http://allowed.test/loop", http.StatusFound)
	}))
	defer srv.Close()
	target, _ := url.Parse(srv.URL)

	client := Client(allowOnly("allowed.test"), Options{
		Schemes:      []string{"http", "https"},
		MaxRedirects: 3,
		Transport:    &httpsafe.RewriteRoundTripper{Target: target},
	})
	resp, err := client.Get("http://allowed.test/loop")
	if err == nil {
		resp.Body.Close()
		t.Fatal("expected a too-many-redirects error")
	}
}
