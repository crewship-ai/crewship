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

	"github.com/crewship-ai/crewship/internal/egresspolicy"
	"github.com/crewship-ai/crewship/internal/httpsafe"
)

// Egress rule names carried on EgressBlockedError so operators (and
// tests) can tell WHICH layer denied a host without parsing prose.
const (
	// EgressRuleRoutineTargets — the routine's declared egress_targets
	// list does not cover the host.
	EgressRuleRoutineTargets = "routine_egress_targets"
	// EgressRuleCrewNetworkPolicy — the authoring crew's network policy
	// (crews.network_mode=restricted + allowed_domains) denies the host.
	EgressRuleCrewNetworkPolicy = "crew_network_policy"
)

// EgressBlockedError is the structured error an http step (or hook)
// fails with when a host is denied by an egress layer — on the first
// request or on any redirect hop. It names the step, the host, the
// rule that fired, and an operator-legible detail so the journal entry
// tells the author exactly what to change.
type EgressBlockedError struct {
	StepID string // pipeline step (or hook) id
	Host   string // the denied host (redirect targets included)
	Rule   string // EgressRuleRoutineTargets | EgressRuleCrewNetworkPolicy
	Detail string // actionable reason ("add X to egress_targets", ...)
}

func (e *EgressBlockedError) Error() string {
	return fmt.Sprintf("http step %q: egress to host %q blocked by %s: %s",
		e.StepID, e.Host, e.Rule, e.Detail)
}

// runHTTPStep handles a StepHTTP. Single outbound request,
// template-substituted URL/body/headers, optional credential
// injection from a workspace-typed reference. The runtime resolves
// CredentialRef via the supplied Executor.credentialByType.
//
// Why a separate file: HTTP step has its own security perimeter
// (egress allowlist, credential injection, body size cap) that
// shouldn't muddy executor.go's step-dispatch loop.
//
// Returns (output, costUSD=0, durationMs, err). HTTP steps don't
// burn LLM tokens, so cost is always 0 — pipeline cost reporting
// stays accurate.
func (e *Executor) runHTTPStep(ctx context.Context, step Step, parentRender RenderContext, in RunInput) (out string, cost float64, dur int64, err error) {
	stepStart := time.Now()
	if step.HTTP == nil {
		return "", 0, 0, fmt.Errorf("http step missing body")
	}
	// {{ secrets.<type> }} in the URL / body / header values resolves the
	// SAME way credential_ref does (workspace vault, ACTIVE-only), unifying
	// the two paths. The deferred scrub keeps a resolved value from
	// surfacing in the response body (step output) or an error; a scrubbed
	// EgressBlockedError keeps its type unless a secret was actually present
	// (see secretScrub.scrubErr), so the egress-rule assertions still hold.
	var secrets *secretScrub
	parentRender, secrets = e.resolveStepSecrets(ctx, step, parentRender, in)
	defer func() { out, err = secrets.scrub(out), secrets.scrubErr(err) }()
	// Policy scope for the egress gate + credential resolver. WorkspaceID
	// and AuthorCrewID come off RunInput as before; WebhookTriggered +
	// RoutineDeclaresEgress feed the SSRF/webhook hardening in
	// NewCrewNetworkPolicyGate (#1416 items 1 & 3) — EgressTargets is
	// fixed for the whole run (dsl.EgressTargets), so "did the routine
	// declare one" is a scope-level fact, not a per-request one.
	scope := RunScope{
		WorkspaceID:           in.WorkspaceID,
		AuthorCrewID:          in.AuthorCrewID,
		WebhookTriggered:      in.TriggeredVia == TriggeredViaWebhook,
		RoutineDeclaresEgress: len(parentRender.EgressTargets) > 0,
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
	// intranet HTTP endpoints from pipeline steps, and the crew
	// network-policy gate + routine egress_targets below are the
	// host-level gates.
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
	// Crew/workspace egress policy: enforce the host check at the
	// runtime even though sidecar already gates network for agent_run.
	// For HTTP step we go direct, so the gate has to live here. NOT
	// skipped under the allowPrivateHTTP test hatch — the hatch only
	// bypasses the SSRF (private-IP) guards, never policy layers.
	if e.egressAllowed != nil {
		if gerr := e.egressAllowed(ctx, scope, parsed.Host); gerr != nil {
			return "", 0, 0, &EgressBlockedError{
				StepID: step.ID, Host: parsed.Host,
				Rule: EgressRuleCrewNetworkPolicy, Detail: gerr.Error(),
			}
		}
	}
	// Per-routine egress_targets: when the routine declares a host
	// allowlist, enforce it here. httpsafe already blocks private/
	// link-local IPs; this restricts the *public* hosts to the declared
	// set. Backward-compat contract (must match hostInEgressTargets +
	// the DSL validator, which both treat egress_targets as OPTIONAL):
	// an EMPTY list means "no routine-level restriction" — the crew
	// policy gate above and the httpsafe guard remain the host-level
	// gates. Tightening empty to deny-all would break every existing
	// http routine that predates the declaration, so any future
	// default-deny must ride a DSL version bump, not this check.
	if !hostInEgressTargets(parsed.Hostname(), parentRender.EgressTargets) {
		return "", 0, 0, &EgressBlockedError{
			StepID: step.ID, Host: parsed.Hostname(),
			Rule: EgressRuleRoutineTargets,
			Detail: fmt.Sprintf("declared egress_targets %v do not cover this host — add it to the routine's egress_targets to allow",
				parentRender.EgressTargets),
		}
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
	// resolver wired, we skip; if the resolver errors or returns
	// empty (no ACTIVE credential of the declared type in the
	// workspace vault), we skip. Failing here would block legitimate
	// requests against public endpoints that don't need auth. The
	// resolved value is injected into the outbound request ONLY —
	// never logged, journaled, or surfaced in the step output.
	if step.HTTP.CredentialRef != nil && e.credentialByType != nil {
		credValue, credErr := e.credentialByType(rctx, scope, step.HTTP.CredentialRef.Type)
		if credErr == nil && credValue != "" {
			injectCredential(req, step.HTTP.CredentialRef, credValue)
		}
	}

	// The redirect gate is the shared egresspolicy.Client — the SAME
	// factory notify/webhook, hooks, and the MCP gateway build from, so
	// the http step no longer hand-rolls its own CheckRedirect (the
	// "one gated client, all paths" goal). Constructing it wires, on
	// EVERY 3xx hop:
	//
	//   - the scheme + literal-IP SSRF re-check (ValidateURLForEndpoint,
	//     endpoint-aware so the allowPrivateHTTP test hatch reaches a
	//     loopback httptest server while cloud metadata stays blocked);
	//   - the crew network-policy re-check (crewHopCheck below), so a
	//     host in the crew allowlist that 302s to one that isn't is
	//     refused instead of followed;
	//   - the routine's egress_targets re-check (ExtraHop below), the
	//     same allowlist-bypass-via-redirect defence, keyed on the
	//     routine's declared targets.
	//
	// Both crew and routine layers still fail with the structured
	// *EgressBlockedError the journal and tests key off — the crew one
	// rides through egresspolicy's ErrEgressBlocked wrapper (joined with
	// %w, so errors.As still recovers it).
	crewHopCheck := func(hctx context.Context, host string) error {
		if e.egressAllowed == nil {
			return nil
		}
		if gerr := e.egressAllowed(hctx, scope, host); gerr != nil {
			return &EgressBlockedError{
				StepID: step.ID, Host: host,
				Rule:   EgressRuleCrewNetworkPolicy,
				Detail: "redirect target: " + gerr.Error(),
			}
		}
		return nil
	}
	client := egresspolicy.Client(crewHopCheck, egresspolicy.Options{
		Timeout:      time.Duration(timeoutSec) * time.Second,
		Schemes:      []string{"http", "https"},
		AllowPrivate: e.allowPrivateHTTP,
		MaxRedirects: 10,
		ExtraHop: func(redirReq *http.Request) error {
			if !hostInEgressTargets(redirReq.URL.Hostname(), parentRender.EgressTargets) {
				return &EgressBlockedError{
					StepID: step.ID, Host: redirReq.URL.Hostname(),
					Rule: EgressRuleRoutineTargets,
					Detail: fmt.Sprintf("redirect target not covered by declared egress_targets %v",
						parentRender.EgressTargets),
				}
			}
			return nil
		},
	})
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
