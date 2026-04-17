package sidecar

import (
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"time"

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
type EgressObserver func(host, method, provider string, statusCode int)

// Proxy is an HTTP forward proxy that intercepts agent outbound requests,
// injects LLM API credentials, and blocks non-allowed domains.
type Proxy struct {
	credStore *CredStore
	allowlist *DomainAllowlist
	scrubber  *scrubber.Scrubber
	logger    *slog.Logger
	transport http.RoundTripper
	freeMode  bool
	onEgress  EgressObserver
}

// ProxyConfig configures the sidecar proxy.
type ProxyConfig struct {
	CredStore *CredStore
	Allowlist *DomainAllowlist
	Scrubber  *scrubber.Scrubber
	Logger    *slog.Logger
	FreeMode  bool // When true, skip domain allowlist checks (allow all domains)
	// OnEgress is invoked after a successful upstream request. Optional —
	// leaving it nil disables observability emits. The proxy holds the
	// callback by reference (no copy), so installing a new observer
	// requires rebuilding the Proxy; for the sidecar's lifecycle that
	// happens at startup only, which keeps this lock-free on the hot path.
	OnEgress EgressObserver
}

// NewProxy creates a forward proxy with credential injection.
func NewProxy(cfg ProxyConfig) *Proxy {
	return &Proxy{
		credStore: cfg.CredStore,
		allowlist: cfg.Allowlist,
		scrubber:  cfg.Scrubber,
		logger:    cfg.Logger,
		freeMode:  cfg.FreeMode,
		onEgress:  cfg.OnEgress,
		transport: &http.Transport{
			DialContext: (&net.Dialer{
				Timeout:   10 * time.Second,
				KeepAlive: 30 * time.Second,
			}).DialContext,
			MaxIdleConns:        100,
			IdleConnTimeout:     90 * time.Second,
			TLSHandshakeTimeout: 10 * time.Second,
		},
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
			p.onEgress(host, r.Method, string(provider), 0)
		}
		return
	}
	defer resp.Body.Close()

	// Fire the egress observer BEFORE streaming the body so a slow
	// upstream doesn't delay the Crow's Nest event. Passing only host /
	// method / provider / status keeps PII and credentials out of the
	// journal — path and body are deliberately excluded.
	if p.onEgress != nil {
		p.onEgress(host, r.Method, string(provider), resp.StatusCode)
	}

	// Copy response headers
	for k, vv := range resp.Header {
		for _, v := range vv {
			w.Header().Add(k, v)
		}
	}
	w.WriteHeader(resp.StatusCode)
	io.Copy(w, resp.Body)
}

// handleConnect handles HTTPS CONNECT tunnel requests.
// The sidecar checks the domain allowlist but does NOT inject credentials
// into HTTPS tunnels (the agent must use HTTP_PROXY path for credential injection).
func (p *Proxy) handleConnect(w http.ResponseWriter, r *http.Request) {
	host := r.Host

	if !p.freeMode && !p.allowlist.IsAllowed(host) {
		p.logger.Warn("blocked CONNECT to non-allowed domain", "host", host)
		http.Error(w, "domain not allowed", http.StatusForbidden)
		return
	}

	// Establish TCP tunnel
	targetConn, err := net.DialTimeout("tcp", host, 10*time.Second)
	if err != nil {
		p.logger.Error("CONNECT dial failed", "host", host, "error", err)
		http.Error(w, "failed to connect", http.StatusBadGateway)
		if p.onEgress != nil {
			p.onEgress(host, http.MethodConnect, "", 0)
		}
		return
	}

	// Crow's Nest: one egress event per successful tunnel setup.
	// CONNECT hides the eventual method / status inside TLS, so we record
	// 200 as the setup result. The event marks "agent opened an HTTPS
	// connection to host X" which is the level of resolution Crow's Nest
	// needs — we deliberately do NOT decrypt or inspect the tunnel.
	if p.onEgress != nil {
		p.onEgress(host, http.MethodConnect, "", http.StatusOK)
	}

	w.WriteHeader(http.StatusOK)

	hijacker, ok := w.(http.Hijacker)
	if !ok {
		targetConn.Close()
		http.Error(w, "hijacking not supported", http.StatusInternalServerError)
		return
	}
	clientConn, _, err := hijacker.Hijack()
	if err != nil {
		targetConn.Close()
		http.Error(w, "hijack failed", http.StatusInternalServerError)
		return
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
		fmt.Fprintf(w, `{"status":"ok","anthropic_creds":%d,"openai_creds":%d,"google_creds":%d,"network_mode":"%s"}`,
			p.credStore.Count(ProviderAnthropic),
			p.credStore.Count(ProviderOpenAI),
			p.credStore.Count(ProviderGoogle),
			networkMode,
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
			p.onEgress("api.anthropic.com", r.Method, string(ProviderAnthropic), 0)
		}
		return
	}
	defer resp.Body.Close()

	if p.onEgress != nil {
		p.onEgress("api.anthropic.com", r.Method, string(ProviderAnthropic), resp.StatusCode)
	}

	for k, vv := range resp.Header {
		for _, v := range vv {
			w.Header().Add(k, v)
		}
	}
	w.WriteHeader(resp.StatusCode)
	io.Copy(w, resp.Body)
}

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
