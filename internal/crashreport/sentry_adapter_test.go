package crashreport

import (
	"errors"
	"net/url"
	"strings"
	"testing"
	"time"
)

// ---------------------------------------------------------------------------
// sentry_adapter.go — sentryBackend pre-init safety, scrubQueryString
// redaction, and classifyEnv categorisation.
//
// The "happy-path" Init that actually calls sentry.Init mutates global
// SDK state and is exercised end-to-end by TestInit_HappyPath via the
// fakeBackend swap. Here we cover the surfaces that the existing tests
// don't touch: the empty-DSN gate, the not-initialized short-circuits on
// Capture/Flush, and the two pure helpers used by scrubEvent + Init.
// ---------------------------------------------------------------------------

func TestSentryBackend_Init_EmptyDSN_ReturnsError(t *testing.T) {
	b := &sentryBackend{}
	err := b.Init("", "install-1", "v0.1.0")
	if err == nil {
		t.Fatal("Init(empty DSN) = nil, want error")
	}
	if !strings.Contains(err.Error(), "empty DSN") {
		t.Errorf("err = %v, want \"empty DSN\"", err)
	}
	if b.initialized {
		t.Error("initialized = true after empty-DSN Init; must stay false so Capture/Flush short-circuit")
	}
}

func TestSentryBackend_Capture_NoOpWhenNotInitialized(t *testing.T) {
	// initialized=false (the fresh struct state). Capture must short-circuit
	// without touching the sentry-go global client — the previous bug here
	// was a panic when the SDK was queried pre-Init.
	b := &sentryBackend{}
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("Capture pre-init panicked: %v", r)
		}
	}()
	b.Capture(errors.New("boom"), map[string]string{"feature": "test"})
	// No observable side effect; the test passes by not panicking and by
	// initialized staying false.
	if b.initialized {
		t.Error("Capture flipped initialized")
	}
}

func TestSentryBackend_Capture_NoOpWhenErrIsNil(t *testing.T) {
	// Even on an initialized backend, a nil error must short-circuit —
	// Sentry would otherwise see a nil exception payload.
	b := &sentryBackend{initialized: true}
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("Capture(nil err) panicked: %v", r)
		}
	}()
	b.Capture(nil, map[string]string{"feature": "test"})
}

func TestSentryBackend_Flush_NoOpWhenNotInitialized(t *testing.T) {
	// Pre-init Flush must not call into sentry.Flush — which would block
	// for the full timeout waiting for a queue that doesn't exist.
	b := &sentryBackend{}
	start := time.Now()
	b.Flush(2 * time.Second)
	if elapsed := time.Since(start); elapsed > 100*time.Millisecond {
		t.Errorf("Flush pre-init took %v; expected near-instant no-op", elapsed)
	}
}

// ---- scrubQueryString ----

func TestScrubQueryString_RedactsSensitiveKeysOnly(t *testing.T) {
	// Verified per ShouldScrubQueryKey: token, secret, password, auth,
	// session, code, key (case-insensitive substring). Sanity-check a
	// representative spread; pin that an unrelated key passes through.
	in := "user=alice&access_token=hunter2&api_key=sk-abc&page=2"
	got := scrubQueryString(in)
	parsed, err := url.ParseQuery(got)
	if err != nil {
		t.Fatalf("output not a valid query: %v (%q)", err, got)
	}
	if parsed.Get("user") != "alice" {
		t.Errorf("user = %q, want \"alice\" (non-sensitive must pass through)", parsed.Get("user"))
	}
	if parsed.Get("page") != "2" {
		t.Errorf("page = %q, want \"2\"", parsed.Get("page"))
	}
	if parsed.Get("access_token") != "[redacted]" {
		t.Errorf("access_token = %q, want \"[redacted]\"", parsed.Get("access_token"))
	}
	if parsed.Get("api_key") != "[redacted]" {
		t.Errorf("api_key = %q, want \"[redacted]\"", parsed.Get("api_key"))
	}
}

func TestScrubQueryString_CaseInsensitive(t *testing.T) {
	// ShouldScrubQueryKey lowercases before substring-matching, so
	// SHOUTY_TOKEN and Mixed-Case-Secret must both be caught.
	in := "X-API-Key=k1&AUTH_TOKEN=t1&NotSecretAtAll=ok&Password=p1"
	got := scrubQueryString(in)
	parsed, _ := url.ParseQuery(got)
	if parsed.Get("X-API-Key") != "[redacted]" {
		t.Errorf("X-API-Key = %q, want \"[redacted]\"", parsed.Get("X-API-Key"))
	}
	if parsed.Get("AUTH_TOKEN") != "[redacted]" {
		t.Errorf("AUTH_TOKEN = %q, want \"[redacted]\"", parsed.Get("AUTH_TOKEN"))
	}
	if parsed.Get("Password") != "[redacted]" {
		t.Errorf("Password = %q, want \"[redacted]\"", parsed.Get("Password"))
	}
	// "NotSecretAtAll" contains the substring "secret" (case-insensitive)
	// — by the current predicate it WILL get redacted. Pin this so the
	// substring-vs-token behavior is locked; a regression to whole-word
	// matching would change the answer here.
	if parsed.Get("NotSecretAtAll") != "[redacted]" {
		t.Errorf("NotSecretAtAll = %q, want \"[redacted]\" (substring match on \"secret\")", parsed.Get("NotSecretAtAll"))
	}
}

func TestScrubQueryString_MalformedReturnsEmpty(t *testing.T) {
	// url.ParseQuery on truly unparseable input returns an error; the
	// scrubber's contract is to return "" rather than propagate garbage
	// (better to drop than to leak partially-parsed values).
	got := scrubQueryString("%zz=invalid")
	if got != "" {
		t.Errorf("scrubQueryString(malformed) = %q, want \"\"", got)
	}
}

func TestScrubQueryString_EmptyInputReturnsEmpty(t *testing.T) {
	if got := scrubQueryString(""); got != "" {
		t.Errorf("scrubQueryString(\"\") = %q, want \"\"", got)
	}
}

// ---- classifyEnv ----

func TestClassifyEnv_Categorisation(t *testing.T) {
	// Source comment: anything with -beta or -rc is "beta"; "" or "dev"
	// is "development"; everything else is "production". Pin every case.
	cases := []struct {
		name, version, want string
	}{
		{"beta-suffix", "v0.1.0-beta.3", "beta"},
		{"beta-anywhere", "v0.1.0-beta", "beta"},
		{"rc-suffix", "v1.0.0-rc.1", "beta"},
		{"rc-anywhere", "v1.0.0-rc", "beta"},
		{"production-stable", "v1.2.3", "production"},
		{"production-no-prefix", "1.2.3", "production"},
		{"empty-string", "", "development"},
		{"dev-literal", "dev", "development"},
		{"sneaky-beta-substring", "v1.0.0-betatest", "beta"}, // substring match — pin current behavior
		{"alpha-not-classed", "v1.0.0-alpha.1", "production"}, // only beta/rc map to beta; alpha lands in production
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := classifyEnv(tc.version); got != tc.want {
				t.Errorf("classifyEnv(%q) = %q, want %q", tc.version, got, tc.want)
			}
		})
	}
}

// ---- scrubRequest (covers the partial-coverage Headers/URL/QueryString branches) ----

func TestScrubRequest_NilIsSafe(t *testing.T) {
	// nil-request guard: scrubEvent's Sentry.Event.Request can be nil
	// in non-HTTP capture paths; scrubRequest must not panic.
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("scrubRequest(nil) panicked: %v", r)
		}
	}()
	scrubRequest(nil)
}
