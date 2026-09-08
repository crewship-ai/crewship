package main

import (
	"context"
	"io"
	"net/http"
	"strconv"
	"time"

	"github.com/crewship-ai/crewship/internal/cli"
)

// Only 429s from the normal auth gates are replayed. Reset and credentials POSTs
// need twelve requests for six fresh accounts, exceeding the default ten-token
// burst. The server's Retry-After is respected rather than weakening that gate.
type teamSeedAuthTransport struct {
	base http.RoundTripper
	ctx  context.Context
	wait func(context.Context, time.Duration) error
}

func teamSeedAuthClient(ctx context.Context, server string) *cli.Client {
	client := cli.NewClient(server, "", "").WithContext(ctx).WithTimeout(3 * time.Minute)
	base := client.HTTPClient.Transport
	if base == nil {
		base = http.DefaultTransport
	}
	client.HTTPClient.Transport = &teamSeedAuthTransport{base: base, ctx: ctx, wait: teamSeedWait}
	return client
}
func teamSeedWait(ctx context.Context, d time.Duration) error {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}
func teamSeedRetryDelay(value string, now time.Time) (time.Duration, bool) {
	delay := time.Minute
	if seconds, err := strconv.Atoi(value); err == nil && seconds >= 0 {
		if seconds > 60 {
			return 0, false
		}
		delay = time.Duration(seconds) * time.Second
	} else if date, err := http.ParseTime(value); err == nil {
		delay = date.Sub(now)
		if delay < 0 {
			delay = 0
		}
	}
	// Three attempts and at most one minute per wait bound a stuck auth service.
	// A longer server hint is not shortened: return the 429 to the caller instead.
	return delay, delay <= time.Minute
}

type teamSeedAuthBody struct {
	io.ReadCloser
	done func()
}

func (b *teamSeedAuthBody) Close() error { err := b.ReadCloser.Close(); b.done(); return err }
func (t *teamSeedAuthTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	eligible := request.Method == http.MethodPost && (request.URL.Path == "/api/v1/auth/reset" || request.URL.Path == "/api/auth/callback/credentials")
	if !eligible {
		return t.base.RoundTrip(request)
	}
	ctx, cancel := context.WithCancel(request.Context())
	stop := context.AfterFunc(t.ctx, cancel)
	done := func() { stop(); cancel() }
	current := request.Clone(ctx)
	for attempt := 0; ; attempt++ {
		response, err := t.base.RoundTrip(current)
		if err != nil {
			done()
			return nil, err
		}
		delay, retry := teamSeedRetryDelay(response.Header.Get("Retry-After"), time.Now())
		if response.StatusCode != http.StatusTooManyRequests || attempt >= 2 || !retry || request.GetBody == nil {
			response.Body = &teamSeedAuthBody{ReadCloser: response.Body, done: done}
			return response, nil
		}
		_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 4096))
		response.Body.Close()
		if err = t.wait(ctx, delay); err != nil {
			done()
			return nil, err
		}
		body, err := request.GetBody()
		if err != nil {
			done()
			return nil, err
		}
		current = request.Clone(ctx)
		current.Body = body
	}
}
