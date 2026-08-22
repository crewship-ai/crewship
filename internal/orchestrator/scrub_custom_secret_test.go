package orchestrator

// Regression guard for value-aware output scrubbing (credentials
// hardening B1): a CUSTOM credential value with NO known prefix (a
// GENERIC_SECRET / webhook secret / self-issued token) must be
// redacted from the run's streamed output because the orchestrator
// feeds every loaded credential PlainValue into the per-run
// StreamScrubber (orchestrator_run.go → wrapScrubHandler →
// scrubber.AddSecretValues). The scrubber package tests cover the
// pattern mechanics; this pins the ORCHESTRATOR wiring — the exact
// gap the 2026-06 audit's SC1 finding closed — so a future refactor
// that drops the secretValues plumbing fails a test instead of
// silently reopening the leak.

import (
	"encoding/base64"
	"log/slog"
	"strings"
	"testing"
)

func TestWrapScrubHandler_CustomSecretValueRedacted(t *testing.T) {
	// Deliberately prefix-less: no built-in pattern (sk-*, ghp_, AKIA…)
	// matches this, so only the value-aware path can catch it.
	const secret = "wh00k-c4stom-v41ue-9f27ab" //gitleaks:allow — fake fixture, asserts this value gets redacted

	cases := []struct {
		name   string
		events []string
	}{
		{"whole value in one delta", []string{"config dump: " + secret + " end"}},
		{"value split across two deltas", []string{"leak> " + secret[:9], secret[9:] + " <end"}},
		{"base64-encoded value", []string{"exfil: " + base64.StdEncoding.EncodeToString([]byte(secret)) + " done"}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			o := &Orchestrator{logger: slog.Default()}
			var out strings.Builder
			handler := EventHandler(func(e AgentEvent) {
				out.WriteString(e.Content)
			})

			wrapped, flush := o.wrapScrubHandler(handler, []string{secret})
			for _, ev := range tc.events {
				wrapped(AgentEvent{Type: "text", Content: ev})
			}
			flush()

			got := out.String()
			if strings.Contains(got, secret) {
				t.Fatalf("custom secret leaked through the run scrubber — got %q", got)
			}
			if tc.name == "base64-encoded value" {
				if enc := base64.StdEncoding.EncodeToString([]byte(secret)); strings.Contains(got, enc) {
					t.Fatalf("base64-encoded custom secret leaked — got %q", got)
				}
			}
			if !strings.Contains(got, "[REDACTED") {
				t.Fatalf("expected a redaction marker in output, got %q", got)
			}
		})
	}

	t.Run("benign text passes through untouched", func(t *testing.T) {
		o := &Orchestrator{logger: slog.Default()}
		var out strings.Builder
		handler := EventHandler(func(e AgentEvent) { out.WriteString(e.Content) })
		wrapped, flush := o.wrapScrubHandler(handler, []string{secret})
		const benign = "deploy finished in 42s, all checks green"
		wrapped(AgentEvent{Type: "text", Content: benign})
		flush()
		if out.String() != benign {
			t.Fatalf("benign output mutated: %q", out.String())
		}
	})
}

// The header-value floor. Registering a credential's custom header values as
// scrub literals closes a real gap — a gateway authenticating via X-Api-Key puts
// the whole secret in a header value, matching no built-in pattern — but headers
// are not always secret. The field's own contract calls them "an org / route
// selector some gateways require", and a global redaction literal is not free:
// registering "acme-production" turns every occurrence of that string in agent
// output, chat and the journal into REDACTED, which reads as a bug and hides the
// text the operator is trying to debug.
func TestMinHeaderSecretLen_SeparatesSelectorsFromSecrets(t *testing.T) {
	selectors := []string{"acme", "prod", "team-a", "us-east-1", "v1", "acme-prod"}
	for _, v := range selectors {
		if len(v) >= minHeaderSecretLen {
			t.Errorf("%q (%d) would be registered as a scrub literal; a route selector redacted globally reads as a bug", v, len(v))
		}
	}

	// The shapes that plausibly arrive AS AN ENDPOINT AUTH HEADER VALUE must
	// clear the floor, or a genuine key goes unredacted. That set is LLM gateway
	// keys — the credential kind this delivery path exists for — not every
	// pattern the scrubber knows.
	//
	// Asserted as lengths rather than sample strings: the property is purely
	// about length, and writing plausible keys to express it means writing
	// strings the gitleaks pre-commit gate flags by shape, for values that were
	// never secrets. The repo's allowlist is keyed by commit SHA, so each one
	// would need a fresh entry on every amend.
	//
	// The numbers are the scrubber's own regex lower bounds (internal/scrubber),
	// which are deliberately PERMISSIVE for matching and therefore shorter than
	// any key a vendor actually mints. Using them makes this a worst case rather
	// than a typical one.
	gatewayKeyFloor := []struct {
		shape  string
		length int
	}{
		{"sk-ant-api03-… (Anthropic)", len("sk-ant-api03-") + 10},
		{"sk-proj-… (OpenAI project)", len("sk-proj-") + 10},
		{"sk-or-… (OpenRouter)", len("sk-or-") + 20},
		{"AIzaSy… (Google)", len("AIzaSy") + 33},
		{"sk-… (generic OpenAI-compatible gateway)", len("sk-") + 20},
	}
	for _, k := range gatewayKeyFloor {
		if k.length < minHeaderSecretLen {
			t.Errorf("%s is %d chars at the scrubber's own lower bound, below the %d floor; a genuine gateway key in a header would survive Scrub",
				k.shape, k.length, minHeaderSecretLen)
		}
	}

	// Known and accepted gap, stated rather than left for someone to rediscover:
	// a FORGE token (ghp_ + 10 at the regex lower bound = 14) sits under the
	// floor. Real ones are far longer, and a GitHub or GitLab token is not
	// something an LLM endpoint authenticates with — it would be in a
	// credential's own value, which is registered unconditionally, not in an
	// endpoint's custom headers. If that ever stops being true, this is the line
	// that has to move.
	if forge := len("ghp_") + 10; forge >= minHeaderSecretLen {
		t.Errorf("a forge token now clears the floor at %d; the waiver comment above is stale and should be deleted", forge)
	}

	// The floor is deliberately stricter than the scrubber's own, which exists
	// for values that are secrets by construction.
	if minHeaderSecretLen <= 5 {
		t.Errorf("minHeaderSecretLen = %d; it must be stricter than the scrubber's own 5-char floor or it buys nothing", minHeaderSecretLen)
	}
}
