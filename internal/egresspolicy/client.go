package egresspolicy

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/crewship-ai/crewship/internal/egressallow"
	"github.com/crewship-ai/crewship/internal/httpsafe"
)

// ErrEgressBlocked marks a request refused by the crew allowlist on a redirect
// hop, so callers can classify it as a policy block (not a transport error).
// The SSRF/scheme re-checks surface httpsafe's own errors.
var ErrEgressBlocked = errors.New("egress policy blocked redirect hop")

// HostChecker returns nil to allow an outbound host, non-nil (message names the
// remediation) to block it. It is the crew-allowlist half of the egress gate;
// SSRF / private-IP blocking is handled by the transport + ValidateURL inside
// Client. Bind one of the constructors below to a crew scope, then hand it to
// Client — every hop of every request the client makes is checked against it.
type HostChecker func(ctx context.Context, host string) error

// DBChecker binds the control-plane crew policy (crews.network_mode /
// allowed_domains, via Check) as a HostChecker. Used by every path that has the
// control-plane DB: routine http steps, notify/webhook channels, hooks.
func DBChecker(db *sql.DB, crewID string) HostChecker {
	return func(ctx context.Context, host string) error {
		return Check(ctx, db, crewID, host)
	}
}

// AllowlistChecker binds a pre-resolved allowlist as a HostChecker, for callers
// with no control-plane DB — the in-container sidecar MCP gateway, which is
// pushed the crew's DomainAllowlist rather than querying crews. freeMode true
// (or a nil allowlist) means the crew is unrestricted. Captures the pointer and
// freeMode at construction, so callers whose allowlist is installed after the
// gateway is built must (re)construct the client once the allowlist is known —
// the sidecar MCP gateway does exactly this in SetEgressAllowlist.
func AllowlistChecker(al *egressallow.DomainAllowlist, freeMode bool) HostChecker {
	return func(_ context.Context, host string) error {
		if freeMode || al == nil {
			return nil
		}
		if !al.IsAllowed(host) {
			return fmt.Errorf("egress policy: host %q is not in the crew allowlist", host)
		}
		return nil
	}
}

// NoopChecker allows every host — for paths with no crew scope (dry-run / draft
// / system). SSRF blocking still applies via the transport.
func NoopChecker() HostChecker {
	return func(context.Context, string) error { return nil }
}

// Options configures Client. The zero value is usable: https-only, 30s timeout,
// 10-redirect cap, strict SSRF (no private networks).
type Options struct {
	// Timeout is the total per-request timeout. Default 30s.
	Timeout time.Duration
	// Schemes are the allowed URL schemes, re-checked on every hop. Default
	// {"https"}. Pass {"http","https"} for intranet/LAN receivers (hooks, MCP,
	// webhooks).
	Schemes []string
	// AllowPrivate lets the dialer reach loopback / RFC1918 / ULA endpoints (a
	// LAN webhook or on-prem MCP server). The hard tier (link-local / IMDS /
	// multicast / reserved) is always blocked regardless.
	AllowPrivate bool
	// MaxRedirects caps the redirect chain. Default 10.
	MaxRedirects int
	// ExtraHop is an optional additional per-hop check (e.g. a routine's
	// explicit egress_targets allowlist), run after the SSRF + crew-allowlist
	// checks pass.
	ExtraHop func(req *http.Request) error
	// Transport overrides the default SSRF-safe transport. Test-only — used to
	// route validation-passing URLs to a loopback httptest server without
	// weakening the production dial guard.
	Transport http.RoundTripper
}

// Client is the ONE gated *http.Client every non-container egress path should
// use. Constructing it IS the gate — a new integration cannot forget it:
//
//   - Connect time (Transport/DialContext): refuses to dial any address that
//     resolves into a blocked CIDR, on every hop's dial (DNS-rebind defense).
//   - Per redirect hop (CheckRedirect → ValidateURL): rejects a disallowed
//     scheme or a literal private/blocked IP before following the hop.
//   - Per redirect hop (CheckRedirect → check): re-checks the CREW ALLOWLIST,
//     so a gated first request that 3xx-redirects to a non-allowlisted host is
//     refused instead of silently followed. This is the layer bespoke clients
//     kept forgetting.
//
// The initial request's host is the caller's responsibility to pre-flight (a
// cheap fast-fail before the first byte); Client guarantees every SUBSEQUENT
// hop is re-gated, which is what closes the redirect bypass.
func Client(check HostChecker, opts Options) *http.Client {
	if check == nil {
		check = NoopChecker()
	}
	schemes := opts.Schemes
	if len(schemes) == 0 {
		schemes = []string{"https"}
	}
	maxRedirects := opts.MaxRedirects
	if maxRedirects <= 0 {
		maxRedirects = 10
	}
	timeout := opts.Timeout
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	var tr http.RoundTripper = opts.Transport
	if tr == nil {
		tr = httpsafe.SafeTransportForEndpoint(opts.AllowPrivate)
	}
	return &http.Client{
		Timeout:   timeout,
		Transport: tr,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= maxRedirects {
				return fmt.Errorf("egresspolicy: too many redirects (%d)", len(via))
			}
			// Endpoint-aware so AllowPrivate is honoured on the hop exactly as the
			// dialer honours it: a caller that legitimately reaches loopback/RFC1918
			// (LAN webhook, on-prem MCP, the http-step test hatch) is not tripped by
			// a strict literal-IP reject here, while cloud metadata stays blocked.
			if _, err := httpsafe.ValidateURLForEndpoint(req.URL.String(), opts.AllowPrivate, schemes...); err != nil {
				return err
			}
			// Wrap with %w (not %v) so callers can BOTH classify the block via
			// errors.Is(ErrEgressBlocked) AND introspect a structured checker error
			// (e.g. the pipeline's *EgressBlockedError) via errors.As — the crew
			// re-gate closes the redirect bypass without erasing the path's own
			// error taxonomy.
			if err := check(req.Context(), req.URL.Host); err != nil {
				return fmt.Errorf("%w: %w", ErrEgressBlocked, err)
			}
			if opts.ExtraHop != nil {
				return opts.ExtraHop(req)
			}
			return nil
		},
	}
}
