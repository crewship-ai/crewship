// Package httpsafe centralises SSRF defences for outbound HTTP. The
// codebase had several near-duplicate implementations of "build an
// http.Client whose dialer refuses private IPs"; consolidating them
// makes the guarantee easier to audit and easier for static analysis
// (CodeQL go/request-forgery) to see at every call site.
//
// Two layers:
//   - ValidateURL is a cheap string-level reject for the obvious red
//     flags (non-http(s) scheme, literal RFC1918/loopback host, embedded
//     userinfo). Run it before issuing a request.
//   - SafeTransport returns an http.Transport whose DialContext re-resolves
//     the host at connect time and refuses any private/loopback/link-local
//     IP. This catches DNS aliases that point at internal addresses
//     (localtest.me → 127.0.0.1, split-horizon DNS) and DNS-rebinding
//     attacks that flip a TOCTOU window between validation and dial.
//
// Use both: ValidateURL filters obvious abuse early without a network
// round-trip; SafeTransport is the irreducible guarantee that even a
// host name we believe is public can't actually route to an internal
// address. Either alone is a single point of failure.
package httpsafe

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// ErrBlocked is returned by SafeTransport's dialer when the resolved IP
// belongs to a private/loopback/link-local range.
var ErrBlocked = errors.New("httpsafe: blocked outbound connection to private/internal address")

// ErrInvalidURL is returned by ValidateURL for unfetchable URLs (wrong
// scheme, missing host, etc.).
var ErrInvalidURL = errors.New("httpsafe: invalid outbound URL")

// blockedV4 / blockedV6 list the IP ranges SafeTransport's dialer
// refuses. The full list mirrors what the skills package already
// enforces (CGNAT, documentation, multicast, ipv6 ULA) so SSRF defence
// is uniform across both surfaces.
var blockedCIDRs = mustCIDRs(
	"0.0.0.0/8",
	"10.0.0.0/8",
	"100.64.0.0/10",
	"127.0.0.0/8",
	"169.254.0.0/16",
	"172.16.0.0/12",
	"192.0.0.0/24",
	"192.0.2.0/24",
	"192.168.0.0/16",
	"198.18.0.0/15",
	"198.51.100.0/24",
	"203.0.113.0/24",
	"224.0.0.0/4",
	"240.0.0.0/4",
	"255.255.255.255/32",
	"::/128",
	"::1/128",
	"64:ff9b::/96",
	"100::/64",
	"fc00::/7",
	"fe80::/10",
	"ff00::/8",
)

func mustCIDRs(specs ...string) []*net.IPNet {
	out := make([]*net.IPNet, 0, len(specs))
	for _, s := range specs {
		_, n, err := net.ParseCIDR(s)
		if err != nil {
			panic("httpsafe: invalid hardcoded CIDR " + s + ": " + err.Error())
		}
		out = append(out, n)
	}
	return out
}

// IsBlockedIP reports whether the given IP literal sits in a range
// SafeTransport refuses to dial. Exported for callers that already
// resolved the address themselves (e.g. WS upgrade flows).
func IsBlockedIP(ip net.IP) bool {
	if ip == nil {
		return false
	}
	if v4 := ip.To4(); v4 != nil {
		ip = v4
	}
	for _, c := range blockedCIDRs {
		if c.Contains(ip) {
			return true
		}
	}
	return false
}

// ValidateURL parses raw and returns the parsed value when it is safe
// to fetch. "Safe" here is the cheap, no-network subset:
//
//   - scheme is http or https (allowSchemes can widen this)
//   - host is non-empty
//   - userinfo is absent (credentials in URLs are a code smell and the
//     downstream logger would leak them)
//   - host is not "localhost"
//   - if the host is an IP literal it isn't in a blocked range
//
// The DNS lookup that closes DNS-rebinding/split-horizon gaps is the
// job of SafeTransport's DialContext, not this function — keeping
// ValidateURL synchronous lets handler code reject early without
// burning a network round-trip on every API call.
//
// allowSchemes is nil → defaults to {"https"}. Pass {"http","https"}
// for cases where http URLs are legitimate (admin-configured intranet
// MCP servers).
func ValidateURL(raw string, allowSchemes ...string) (*url.URL, error) {
	if raw == "" {
		return nil, fmt.Errorf("%w: empty", ErrInvalidURL)
	}
	u, err := url.Parse(raw)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrInvalidURL, err)
	}
	if u.User != nil {
		return nil, fmt.Errorf("%w: userinfo not allowed in outbound URL", ErrInvalidURL)
	}
	if len(allowSchemes) == 0 {
		allowSchemes = []string{"https"}
	}
	schemeOK := false
	for _, s := range allowSchemes {
		if strings.EqualFold(u.Scheme, s) {
			schemeOK = true
			break
		}
	}
	if !schemeOK {
		return nil, fmt.Errorf("%w: scheme %q not in %v", ErrInvalidURL, u.Scheme, allowSchemes)
	}
	host := u.Hostname()
	if host == "" {
		return nil, fmt.Errorf("%w: missing host", ErrInvalidURL)
	}
	if strings.EqualFold(host, "localhost") {
		return nil, fmt.Errorf("%w: localhost not allowed", ErrInvalidURL)
	}
	if ip := net.ParseIP(host); ip != nil && IsBlockedIP(ip) {
		return nil, fmt.Errorf("%w: literal private/internal IP %s not allowed", ErrInvalidURL, ip)
	}
	return u, nil
}

// SafeTransport returns an http.Transport whose DialContext refuses to
// connect to any address that resolves into a blocked CIDR. Pair with
// ValidateURL for the full defence in depth — see package doc.
func SafeTransport() *http.Transport {
	dialer := &net.Dialer{Timeout: 10 * time.Second, KeepAlive: 30 * time.Second}
	return &http.Transport{
		DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
			host, port, err := net.SplitHostPort(addr)
			if err != nil {
				return nil, fmt.Errorf("httpsafe: invalid address %q: %w", addr, err)
			}
			ips, err := net.DefaultResolver.LookupIPAddr(ctx, host)
			if err != nil {
				return nil, fmt.Errorf("httpsafe: DNS resolution failed for %s: %w", host, err)
			}
			if len(ips) == 0 {
				return nil, fmt.Errorf("httpsafe: no addresses for %s", host)
			}
			for _, ip := range ips {
				if IsBlockedIP(ip.IP) {
					return nil, fmt.Errorf("%w: %s", ErrBlocked, ip.IP)
				}
			}
			// Connect directly to the first resolved IP so that a
			// second resolver call (DNS rebind) can't pick a different
			// destination between our check and the dial.
			return dialer.DialContext(ctx, network, net.JoinHostPort(ips[0].IP.String(), port))
		},
		TLSHandshakeTimeout:   10 * time.Second,
		ResponseHeaderTimeout: 30 * time.Second,
		IdleConnTimeout:       60 * time.Second,
		MaxIdleConns:          16,
	}
}

// RewriteRoundTripper retargets every outbound request to `Target`
// while preserving the path / method / headers / body. Intended for
// test wiring only: tests pass a validation-passing URL like
// "https://test.example/path" and install a RewriteRoundTripper that
// reroutes the bytes to an httptest.NewServer on 127.0.0.1. This
// keeps the production code path's inline ValidateURL guard
// unconditional — CodeQL go/request-forgery sees a single, complete
// sanitiser chain because the bypass lives entirely in the transport
// layer.
//
// Production code must not construct one; the type is only exported
// so test packages outside internal/api can reuse the same trick
// (skills, future packages with their own http.Client).
type RewriteRoundTripper struct {
	Target *url.URL
}

// RoundTrip implements http.RoundTripper. Clones the request so the
// caller's URL value isn't mutated, swaps the scheme + host, and
// hands off to http.DefaultTransport. The original Host header is
// preserved so handlers that key off it still see the "logical" host.
func (rt *RewriteRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	r := req.Clone(req.Context())
	r.URL.Scheme = rt.Target.Scheme
	r.URL.Host = rt.Target.Host
	return http.DefaultTransport.RoundTrip(r)
}

// SafeClient returns an http.Client with SafeTransport and the given
// total timeout (request + response). A CheckRedirect is wired in to
// re-validate every 3xx hop so a permissive host can't bounce into a
// blocked one.
func SafeClient(timeout time.Duration, allowSchemes ...string) *http.Client {
	if len(allowSchemes) == 0 {
		allowSchemes = []string{"https"}
	}
	return &http.Client{
		Timeout:   timeout,
		Transport: SafeTransport(),
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= 10 {
				return fmt.Errorf("httpsafe: too many redirects")
			}
			if _, err := ValidateURL(req.URL.String(), allowSchemes...); err != nil {
				return err
			}
			return nil
		},
	}
}
