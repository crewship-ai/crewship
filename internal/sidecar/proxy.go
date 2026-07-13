package sidecar

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/crewship-ai/crewship/internal/httpsafe"
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
	credStore    *CredStore
	allowlist    *DomainAllowlist
	scrubber     *scrubber.Scrubber
	logger       *slog.Logger
	transport    http.RoundTripper
	freeMode     bool
	allowPrivate bool // #961: permit RFC1918/loopback dial targets (crew opt-in); link-local/metadata always blocked
	onEgress     EgressObserver
	onLLMCall    LLMCallObserver
	billingMode  string // "metered" | "flat_rate" | "" — set from env at startup
	subPlan      string // human label for flat-rate (e.g. "Anthropic Max 20×")
	buildHash    string // #1008: content hash of the running sidecar binary, advertised on /health

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
}

// NewProxy creates a forward proxy with credential injection.
func NewProxy(cfg ProxyConfig) *Proxy {
	p := &Proxy{
		credStore:    cfg.CredStore,
		allowlist:    cfg.Allowlist,
		scrubber:     cfg.Scrubber,
		logger:       cfg.Logger,
		freeMode:     cfg.FreeMode,
		allowPrivate: cfg.AllowPrivate,
		onEgress:     cfg.OnEgress,
		onLLMCall:    cfg.OnLLMCall,
		billingMode:  cfg.BillingMode,
		subPlan:      cfg.SubscriptionPlan,
		buildHash:    cfg.BuildHash,
		dnsCache:     newDNSPositiveCache(ssrfDNSCacheTTL),
		dnsResolve:   net.DefaultResolver.LookupIPAddr,
		dialer:       &net.Dialer{Timeout: 10 * time.Second, KeepAlive: 30 * time.Second},
	}
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

	// Requests to localhost are internal control-plane calls (health, etc.)
	if isLocalhost(host) {
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

	// Inject credentials for known LLM providers
	provider := providerForHost(host)
	if provider != "" {
		cred := p.credStore.Select(provider)
		if cred == nil {
			p.logger.Error("no credential available", "provider", provider)
			http.Error(w, "no credential available for "+string(provider), http.StatusServiceUnavailable)
			return
		}
		injectCredential(r, provider, cred.Token)
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
			p.onEgress(host, r.Method, string(provider), 0, false)
		}
		return
	}
	defer resp.Body.Close()

	// Fire the egress observer BEFORE streaming the body so a slow
	// upstream doesn't delay the Crow's Nest event. Passing only host /
	// method / provider / status keeps PII and credentials out of the
	// journal — path and body are deliberately excluded.
	if p.onEgress != nil {
		p.onEgress(host, r.Method, string(provider), resp.StatusCode, false)
	}

	// Copy response headers
	for k, vv := range resp.Header {
		for _, v := range vv {
			w.Header().Add(k, v)
		}
	}
	w.WriteHeader(resp.StatusCode)
	p.copyAndObserveLLM(w, resp, string(provider))
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

// handleLocal handles requests to localhost (health check, Anthropic reverse proxy).
func (p *Proxy) handleLocal(w http.ResponseWriter, r *http.Request) {
	switch {
	case r.URL.Path == "/health" || r.URL.Path == "/healthz":
		networkMode := "free"
		if !p.freeMode {
			networkMode = "restricted"
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"status":"ok","anthropic_creds":%d,"openai_creds":%d,"google_creds":%d,"network_mode":"%s","sidecar_hash":"%s"}`,
			p.credStore.Count(ProviderAnthropic),
			p.credStore.Count(ProviderOpenAI),
			p.credStore.Count(ProviderGoogle),
			networkMode,
			p.buildHash,
		)
	case strings.HasPrefix(r.URL.Path, "/v1/"):
		// Reverse-proxy to api.anthropic.com.
		// This handles the ANTHROPIC_BASE_URL=http://127.0.0.1:9119 mode where
		// Claude Code sends API requests directly to the sidecar over plain HTTP.
		// For OAuth tokens the request already carries Authorization: Bearer;
		// for API keys we inject x-api-key from the CredStore.
		p.handleReverseProxy(w, r)
	default:
		http.Error(w, "not found", http.StatusNotFound)
	}
}

// handleReverseProxy reverse-proxies a request to api.anthropic.com.
// It injects an API key from the CredStore when available (API key mode).
// For OAuth token mode, CLAUDE_CODE_OAUTH_TOKEN is already set in the container env
// so the request already carries Authorization: Bearer — no injection needed.
func (p *Proxy) handleReverseProxy(w http.ResponseWriter, r *http.Request) {
	// Inject API key from CredStore if present (overwrites any dummy key from agent env).
	// If CredStore is empty the request is forwarded as-is (OAuth Bearer auth path).
	cred := p.credStore.Select(ProviderAnthropic)
	if cred != nil {
		injectCredential(r, ProviderAnthropic, cred.Token)
		p.logger.Debug("api key injected for reverse proxy",
			"credential_id", cred.ID,
			"path", r.URL.Path,
		)
	}

	if r.Body != nil {
		r.Body = http.MaxBytesReader(w, r.Body, maxRequestBodyBytes)
	}

	outReq := r.Clone(r.Context())
	outReq.RequestURI = ""
	outReq.URL.Scheme = "https"
	outReq.URL.Host = "api.anthropic.com"
	outReq.Host = "api.anthropic.com"

	for _, h := range hopByHopHeaders {
		outReq.Header.Del(h)
	}

	resp, err := p.transport.RoundTrip(outReq)
	if err != nil {
		p.logger.Error("reverse proxy upstream failed", "path", r.URL.Path, "error", err)
		http.Error(w, "upstream request failed", http.StatusBadGateway)
		if p.onEgress != nil {
			p.onEgress("api.anthropic.com", r.Method, string(ProviderAnthropic), 0, false)
		}
		return
	}
	defer resp.Body.Close()

	if p.onEgress != nil {
		p.onEgress("api.anthropic.com", r.Method, string(ProviderAnthropic), resp.StatusCode, false)
	}

	for k, vv := range resp.Header {
		for _, v := range vv {
			w.Header().Add(k, v)
		}
	}
	w.WriteHeader(resp.StatusCode)
	p.copyAndObserveLLM(w, resp, string(ProviderAnthropic))
}

// copyAndObserveLLM streams the upstream response body to the client and,
// when the upstream is a known LLM provider returning a non-streaming JSON
// body, also parses usage / quota and fires the OnLLMCall observer.
//
// Streaming responses (text/event-stream) and non-LLM hosts skip the buffer
// path and pass through unmodified — buffering an SSE stream would defeat
// its low-latency UX, and we don't want to pay the buffer cost on generic
// HTTPS traffic that has nothing to do with billing.
//
// Body buffering is bounded by maxRequestBodyBytes (10 MB) — the same cap
// that protects the request path, applied here to the response so a
// pathological upstream can't OOM the sidecar.
func (p *Proxy) copyAndObserveLLM(w http.ResponseWriter, resp *http.Response, provider string) {
	// Bail out fast for non-LLM traffic or when nobody's listening for usage.
	if provider == "" || p.onLLMCall == nil {
		_, _ = io.Copy(w, resp.Body)
		return
	}

	contentType := resp.Header.Get("Content-Type")
	if !isJSONResponse(contentType) {
		// Streaming (SSE) or unknown shape — pass through, only fire quota
		// signal from headers so EnforceQuota still gets the most-restrictive
		// reading even when we can't see the body.
		_, _ = io.Copy(w, resp.Body)
		quota := parseQuotaInfo(resp.Header, resp.StatusCode)
		if quota.RemainingPct > 0 || quota.HadStatus429 {
			p.onLLMCall(LLMUsage{Provider: provider}, quota, p.billingMode, p.subPlan)
		}
		return
	}

	// Non-streaming JSON: tee through a bounded buffer so we keep streaming
	// to the client while accumulating bytes for the parser. Using
	// io.MultiWriter with a bytes.Buffer would buffer fully before flushing,
	// which surfaces as latency to the agent — io.TeeReader is the right
	// shape: read once, write twice.
	limited := http.MaxBytesReader(w, resp.Body, maxRequestBodyBytes)
	buf := &boundedBuffer{cap: maxRequestBodyBytes}
	tee := io.TeeReader(limited, buf)
	if _, err := io.Copy(w, tee); err != nil {
		// Client disconnected or upstream cut off mid-stream. We still try
		// to parse whatever we've got — partial JSON returns zero usage,
		// which is fine.
		p.logger.Debug("response copy interrupted", "provider", provider, "error", err)
	}

	usage := parseLLMUsage(provider, buf.String())
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

// injectCredential adds the appropriate authentication header for the LLM provider.
func injectCredential(r *http.Request, provider ProviderType, token string) {
	switch provider {
	case ProviderAnthropic:
		if strings.HasPrefix(token, "sk-ant-oat") {
			r.Header.Set("Authorization", "Bearer "+token)
		} else {
			r.Header.Set("x-api-key", token)
		}
		r.Header.Set("anthropic-version", "2023-06-01")
	case ProviderOpenAI:
		r.Header.Set("Authorization", "Bearer "+token)
	case ProviderGoogle:
		// Google uses ?key= query param or Authorization header
		q := r.URL.Query()
		q.Set("key", token)
		r.URL.RawQuery = q.Encode()
	}
}

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
