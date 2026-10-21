package main

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
)

type seedRetryRoundTripper func(*http.Request) (*http.Response, error)

func (f seedRetryRoundTripper) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestSeedRBACRetryPreservesSetupBody(t *testing.T) {
	calls := 0
	transport := seedRateLimitTransport{base: seedRetryRoundTripper(func(r *http.Request) (*http.Response, error) {
		calls++
		raw, _ := io.ReadAll(r.Body)
		r.Body.Close()
		if string(raw) != `{"token":"fixture","new_password":"fixture-password"}` {
			t.Fatalf("retry changed setup request: %s", raw)
		}
		status := 200
		if calls < 3 {
			status = 429
		}
		return &http.Response{StatusCode: status, Header: http.Header{"Retry-After": []string{"0"}}, Body: io.NopCloser(strings.NewReader("{}"))}, nil
	})}
	req, err := http.NewRequest("POST", "http://seed.invalid/api/v1/auth/reset", strings.NewReader(`{"token":"fixture","new_password":"fixture-password"}`))
	if err != nil {
		t.Fatal(err)
	}
	resp, err := transport.RoundTrip(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if calls != 3 || resp.StatusCode != 200 {
		t.Fatalf("retry: %d calls, HTTP %d", calls, resp.StatusCode)
	}
}

func TestSeedRBACRetryCancellationAndServerErrors(t *testing.T) {
	for _, status := range []int{429, 500} {
		calls := 0
		ctx, cancel := context.WithCancel(t.Context())
		transport := seedRateLimitTransport{base: seedRetryRoundTripper(func(r *http.Request) (*http.Response, error) {
			calls++
			cancel()
			return &http.Response{StatusCode: status, Header: http.Header{"Retry-After": []string{"60"}}, Body: io.NopCloser(strings.NewReader("{}"))}, nil
		})}
		req, _ := http.NewRequestWithContext(ctx, "GET", "http://seed.invalid/api", nil)
		resp, err := transport.RoundTrip(req)
		if resp != nil {
			resp.Body.Close()
		}
		if calls != 1 || status == 429 && err != context.Canceled || status == 500 && err != nil {
			t.Fatalf("HTTP %d: calls=%d err=%v", status, calls, err)
		}
	}
}
