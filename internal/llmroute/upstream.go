package llmroute

import (
	"fmt"
	"net/url"
	"strings"
)

// maxCredBaseURLBytes caps a credential-supplied base URL. The credentials
// surface validates the same bound at create time; this one exists because a
// value that somehow got stored oversized must not be parsed here either.
const maxCredBaseURLBytes = 2048

// Upstream is the dial target a request is forwarded to.
type Upstream struct {
	// Scheme is "https" for a built-in provider; a credential-supplied
	// endpoint may be "http" (a self-hosted runtime on a private network).
	Scheme string
	// Host is the hostname with optional port, lowercased.
	Host string
	// BasePath is joined ahead of the outbound path, without a trailing
	// slash. Empty for a provider whose upstream serves at the root.
	BasePath string
}

// ResolveUpstream returns the dial target for a spec.
//
// For a fixed-host spec it ignores credBaseURL entirely — an operator cannot
// redirect Anthropic traffic by putting a URL in a credential.
//
// For UpstreamFromCredential it parses credBaseURL and refuses a non-http(s)
// scheme, a missing host, embedded userinfo (which would put a secret in every
// log line that records the target) and anything past maxCredBaseURLBytes.
//
// NOTE for callers: this is validation, not an egress control. It cannot be
// one — DNS can rebind between here and the dial, and the crew-scoped decision
// about which hosts an agent may reach is made at run time. The controls are
// the sidecar's allowlist check and the resolve-then-pin SSRF dialer; this
// function only guarantees the string is a URL of a shape we are willing to
// dial at all.
func ResolveUpstream(s Spec, credBaseURL string) (Upstream, error) {
	if !s.UpstreamFromCredential {
		return Upstream{
			Scheme:   "https",
			Host:     s.UpstreamHost,
			BasePath: strings.TrimSuffix(s.UpstreamBasePath, "/"),
		}, nil
	}

	if credBaseURL == "" {
		return Upstream{}, fmt.Errorf("llmroute: %s: credential carries no base URL", s.ID)
	}
	if len(credBaseURL) > maxCredBaseURLBytes {
		return Upstream{}, fmt.Errorf("llmroute: %s: base URL is %d bytes, over the %d-byte cap", s.ID, len(credBaseURL), maxCredBaseURLBytes)
	}
	u, err := url.Parse(credBaseURL)
	if err != nil {
		return Upstream{}, fmt.Errorf("llmroute: %s: base URL is not a URL: %w", s.ID, err)
	}
	switch strings.ToLower(u.Scheme) {
	case "http", "https":
	default:
		return Upstream{}, fmt.Errorf("llmroute: %s: base URL scheme %q is not http or https", s.ID, u.Scheme)
	}
	if u.Host == "" {
		return Upstream{}, fmt.Errorf("llmroute: %s: base URL has no host", s.ID)
	}
	if u.User != nil {
		return Upstream{}, fmt.Errorf("llmroute: %s: base URL must not carry userinfo", s.ID)
	}

	// The credential's path is the endpoint's base path ("…/v1"); its query
	// and fragment are not ours to forward and are dropped here rather than
	// silently concatenated onto every agent request.
	return Upstream{
		Scheme:   strings.ToLower(u.Scheme),
		Host:     strings.ToLower(u.Host),
		BasePath: strings.TrimSuffix(u.Path, "/"),
	}, nil
}

// OutboundPath computes the upstream path for a request that arrived on the
// spec's loopback prefix: strip the routing prefix when the spec says to, then
// join what remains under the upstream's base path.
//
// Callers must clear URL.RawPath after setting the result — RawPath is an
// optional escaped hint, and leaving a stale one is how a stripped prefix
// survives into the request target.
func OutboundPath(s Spec, u Upstream, reqPath string) string {
	out := reqPath
	if s.StripPrefix {
		out = strings.TrimPrefix(out, s.PathPrefix)
		if out == "" {
			out = "/"
		}
	}
	if u.BasePath == "" {
		return out
	}
	if !strings.HasPrefix(out, "/") {
		out = "/" + out
	}
	return u.BasePath + out
}
