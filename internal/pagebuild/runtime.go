package pagebuild

import (
	"crypto/rand"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strings"

	"golang.org/x/net/publicsuffix"
)

const RuntimePath = "/api/v1/pages/runtime/bootstrap"

//go:embed bootstrap.js
var bootstrap string

func site(host string) string {
	host = strings.ToLower(strings.TrimSuffix(host, "."))
	if net.ParseIP(host) != nil {
		return host
	}
	domain, err := publicsuffix.EffectiveTLDPlusOne(host)
	if err != nil {
		return host
	}
	return domain
}

// A different port or sibling subdomain is NOT a different browser site.
// srcdoc passed privilege tests but failed the infinite-loop isolation test.
func ValidateRuntimeOrigin(runtime, studio string) error {
	return ValidateRuntimeOriginForDevelopment(runtime, studio, false)
}

// ValidateRuntimeOriginForDevelopment permits a reviewed same-origin demo only
// when explicitly enabled by the operator. It does not promise process isolation.
// URL, TLS and sandbox restrictions are never relaxed.
func ValidateRuntimeOriginForDevelopment(runtime, studio string, sameOriginDemo bool) error {
	parse := func(raw string) (*url.URL, error) {
		u, err := url.Parse(raw)
		if err != nil {
			return nil, err
		}
		if (u.Scheme != "https" && u.Scheme != "http") || u.Hostname() == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Path != "" && u.Path != "/") {
			return nil, errors.New("Page runtime and Studio URLs must be HTTP(S) origins without credentials, paths or queries")
		}
		return u, nil
	}
	r, err := parse(runtime)
	if err != nil {
		return err
	}
	s, err := parse(studio)
	if err != nil {
		return err
	}
	if site(r.Hostname()) == site(s.Hostname()) && !(sameOriginDemo && r.Scheme == s.Scheme && strings.EqualFold(r.Host, s.Host)) {
		return errors.New("Page runtime must use a different site from Studio; another port or sibling subdomain cannot isolate a stuck application")
	}
	if r.Scheme == "http" {
		ip := net.ParseIP(r.Hostname())
		if ip == nil || !ip.IsLoopback() {
			return errors.New("Page runtime requires HTTPS except on literal loopback addresses")
		}
	}
	if s.Scheme == "https" && r.Scheme != "https" {
		return errors.New("HTTPS Studio requires an HTTPS Page runtime")
	}
	return nil
}
func RuntimeMatchesHost(runtime, host string) bool {
	u, err := url.Parse(runtime)
	return err == nil && strings.EqualFold(u.Host, host)
}

// ServeRuntime is public, constant bootstrap HTML, with no user data. It is
// served by the existing Go listener via a separate DNS site, not a Vite server.
func ServeRuntime(w http.ResponseWriter, studio string) {
	nonce := rand.Text()
	// Match the browser's event.origin serialization, including default ports.
	parent := strings.TrimSuffix(studio, "/")
	if u, err := url.Parse(parent); err == nil {
		if (u.Scheme == "https" && u.Port() == "443") || (u.Scheme == "http" && u.Port() == "80") {
			host := u.Hostname()
			if strings.Contains(host, ":") {
				host = "[" + host + "]"
			}
			u.Host = host
		}
		u.Host = strings.ToLower(u.Host)
		parent = u.String()
	}
	encodedParent, _ := json.Marshal(parent)
	w.Header().Del("X-Frame-Options")
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Referrer-Policy", "no-referrer")
	w.Header().Set("Cross-Origin-Resource-Policy", "cross-origin")
	w.Header().Set("Cross-Origin-Embedder-Policy", "credentialless")
	w.Header().Set("Origin-Agent-Cluster", "?1")
	w.Header().Set("Content-Security-Policy", fmt.Sprintf("default-src 'none'; script-src 'nonce-%s'; style-src 'unsafe-inline'; img-src data: blob:; font-src data:; connect-src 'none'; frame-src 'none'; worker-src 'none'; object-src 'none'; base-uri 'none'; form-action 'none'; frame-ancestors %s", nonce, strings.TrimSuffix(studio, "/")))
	_, _ = fmt.Fprintf(w, `<!doctype html><html><head><meta charset="utf-8"><meta name="viewport" content="width=device-width, initial-scale=1"></head><body><div id="root"></div><script nonce="%s">const expectedParent=%s;const bootNonce=%q;%s</script></body></html>`, nonce, encodedParent, nonce, bootstrap)
}
