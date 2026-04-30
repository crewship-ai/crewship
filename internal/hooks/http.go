package hooks

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"syscall"
	"time"
)

// ErrSSRFBlocked is returned when a hook URL resolves to an address the
// guard refuses to dial. Loopback / link-local / unspecified / multicast
// destinations are always blocked. RFC1918 (private) destinations are
// blocked unless the operator explicitly opts in via the env var below —
// internal LAN webhook receivers are common enough to be worth the
// override, but the default has to be strict so a misconfigured hook
// cannot hit cloud metadata IMDS or the crewshipd internal API on
// localhost.
var ErrSSRFBlocked = errors.New("ssrf guard: destination not allowed")

const allowPrivateEnvVar = "CREWSHIP_HOOKS_ALLOW_PRIVATE"

func allowPrivateHookDestinations() bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv(allowPrivateEnvVar))) {
	case "1", "true", "yes", "on":
		return true
	}
	return false
}

// blockedAddrReason returns a non-empty reason if ip is unsafe to dial
// from a hook handler. Empty string = OK to dial.
//
// Always blocked, no override: link-local (cloud metadata IMDS lives at
// 169.254.169.254 — never a legitimate webhook target), multicast,
// unspecified. The allow-private opt-in covers loopback + RFC1918, both
// of which are "trust your internal network" calls the operator should
// make explicitly.
func blockedAddrReason(ip net.IP) string {
	if ip == nil {
		return "unresolved address"
	}
	if ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() {
		return "link-local (cloud metadata IMDS lives here)"
	}
	if ip.IsUnspecified() {
		return "unspecified"
	}
	if ip.IsMulticast() {
		return "multicast"
	}
	if allowPrivateHookDestinations() {
		return ""
	}
	if ip.IsLoopback() {
		return "loopback — set " + allowPrivateEnvVar + "=true to allow"
	}
	if ip.IsPrivate() {
		return "private (RFC1918) — set " + allowPrivateEnvVar + "=true to allow"
	}
	return ""
}

// hookDialer fires the SSRF guard inside Control, which runs after DNS
// resolution and on every redirect target. This is the authoritative
// defense — pre-flight URL parsing only catches static literals.
var hookDialer = &net.Dialer{
	Timeout:   10 * time.Second,
	KeepAlive: 30 * time.Second,
	Control: func(network, address string, _ syscall.RawConn) error {
		host, _, err := net.SplitHostPort(address)
		if err != nil {
			return fmt.Errorf("ssrf guard: bad address %q: %w", address, err)
		}
		ip := net.ParseIP(host)
		if reason := blockedAddrReason(ip); reason != "" {
			return fmt.Errorf("%w: %s -> %s", ErrSSRFBlocked, address, reason)
		}
		return nil
	},
}

// defaultHTTPClient is shared across handler calls so we benefit from
// connection reuse. Timeout is also enforced per-call via
// http.NewRequestWithContext; the client-level Timeout is a belt-and-braces
// guard against a context leak. The Transport's DialContext is wired
// through hookDialer so every dial (including redirects) goes through
// the SSRF guard.
var defaultHTTPClient = &http.Client{
	Timeout: 30 * time.Second,
	Transport: &http.Transport{
		Proxy:                 http.ProxyFromEnvironment,
		DialContext:           hookDialer.DialContext,
		ForceAttemptHTTP2:     true,
		MaxIdleConns:          16,
		IdleConnTimeout:       90 * time.Second,
		TLSHandshakeTimeout:   10 * time.Second,
		ExpectContinueTimeout: 1 * time.Second,
	},
}

// validateHookURL is a fast-fail static check on the URL before we even
// dispatch the request. Only http/https are allowed — file://, gopher://
// etc. would bypass the dialer entirely (handled by special transports).
// Pure-IP literals are also resolved here so a misconfigured hook
// surfaces a useful error instead of "ssrf guard: ..." after the dial.
func validateHookURL(raw string) error {
	u, err := url.Parse(raw)
	if err != nil {
		return fmt.Errorf("invalid url: %w", err)
	}
	scheme := strings.ToLower(u.Scheme)
	if scheme != "http" && scheme != "https" {
		return fmt.Errorf("scheme %q not allowed (http/https only)", u.Scheme)
	}
	host := u.Hostname()
	if host == "" {
		return fmt.Errorf("missing host")
	}
	// If the URL is an IP literal we can fail fast without DNS.
	if ip := net.ParseIP(host); ip != nil {
		if reason := blockedAddrReason(ip); reason != "" {
			return fmt.Errorf("%w: %s -> %s", ErrSSRFBlocked, host, reason)
		}
	}
	return nil
}

// httpHandler POSTs a JSON body to handler_config.url and translates the
// response status into an Outcome. 2xx = Pass, 4xx/5xx = Block, transport
// errors = Error. If handler_config.secret is set the body is signed with
// HMAC-SHA256 and the hex digest shipped in X-Crewship-Signature so the
// receiver can verify the request originated from this workspace.
func httpHandler(ctx context.Context, h Hook, ec EventContext) (Result, error) {
	start := time.Now()

	rawURL, _ := h.HandlerConfig["url"].(string)
	if rawURL == "" {
		return Result{
			Outcome: OutcomeError,
			Message: "http handler missing handler_config.url",
			Latency: time.Since(start),
		}, fmt.Errorf("http: empty url")
	}
	if err := validateHookURL(rawURL); err != nil {
		return Result{
			Outcome: OutcomeBlock,
			Message: "url rejected: " + err.Error(),
			Latency: time.Since(start),
		}, err
	}

	body := map[string]any{
		"event":        string(ec.Event),
		"workspace_id": ec.WorkspaceID,
		"crew_id":      ec.CrewID,
		"agent_id":     ec.AgentID,
		"mission_id":   ec.MissionID,
		"tool_name":    ec.ToolName,
		"severity":     ec.Severity,
		"payload":      ec.Payload,
		"ts":           time.Now().UTC().Format(time.RFC3339Nano),
	}
	bodyBytes, err := json.Marshal(body)
	if err != nil {
		return Result{
			Outcome: OutcomeError,
			Message: "marshal body: " + err.Error(),
			Latency: time.Since(start),
		}, err
	}

	// Per-call timeout overrides the shared client default when specified.
	timeout := 30 * time.Second
	if t, ok := h.HandlerConfig["timeout_secs"].(float64); ok && t > 0 {
		timeout = time.Duration(t) * time.Second
	}
	if t, ok := h.HandlerConfig["timeout_secs"].(int); ok && t > 0 {
		timeout = time.Duration(t) * time.Second
	}
	cctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	req, err := http.NewRequestWithContext(cctx, http.MethodPost, rawURL, bytes.NewReader(bodyBytes))
	if err != nil {
		return Result{
			Outcome: OutcomeError,
			Message: "build request: " + err.Error(),
			Latency: time.Since(start),
		}, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "crewship-hooks/1")

	if secret, ok := h.HandlerConfig["secret"].(string); ok && secret != "" {
		sig := signBody([]byte(secret), bodyBytes)
		req.Header.Set("X-Crewship-Signature", "sha256="+sig)
	}

	resp, err := defaultHTTPClient.Do(req)
	latency := time.Since(start)
	if err != nil {
		// SSRF rejections wear an OutcomeBlock so the journal makes the
		// reason visible (Error implies transport hiccup, not a policy
		// decision).
		if errors.Is(err, ErrSSRFBlocked) {
			return Result{
				Outcome: OutcomeBlock,
				Message: "ssrf guard: " + err.Error(),
				Latency: latency,
			}, err
		}
		return Result{
			Outcome: OutcomeError,
			Message: "request: " + err.Error(),
			Latency: latency,
		}, err
	}
	defer resp.Body.Close()

	// Drain and truncate so the full response lands in the journal without
	// risking multi-MB entries from a misbehaving webhook.
	respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	payload := map[string]any{
		"status": resp.StatusCode,
		"body":   string(respBody),
	}

	switch {
	case resp.StatusCode >= 200 && resp.StatusCode < 300:
		return Result{
			Outcome: OutcomePass,
			Message: fmt.Sprintf("http %d", resp.StatusCode),
			Latency: latency,
			Payload: payload,
		}, nil
	default:
		return Result{
			Outcome: OutcomeBlock,
			Message: fmt.Sprintf("http %d: %s", resp.StatusCode, truncate(string(respBody), 200)),
			Latency: latency,
			Payload: payload,
		}, nil
	}
}

// signBody returns the lowercase hex HMAC-SHA256 of body keyed on secret.
// Receivers validate by recomputing the same value and comparing with
// hmac.Equal so the comparison is constant-time. The format mirrors
// GitHub / Stripe webhook conventions for least surprise.
func signBody(secret, body []byte) string {
	mac := hmac.New(sha256.New, secret)
	mac.Write(body)
	return hex.EncodeToString(mac.Sum(nil))
}
