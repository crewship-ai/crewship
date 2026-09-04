package api

// One place that knows how to ask Anthropic whether a credential works.
//
// There were two, and they disagreed about whether the question could even be
// asked. probeAnthropicOAuthToken (onboarding.go) has been live-probing CLI
// OAuth tokens against /v1/messages since it was written. Forty lines away in
// the same package, probeProviderInner short-circuited the identical case:
//
//	// OAuth setup tokens (sk-ant-oat*) cannot be validated via standard API.
//	// They only work inside Claude Code's authenticated tunnel.
//	if ctype == "AI_CLI_TOKEN" || isAnthropicOAuthToken(value) {
//	    return testResult{Valid: true, Error: "OAuth token accepted (cannot validate via API, ...)"}
//	}
//
// That comment is disproven by its neighbour. The consequence was not a
// cosmetic inconsistency: /credentials/test, /credentials/{id}/test, the
// "Test now" button and `crewship credential test-stored` are the only tools
// anyone has for checking a key, and for the one credential type Crewship's
// onboarding actually accepts they all returned Valid:true without dialling
// anything. Every OAuth credential added or rotated outside the onboarding
// wizard was unverified and reported healthy.
//
// A check that cannot run must not report success. That is the same rule the
// sidecar health check broke in the other direction, and it is why this lives
// in one function now rather than in each caller's idea of what is possible.

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/crewship-ai/crewship/internal/llm"
	"io"
	"net/http"
	"strings"
	"time"
)

// anthropicAPIBase is a var, not a const, so tests can point the probe at an
// httptest server. Nothing in production reassigns it.
var anthropicAPIBase = "https://api.anthropic.com"

// anthropicProbeModel is the model used when the caller has no opinion — the
// catalog's cheapest Anthropic model, because this call is billed to the
// customer. It used to be a fixed "claude-3-5-haiku-latest", which meant the
// day Anthropic retired that alias the token check, and with it the whole
// wizard, would have failed for every new install.
var anthropicProbeModel = llm.HousekeepingModel("anthropic")

// anthropicProbeResult separates the three answers a probe can give, because
// collapsing them is what produced "OAuth token accepted" for a token nobody
// had asked about.
type anthropicProbeResult struct {
	// Reached is false when the probe never got an answer — DNS, TLS,
	// timeout, provider outage. NOT a statement about the credential.
	Reached bool
	Status  int
	// Detail is the provider's own error type when it sent one
	// ("authentication_error", "not_found_error", …), used to tell a bad
	// token apart from a model this account cannot use.
	Detail string
	Err    error
}

// Rejected reports a credential the provider actively refused.
func (r anthropicProbeResult) Rejected() bool {
	return r.Reached && (r.Status == http.StatusUnauthorized || r.Status == http.StatusForbidden)
}

// ModelUnavailable reports a credential that authenticated but may not use
// the requested model — a different problem with a different fix, and one the
// old single-model probe could not see at all.
func (r anthropicProbeResult) ModelUnavailable() bool {
	return r.Reached && r.Status == http.StatusNotFound
}

// OK reports a credential the provider accepted for this model.
func (r anthropicProbeResult) OK() bool {
	return r.Reached && r.Status >= 200 && r.Status <= 299
}

// probeAnthropicCredential asks Anthropic whether this credential works, for
// this model.
//
// An OAuth CLI token (sk-ant-oat…) authenticates as a Bearer token with the
// oauth beta header; a raw API key uses x-api-key. Both go to /v1/messages
// with max_tokens=1 — the cheapest call that still exercises the auth path,
// and unlike /v1/models it is proven to work for both credential shapes.
//
// model may be empty, in which case the cheap default is used. Callers that
// validate model capability may pass an exact Anthropic API model id. The
// credential and onboarding endpoints intentionally leave it empty: their
// model value can be a Claude Code CLI alias from a different namespace.
func probeAnthropicCredential(parent context.Context, token, model string) anthropicProbeResult {
	if strings.TrimSpace(model) == "" {
		model = anthropicProbeModel
	}

	payload := map[string]any{
		"model":      model,
		"max_tokens": 1,
		"messages":   []map[string]string{{"role": "user", "content": "ok"}},
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return anthropicProbeResult{Err: err}
	}

	ctx, cancel := context.WithTimeout(parent, 8*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		anthropicAPIBase+"/v1/messages", strings.NewReader(string(body)))
	if err != nil {
		return anthropicProbeResult{Err: err}
	}
	req.Header.Set("anthropic-version", "2023-06-01")
	req.Header.Set("Content-Type", "application/json")
	if isAnthropicOAuthToken(token) {
		req.Header.Set("Authorization", "Bearer "+token)
		req.Header.Set("anthropic-beta", "oauth-2025-04-20")
	} else {
		req.Header.Set("x-api-key", token)
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		// Reached stays false. The caller must not read this as either a
		// good or a bad credential — it is an unanswered question.
		return anthropicProbeResult{Err: err}
	}
	defer resp.Body.Close()

	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 8<<10))
	out := anthropicProbeResult{Reached: true, Status: resp.StatusCode}

	// The provider's own error type distinguishes "your token is bad" from
	// "that model is not yours", which share a shape but not a remedy.
	var envelope struct {
		Error struct {
			Type    string `json:"type"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if json.Unmarshal(raw, &envelope) == nil {
		out.Detail = envelope.Error.Type
	}
	return out
}

// anthropicProbeMessage renders a probe result as the sentence a person
// should read. Empty when the credential is fine.
//
// "could not check" is deliberately its own outcome and is never rendered as
// either a pass or a failure — the caller decides whether to block, but it
// must do so knowing the difference.
func anthropicProbeMessage(r anthropicProbeResult) string {
	switch {
	case r.OK():
		return ""
	case r.Rejected():
		return "Anthropic rejected this token. The most common cause is pasting only part of the value — " +
			"run `claude setup-token` again and paste the entire sk-ant-oat… string in one go."
	case r.ModelUnavailable():
		return "Anthropic accepted the token but not this model. Your account may not have access to it — " +
			"pick a different model, or check your plan."
	case r.Reached && r.Status == http.StatusTooManyRequests:
		return "This token is rate-limited right now. It is valid, but the crew may stall until the limit clears."
	case r.Reached:
		return fmt.Sprintf("Anthropic answered %d. The token was not confirmed either way.", r.Status)
	default:
		return "Could not reach Anthropic to check this token — this says nothing about the token itself. " +
			"Check outbound network access from the Crewship host."
	}
}
