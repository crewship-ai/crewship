package main

import (
	"fmt"
	"net/http"
	"os"
	"strconv"
	"time"

	"github.com/crewship-ai/crewship/internal/cli"
)

// The additive user seed must also work with the normal auth limiter enabled.
// Retry only explicit pre-handler 429s, never ambiguous network/write failures.
func seedRetryClient(client *cli.Client) *cli.Client {
	clone := client.WithTimeout(3 * time.Minute)
	transport := clone.HTTPClient.Transport
	if transport == nil {
		transport = http.DefaultTransport
	}
	clone.HTTPClient.Transport = seedRateLimitTransport{base: transport}
	return clone
}

type seedRateLimitTransport struct{ base http.RoundTripper }

func (t seedRateLimitTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	current := request
	for attempt := 0; ; attempt++ {
		response, err := t.base.RoundTrip(current)
		if err != nil || response.StatusCode != http.StatusTooManyRequests || attempt >= 2 || request.Body != nil && request.GetBody == nil {
			return response, err
		}
		delay := time.Minute
		if seconds, err := strconv.Atoi(response.Header.Get("Retry-After")); err == nil && seconds >= 0 && seconds <= 60 {
			delay = time.Duration(seconds) * time.Second
		}
		response.Body.Close()
		fmt.Fprintln(os.Stderr, "RBAC seed rate limited; waiting for Retry-After before retrying the request.")
		timer := time.NewTimer(delay)
		select {
		case <-request.Context().Done():
			timer.Stop()
			return nil, request.Context().Err()
		case <-timer.C:
		}
		current = request.Clone(request.Context())
		if request.GetBody != nil {
			current.Body, err = request.GetBody()
			if err != nil {
				return nil, err
			}
		}
	}
}
