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
