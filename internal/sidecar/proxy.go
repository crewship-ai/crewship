package sidecar

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/crewship-ai/crewship/internal/httpsafe"
	"github.com/crewship-ai/crewship/internal/llmroute"
	"github.com/crewship-ai/crewship/internal/scrubber"
)

const (
	// maxRequestBodyBytes limits agent request bodies to prevent OOM.
	// LLM API requests are typically <1MB; 10MB is generous.
	maxRequestBodyBytes = 10 * 1024 * 1024 // 10 MB
)

// hopByHopHeaders are headers that MUST be removed by proxies per RFC 2616 Section 13.5.1.
// Proxy-Authorization is especially sensitive -- an agent could use it to exfiltrate data.
var hopByHopHeaders = []string{
	"Connection",
	"Keep-Alive",
	"Proxy-Authenticate",
	"Proxy-Authorization",
	"TE",
	"Trailers",
	"Transfer-Encoding",
	"Upgrade",
}

// EgressObserver receives a notification for every allowed outbound HTTP
// request that the sidecar proxy forwards. Host, method, and status are
// captured; path and body are NOT because they can carry user content or
// credentials that we must never persist. The hook runs synchronously on
// the proxy goroutine, so implementations should return quickly and do
// any heavy work (HTTP, DB) asynchronously.
//
// `provider` is the LLM-provider label (e.g. "anthropic") when the
// request was to a known LLM endpoint, empty otherwise. Useful for
// Crow's Nest filters that want to separate "agent talked to Anthropic"
// from "agent fetched generic HTTPS".
type EgressObserver func(host, method, provider string, statusCode int, denied bool)

// LLMCallObserver fires after a known LLM provider call returns, with the
// parsed token usage and rate-limit signal. Wired by ServerConfig at
// startup; the typical implementation HTTP-POSTs to crewshipd which then
// calls paymaster.Record. nil = no observer = no cost-ledger writes from
// CLI traffic (agents in metered mode still produce direct-API ledger
// rows via the Go middleware path).
//
// `mode` and `plan` carry the values of CREWSHIP_BILLING_MODE and
// CREWSHIP_SUBSCRIPTION_PLAN env vars set by the orchestrator at exec
// time, so the observer can tag the row correctly without re-deriving
// credential type. Empty mode is treated as "metered" by the recorder.
//
// Implementations MUST return quickly — the call runs on the proxy
// goroutine, blocking the response to the agent.
type LLMCallObserver func(usage LLMUsage, quota QuotaInfo, mode, plan string)

// Proxy is an HTTP forward proxy that intercepts agent outbound requests,
// injects LLM API credentials, and blocks non-allowed domains.
type Proxy struct {
	credStore          *CredStore
	allowlist          *DomainAllowlist
	scrubber           *scrubber.Scrubber
	logger             *slog.Logger
	transport          http.RoundTripper
	freeMode           bool
	allowPrivate       bool // #961: permit RFC1918/loopback dial targets (crew opt-in); link-local/metadata always blocked
	onEgress           EgressObserver
	onLLMCall          LLMCallObserver
	resolveLLMIdentity func(*http.Request) (agentID, configFingerprint string, present, ok bool)
	billingMode        string // "metered" | "flat_rate" | "" — set from env at startup
	subPlan            string // human label for flat-rate (e.g. "Anthropic Max 20×")
	buildHash          string // #1008: content hash of the running sidecar binary, advertised on /health
	// policyDomainsHash (#1160) is a hash of ONLY the per-crew policy
	// domains (restricted mode's cfg.NetworkPolicy.AllowedDomains), NEVER
	// the DefaultAllowedDomains merged into `allowlist` — see the doc
	// comment in server.go's NewServer for why the distinction matters.
	// Advertised on /health as domains_hash.
	policyDomainsHash string
	// tokenFP (#1385) is a short one-way fingerprint of the crew-bound
	// X-Internal-Token this sidecar was booted with, advertised on /health as
	// token_fp. The server compares it against the fingerprint of the token it
	// WOULD mint today: a mismatch means this container survived a restart that
	// rotated the internal-token master and now holds a stale, permanently-
	// rejected token ("invalid crew-bound token"). Empty when no IPC token is
	// configured (a crew-less/standalone sidecar) — the server never
	// false-classifies an empty fingerprint as orphaned.
	tokenFP string
	// configFingerprint is the keyed identity of the exact credential set. It
	// lets the orchestrator detect rotation/addition/removal
	// without exposing a plain hash of credential material.
	configFingerprint string

	// dnsCache, dnsResolve, and dialer back the shared resolve-then-pin SSRF
	// dialer (#961, cache added #1081). ONE instance lives on the Proxy and is
	// used by both the HTTP transport's DialContext (handleHTTP /
	// handleReverseProxy) and handleConnect's tunnel dial — a PR #1139 review
	// finding was that handleConnect used to build a fresh cache (and dialer)
	// per CONNECT request, so the positive DNS cache never got a hit on the
	// HTTPS-tunnel path. dnsResolve defaults to the real resolver and is only
	// overridden by tests (same package, unexported field).
	dnsCache   *dnsPositiveCache
	dnsResolve resolveFunc
	dialer     *net.Dialer
}

// ProxyConfig configures the sidecar proxy.
type ProxyConfig struct {
	CredStore *CredStore
	Allowlist *DomainAllowlist
	Scrubber  *scrubber.Scrubber
	Logger    *slog.Logger
	FreeMode  bool // When true, skip domain allowlist checks (allow all domains)
	// AllowPrivate (#961) permits the dial-time SSRF guard to reach
	// RFC1918/loopback destinations (a crew-opted-in private/LAN endpoint).
	// Link-local and cloud-metadata addresses stay blocked regardless.
	AllowPrivate bool
	// OnEgress is invoked after a successful upstream request. Optional —
	// leaving it nil disables observability emits. The proxy holds the
	// callback by reference (no copy), so installing a new observer
	// requires rebuilding the Proxy; for the sidecar's lifecycle that
	// happens at startup only, which keeps this lock-free on the hot path.
	OnEgress EgressObserver
	// OnLLMCall is invoked after a successful LLM-provider call, with the
	// parsed usage and quota signal. Optional. See LLMCallObserver.
	OnLLMCall LLMCallObserver
	// ResolveLLMIdentity authenticates the per-agent token embedded in the
	// disposable provider key before that key is overwritten. The returned
	// fingerprint must match this sidecar's keyed credential-set fingerprint.
	ResolveLLMIdentity func(*http.Request) (agentID, configFingerprint string, present, ok bool)
	// BillingMode and SubscriptionPlan come from the agent container's
	// CREWSHIP_BILLING_MODE / CREWSHIP_SUBSCRIPTION_PLAN env vars (set by
	// orchestrator/exec_env.go based on credential type). Pass-through
	// values that the LLMCallObserver receives for ledger row tagging.
	BillingMode      string
	SubscriptionPlan string
	// BuildHash is the content hash of the running sidecar binary, echoed on
	// /health so the server can detect a container serving a STALE sidecar
	// after a redeploy (#1008). Empty = unknown (server never false-alarms).
	BuildHash string
	// PolicyDomainsHash (#1160) is a hash of ONLY the per-crew policy
	// domains — see the Proxy field doc comment. Empty is a valid value
	// (free mode, or restricted with an empty allowlist).
	PolicyDomainsHash string
	// TokenFP (#1385) is the one-way fingerprint of the crew-bound internal
	// token this sidecar holds (internaltoken.Fingerprint). Echoed on /health
	// as token_fp so the server can detect a container orphaned by a master
	// rotation. Empty = no IPC token configured (never flagged as orphaned).
	TokenFP string
	// ConfigFingerprint is an HMAC produced by crewshipd and echoed on health.
	// The sidecar never computes it and therefore never needs the HMAC key.
	ConfigFingerprint string
	// Transport overrides the outbound RoundTripper the proxy hands every
	// forwarded request to (handleHTTP, handleReverseProxy). nil (the
	// default) builds the real resolve-then-pin SSRF-guarded *http.Transport
	// exactly as before — this field changes nothing for production sidecars.
	// It exists so a replay/cassette-backed RoundTripper (quality/testability
	// plan A4) can be wired in from OUTSIDE this package (e.g. the sidecar's
	// main(), gated on an explicit replay-mode flag) without reaching into an
	// unexported field. In-package tests already do that directly (see
	// proxy_test.go's `proxy.transport = …` assignments) and are unaffected.
	Transport http.RoundTripper
}

// NewProxy creates a forward proxy with credential injection.
func NewProxy(cfg ProxyConfig) *Proxy {
	p := &Proxy{
		credStore:          cfg.CredStore,
		allowlist:          cfg.Allowlist,
		scrubber:           cfg.Scrubber,
		logger:             cfg.Logger,
		freeMode:           cfg.FreeMode,
		allowPrivate:       cfg.AllowPrivate,
		onEgress:           cfg.OnEgress,
		onLLMCall:          cfg.OnLLMCall,
		resolveLLMIdentity: cfg.ResolveLLMIdentity,
		billingMode:        cfg.BillingMode,
		subPlan:            cfg.SubscriptionPlan,
		buildHash:          cfg.BuildHash,
		policyDomainsHash:  cfg.PolicyDomainsHash,
		tokenFP:            cfg.TokenFP,
		configFingerprint:  cfg.ConfigFingerprint,
		dnsCache:           newDNSPositiveCache(ssrfDNSCacheTTL),
		dnsResolve:         net.DefaultResolver.LookupIPAddr,
		dialer:             &net.Dialer{Timeout: 10 * time.Second, KeepAlive: 30 * time.Second},
	}
	if cfg.Transport != nil {
		p.transport = cfg.Transport
	} else {
		p.transport = &http.Transport{
			// #961: resolve-then-pin SSRF guard. The allowlist matches a
			// hostname string; this closes the DNS-rebinding gap by checking
			// every resolved IP at dial time and connecting to that exact IP.
			// FreeMode is the operator's explicit opt-out of egress limits, so
			// the guard permits private targets there too (no free-mode regression);
			// the fence's teeth are in restricted mode, where the local-model
			// endpoint path lives. Shares p.dnsCache with handleConnect (#1139).
			DialContext:         p.dialSSRF,
			MaxIdleConns:        100,
			IdleConnTimeout:     90 * time.Second,
			TLSHandshakeTimeout: 10 * time.Second,
		}
	}
	return p
}

// dialSSRF is the Proxy's shared resolve-then-pin SSRF dialer. Both the HTTP
// transport (handleHTTP / handleReverseProxy, via http.Transport.DialContext)
// and the CONNECT tunnel path (handleConnect) call this method so they share
// ONE dnsPositiveCache instance instead of each building its own.
func (p *Proxy) dialSSRF(ctx context.Context, network, addr string) (net.Conn, error) {
	return ssrfDial(ctx, network, addr, p.allowPrivate || p.freeMode, p.dnsResolve, p.dnsCache, p.dialer)
}

// ssrfDialContext returns a DialContext that resolves the target host,
// refuses any resolved IP a workspace endpoint must never reach (link-local
// / cloud metadata / reserved always; RFC1918 / loopback unless allowPrivate),
// then connects to the exact validated IP so a second resolution can't
// rebind to an internal address between the check and the dial.
func ssrfDialContext(allowPrivate bool) func(ctx context.Context, network, addr string) (net.Conn, error) {
	return ssrfDialContextWithResolver(allowPrivate, net.DefaultResolver.LookupIPAddr)
}

// resolveFunc resolves a host to a set of IP addresses. It matches
// net.Resolver.LookupIPAddr so the production path uses the default resolver
// and tests can inject a counting/stub resolver.
type resolveFunc func(ctx context.Context, host string) ([]net.IPAddr, error)

// ssrfDNSCacheTTL bounds how long a successful resolution is reused on the dial
// hot path. Short enough that a legitimately changed record is picked up
// quickly; long enough to spare a chatty agent a lookup on every cold dial.
const ssrfDNSCacheTTL = 30 * time.Second

// dnsCacheMaxEntries hard-caps the positive DNS cache (#1139 review). In free
// network mode the agent chooses the hostnames it asks the sidecar to dial
// (e.g. a wildcard-DNS domain gives it one distinct hostname per request), so
// without a cap the map would grow without bound and slowly OOM the
// credential-holding sidecar process. 512 comfortably covers realistic
// distinct-upstream-host counts for a single agent session.
const dnsCacheMaxEntries = 512

// dnsCacheEntry is a cached positive resolution with its expiry.
type dnsCacheEntry struct {
	ips    []net.IPAddr
	expiry time.Time
}

// dnsPositiveCache caches successful host→IP resolutions for a short TTL on the
// SSRF dial path (#1081). It caches ONLY the resolution — never the block
// decision. Every dial re-validates the (possibly cached) IPs against the
// endpoint blocklist and pins the connection to a validated IP, so the
// resolve-then-pin anti-rebind property is unchanged: we still only ever dial
// an IP we validated on this call. Failed lookups are not cached.
//
// The map is bounded by dnsCacheMaxEntries (#1139 review): unbounded growth
// under agent-controlled hostnames is a slow memory-exhaustion path for a
// process that also holds decrypted credentials in memory.
type dnsPositiveCache struct {
	mu  sync.Mutex
	ttl time.Duration
	m   map[string]dnsCacheEntry
}

func newDNSPositiveCache(ttl time.Duration) *dnsPositiveCache {
	return &dnsPositiveCache{ttl: ttl, m: make(map[string]dnsCacheEntry)}
}

// resolve returns cached IPs for host when a fresh entry exists, else calls fn
// and caches a successful result. Errors are propagated and never cached.
func (c *dnsPositiveCache) resolve(ctx context.Context, host string, fn resolveFunc) ([]net.IPAddr, error) {
	now := time.Now()
	c.mu.Lock()
	if e, ok := c.m[host]; ok && now.Before(e.expiry) {
		ips := e.ips
		c.mu.Unlock()
		return ips, nil
	}
	c.mu.Unlock()

	ips, err := fn(ctx, host)
	if err != nil {
		return nil, err
	}
	c.mu.Lock()
	c.insertLocked(host, dnsCacheEntry{ips: ips, expiry: now.Add(c.ttl)}, now)
	c.mu.Unlock()
	return ips, nil
}

// insertLocked stores entry under host, making room first if the cache is at
// its cap and host isn't already a key (an overwrite of an existing key never
// grows the map, so it never needs to evict). Must be called with c.mu held.
func (c *dnsPositiveCache) insertLocked(host string, entry dnsCacheEntry, now time.Time) {
	if _, exists := c.m[host]; !exists && len(c.m) >= dnsCacheMaxEntries {
		c.evictToFitLocked(now)
	}
	c.m[host] = entry
}

// evictToFitLocked frees at least one slot: first by dropping every entry
// that has already expired (a cheap, always-correct reclaim — an expired
// entry is dead weight, never served by resolve's expiry check above), then,
// if the cache is still at cap, by dropping arbitrary entries. Go randomizes
// map iteration order per run, so that second pass doubles as the "oldest/
// random" fallback the review asked for without needing a separate LRU
// structure on this hot path. Must be called with c.mu held.
func (c *dnsPositiveCache) evictToFitLocked(now time.Time) {
	for h, e := range c.m {
		if !now.Before(e.expiry) {
			delete(c.m, h)
		}
	}
	for h := range c.m {
		if len(c.m) < dnsCacheMaxEntries {
			break
		}
		delete(c.m, h)
	}
}

// ssrfDial resolves host (via cache, falling back to resolve on a miss),
// re-validates every resolved IP against the SSRF blocklist, and dials the
// first validated IP. Shared by the Proxy's transport DialContext and
// handleConnect (via Proxy.dialSSRF) so both paths reuse the same cache
// instance, and by the standalone ssrfDialContext helpers used directly by
// tests.
func ssrfDial(ctx context.Context, network, addr string, allowPrivate bool, resolve resolveFunc, cache *dnsPositiveCache, dialer *net.Dialer) (net.Conn, error) {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return nil, fmt.Errorf("sidecar: invalid dial address %q: %w", addr, err)
	}
	ips, err := cache.resolve(ctx, host, resolve)
	if err != nil {
		return nil, fmt.Errorf("sidecar: DNS resolution failed for %s: %w", host, err)
	}
	if len(ips) == 0 {
		return nil, fmt.Errorf("sidecar: no addresses for %s", host)
	}
	// Re-validate on EVERY dial, including cache hits — the cache holds the
	// resolution, not the verdict. This preserves the SSRF guarantee even
	// if a record was cached moments before a policy/blocklist evaluation.
	for _, ip := range ips {
		if httpsafe.IsBlockedIPForEndpoint(ip.IP, allowPrivate) {
			return nil, fmt.Errorf("sidecar: refusing to dial blocked address %s (host %s)", ip.IP, host)
		}
	}
	return dialer.DialContext(ctx, network, net.JoinHostPort(ips[0].IP.String(), port))
}

// ssrfDialContextWithResolver is ssrfDialContext with an injectable resolver so
// the DNS positive cache and blocklist re-validation can be unit-tested without
// real network lookups. Each call gets its own dialer + cache — callers that
// want cache sharing across multiple dials/paths (the Proxy itself) use
// Proxy.dialSSRF instead, which holds one long-lived cache.
func ssrfDialContextWithResolver(allowPrivate bool, resolve resolveFunc) func(ctx context.Context, network, addr string) (net.Conn, error) {
	dialer := &net.Dialer{Timeout: 10 * time.Second, KeepAlive: 30 * time.Second}
	cache := newDNSPositiveCache(ssrfDNSCacheTTL)
	return func(ctx context.Context, network, addr string) (net.Conn, error) {
		return ssrfDial(ctx, network, addr, allowPrivate, resolve, cache, dialer)
	}
}

// ServeHTTP handles both CONNECT (HTTPS tunnel) and plain HTTP proxy requests.
func (p *Proxy) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodConnect {
		p.handleConnect(w, r)
		return
	}
	p.handleHTTP(w, r)
}

// handleHTTP handles plain HTTP proxy requests (agent sets HTTP_PROXY).
// This is the primary path for ANTHROPIC_BASE_URL=http://localhost:9119.
func (p *Proxy) handleHTTP(w http.ResponseWriter, r *http.Request) {
	host := r.Host
	if host == "" {
		host = r.URL.Host
	}

	// Requests to localhost are internal control-plane calls (health, the
	// reverse-proxy provider paths).
	//
	// Gated on BOTH the Host header parsing as localhost AND the underlying
	// TCP connection coming from a loopback IP — the same pair server.go's
	// buildHandler has used since Patch-E, for the same reason: the Host
	// header is attacker-controllable over a shared crew bridge
	// (`curl --resolve localhost:9119:172.18.0.5 …`) and the sidecar's
	// loopback bind was the only thing standing behind it. Not exploitable
	// today — the sidecar binds 127.0.0.1:9119 inside the agent's own network
	// namespace — but this handler now selects an upstream and injects a
	// credential from a descriptor table, so the gate in front of it should
	// not be weaker than the one in front of /credentials.
	if isLocalhost(host) && remoteIsLoopback(r) {
		p.handleLocal(w, r)
		return
	}

	if !p.freeMode && !p.allowlist.IsAllowed(host) {
		p.logger.Warn("blocked request to non-allowed domain", "host", host)
		// Make the denial LOUD: emit a network.egress journal entry so a
		// restricted crew's blocked traffic surfaces in Crow's Nest, not just
		// the sidecar log (the operator can then add the host to allowed_domains).
		if p.onEgress != nil {
			p.onEgress(host, r.Method, "", http.StatusForbidden, true)
		}
		http.Error(w, "domain not allowed", http.StatusForbidden)
		return
	}

	// Inject credentials for known LLM providers. MatchHost only ever
	// resolves the three grandfathered specs (see providerForHost) — a
	// credential-era provider is reached by path prefix, not by host.
	spec, isLLM := llmroute.MatchHost(strings.ToLower(stripPort(host)))
	provider := ""
	actorID := ""
	if isLLM {
		var allowed bool
		actorID, allowed = p.authorizeLLMRoute(w, r)
		if !allowed {
			return
		}
		provider = spec.ID
		// The ACTING agent, not the store, decides which credential answers
		// (#2052): the store is crew-wide, and a credential an operator granted
		// to one member must not be handed to a sibling because a round-robin
		// counter landed on it.
		cred := p.credStore.Select(ProviderType(spec.ID), actorID)
		if cred == nil {
			p.logger.Error("no credential available", "provider", provider, "agent_id", actorID)
			http.Error(w, "no credential available for "+provider, http.StatusServiceUnavailable)
			return
		}
		llmroute.ApplyAuth(r, spec, cred.Token, cred.Headers)
		p.logger.Debug("credential injected",
			"provider", provider,
			"credential_id", cred.ID,
			"host", host,
			"method", r.Method,
			"path", r.URL.Path,
		)
	}

	// SECURITY: Limit request body size to prevent OOM attacks.
	// LLM API requests are typically <1MB; 10MB is generous.
	if r.Body != nil {
		r.Body = http.MaxBytesReader(w, r.Body, maxRequestBodyBytes)
	}

	// Forward the request
	outReq := r.Clone(r.Context())
	outReq.RequestURI = ""
	if outReq.URL.Scheme == "" {
		outReq.URL.Scheme = "https"
	}
	outReq.URL.Host = host

	// SECURITY: Strip hop-by-hop headers per RFC 2616 Section 13.5.1.
	// Proxy-Authorization is especially dangerous (data exfiltration vector).
	for _, h := range hopByHopHeaders {
		outReq.Header.Del(h)
	}

	resp, err := p.transport.RoundTrip(outReq)
	if err != nil {
		p.logger.Error("upstream request failed", "host", host, "error", err)
		http.Error(w, "upstream request failed", http.StatusBadGateway)
		// Still notify the observer so Crow's Nest surfaces failed egress
		// too — otherwise a flapping outbound endpoint looks like silent
		// success from the journal's perspective. statusCode 0 marks the
		// "transport error" case distinctly from any HTTP 5xx response.
		if p.onEgress != nil {
			p.onEgress(host, r.Method, provider, 0, false)
		}
		return
	}
	defer resp.Body.Close()

	// Fire the egress observer BEFORE streaming the body so a slow
	// upstream doesn't delay the Crow's Nest event. Passing only host /
	// method / provider / status keeps PII and credentials out of the
	// journal — path and body are deliberately excluded.
	if p.onEgress != nil {
		p.onEgress(host, r.Method, provider, resp.StatusCode, false)
	}

	// Copy response headers
	for k, vv := range resp.Header {
		for _, v := range vv {
			w.Header().Add(k, v)
		}
	}
	w.WriteHeader(resp.StatusCode)
	p.copyAndObserveLLM(w, resp, spec.BodyCodec, spec.LedgerProvider, actorID)
}

// handleConnect handles HTTPS CONNECT tunnel requests.
// The sidecar checks the domain allowlist but does NOT inject credentials
// into HTTPS tunnels (the agent must use HTTP_PROXY path for credential injection).
func (p *Proxy) handleConnect(w http.ResponseWriter, r *http.Request) {
	host := r.Host

	if !p.freeMode && !p.allowlist.IsAllowed(host) {
		p.logger.Warn("blocked CONNECT to non-allowed domain", "host", host)
		if p.onEgress != nil {
			p.onEgress(host, http.MethodConnect, "", http.StatusForbidden, true)
		}
		http.Error(w, "domain not allowed", http.StatusForbidden)
		return
	}

	// Establish TCP tunnel through the resolve-then-pin SSRF guard (#961):
	// an allowlisted hostname whose DNS now points at 169.254.169.254 /
	// RFC1918 / loopback is refused here even though the string matched.
	// Uses p.dialSSRF (shared dnsPositiveCache) rather than building a fresh
	// cache per CONNECT — the earlier per-request cache meant the positive
	// DNS cache never got a hit on this path (#1139 review).
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	targetConn, err := p.dialSSRF(ctx, "tcp", host)
	if err != nil {
		p.logger.Error("CONNECT dial failed", "host", host, "error", err)
		http.Error(w, "failed to connect", http.StatusBadGateway)
		if p.onEgress != nil {
			p.onEgress(host, http.MethodConnect, "", 0, false)
		}
		return
	}

	// Crow's Nest: one egress event per successful tunnel setup.
	// CONNECT hides the eventual method / status inside TLS, so we record
	// 200 as the setup result. The event marks "agent opened an HTTPS
	// connection to host X" which is the level of resolution Crow's Nest
	// needs — we deliberately do NOT decrypt or inspect the tunnel.
	if p.onEgress != nil {
		p.onEgress(host, http.MethodConnect, "", http.StatusOK, false)
	}

	w.WriteHeader(http.StatusOK)

	hijacker, ok := w.(http.Hijacker)
	if !ok {
		targetConn.Close()
		http.Error(w, "hijacking not supported", http.StatusInternalServerError)
		return
	}
	clientConn, clientBuf, err := hijacker.Hijack()
	if err != nil {
		targetConn.Close()
		http.Error(w, "hijack failed", http.StatusInternalServerError)
		return
	}

	// The hijacked bufio.Reader may already hold client bytes the HTTP server
	// read past the CONNECT request line (a client that pipelines its first
	// tunnel payload — TLS ClientHello, or a raw write — into the same segment
	// as the CONNECT). Those bytes live in clientBuf, NOT in the raw socket, so
	// splicing clientConn directly would drop them and the tunnel would stall
	// until a deadline (the intermittent `tunnel read: i/o timeout`, #892).
	// Flush any buffered remainder to the target before raw splicing.
	if n := clientBuf.Reader.Buffered(); n > 0 {
		if pending, perr := clientBuf.Reader.Peek(n); perr == nil {
			if _, werr := targetConn.Write(pending); werr != nil {
				p.logger.Error("CONNECT flush buffered client bytes failed", "host", host, "error", werr)
			}
		}
	}

	go transfer(targetConn, clientConn)
	go transfer(clientConn, targetConn)
}

// handleLocal handles requests to localhost (health check, LLM reverse proxies).
//
// The provider arms used to be a hardcoded prefix switch (/gemini/, /openai/,
// /v1/, in that order — Anthropic's /v1/ is a catch-all, so anything sharing
// its prefix had to be listed ABOVE it). That ordering hazard is what made a
// fourth provider unsafe to add by hand, so routing is now one
// longest-prefix-wins lookup over llmroute.Specs(): /v1 → ANTHROPIC,
// /openai → OPENAI, /gemini → GOOGLE, /llm/{id} → everything registered since.
// /health keeps its own arm ahead of the lookup because it is control plane,
// not a provider.
func (p *Proxy) handleLocal(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path == "/health" || r.URL.Path == "/healthz" {
		p.writeHealth(w)
		return
	}
	if s, ok := llmroute.MatchPath(r.URL.Path); ok {
		p.reverseProxyToProvider(w, r, s)
		return
	}
	http.Error(w, "not found", http.StatusNotFound)
}

// healthPayload is the /health response. It is a struct rather than the
// fmt.Fprintf template it replaced so a field can't drift out of sync with its
// format verb, and the field ORDER is load-bearing: the first eight keys must
// serialise byte-identically to the pre-descriptor payload (the orchestrator's
// restart-skip and the stale-sidecar / orphan-token checks all read it, and two
// tests assert on raw substrings). provider_creds is appended last.
type healthPayload struct {
	Status            string         `json:"status"`
	AnthropicCreds    int            `json:"anthropic_creds"`
	OpenAICreds       int            `json:"openai_creds"`
	GoogleCreds       int            `json:"google_creds"`
	NetworkMode       string         `json:"network_mode"`
	SidecarHash       string         `json:"sidecar_hash"`
	DomainsHash       string         `json:"domains_hash"`
	TokenFP           string         `json:"token_fp"`
	ProviderCreds     map[string]int `json:"provider_creds"`
	ConfigFingerprint string         `json:"config_fingerprint,omitempty"`
}

// writeHealth emits the /health payload. Credential counts come from ONE
// CredStore pass (CountsByProvider) rather than one Count call per provider —
// with a descriptor table that grows, the old shape was N locks and N scans on
// a polled endpoint.
//
// The three legacy `*_creds` fields are derived from each spec's
// LegacyHealthKey rather than hardcoded, so they stay wired to the providers
// they name without inviting a fourth `openrouter_creds` sibling: a new
// provider is reported only under provider_creds.
func (p *Proxy) writeHealth(w http.ResponseWriter) {
	networkMode := "free"
	if !p.freeMode {
		networkMode = "restricted"
	}

	counts := p.credStore.CountsByProvider()
	// One call, not two: Specs() deep-copies every spec's Hosts, KeyEnvVars,
	// StaticHeaders and AuthRules, and /health is polled. Taking the length
	// from a second call cloned the whole table again for an int.
	specs := llmroute.Specs()
	legacy := make(map[string]int, 3)
	perProvider := make(map[string]int, len(specs))
	for _, s := range specs {
		perProvider[s.ID] = counts[s.ID]
		if s.LegacyHealthKey != "" {
			legacy[s.LegacyHealthKey] = counts[s.ID]
		}
	}

	body, err := json.Marshal(healthPayload{
		Status:            "ok",
		AnthropicCreds:    legacy["anthropic_creds"],
		OpenAICreds:       legacy["openai_creds"],
		GoogleCreds:       legacy["google_creds"],
		NetworkMode:       networkMode,
		SidecarHash:       p.buildHash,
		DomainsHash:       p.policyDomainsHash,
		TokenFP:           p.tokenFP,
		ProviderCreds:     perProvider,
		ConfigFingerprint: p.configFingerprint,
	})
	if err != nil {
		// Unreachable: every field is a string/int/map[string]int. Fail loudly
		// rather than emitting a truncated body a health checker would parse.
		p.logger.Error("health payload marshal failed", "error", err)
		http.Error(w, "health unavailable", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write(body)
}

// reverseProxyToProvider is the shared reverse-proxy body used by every
// provider's ANTHROPIC_BASE_URL-style plain-HTTP endpoint. Everything that
// used to be a per-provider argument (upstream host, strip prefix, auth
// header) now comes off the llmroute.Spec, so adding a provider is a table
// row rather than a new switch arm in three places.
//
// It injects the provider's key from the CredStore when one is present
// (overwriting any dummy the agent env carries); for the three grandfathered
// providers an empty store still forwards the request as-is, which is the
// Anthropic OAuth path — the agent already holds CLAUDE_CODE_OAUTH_TOKEN and
// the request arrives with Authorization: Bearer. A spec that declares
// RequireCredential refuses instead: for a credential-supplied upstream a nil
// credential means there is no upstream to forward to, and for a new provider
// there is no pre-existing pass-through behaviour to preserve.
//
// It reuses p.transport (whose DialContext is p.dialSSRF), so the resolve-
// then-pin SSRF guard and the #1139 shared DNS cache apply identically to
// every provider — no provider gets a weaker egress path than Anthropic.
func (p *Proxy) reverseProxyToProvider(w http.ResponseWriter, r *http.Request, s llmroute.Spec) {
	actorID, allowed := p.authorizeLLMRoute(w, r)
	if !allowed {
		return
	}
	// Scoped to the acting agent (#2052). This is the path that matters most:
	// OPENAI_COMPAT is reached here, and its upstream comes from the credential,
	// so a credential picked for the wrong member sends this agent's prompt to
	// another member's gateway with that member's key — allowlisted (the #2051
	// union covers it) and therefore silent.
	cred := p.credStore.Select(ProviderType(s.ID), actorID)
	// A nil credential means two different things now, and only one of them may
	// fall through. An EMPTY store for a spec that does not RequireCredential is
	// the pass-through this route has always had: the request goes upstream
	// unauthenticated (in practice carrying the agent's disposable dummy key)
	// and the vendor answers. A REFUSAL — the store holds one for this provider
	// and the acting agent was not granted it — must not take that path, or
	// #2052's silent crossover is replaced by a vendor 401 that blames the key,
	// which is no more diagnosable. Fail closed with the same 503 the
	// RequireCredential specs already give.
	if cred == nil && (s.RequireCredential || p.credStore.HeldForAnotherAgent(ProviderType(s.ID), actorID)) {
		p.logger.Error("no credential available for reverse proxy",
			"provider", s.ID, "path", r.URL.Path, "agent_id", actorID)
		http.Error(w, "no credential available for "+s.ID, http.StatusServiceUnavailable)
		return
	}

	var credBaseURL string
	var credHeaders map[string]string
	if cred != nil {
		credBaseURL, credHeaders = cred.BaseURL, cred.Headers
	}
	up, err := llmroute.ResolveUpstream(s, credBaseURL)
	if err != nil {
		// Only reachable for an UpstreamFromCredential spec: the fixed-host
		// specs carry a compile-time-valid host. A credential that stored a
		// malformed base URL is refused here rather than dialled — but note
		// this is validation, NOT the egress control; the two gates below are.
		p.logger.Error("reverse proxy upstream unresolvable", "provider", s.ID, "error", err)
		http.Error(w, "upstream not configured", http.StatusBadGateway)
		return
	}

	// Crew egress fence for a credential-supplied upstream. The three fixed-host
	// providers make ZERO allowlist calls (their host is a compile-time literal
	// that was never checked here), so their behaviour is untouched. For an
	// operator-supplied host this check is what stops a credential from being
	// an unmetered egress primitive: without it, "paste a base URL" would let
	// any crew reach any host the sidecar can dial, bypassing the allowlist
	// that governs every other outbound request the agent makes.
	//
	// This is layer 2 of 3. Create-time URL validation (crewshipd) cannot be
	// the control because DNS can rebind between validate and dial, and because
	// the crew-scoped decision isn't knowable then. Layer 3 is p.dialSSRF, which
	// resolves-then-pins and refuses link-local / cloud-metadata / reserved
	// unconditionally and RFC1918 / loopback unless the crew opted in.
	if s.UpstreamFromCredential && !p.freeMode && !p.allowlist.IsAllowed(up.Host) {
		p.logger.Warn("blocked reverse-proxy upstream not on the crew allowlist", "provider", s.ID, "host", up.Host)
		if p.onEgress != nil {
			p.onEgress(up.Host, r.Method, s.ID, http.StatusForbidden, true)
		}
		http.Error(w, "domain not allowed", http.StatusForbidden)
		return
	}

	if cred != nil {
		llmroute.ApplyAuth(r, s, cred.Token, credHeaders)
		p.logger.Debug("api key injected for reverse proxy",
			"provider", s.ID,
			"credential_id", cred.ID,
			"path", r.URL.Path,
		)
	}

	if r.Body != nil {
		r.Body = http.MaxBytesReader(w, r.Body, maxRequestBodyBytes)
	}

	outReq := r.Clone(r.Context())
	outReq.RequestURI = ""
	outReq.URL.Scheme = up.Scheme
	outReq.URL.Host = up.Host
	outReq.Host = up.Host
	outReq.URL.Path = llmroute.OutboundPath(s, up, outReq.URL.Path)
	outReq.URL.RawQuery = llmroute.OutboundQuery(up.BaseQuery, outReq.URL.RawQuery)
	if s.StripPrefix || up.BasePath != "" {
		// RawPath is an optional escaped hint; clearing it makes URL.String()
		// re-derive the request-target from the (now-rewritten) Path so the
		// prefix can't survive via a stale RawPath. Left alone when the path
		// passes through verbatim (Anthropic), because clearing it there would
		// silently un-escape a path the old code forwarded byte-for-byte.
		outReq.URL.RawPath = ""
	}

	for _, h := range hopByHopHeaders {
		outReq.Header.Del(h)
	}

	resp, err := p.transport.RoundTrip(outReq)
	if err != nil {
		p.logger.Error("reverse proxy upstream failed", "provider", s.ID, "host", up.Host, "path", r.URL.Path, "error", err)
		http.Error(w, "upstream request failed", http.StatusBadGateway)
		if p.onEgress != nil {
			p.onEgress(up.Host, r.Method, s.ID, 0, false)
		}
		return
	}
	defer resp.Body.Close()

	if p.onEgress != nil {
		p.onEgress(up.Host, r.Method, s.ID, resp.StatusCode, false)
	}

	for k, vv := range resp.Header {
		for _, v := range vv {
			w.Header().Add(k, v)
		}
	}
	w.WriteHeader(resp.StatusCode)
	p.copyAndObserveLLM(w, resp, s.BodyCodec, s.LedgerProvider, actorID)
}

// authorizeLLMRoute authenticates the disposable provider key before the
// CredStore overwrites it. Once a sidecar advertises a keyed configuration,
// absence is not a legacy fallback: every process in a shared crew container
// can reach loopback, so accepting a token-less request would let one agent
// deliberately consume another agent's currently-loaded provider credential.
func (p *Proxy) authorizeLLMRoute(w http.ResponseWriter, r *http.Request) (string, bool) {
	if p.resolveLLMIdentity == nil {
		if p.configFingerprint != "" {
			http.Error(w, "sidecar route identity unavailable", http.StatusServiceUnavailable)
			return "", false
		}
		return "", true
	}

	actorID, routeFingerprint, present, ok := p.resolveLLMIdentity(r)
	if !present {
		if p.configFingerprint != "" {
			http.Error(w, "missing agent route token", http.StatusForbidden)
			return "", false
		}
		return "", true
	}
	if !ok {
		http.Error(w, "invalid agent route token", http.StatusForbidden)
		return "", false
	}
	if p.configFingerprint != "" && routeFingerprint != p.configFingerprint {
		// A concurrent run can outlive the sidecar instance it started. Once
		// another agent restarts the shared sidecar with a different credential
		// set, fail closed instead of silently sending this agent's prompt to the
		// other agent's endpoint/key (#2052).
		http.Error(w, "sidecar credential configuration changed; retry the run", http.StatusServiceUnavailable)
		return "", false
	}
	return actorID, true
}

// copyAndObserveLLM streams the upstream response body to the client and,
// when the upstream is a known LLM provider returning JSON or SSE, also parses
// usage / quota and fires the OnLLMCall observer.
//
// SSE is tee'd while it streams, so the client receives each byte immediately;
// parsing happens only after EOF. Non-LLM hosts skip the buffer path entirely.
//
// Body buffering is bounded by maxRequestBodyBytes (10 MB) — the same cap
// that protects the request path, applied here to the response so a
// pathological upstream can't OOM the sidecar.
//
// The two provider-ish arguments are NOT interchangeable. `codec` is the
// response body SHAPE (llmroute.Spec.BodyCodec) the parser switches on;
// `ledgerProvider` is the lowercase paymaster rate-card key
// (Spec.LedgerProvider) stamped onto the usage row. OpenRouter is why they
// are separate — OpenAI-shaped bodies, its own rate card.
func (p *Proxy) copyAndObserveLLM(w http.ResponseWriter, resp *http.Response, codec, ledgerProvider, actorID string) {
	// Bail out fast for non-LLM traffic or when nobody's listening for usage.
	if ledgerProvider == "" || p.onLLMCall == nil {
		_, _ = io.Copy(w, resp.Body)
		return
	}

	contentType := resp.Header.Get("Content-Type")
	if !isJSONResponse(contentType) {
		var usage LLMUsage
		if strings.HasPrefix(strings.ToLower(contentType), "text/event-stream") {
			buf := &boundedBuffer{cap: maxRequestBodyBytes}
			_, _ = io.Copy(w, io.TeeReader(resp.Body, buf))
			usage = parseLLMUsageSSE(codec, buf.String())
		} else {
			_, _ = io.Copy(w, resp.Body)
		}
		usage.AgentID = actorID
		usage.Provider = ledgerProvider
		quota := parseQuotaInfo(resp.Header, resp.StatusCode)
		if usage.InputTokens != 0 || usage.OutputTokens != 0 || usage.CachedInputTokens != 0 ||
			usage.CacheCreationTokens != 0 || quota.Window != "" || quota.HadStatus429 {
			p.onLLMCall(usage, quota, p.billingMode, p.subPlan)
		}
		return
	}

	// Non-streaming JSON: tee through a bounded buffer so we keep streaming
	// to the client while accumulating bytes for the parser. Using
	// io.MultiWriter with a bytes.Buffer would buffer fully before flushing,
	// which surfaces as latency to the agent — io.TeeReader is the right
	// shape: read once, write twice.
	buf := &boundedBuffer{cap: maxRequestBodyBytes}
	tee := io.TeeReader(resp.Body, buf)
	if _, err := io.Copy(w, tee); err != nil {
		// Client disconnected or upstream cut off mid-stream. We still try
		// to parse whatever we've got — partial JSON returns zero usage,
		// which is fine.
		p.logger.Debug("response copy interrupted", "provider", ledgerProvider, "error", err)
	}

	usage := parseLLMUsage(codec, buf.String())
	usage.AgentID = actorID
	usage.Provider = ledgerProvider
	quota := parseQuotaInfo(resp.Header, resp.StatusCode)
	p.onLLMCall(usage, quota, p.billingMode, p.subPlan)
}

// boundedBuffer is a Write target that drops bytes once it hits cap. We use
// it for the response-body tee so a pathological multi-megabyte response
// can't blow past the size guard while we're parsing for usage.
type boundedBuffer struct {
	buf []byte
	cap int
}

func (b *boundedBuffer) Write(p []byte) (int, error) {
	room := b.cap - len(b.buf)
	if room <= 0 {
		return len(p), nil // accept the write but discard
	}
	if len(p) > room {
		b.buf = append(b.buf, p[:room]...)
		return len(p), nil
	}
	b.buf = append(b.buf, p...)
	return len(p), nil
}

func (b *boundedBuffer) String() string { return string(b.buf) }

// injectCredential is gone: the per-provider auth switch it held is now
// llmroute.ApplyAuth over the spec's AuthRules. Beyond removing the third
// place a new provider had to be spelled out, that closes a fail-open default
// — CURSOR and FACTORY credentials fell off the end of the three-arm switch to
// a silent no-op and the request was forwarded upstream unauthenticated. A
// spec with no matching rule is impossible by construction (registration
// enforces exactly one default rule), and CURSOR/FACTORY have no spec, so they
// never reach a proxy path at all.

func transfer(dst io.WriteCloser, src io.ReadCloser) {
	defer dst.Close()
	defer src.Close()
	io.Copy(dst, src)
}

// remoteIsLoopback reports whether the request's underlying TCP source
// IP is a loopback address. Distinct from isLocalhost, which only
// inspects the Host header — the Host header is attacker-controllable
// via --resolve tricks when crew bridges aren't network-isolated.
// Sidecar control-plane handlers must gate on BOTH so a peer crew's
// agent can't hit /credentials over the shared bridge.
func remoteIsLoopback(r *http.Request) bool {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	return isLoopbackIP(host)
}

func isLocalhost(host string) bool {
	h := host
	// Handle IPv6 bracket notation [::1]:port
	if strings.HasPrefix(h, "[") {
		if idx := strings.Index(h, "]"); idx != -1 {
			h = h[1:idx]
			return isLoopbackIP(h)
		}
	}
	// Handle host:port -- only strip if exactly one colon (not IPv6)
	if idx := strings.LastIndex(h, ":"); idx != -1 {
		if strings.Count(h, ":") == 1 {
			h = h[:idx]
		}
	}
	if h == "localhost" || h == "localhost.localdomain" {
		return true
	}
	return isLoopbackIP(h)
}

// isLoopbackIP checks if an IP string is a loopback address.
// Covers: 127.0.0.0/8 (entire range), ::1, 0:0:0:0:0:0:0:1
func isLoopbackIP(s string) bool {
	// Fast-path: loopback IPs only contain digits, '.', ':', and hex letters
	// (a–f / A–F). Any other character means `s` cannot be an IP, so we can
	// short-circuit and skip net.ParseIP — which otherwise allocates a 16-byte
	// IP buffer even on parse failure.
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c >= '0' && c <= '9':
		case c >= 'a' && c <= 'f':
		case c >= 'A' && c <= 'F':
		case c == '.' || c == ':':
		default:
			return false
		}
	}
	ip := net.ParseIP(s)
	if ip == nil {
		return false
	}
	return ip.IsLoopback()
}
