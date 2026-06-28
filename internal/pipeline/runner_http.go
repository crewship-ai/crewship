package pipeline

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/crewship-ai/crewship/internal/httpsafe"
)

// runHTTPStep handles a StepHTTP. Single outbound request,
// template-substituted URL/body/headers, optional credential
// injection from a workspace-typed reference. The runtime resolves
// CredentialRef via the supplied Executor.credentialResolver.
//
// Why a separate file: HTTP step has its own security perimeter
// (egress allowlist, credential injection, body size cap) that
// shouldn't muddy executor.go's step-dispatch loop.
//
// Returns (output, costUSD=0, durationMs, err). HTTP steps don't
// burn LLM tokens, so cost is always 0 — pipeline cost reporting
// stays accurate.
func (e *Executor) runHTTPStep(ctx context.Context, step Step, parentRender RenderContext) (string, float64, int64, error) {
	stepStart := time.Now()
	if step.HTTP == nil {
		return "", 0, 0, fmt.Errorf("http step missing body")
	}

	// Render templates on URL, body, and header values. We
	// deliberately DO NOT render header keys — that's a misuse
	// vector (a template-injected key could overwrite Authorization).
	rawURL := Render(step.HTTP.URL, parentRender)
	if rawURL == "" {
		return "", 0, 0, fmt.Errorf("http step %q rendered empty URL", step.ID)
	}
	// Defence in depth: ValidateURL is the cheap scheme + literal-IP
	// reject; SafeTransport below catches DNS aliases / rebinding.
	// http+https are both permitted here — operators legitimately call
	// intranet HTTP endpoints from pipeline steps, and the egress
	// allowlist (set per-deployment) is the host-level gate.
	//
	// The test-only allowPrivateHTTP escape hatch routes around the
	// SSRF check so unit tests can use httptest.NewServer (which binds
	// to 127.0.0.1). Production callers never flip this flag — see
	// Executor.SetAllowPrivateHTTPForTesting for the rationale.
	var parsed *url.URL
	if e.allowPrivateHTTP {
		var perr error
		parsed, perr = url.Parse(rawURL)
		if perr != nil {
			return "", 0, 0, fmt.Errorf("http step %q parse url: %w", step.ID, perr)
		}
	} else {
		var verr error
		parsed, verr = httpsafe.ValidateURL(rawURL, "http", "https")
		if verr != nil {
			return "", 0, 0, fmt.Errorf("http step %q: %w", step.ID, verr)
		}
	}
	// Egress allowlist: enforce host check at the runtime even
	// though sidecar already gates network for agent_run. For
	// HTTP step we go direct, so the gate has to live here.
	if e.egressAllowed != nil && !e.egressAllowed(parsed.Host) {
		return "", 0, 0, fmt.Errorf("http step %q host %q not in egress allowlist", step.ID, parsed.Host)
	}
	// Per-routine egress_targets: when the routine declares a host
	// allowlist, enforce it here (the deployment-level egressAllowed gate
	// above is currently unwired, so without this the declared allowlist
	// is silently ignored — a routine pinned to api.partner.com could
	// exfiltrate to any public host). httpsafe already blocks private/
	// link-local IPs; this restricts the *public* hosts to the declared
	// set. Skipped under the allowPrivateHTTP test hatch, mirroring the
	// httpsafe bypass above.
	if !e.allowPrivateHTTP && !hostInEgressTargets(parsed.Hostname(), parentRender.EgressTargets) {
		return "", 0, 0, fmt.Errorf("http step %q host %q not in routine egress_targets", step.ID, parsed.Hostname())
	}

	body := Render(step.HTTP.Body, parentRender)
	method := strings.ToUpper(step.HTTP.Method)

	timeoutSec := step.TimeoutSec
	if timeoutSec <= 0 {
		timeoutSec = 30
	}
	rctx, cancel := context.WithTimeout(ctx, time.Duration(timeoutSec)*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(rctx, method, parsed.String(), strings.NewReader(body))
	if err != nil {
		return "", 0, 0, fmt.Errorf("http step %q new request: %w", step.ID, err)
	}
	for k, v := range step.HTTP.Headers {
		req.Header.Set(k, Render(v, parentRender))
	}
	// Default content type for bodied verbs if caller didn't set one.
	if body != "" && req.Header.Get("Content-Type") == "" {
		req.Header.Set("Content-Type", "application/json")
	}

	// Credential injection. Resolution is best-effort — if no
	// resolver wired, we skip; if resolver returns empty, we skip
	// + log. Failing here would block legitimate requests against
	// public endpoints that don't need auth.
	if step.HTTP.CredentialRef != nil && e.credentialByType != nil {
		credValue, credErr := e.credentialByType(rctx, step.HTTP.CredentialRef.Type)
		if credErr == nil && credValue != "" {
			injectCredential(req, step.HTTP.CredentialRef, credValue)
		}
	}

	// CheckRedirect re-validates the destination host against the
	// egress allowlist AND re-runs the URL scheme/IP guard on every
	// 3xx hop. Without this, a sender that allows api.partner.com →
	// 302 → 169.254.169.254 (AWS IMDS) or localhost or any other
	// internal host would leak metadata into the step output. Default
	// Go client follows up to 10 redirects; checking each is the only
	// safe stance.
	//
	// httpsafe.SafeTransport is the dial-time guard: even if a
	// rendered URL passes the string-level checks, a DNS alias to a
	// private IP is refused at connect time. Tests with
	// allowPrivateHTTP=true fall back to the default transport so they
	// can target httptest.NewServer on 127.0.0.1.
	transport := http.RoundTripper(httpsafe.SafeTransport())
	if e.allowPrivateHTTP {
		transport = http.DefaultTransport
	}
	client := http.Client{
		Timeout:   time.Duration(timeoutSec) * time.Second,
		Transport: transport,
		CheckRedirect: func(redirReq *http.Request, via []*http.Request) error {
			if len(via) >= 10 {
				return fmt.Errorf("http step %q: too many redirects", step.ID)
			}
			if !e.allowPrivateHTTP {
				if _, err := httpsafe.ValidateURL(redirReq.URL.String(), "http", "https"); err != nil {
					return fmt.Errorf("http step %q redirect %w", step.ID, err)
				}
			}
			if e.egressAllowed != nil && !e.egressAllowed(redirReq.URL.Host) {
				return fmt.Errorf("http step %q redirect to %q blocked by egress allowlist", step.ID, redirReq.URL.Host)
			}
			// Re-enforce the routine's egress_targets on every redirect
			// hop, else a host in the allowlist could 302 to one that
			// isn't (the classic allowlist-bypass-via-redirect).
			if !e.allowPrivateHTTP && !hostInEgressTargets(redirReq.URL.Hostname(), parentRender.EgressTargets) {
				return fmt.Errorf("http step %q redirect to %q not in routine egress_targets", step.ID, redirReq.URL.Hostname())
			}
			return nil
		},
	}
	resp, err := client.Do(req)
	if err != nil {
		return "", 0, time.Since(stepStart).Milliseconds(), fmt.Errorf("http step %q request: %w", step.ID, err)
	}
	defer resp.Body.Close()

	maxBytes := int64(step.HTTP.MaxResponseBytes)
	if maxBytes <= 0 {
		maxBytes = 1_000_000 // 1 MB default
	}
	respBody, err := io.ReadAll(io.LimitReader(resp.Body, maxBytes))
	if err != nil {
		return "", 0, time.Since(stepStart).Milliseconds(), fmt.Errorf("http step %q read body: %w", step.ID, err)
	}
	if int64(len(respBody)) >= maxBytes {
		// Truncated — append marker so downstream consumers see it
		respBody = append(respBody, []byte("\n...(response truncated)")...)
	}

	successCodes := step.HTTP.SuccessCodes
	if len(successCodes) == 0 {
		successCodes = []int{200, 201, 202, 204}
	}
	if !containsInt(successCodes, resp.StatusCode) {
		return string(respBody), 0, time.Since(stepStart).Milliseconds(),
			fmt.Errorf("http step %q got HTTP %d (success codes: %v)", step.ID, resp.StatusCode, successCodes)
	}
	return string(respBody), 0, time.Since(stepStart).Milliseconds(), nil
}

// injectCredential mutates the request to carry the credential per
// the configured InjectAs scheme. Default is bearer.
func injectCredential(req *http.Request, ref *CredentialRef, value string) {
	switch ref.InjectAs {
	case "", "bearer":
		req.Header.Set("Authorization", "Bearer "+value)
	case "header":
		req.Header.Set(ref.HeaderName, value)
	case "query":
		q := req.URL.Query()
		q.Set(ref.QueryName, value)
		req.URL.RawQuery = q.Encode()
	}
}

// containsInt is a tiny helper kept here (rather than pulling in
// slices.Contains) to keep the package go-mod minimal.
func containsInt(xs []int, x int) bool {
	for _, v := range xs {
		if v == x {
			return true
		}
	}
	return false
}

// FingerprintHTTPRequest returns a stable fingerprint for journal
// entries so pipeline run HTTP steps can be grouped independently
// of the rendered URL — useful for retry analytics. Currently
// sha256 of method+host+path; query string and body excluded so
// per-input variance doesn't fragment the fingerprint. Exported
// for the API handler that surfaces run summaries.
func FingerprintHTTPRequest(method, rawURL string) string {
	parsed, err := url.Parse(rawURL)
	host, path := "", ""
	if err == nil {
		host = parsed.Host
		path = parsed.Path
	}
	sum := sha256.Sum256([]byte(strings.ToUpper(method) + " " + host + path))
	return hex.EncodeToString(sum[:8])
}

// hostInEgressTargets reports whether host is permitted by the routine's
// declared egress_targets. Empty targets → no restriction (back-compat
// for routines that declare none; httpsafe remains the IP-level gate).
// A target matches the exact host or any subdomain of it ("api.x.com"
// matches target "x.com" via the "."+target suffix, but "evilx.com"
// does NOT match "x.com" — the leading dot prevents the classic
// suffix-bypass).
func hostInEgressTargets(host string, targets []string) bool {
	if len(targets) == 0 {
		return true
	}
	host = strings.ToLower(strings.TrimSuffix(host, "."))
	for _, t := range targets {
		t = strings.ToLower(strings.TrimSpace(t))
		if t == "" {
			continue
		}
		if host == t || strings.HasSuffix(host, "."+t) {
			return true
		}
	}
	return false
}
