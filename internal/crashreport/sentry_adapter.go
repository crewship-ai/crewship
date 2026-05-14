package crashreport

import (
	"errors"
	"net/url"
	"strings"
	"time"

	"github.com/getsentry/sentry-go"
)

// sentryBackend bridges the crashreport.Backend interface to the official
// getsentry/sentry-go SDK. Registered as the default backend via init();
// tests override with SetBackend(<fake>) when they need to assert on
// captured events.
type sentryBackend struct {
	initialized bool
}

func init() {
	SetBackend(&sentryBackend{})
}

// Init configures the sentry-go global client. Called from crashreport.Init
// only when DSN is non-empty AND the operator has opted in, so this never
// runs in the no-DSN / opted-out paths.
func (b *sentryBackend) Init(dsn, installID, version string) error {
	if dsn == "" {
		return errors.New("empty DSN")
	}
	err := sentry.Init(sentry.ClientOptions{
		Dsn:         dsn,
		Release:     version,
		ServerName:  installID, // pseudonymous; never a hostname
		Environment: classifyEnv(version),

		// Limit performance-tracing volume; we don't need APM right now,
		// just crash visibility. 0% trace sampling keeps the free-tier
		// quota for actual errors.
		TracesSampleRate: 0,
		// Send 100% of captured errors — the volume is low enough that
		// down-sampling would hurt more than it helps.
		SampleRate: 1.0,

		// AttachStacktrace=true so plain string errors still ship with a
		// stack rather than just a one-liner.
		AttachStacktrace: true,

		BeforeSend: scrubEvent,
	})
	if err != nil {
		return err
	}
	b.initialized = true
	return nil
}

// Capture is fire-and-forget on the Sentry client; the SDK queues internally
// and drains on Flush. We attach the tags as Sentry tags (indexed, filterable
// in the UI) rather than extras.
func (b *sentryBackend) Capture(err error, tags map[string]string) {
	if !b.initialized || err == nil {
		return
	}
	sentry.WithScope(func(scope *sentry.Scope) {
		for k, v := range tags {
			scope.SetTag(k, v)
		}
		sentry.CaptureException(err)
	})
}

// Flush blocks up to timeout for the SDK's outbound queue to drain. Called
// from cmd_start cleanup so a panic during shutdown still ships before the
// process exits.
func (b *sentryBackend) Flush(timeout time.Duration) {
	if !b.initialized {
		return
	}
	sentry.Flush(timeout)
}

// scrubEvent is Sentry's BeforeSend hook. It runs synchronously on every
// outbound event, in the goroutine that captured it. The contract: return
// the (possibly mutated) event to send, or nil to drop entirely.
//
// We never drop events here — we only redact sensitive fields. Caller-side
// `Capture(err, tags)` already gates the call, so reaching this point means
// the operator opted in AND the error happened.
func scrubEvent(event *sentry.Event, _ *sentry.EventHint) *sentry.Event {
	if event == nil {
		return nil
	}
	scrubRequest(event.Request)
	event.User = sentry.User{} // never identify the user
	// Sentry collects host env vars by default; we don't trust them not to
	// contain DATABASE_URL with creds.
	delete(event.Contexts, "environment_variables")
	delete(event.Contexts, "os_user")
	return event
}

func scrubRequest(req *sentry.Request) {
	if req == nil {
		return
	}
	// Drop body wholesale — we have no way to tell which fields are sensitive.
	req.Data = ""

	for h := range req.Headers {
		if ShouldScrubHeader(h) {
			req.Headers[h] = "[redacted]"
		}
	}

	if req.URL != "" {
		if u, err := url.Parse(req.URL); err == nil {
			q := u.Query()
			for k := range q {
				if ShouldScrubQueryKey(k) {
					q.Set(k, "[redacted]")
				}
			}
			u.RawQuery = q.Encode()
			req.URL = u.String()
		}
	}

	if req.QueryString != "" {
		req.QueryString = scrubQueryString(req.QueryString)
	}
}

func scrubQueryString(qs string) string {
	parsed, err := url.ParseQuery(qs)
	if err != nil {
		return ""
	}
	for k := range parsed {
		if ShouldScrubQueryKey(k) {
			parsed.Set(k, "[redacted]")
		}
	}
	return parsed.Encode()
}

// classifyEnv lets us split production crashes from beta/RC noise in the
// Sentry UI. Anything with a pre-release suffix is "beta"; everything else
// is "production". "dev" never reaches here because crashreport.Init bails
// out for empty-DSN builds.
func classifyEnv(version string) string {
	if strings.Contains(version, "-beta") || strings.Contains(version, "-rc") {
		return "beta"
	}
	if version == "" || version == "dev" {
		return "development"
	}
	return "production"
}
