package hooks

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

// defaultHTTPClient is shared across handler calls so we benefit from
// connection reuse. Timeout is also enforced per-call via
// http.NewRequestWithContext; the client-level Timeout is a belt-and-braces
// guard against a context leak.
var defaultHTTPClient = &http.Client{
	Timeout: 30 * time.Second,
}

// httpHandler POSTs a JSON body to handler_config.url and translates the
// response status into an Outcome. 2xx = Pass, 4xx/5xx = Block, transport
// errors = Error. If handler_config.secret is set the body is signed with
// HMAC-SHA256 and the hex digest shipped in X-Crewship-Signature so the
// receiver can verify the request originated from this workspace.
func httpHandler(ctx context.Context, h Hook, ec EventContext) (Result, error) {
	start := time.Now()

	url, _ := h.HandlerConfig["url"].(string)
	if url == "" {
		return Result{
			Outcome: OutcomeError,
			Message: "http handler missing handler_config.url",
			Latency: time.Since(start),
		}, fmt.Errorf("http: empty url")
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

	req, err := http.NewRequestWithContext(cctx, http.MethodPost, url, bytes.NewReader(bodyBytes))
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
