package main

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

type teamSeedRoundTripper func(*http.Request) (*http.Response, error)

func (f teamSeedRoundTripper) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
func TestTeamSeedAuth429HonorsDelayAndReplaysOnlyThrottledBody(t *testing.T) {
	calls, waits := 0, 0
	transport := &teamSeedAuthTransport{ctx: t.Context(), wait: func(_ context.Context, d time.Duration) error {
		waits++
		if d != time.Minute {
			t.Fatalf("Retry-After shortened: %s", d)
		}
		return nil
	}, base: teamSeedRoundTripper(func(r *http.Request) (*http.Response, error) {
		calls++
		body, _ := io.ReadAll(r.Body)
		if string(body) != `{"token":"owned-token"}` {
			t.Fatal("retry body changed")
		}
		status := 200
		if calls == 1 {
			status = 429
		}
		return &http.Response{StatusCode: status, Header: http.Header{"Retry-After": []string{"60"}}, Body: io.NopCloser(strings.NewReader(`{}`))}, nil
	})}
	request, _ := http.NewRequest("POST", "http://server/api/v1/auth/reset", strings.NewReader(`{"token":"owned-token"}`))
	response, err := transport.RoundTrip(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != 200 || calls != 2 || waits != 1 {
		t.Fatalf("calls %d waits %d status %d", calls, waits, response.StatusCode)
	}
}
func TestTeamSeedAuth429CancellationAndLongHintsDoNotRetry(t *testing.T) {
	for _, value := range []string{"60", "120"} {
		t.Run(value, func(t *testing.T) {
			calls, waits := 0, 0
			transport := &teamSeedAuthTransport{ctx: t.Context(), base: teamSeedRoundTripper(func(*http.Request) (*http.Response, error) {
				calls++
				return &http.Response{StatusCode: 429, Header: http.Header{"Retry-After": []string{value}}, Body: io.NopCloser(strings.NewReader(`{}`))}, nil
			}), wait: func(context.Context, time.Duration) error { waits++; return context.Canceled }}
			req, _ := http.NewRequest("POST", "http://server/api/auth/callback/credentials", strings.NewReader(`{}`))
			response, err := transport.RoundTrip(req)
			if value == "60" {
				if !errors.Is(err, context.Canceled) || waits != 1 {
					t.Fatalf("cancel %v waits%d", err, waits)
				}
			} else {
				if err != nil || response.StatusCode != 429 || waits != 0 {
					t.Fatalf("longhint %v waits%d", err, waits)
				}
				response.Body.Close()
			}
			if calls != 1 {
				t.Fatal("retry ignored cancellation or long hint")
			}
		})
	}
}
func TestTeamSeedAuthDoesNotRetryOtherWrites(t *testing.T) {
	calls := 0
	transport := &teamSeedAuthTransport{ctx: t.Context(), base: teamSeedRoundTripper(func(*http.Request) (*http.Response, error) {
		calls++
		return &http.Response{StatusCode: 429, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(`{}`))}, nil
	}), wait: func(context.Context, time.Duration) error { t.Fatal("non-auth mutation retried"); return nil }}
	req, _ := http.NewRequest("POST", "http://server/api/v1/conversations", strings.NewReader(`{}`))
	response, err := transport.RoundTrip(req)
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if calls != 1 {
		t.Fatal("duplicate mutation")
	}
}
