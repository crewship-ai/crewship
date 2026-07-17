package llm

import (
	"context"
	"fmt"
	"io"
	"math/rand"
	"net/http"
	"strconv"
	"time"
)

// retryableStatusCodes are HTTP status codes that should trigger a retry.
var retryableStatusCodes = map[int]bool{
	429: true, // Rate limited
	500: true, // Internal server error
	503: true, // Service unavailable
	529: true, // Overloaded
}

// checkStatus maps a non-200 provider response to a human-readable error.
// providerName is display-cased ("Anthropic", "OpenAI") — operators see these
// strings in dashboards, so the wording is part of the contract
// (see error_mapping_test.go).
func checkStatus(resp *http.Response, providerName string) error {
	if resp.StatusCode == http.StatusOK {
		return nil
	}
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
	switch resp.StatusCode {
	case http.StatusUnauthorized:
		return fmt.Errorf("invalid %s API key", providerName)
	case http.StatusTooManyRequests:
		return fmt.Errorf("%s rate limit exceeded", providerName)
	default:
		return fmt.Errorf("%s API returned %d: %s", providerName, resp.StatusCode, body)
	}
}

// doWithRetry executes an HTTP request with exponential backoff retry on
// transient errors. Max 3 attempts (1s/2s/4s plus jitter), Retry-After
// honoured. newReq rebuilds the request for each attempt — request bodies are
// single-use. Two provider-name params keep error strings byte-identical to
// what each provider produced before extraction: lowerName is the lowercase
// wrap prefix ("anthropic http: ..."), displayName the API-facing casing
// ("Anthropic API returned ...").
func doWithRetry(ctx context.Context, client *http.Client, newReq func(context.Context) (*http.Request, error), lowerName, displayName string) (*http.Response, error) {
	const maxRetries = 3
	baseDelay := time.Second
	var retryAfter time.Duration

	var lastErr error
	for attempt := 0; attempt < maxRetries; attempt++ {
		httpReq, err := newReq(ctx)
		if err != nil {
			return nil, err
		}

		resp, err := client.Do(httpReq)
		if err != nil {
			lastErr = fmt.Errorf("%s http: %w", lowerName, err)
			if ctx.Err() != nil {
				return nil, lastErr
			}
			// Network error — retry
		} else if !retryableStatusCodes[resp.StatusCode] {
			return resp, nil // Success or non-retryable error
		} else {
			respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
			resp.Body.Close()
			lastErr = fmt.Errorf("%s API returned %d: %s", displayName, resp.StatusCode, respBody)

			// Check Retry-After header
			if ra := resp.Header.Get("Retry-After"); ra != "" {
				if secs, err := strconv.Atoi(ra); err == nil && secs > 0 {
					retryAfter = time.Duration(secs) * time.Second
				}
			}
		}

		if attempt < maxRetries-1 {
			delay := baseDelay * (1 << attempt) // 1s, 2s, 4s
			// Use Retry-After if it exceeds the calculated exponential delay
			if retryAfter > delay {
				delay = retryAfter
			}
			retryAfter = 0 // reset for next attempt
			jitter := time.Duration(rand.Int63n(int64(delay / 4)))
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(delay + jitter):
			}
		}
	}
	return nil, fmt.Errorf("%s: max retries exceeded: %w", lowerName, lastErr)
}
